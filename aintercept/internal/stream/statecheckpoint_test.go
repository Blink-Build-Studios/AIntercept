package stream

import (
	"fmt"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// bigSession is n records of one Claude Code session from index i0: calls and
// the events that follow them (queries, reports, tools).
func bigSession(sess string, i0, n int) []records.Record {
	out := make([]records.Record, 0, n)
	for i := i0; i < i0+n; i++ {
		ts := int64(1_800_000_000_000_000_000) + int64(i)*1_000_000_000
		id := fmt.Sprintf("big-%08d", i)
		var r records.Record
		switch i % 4 {
		case 0, 1:
			r = records.Record{Kind: records.KindCallEnd, CallID: id, Call: &canon.Request{CallID: id, CorrelationID: id,
				StartNS: ts, EndNS: ts + 500_000_000, Host: "api.anthropic.com", Method: "POST", Path: "/v1/messages",
				Status: 200, SessionID: sess, Product: canon.ProductClaudeCode, KeyHash: "k1", RequestID: "r" + id}}
		case 2:
			r = records.Record{Kind: records.KindClientEvent, EventKey: id, Event: &canon.Event{EventKey: id, CorrelationID: "b" + id,
				RowNS: ts, NS: ts, Seq: 1, Source: canon.ProductClaudeCode, Name: "tengu_api_success", SessionID: sess,
				MetadataJSON: fmt.Sprintf(`{"requestId":"rbig-%08d"}`, i-2)}}
		default:
			r = records.Record{Kind: records.KindClientEvent, EventKey: id, Event: &canon.Event{EventKey: id, CorrelationID: "b" + id,
				RowNS: ts, NS: ts, Seq: 1, Source: canon.ProductClaudeCode, Name: "tengu_api_query", SessionID: sess,
				MetadataJSON: `{"permissionMode":"plan"}`}}
		}
		r.Version, r.RecordID = records.Version, id
		r.RoutingKey = records.RoutingKey(&r)
		out = append(out, r)
	}
	return out
}

// A cycle's state file holds what its keys' records changed, not their
// history: the same 40 new records of a key write the same bytes of state
// whether the key already holds 200 or 2,000.
func TestStateFilesHoldChangesNotHistory(t *testing.T) {
	stateBytes := func(held int) int {
		h, ctx := setup(t, 1)
		h.m = StateModel{}
		w := h.worker("a", 1)
		w.CompactRuns = 1 << 20
		h.upload(0, bigSession("big", 0, held))
		if _, err := w.Cycle(ctx); err != nil {
			t.Fatal(err)
		}
		h.clock.Advance(10 * time.Second)
		h.upload(0, bigSession("big", held, 40))
		if _, err := w.Cycle(ctx); err != nil {
			t.Fatal(err)
		}
		b, err := h.s.Get(ctx, h.l.StateKey(0, 1, 2)) // the second cycle's state run (kept until GC)
		if err != nil {
			t.Fatal(err)
		}
		return len(b)
	}
	small, big := stateBytes(200), stateBytes(2_000)
	t.Logf("state written for 40 new records: holding 200 %d bytes, holding 2,000 %d bytes", small, big)
	if float64(big) > 1.2*float64(small)+512 {
		t.Fatalf("a cycle's state grows with the key's history: %d bytes at 200, %d at 2,000", small, big)
	}
}

// A key written over many cycles, evicted from memory and compacted between
// them, loads back from its checkpoints exactly: the tables equal one pass.
func TestStateLoadsBackAcrossCyclesAndCompaction(t *testing.T) {
	h, ctx := setup(t, 1)
	h.m = StateModel{}
	w := h.worker("a", 1)
	w.CompactRuns = 3
	w.CacheBytes = 1 // every key is loaded back each cycle
	for i := 0; i < 12; i++ {
		h.upload(0, append(bigSession("big", i*30, 30), bigSession(fmt.Sprintf("other%d", i%3), i*30, 30)...))
		h.clock.Advance(10 * time.Second)
		if _, err := w.Cycle(ctx); err != nil {
			t.Fatal(err)
		}
		if i == 6 {
			if err := w.Compact(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	h.checkAll(ctx)
}
