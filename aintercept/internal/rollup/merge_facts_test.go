package rollup

import (
	"context"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream/state"
)

// What /status reads from the merge commit: the newest raw row time (the
// proxy's clock, as today's processor counts it: a call's start or end, an
// event's row, a call in flight's first row, not a client's clock or a
// derived time) and whether content is retained (a call's bodies were read).

func factKey(t *testing.T, s stream.Store, key string, reqs []canon.Request, pend []canon.Pending, evs []canon.Event, derivedNS int64) {
	t.Helper()
	tables := map[string][]stream.Row{mtTable: {{Key: key, ID: "1", Value: mtRow{LastNS: derivedNS, V: "x"}}}}
	for _, r := range reqs {
		tables[state.TableFactRequests] = append(tables[state.TableFactRequests], stream.Row{Key: key, ID: r.CallID, Value: r})
	}
	for _, p := range pend {
		tables[state.TableFactPending] = append(tables[state.TableFactPending], stream.Row{Key: key, ID: p.CorrelationID, Value: p})
	}
	for _, e := range evs {
		tables[state.TableFactEvents] = append(tables[state.TableFactEvents], stream.Row{Key: key, ID: e.EventKey, Value: e})
	}
	if err := WriteOnePass(context.Background(), s, "", key, tables, day(0)); err != nil {
		t.Fatal(err)
	}
}

func TestMergeNewestIsTheNewestRawRowTime(t *testing.T) {
	cases := []struct {
		name string
		reqs []canon.Request
		pend []canon.Pending
		evs  []canon.Event
		want int64
	}{
		{"a call's end", []canon.Request{{CallID: "c1", StartNS: day(0) + 10, EndNS: day(0) + 50}}, nil, nil, day(0) + 50},
		{"an event's row, not its client clock", nil, nil, []canon.Event{{EventKey: "e1", RowNS: day(0) + 70, NS: day(0) + 900}}, day(0) + 70},
		{"a call in flight's first row", nil, []canon.Pending{{CorrelationID: "p1", SeenNS: day(0) + 30, StartNS: day(0) + 30}}, nil, day(0) + 30},
		{"the newest of them", []canon.Request{{CallID: "c1", StartNS: day(0) + 10, EndNS: day(0) + 50}},
			[]canon.Pending{{CorrelationID: "p1", SeenNS: day(0) + 60}}, []canon.Event{{EventKey: "e1", RowNS: day(0) + 20}}, day(0) + 60},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := stream.NewMemStore(stream.RealClock{})
			factKey(t, s, "s:a", c.reqs, c.pend, c.evs, day(0)+5000) // a derived time later than any row's
			run(t, s, newQueue("s:a"))
			m, err := ReadNewestMerge(context.Background(), s, "")
			if err != nil || m == nil {
				t.Fatalf("merge commit: %v %v", m, err)
			}
			if m.NewestNS != c.want {
				t.Fatalf("newest_ns %d, want %d", m.NewestNS, c.want)
			}
		})
	}
}

func TestMergeKeepsTheNewestAcrossRuns(t *testing.T) {
	s := stream.NewMemStore(stream.RealClock{})
	factKey(t, s, "s:a", []canon.Request{{CallID: "c1", EndNS: day(1)}}, nil, nil, 0)
	run(t, s, newQueue("s:a"))
	factKey(t, s, "s:b", []canon.Request{{CallID: "c2", EndNS: day(0)}}, nil, nil, 0)
	run(t, s, newQueue("s:b"))
	if m, _ := ReadNewestMerge(context.Background(), s, ""); m.NewestNS != day(1) {
		t.Fatalf("newest_ns %d, want %d", m.NewestNS, day(1))
	}
}

func TestMergeSaysWhetherContentIsRetained(t *testing.T) {
	s := stream.NewMemStore(stream.RealClock{})
	factKey(t, s, "s:a", []canon.Request{{CallID: "c1", EndNS: day(0)}}, nil, nil, 0)
	run(t, s, newQueue("s:a"))
	if m, _ := ReadNewestMerge(context.Background(), s, ""); m.ContentRetained {
		t.Fatal("no call's bodies were read: not retained")
	}
	factKey(t, s, "s:b", []canon.Request{{CallID: "c2", EndNS: day(0), DetailJSON: `{"text":"x"}`}}, nil, nil, 0)
	run(t, s, newQueue("s:b"))
	if m, _ := ReadNewestMerge(context.Background(), s, ""); !m.ContentRetained {
		t.Fatal("a call's bodies were read: retained")
	}
	factKey(t, s, "s:c", []canon.Request{{CallID: "c3", EndNS: day(0)}}, nil, nil, 0)
	run(t, s, newQueue("s:c"))
	if m, _ := ReadNewestMerge(context.Background(), s, ""); !m.ContentRetained {
		t.Fatal("once retained, the deployment retains content")
	}
}

