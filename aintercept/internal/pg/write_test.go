package pg_test

import (
	"math"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream/state"
)

// The commit's row writes (docs/PIPELINE.md §2 Run 8.2): one upsert by unnest
// and one delete per table, pipelined in one batch.

func TestRowValuesFollowTheCodec(t *testing.T) {
	tb := pg.TableNamed("request_txs")
	zero := int64(0)
	v := tb.RowValues(state.ReqTx{CallID: "c1", Row: process.RequestRow{RequestID: "r1", ToolCalls: nil, DurationMS: ptr(1.5), Status: &zero}})
	got := map[string]any{}
	for i, c := range tb.Columns[2:] { // after key, id
		got[c.Name] = v[i]
	}
	if len(v) != len(tb.Columns)-2 {
		t.Fatalf("%d values for %d row columns", len(v), len(tb.Columns)-2)
	}
	// A list is never NULL (the codec writes an empty one); a plain string is "" when empty.
	if l, ok := got["row_tool_calls"].([]string); !ok || l == nil || len(l) != 0 {
		t.Errorf("row_tool_calls %#v, want an empty list", got["row_tool_calls"])
	}
	if got["call_id"] != "c1" || got["row_request_id"] != "r1" || got["row_duration_ms"] != 1.5 {
		t.Errorf("values %v", got)
	}
	// A pointer is NULL when nil, its value (even zero) otherwise; a plain optional field's zero is NULL.
	if got["row_agent"] != nil || got["row_status"] != int64(0) {
		t.Errorf("row_agent %#v, row_status %#v", got["row_agent"], got["row_status"])
	}
	ev := pg.TableNamed("analytics")
	for i, val := range ev.RowValues(canon.Event{}) {
		if c := ev.Columns[i+2]; c.Optional && val != nil {
			t.Errorf("analytics.%s: an optional zero is %#v, want NULL", c.Name, val)
		}
	}
}

func ptr[T any](v T) *T { return &v }

func TestUpsertAndDeleteByUnnest(t *testing.T) {
	p := applied(t)
	c := ctx(t)
	tb := pg.TableNamed("request_txs")
	row := func(id string, req state.ReqTx) []any {
		return append([]any{"s:k", []byte(id)}, tb.RowValues(req)...)
	}
	a := state.ReqTx{CallID: "c1", KeyHash: "h", Row: process.RequestRow{RequestID: "r1", ToolCalls: []string{"exec ×2", "", "a\"b"},
		DurationMS: ptr(math.Nextafter(1, 2)), TTFTMS: ptr(math.Inf(1))}}
	b := state.ReqTx{CallID: "c2", Row: process.RequestRow{ToolCalls: []string{}}}
	write := func(cs ...pg.Changes) {
		t.Helper()
		var batch pgx.Batch
		if err := pg.QueueChanges(&batch, cs); err != nil {
			t.Fatal(err)
		}
		if err := p.SendBatch(c, &batch).Close(); err != nil {
			t.Fatal(err)
		}
	}
	write(pg.Changes{Table: "request_txs", Upserts: [][]any{row("s1\x00p1", a), row("s1\x00p2", b)}})
	read := func() map[string]state.ReqTx {
		t.Helper()
		rows, err := p.Query(c, `SELECT id, call_id, key_hash, row_request_id, row_tool_calls, row_duration_ms, row_ttft_ms
			FROM request_txs WHERE key = 's:k'`)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]state.ReqTx{}
		for rows.Next() {
			var id []byte
			var r state.ReqTx
			var hash, rid *string
			if err := rows.Scan(&id, &r.CallID, &hash, &rid, &r.Row.ToolCalls, &r.Row.DurationMS, &r.Row.TTFTMS); err != nil {
				t.Fatal(err)
			}
			if hash != nil {
				r.KeyHash = *hash
			}
			if rid != nil {
				r.Row.RequestID = *rid
			}
			out[string(id)] = r
		}
		return out
	}
	got := read()
	if r := got["s1\x00p1"]; !reflect.DeepEqual(r.Row.ToolCalls, a.Row.ToolCalls) || r.Row.DurationMS == nil ||
		*r.Row.DurationMS != *a.Row.DurationMS || r.Row.TTFTMS == nil || !math.IsInf(*r.Row.TTFTMS, 1) || r.KeyHash != "h" {
		t.Errorf("row 1: %+v", got["s1\x00p1"])
	}
	if l := got["s1\x00p2"].Row.ToolCalls; l == nil || len(l) != 0 {
		t.Errorf("an empty list came back %#v", l)
	}
	// An upsert of an existing id replaces it; a delete removes only its id.
	a.KeyHash = "h2"
	write(pg.Changes{Table: "request_txs", Upserts: [][]any{row("s1\x00p1", a)}, Deletes: [][]any{{"s:k", []byte("s1\x00p2")}}})
	got = read()
	if len(got) != 1 || got["s1\x00p1"].KeyHash != "h2" {
		t.Errorf("after the upsert and delete: %+v", got)
	}
	// A table whose columns are all its key: the upsert does nothing on a conflict.
	tools := pg.TableNamed("call_tool_uses")
	vals := append([]any{}, tools.RowValues(process.CallToolUse{SessionID: "s", TsNS: 5, Kind: "builtin", Tool: "Read"})...)
	write(pg.Changes{Table: "call_tool_uses", Upserts: [][]any{vals}})
	write(pg.Changes{Table: "call_tool_uses", Upserts: [][]any{vals}})
	var n int
	if err := p.QueryRow(c, "SELECT count(*) FROM call_tool_uses").Scan(&n); err != nil || n != 1 {
		t.Errorf("call_tool_uses has %d rows, %v", n, err)
	}
	write(pg.Changes{Table: "call_tool_uses", Deletes: [][]any{{"s", int64(5), "builtin", "", "Read"}}})
	if err := p.QueryRow(c, "SELECT count(*) FROM call_tool_uses").Scan(&n); err != nil || n != 0 {
		t.Errorf("call_tool_uses has %d rows after the delete, %v", n, err)
	}
}

func TestQueueChangesRefusesAMisshapenRow(t *testing.T) {
	var b pgx.Batch
	if err := pg.QueueChanges(&b, []pg.Changes{{Table: "request_txs", Upserts: [][]any{{"s:k"}}}}); err == nil {
		t.Error("a row with too few values was queued")
	}
	if err := pg.QueueChanges(&b, []pg.Changes{{Table: "no_such_table"}}); err == nil {
		t.Error("an unknown table was queued")
	}
}
