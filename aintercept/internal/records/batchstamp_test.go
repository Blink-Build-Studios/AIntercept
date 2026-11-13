package records

import (
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// BatchStamp is what the batch's own call carries: its event count, the name
// the request log shows and its sessions other than its home one.
type BatchStamp struct {
	N      int64
	Name   string
	Others []string
}

// BatchStampReference is each batch's stamp computed as the request log's SQL
// computes it over the processor's batch_members (api stream_sql.py, `batch`):
// a member is a Claude Code or Codex event, held per routing key under
// "bm:"+its event key (a later one with the same key replaces it); the count is
// count(*), the name min(name) over the non-empty names, and the other
// sessions the members' distinct non-empty sessions but the one the batch's
// call record names as its home (BatchSession).
func BatchStampReference(rs []Record) map[string]BatchStamp {
	type member struct{ batch, name, session string }
	members := map[string]member{}
	var order []string
	home := map[string]string{}
	for _, r := range rs {
		switch {
		case r.Kind == KindClientEvent && r.Event != nil:
			e := r.Event
			if e.Source != canon.ProductClaudeCode && e.Source != canon.ProductCodex {
				continue
			}
			k := r.RoutingKey + "\x00bm:" + e.EventKey
			if _, ok := members[k]; !ok {
				order = append(order, k)
			}
			members[k] = member{e.CorrelationID, e.Name, e.SessionID}
		case r.Kind == KindCallEnd && r.BatchID != "":
			home[r.BatchID] = r.BatchSession
		}
	}
	out := map[string]BatchStamp{}
	seen := map[string]map[string]bool{}
	for _, k := range order {
		m := members[k]
		s := out[m.batch]
		s.N++
		if m.name != "" && (s.Name == "" || m.name < s.Name) {
			s.Name = m.name
		}
		if seen[m.batch] == nil {
			seen[m.batch] = map[string]bool{}
		}
		if m.session != "" && m.session != home[m.batch] && !seen[m.batch][m.session] {
			seen[m.batch][m.session] = true
			s.Others = append(s.Others, m.session)
		}
		out[m.batch] = s
	}
	for b, s := range out {
		sort.Strings(s.Others)
		out[b] = s
	}
	return out
}

// checkBatchStamps holds every batch call record's stamp to the reference: a
// call written to two keys carries it on both.
func checkBatchStamps(t *testing.T, rs []Record) map[string]BatchStamp {
	t.Helper()
	ref := BatchStampReference(rs)
	got := map[string]BatchStamp{}
	for _, r := range rs {
		if r.Kind != KindCallEnd || r.BatchID == "" {
			continue
		}
		s := BatchStamp{N: r.BatchEvents, Name: r.BatchName, Others: r.BatchOtherSessions}
		if !reflect.DeepEqual(s, ref[r.BatchID]) {
			t.Errorf("batch %s (key %s): stamped %+v, the SQL gives %+v", r.BatchID, r.RoutingKey, s, ref[r.BatchID])
		}
		got[r.BatchID] = s
	}
	return got
}

func recordsOf(rows []events.Event) []Record {
	a := NewAssembler("inc")
	var got []Record
	for i := range rows {
		got = append(got, a.Apply(&rows[i], line(rows[i]), "f")...)
	}
	return append(got, a.Flush()...)
}

// codexAnalytics is Codex's analytics batch request, and codexAnalyticsEvent one of its events.
func codexAnalytics(id string) []events.Event {
	rows := analytics(id)
	for i := range rows {
		rows[i].Host, rows[i].Path = "chatgpt.com", "/backend-api/codex/analytics-events/events"
	}
	rows[0].Headers = map[string]string{"originator": "codex_cli_rs", "user-agent": "codex_cli_rs/0.164.0"}
	return rows
}

func codexAnalyticsEvent(id, session, name string) events.Event {
	return events.Event{CorrelationID: id, Seq: 5, TimestampNS: at(6 * time.Second), Phase: events.PhaseClientEvent,
		Host: "chatgpt.com", Path: "/backend-api/codex/analytics-events/events", KeyHash: kh,
		ClientEvent: &events.ClientEvent{Source: "codex", Name: name, Type: name, SessionID: session,
			Data: map[string]any{"session_id": session}}}
}

func TestBatchStamp(t *testing.T) {
	named := func(e events.Event, name string) events.Event { e.ClientEvent.Name = name; return e }
	sourced := func(e events.Event, src string) events.Event { e.ClientEvent.Source = src; return e }
	cases := []struct {
		name string
		rows []events.Event
		want BatchStamp
	}{
		{"one session", append(analytics("a1"),
			ccEvent("a1", "e1", "s1", "tengu_b", time.Second),
			ccEvent("a1", "e2", "s1", "tengu_a", 2*time.Second),
			ccEvent("a1", "e3", "s1", "tengu_c", 3*time.Second)),
			BatchStamp{N: 3, Name: "tengu_a"}},
		// The home session is the earliest event's; the others are the rest, sorted.
		{"spanning three sessions", append(analytics("a1"),
			ccEvent("a1", "e1", "s3", "tengu_z", 4*time.Second),
			ccEvent("a1", "e2", "s1", "tengu_y", 1*time.Second),
			ccEvent("a1", "e3", "s2", "tengu_x", 2*time.Second),
			ccEvent("a1", "e4", "s3", "tengu_w", 3*time.Second)),
			BatchStamp{N: 4, Name: "tengu_w", Others: []string{"s2", "s3"}}},
		// Events naming no session are members, and name no session.
		{"events with no session", append(analytics("a1"),
			ccEvent("a1", "e1", "", "tengu_b", time.Second),
			ccEvent("a1", "e2", "s1", "tengu_c", 2*time.Second),
			ccEvent("a1", "e3", "", "tengu_a", 3*time.Second)),
			BatchStamp{N: 3, Name: "tengu_a"}},
		{"no event names a session", append(analytics("a1"),
			ccEvent("a1", "e1", "", "tengu_b", time.Second),
			ccEvent("a1", "e2", "", "tengu_a", 2*time.Second)),
			BatchStamp{N: 2, Name: "tengu_a"}},
		// The name is the least non-empty one; with none, empty.
		{"empty names", append(analytics("a1"),
			ccEvent("a1", "e1", "s1", "", time.Second),
			ccEvent("a1", "e2", "s1", "tengu_b", 2*time.Second),
			ccEvent("a1", "e3", "s2", "", 3*time.Second)),
			BatchStamp{N: 3, Name: "tengu_b", Others: []string{"s2"}}},
		{"no names", append(analytics("a1"),
			ccEvent("a1", "e1", "s1", "", time.Second),
			ccEvent("a1", "e2", "s1", "", 2*time.Second)),
			BatchStamp{N: 2}},
		// Only Claude Code and Codex events are members.
		{"other sources", append(analytics("a1"),
			ccEvent("a1", "e1", "s1", "tengu_b", time.Second),
			sourced(ccEvent("a1", "e2", "s9", "aaa", 2*time.Second), "claude_ai")),
			BatchStamp{N: 1, Name: "tengu_b"}},
		{"no events", analytics("a1"), BatchStamp{}},
		// A client that sent one event twice: one member (same key, same routing key).
		{"repeated event id", append(analytics("a1"),
			ccEvent("a1", "e1", "s1", "tengu_b", time.Second),
			ccEvent("a1", "e1", "s1", "tengu_b", time.Second),
			ccEvent("a1", "e2", "s2", "tengu_c", 2*time.Second)),
			BatchStamp{N: 2, Name: "tengu_b", Others: []string{"s2"}}},
		// Identical events without an id are keyed -2, -3: each a member.
		{"repeated event without id", append(analytics("a1"),
			ccEvent("a1", "", "s1", "tengu_b", time.Second),
			ccEvent("a1", "", "s1", "tengu_b", time.Second)),
			BatchStamp{N: 2, Name: "tengu_b"}},
		// Events that came before their request's row still count.
		{"events before the request's row", append([]events.Event{
			ccEvent("a1", "e1", "s2", "tengu_b", 2*time.Second),
			ccEvent("a1", "e2", "s1", "tengu_c", time.Second)}, analytics("a1")...),
			BatchStamp{N: 2, Name: "tengu_b", Others: []string{"s2"}}},
		{"codex", append(codexAnalytics("x1"),
			codexAnalyticsEvent("x1", "th1", "codex_turn_event"),
			codexAnalyticsEvent("x1", "th2", "codex_thread_initialized"),
			codexAnalyticsEvent("x1", "th1", "codex_turn_event")),
			BatchStamp{N: 3, Name: "codex_thread_initialized", Others: []string{"th2"}}},
		{"codex unnamed", append(codexAnalytics("x1"),
			named(codexAnalyticsEvent("x1", "th1", ""), "")),
			BatchStamp{N: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rs := recordsOf(tc.rows)
			got := checkBatchStamps(t, rs)
			id := tc.rows[len(tc.rows)-1].CorrelationID
			s, ok := got[id]
			if !ok {
				t.Fatalf("no call record for batch %s", id)
			}
			if !reflect.DeepEqual(s, tc.want) {
				t.Errorf("stamped %+v, want %+v", s, tc.want)
			}
		})
	}
}

// A call whose end names a session its start didn't is written to both keys:
// both carry the stamp.
func TestBatchStampOnBothKeys(t *testing.T) {
	// Its start went under its credential's key; its batch names a session.
	rows := append(analytics("a1"), ccEvent("a1", "e1", "s1", "tengu_a", time.Second), ccEvent("a1", "e2", "s2", "tengu_b", 2*time.Second))
	rs := recordsOf(rows)
	n := 0
	for _, r := range rs {
		if r.Kind == KindCallEnd && r.BatchID == "a1" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("%d call records, want 2 (its start's key and its batch's session)", n)
	}
	got := checkBatchStamps(t, rs)
	if want := (BatchStamp{N: 2, Name: "tengu_a", Others: []string{"s2"}}); !reflect.DeepEqual(got["a1"], want) {
		t.Errorf("stamped %+v, want %+v", got["a1"], want)
	}
}

// Only the batch's own call is stamped: its events and other calls carry nothing.
func TestOnlyTheBatchCallIsStamped(t *testing.T) {
	rows := append(analytics("a1"), ccEvent("a1", "e1", "s1", "tengu_a", time.Second))
	rows = append(rows, hdr("m1", true, ccHeaders), complete("m1", ccClient))
	for _, r := range recordsOf(rows) {
		if r.Kind == KindCallEnd && r.BatchID != "" {
			continue
		}
		if r.BatchEvents != 0 || r.BatchName != "" || r.BatchOtherSessions != nil {
			t.Errorf("%s %s: stamped %d %q %v", r.Kind, r.CallID, r.BatchEvents, r.BatchName, r.BatchOtherSessions)
		}
	}
}