// /status's first request and earliest row come from the merge commit, never
// from reading the fact tables: the first is the earliest non-synthetic call
// (its start, else its end), call in flight (its start) or event row; the
// earliest counts synthetic rows too (the proxy's own probes).
func TestMergeFirstRequestAndEarliest(t *testing.T) {
	cases := []struct {
		name          string
		reqs          []canon.Request
		pend          []canon.Pending
		evs           []canon.Event
		first, oldest int64
	}{
		{"a call's start", []canon.Request{{CallID: "c1", StartNS: day(0) + 10, EndNS: day(0) + 50}}, nil, nil, day(0) + 10, day(0) + 10},
		{"a call with no start: its end", []canon.Request{{CallID: "c1", EndNS: day(0) + 40}}, nil, nil, day(0) + 40, day(0) + 40},
		{"an event row", []canon.Request{{CallID: "c1", StartNS: day(0) + 10}}, nil, []canon.Event{{EventKey: "e1", RowNS: day(0) + 5}}, day(0) + 5, day(0) + 5},
		{"a call in flight", nil, []canon.Pending{{CorrelationID: "p1", SeenNS: day(0) + 3, StartNS: day(0) + 3}}, nil, day(0) + 3, day(0) + 3},
		{"a synthetic probe only counts as earliest", []canon.Request{{CallID: "c1", StartNS: day(0) + 10}},
			[]canon.Pending{{CorrelationID: "p1", SeenNS: day(0) + 2, StartNS: day(0) + 2, Synthetic: true}}, nil, day(0) + 10, day(0) + 2},
		{"a call in flight with no start yet isn't a call", nil, []canon.Pending{{CorrelationID: "p1", SeenNS: day(0) + 1}}, nil, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := stream.NewMemStore(stream.RealClock{})
			factKey(t, s, "s:a", c.reqs, c.pend, c.evs, day(0)+5000)
			run(t, s, newQueue("s:a"))
			m, err := ReadNewestMerge(context.Background(), s, "")
			if err != nil || m == nil {
				t.Fatalf("merge commit: %v %v", m, err)
			}
			if m.FirstRequestNS != c.first || m.EarliestNS != c.oldest {
				t.Fatalf("first %d earliest %d, want %d %d", m.FirstRequestNS, m.EarliestNS, c.first, c.oldest)
			}
		})
	}
}

// Both are the minimum over every run, not just the newest.
func TestMergeKeepsTheFirstAcrossRuns(t *testing.T) {
	s := stream.NewMemStore(stream.RealClock{})
	factKey(t, s, "s:a", []canon.Request{{CallID: "c1", StartNS: day(0) + 10}}, nil, nil, day(0)+5000)
	run(t, s, newQueue("s:a"))
	factKey(t, s, "s:b", []canon.Request{{CallID: "c2", StartNS: day(1)}}, nil, nil, day(1)+5000)
	run(t, s, newQueue("s:b"))
	m, _ := ReadNewestMerge(context.Background(), s, "")
	if m.FirstRequestNS != day(0)+10 || m.EarliestNS != day(0)+10 {
		t.Fatalf("first %d earliest %d", m.FirstRequestNS, m.EarliestNS)
	}
}

// A key's probe calls count for /status as today's store counts its probes:
// the earliest row (waiting since) and the newest, never the first request.
func TestMergeCountsProbes(t *testing.T) {
	s := stream.NewMemStore(stream.RealClock{})
	tables := map[string][]stream.Row{
		mtTable:               {{Key: "ip:a", ID: "1", Value: mtRow{LastNS: day(0), V: "x"}}},
		state.TableFactProbes: {{Key: "ip:a", ID: "", Value: state.Probes{FirstNS: day(0) + 10, NewestNS: day(0) + 90}}},
	}
	if err := WriteOnePass(context.Background(), s, "", "ip:a", tables, day(0)); err != nil {
		t.Fatal(err)
	}
	factKey(t, s, "s:b", []canon.Request{{CallID: "c1", StartNS: day(0) + 50, EndNS: day(0) + 60}}, nil, nil, 0)
	run(t, s, newQueue("ip:a", "s:b"))
	m, _ := ReadNewestMerge(context.Background(), s, "")
	if m.FirstRequestNS != day(0)+50 || m.EarliestNS != day(0)+10 || m.NewestNS != day(0)+90 {
		t.Fatalf("first %d earliest %d newest %d, want %d %d %d", m.FirstRequestNS, m.EarliestNS, m.NewestNS, day(0)+50, day(0)+10, day(0)+90)
	}
}

// Content is retained once ingest has written the content mark.
func TestMergeReadsTheContentMark(t *testing.T) {
	s := stream.NewMemStore(stream.RealClock{})
	factKey(t, s, "s:a", []canon.Request{{CallID: "c1", EndNS: day(0)}}, nil, nil, 0)
	if err := s.Put(context.Background(), records.ContentMark(""), nil); err != nil {
		t.Fatal(err)
	}
	run(t, s, newQueue("s:a"))
	if m, _ := ReadNewestMerge(context.Background(), s, ""); !m.ContentRetained {
		t.Fatal("the content mark is there: retained")
	}
}
