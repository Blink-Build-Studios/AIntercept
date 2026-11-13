package stream

import (
	"encoding/json"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// rec is a record routed as ingest routes it.
func rec(id, kind string, call *canon.Request, ev *canon.Event) records.Record {
	r := records.Record{Version: records.Version, RecordID: id, Kind: kind, Call: call, Event: ev}
	r.RoutingKey = records.RoutingKey(&r)
	return r
}

func callRec(id string, start, end int64, synthetic bool, detail string) records.Record {
	return rec(id, records.KindCallEnd, &canon.Request{CallID: id, SessionID: "s1", StartNS: start, EndNS: end, Synthetic: synthetic, DetailJSON: detail}, nil)
}

func eventRec(id string, ns int64) records.Record {
	return rec(id, records.KindClientEvent, nil, &canon.Event{SessionID: "s1", RowNS: ns, NS: ns})
}

// /status reads only these: a bucket's newest data, its earliest captured call
// (synthetic probes aside) and earliest anything, and whether content was kept.
func TestCommitStatsAtomic(t *testing.T) {
	cases := []struct {
		name                 string
		rs                   []records.Record
		newest, first, early int64
		content              bool
	}{
		{"one call", []records.Record{callRec("a", 100, 150, false, "")}, 150, 100, 100, false},
		{"a call with no start counts at its end", []records.Record{callRec("a", 0, 150, false, "")}, 150, 150, 150, false},
		{"a probe is earliest, never first", []records.Record{callRec("a", 100, 150, false, ""), callRec("p", 50, 60, true, "")}, 150, 100, 50, false},
		{"only probes: earliest, no first", []records.Record{callRec("p", 50, 60, true, "")}, 60, 0, 50, false},
		{"an event is earliest", []records.Record{callRec("a", 100, 150, false, ""), eventRec("e", 40)}, 150, 100, 40, false},
		{"an event is newest", []records.Record{callRec("a", 100, 150, false, ""), eventRec("e", 400)}, 400, 100, 100, false},
		{"a call's recorded detail is content", []records.Record{callRec("a", 100, 150, false, `{"text":"x"}`)}, 150, 100, 100, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, ctx := setup(t, 1)
			h.upload(0, c.rs)
			w := h.worker("a", 1)
			if _, err := w.Cycle(ctx); err != nil {
				t.Fatal(err)
			}
			s := w.commit
			if s.NewestNS != c.newest || s.FirstCallNS != c.first || s.EarliestNS != c.early || s.ContentRetained != c.content {
				t.Fatalf("stats newest=%d first=%d earliest=%d content=%v, want %d %d %d %v",
					s.NewestNS, s.FirstCallNS, s.EarliestNS, s.ContentRetained, c.newest, c.first, c.early, c.content)
			}
		})
	}
}

// Sequences: stats carry from commit to commit (delta in): a later cycle only
// widens them, whatever order the uploads came in.
func TestCommitStatsCarryForward(t *testing.T) {
	h, ctx := setup(t, 1)
	w := h.worker("a", 1)
	h.upload(0, []records.Record{callRec("a", 100, 150, false, `{"x":1}`)})
	if _, err := w.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	h.upload(0, []records.Record{callRec("b", 20, 30, false, ""), eventRec("e", 10)})
	if _, err := w.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	s := w.commit
	if s.NewestNS != 150 || s.FirstCallNS != 20 || s.EarliestNS != 10 || !s.ContentRetained {
		t.Fatalf("carried stats %+v", s)
	}
	// The commit's JSON carries them under the contract's names.
	b, _ := json.Marshal(s)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for _, k := range []string{"newest_ns", "first_call_ns", "earliest_ns", "content_retained"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("commit JSON has no %q: %s", k, b)
		}
	}
}

// Generated: the stats of many shuffled cycles equal one pass's min and max.
func TestCommitStatsGenerated(t *testing.T) {
	h, ctx := setup(t, 1)
	w := h.worker("a", 1)
	var all []records.Record
	for i := 0; i < 6; i++ {
		rs := h.gen.batch(40)
		all = append(all, rs...)
		h.upload(0, rs)
		if _, err := w.Cycle(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var newest, first, early int64
	for _, r := range all {
		var ts, at int64
		synthetic := false
		switch {
		case r.Call != nil:
			ts, at, synthetic = firstNonZero(r.Call.StartNS, r.Call.EndNS), max(r.Call.StartNS, r.Call.EndNS), r.Call.Synthetic
		case r.Event != nil:
			ts, at = r.Event.RowNS, r.Event.RowNS
		}
		newest = max(newest, at)
		early = minNZ(early, ts)
		if r.Call != nil && !synthetic {
			first = minNZ(first, ts)
		}
	}
	s := w.commit
	if s.NewestNS != newest || s.FirstCallNS != first || s.EarliestNS != early {
		t.Fatalf("stats %d/%d/%d, want one pass's %d/%d/%d", s.NewestNS, s.FirstCallNS, s.EarliestNS, newest, first, early)
	}
}

func firstNonZero(a, b int64) int64 {
	if a != 0 {
		return a
	}
	return b
}

func minNZ(a, b int64) int64 {
	if a == 0 || (b != 0 && b < a) {
		return b
	}
	return a
}
