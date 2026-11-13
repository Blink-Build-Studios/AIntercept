package records

import (
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// ccEvent is a Claude Code event of batch id at client time ts, with a name.
func ccEvent(id, evID, session, name string, ts time.Duration) events.Event {
	e := clientEvent(id, evID, session, ts)
	e.ClientEvent.Name = name
	return e
}

func codexEvent(id, session string) events.Event {
	e := clientEvent(id, "", session, 0)
	e.ClientEvent.Source = "codex"
	e.ClientEvent.Name = "codex_turn_event"
	return e
}

// ranks applies a batch and returns each event record's rank by its event key.
func ranks(t *testing.T, rows []events.Event) map[string]int64 {
	t.Helper()
	a := NewAssembler("inc")
	var got []Record
	for i := range rows {
		got = append(got, a.Apply(&rows[i], line(rows[i]), "f")...)
	}
	got = append(got, a.Flush()...)
	out := map[string]int64{}
	for _, r := range got {
		if r.Kind == KindClientEvent {
			out[r.Event.EventKey] = r.BatchCCN
		}
	}
	return out
}

// An event's number is its rank among its batch's Claude Code events, ordered
// as the event rows number them (sequence, row time, client time, name,
// metadata). Other sources' events aren't numbered and don't take a place.
func TestRankAmongTheBatchsClaudeCodeEvents(t *testing.T) {
	rows := append(analytics("a1"),
		ccEvent("a1", "e1", "s1", "tengu_b", 3*time.Second),
		codexEvent("a1", "s1"),
		ccEvent("a1", "e2", "s1", "tengu_a", 1*time.Second),
		ccEvent("a1", "e3", "s1", "tengu_a", 3*time.Second), // same time as e1: by name
	)
	got := ranks(t, rows)
	want := map[string]int64{"a1/e2": 1, "a1/e3": 2, "a1/e1": 3}
	for k, n := range want {
		if got[k] != n {
			t.Errorf("%s: rank %d, want %d (all: %v)", k, got[k], n, got)
		}
	}
	for k, n := range got {
		if _, ok := want[k]; !ok && n != 0 {
			t.Errorf("non-Claude Code event %s ranked %d", k, n)
		}
	}
}

// The rank is over the whole batch, across the sessions it carries.
func TestRankSpansTheBatchsSessions(t *testing.T) {
	rows := append(analytics("a1"),
		ccEvent("a1", "x3", "s2", "n", 5*time.Second),
		ccEvent("a1", "x1", "s1", "n", 1*time.Second),
		ccEvent("a1", "x2", "s2", "n", 2*time.Second),
	)
	got := ranks(t, rows)
	for k, n := range map[string]int64{"a1/x1": 1, "a1/x2": 2, "a1/x3": 3} {
		if got[k] != n {
			t.Errorf("%s: rank %d, want %d", k, got[k], n)
		}
	}
}

// Identical events take consecutive ranks in arrival order.
func TestIdenticalEventsRankInArrivalOrder(t *testing.T) {
	e := ccEvent("a1", "", "s1", "n", time.Second)
	rows := append(analytics("a1"), e, e, ccEvent("a1", "", "s1", "m", 0))
	a := NewAssembler("inc")
	var got []Record
	for i := range rows {
		got = append(got, a.Apply(&rows[i], line(rows[i]), "f")...)
	}
	got = append(got, a.Flush()...)
	var seen []int64
	for _, r := range got {
		if r.Kind == KindClientEvent {
			seen = append(seen, r.BatchCCN)
		}
	}
	if len(seen) != 3 || seen[0] != 2 || seen[1] != 3 || seen[2] != 1 {
		t.Fatalf("ranks %v, want [2 3 1]", seen)
	}
}

// The order's every key, one at a time: sequence, row time, client time, name
// (unnamed as "(unnamed)"), metadata, then the event key (events alike in all
// of those but their raw body number the same in every reader).
func TestRankOrderKeys(t *testing.T) {
	ev := func(id string, seq, row, ns int64, name, md string) Record {
		return Record{Event: &canon.Event{EventKey: id, Source: "claude_code", Seq: seq, RowNS: row, NS: ns, Name: name, MetadataJSON: md}}
	}
	rs := []Record{
		ev("md-b", 1, 1, 1, "n", "b"), ev("seq2", 2, 0, 0, "", ""), ev("row2", 1, 2, 0, "", ""), ev("ns2", 1, 1, 2, "", ""),
		ev("unnamed", 1, 1, 1, "", ""), ev("md-a", 1, 1, 1, "n", "a"), ev("key-b", 1, 1, 1, "n", "c"), ev("key-a", 1, 1, 1, "n", "c"),
	}
	rankClaudeCode(rs)
	want := map[string]int64{"unnamed": 1, "md-a": 2, "md-b": 3, "key-a": 4, "key-b": 5, "ns2": 6, "row2": 7, "seq2": 8}
	for _, r := range rs {
		if r.BatchCCN != want[r.Event.EventKey] {
			t.Errorf("%s: %d, want %d", r.Event.EventKey, r.BatchCCN, want[r.Event.EventKey])
		}
	}
}
