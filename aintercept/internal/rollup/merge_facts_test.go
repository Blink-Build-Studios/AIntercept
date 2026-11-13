package rollup

import (
	"context"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
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
