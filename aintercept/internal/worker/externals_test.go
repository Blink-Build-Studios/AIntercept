package worker

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// The render's external inputs (docs/PIPELINE.md v7 §3.7): the drawer reads a
// call's detail object once, and an analytics batch spanning sessions counts
// every key's part, kept fresh by the flags a commit sets on the other keys.

// batchEvent is a Claude Code event of session sid in analytics batch b, the
// n-th of the batch.
func (e *env) batchEvent(sid, b string, n int64, ts int64) records.Record {
	key := fmt.Sprintf("%s/ev-%d", b, n)
	ev := &canon.Event{EventKey: key, CorrelationID: b, RowNS: ts, NS: ts, Seq: n, Source: canon.ProductClaudeCode,
		Name: "tengu_feature_ok", SessionID: sid, PersonID: "acct-1", MetadataJSON: `{"n":1}`, BatchN: n}
	return e.finish(records.Record{Kind: records.KindClientEvent, EventKey: key, BatchID: b, BatchN: n, BatchCCN: n,
		Event: ev, Call: &canon.Request{KeyHash: "k1"}})
}

// batchCall is analytics batch b's ended call, its session sid (its earliest event's).
func (e *env) batchCall(b, sid string, start int64) records.Record {
	c := &canon.Request{CallID: b, CorrelationID: b, StartNS: start, EndNS: start + int64(time.Second), Status: 200,
		Host: "api.anthropic.com", Method: "POST", Path: "/api/event_logging/v2/batch", KeyHash: "k1",
		Product: canon.ProductClaudeCode}
	return e.finish(records.Record{Kind: records.KindCallEnd, CallID: b, Call: c, BatchID: b, BatchSession: sid})
}

// requestDetail is the Requests tab's detail of request id in session sid's rows.
func (e *env) requestDetail(sid, id string) string {
	e.t.Helper()
	var d string
	if err := e.db.QueryRow(e.ctx, `SELECT coalesce(detail, '') FROM session_request_rows
		WHERE scope_session_id = $1 AND request_id = $2`, sid, id).Scan(&d); err != nil {
		e.t.Fatalf("%s's row of %s: %v", sid, id, err)
	}
	return d
}

func (e *env) flagRender(key string) bool {
	e.t.Helper()
	var r bool
	err := e.db.QueryRow(e.ctx, "SELECT coalesce((SELECT render FROM dirty_keys WHERE key = $1), false)", key).Scan(&r)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

func TestABatchSpanningSessionsCountsEveryKeysPart(t *testing.T) {
	e := newEnv(t)
	t0 := e.clock.Now().UnixNano()
	ka, kb := sessionKey("sa"), sessionKey("sb")
	// A holds the batch's earliest event and its call; B's three events come later, in a file of their own.
	e.putFile([]records.Record{e.call("sa", t0), e.batchEvent("sa", "bx", 1, t0+1), e.batchCall("bx", "sa", t0+2)}, 0, e.clock.Now())
	w := e.worker("w1")
	e.run(w, ka)
	if got := e.requestDetail("sa", "bx"); got != "1 events" {
		t.Fatalf("A alone: %q", got)
	}
	e.clock.Advance(time.Second)
	e.putFile([]records.Record{e.call("sb", t0+5), e.batchEvent("sb", "bx", 2, t0+6), e.batchEvent("sb", "bx", 3, t0+7),
		e.batchEvent("sb", "bx", 4, t0+8)}, 0, e.clock.Now())
	e.run(w, kb)
	// B's part is new: A is flagged to render, with no file of its own.
	if !e.flagRender(ka) {
		t.Fatal("B's commit didn't flag A to render")
	}
	if res := e.run(w, ka); !res.Committed || res.Applied != 0 {
		t.Fatalf("A's render run %+v, want a commit with nothing applied", res)
	}
	if got := e.requestDetail("sa", "bx"); got != "4 events" {
		t.Fatalf("A after B: %q, want the whole batch", got)
	}
	e.drain(w)
	e.check()
	// Settled: another run of each writes nothing, and nothing is flagged.
	for _, k := range []string{ka, kb} {
		e.flag(k)
		if res := e.run(w, k); res.Committed {
			t.Fatalf("%s: an idle run committed", k)
		}
	}
	if e.flags() != 0 {
		t.Fatal("flags left")
	}
}

func TestTwoCommitsOfOneBatchAtOnceStillSeeEachOther(t *testing.T) {
	// A and B commit their first parts of one batch at the same time: neither render saw the
	// other's part. The batch lock orders the commits: the second sees the first's part and
	// flags it, and itself; after the drain both count the whole batch.
	e := newEnv(t)
	t0 := e.clock.Now().UnixNano()
	ka, kb := sessionKey("sa"), sessionKey("sb")
	e.putFile([]records.Record{e.batchEvent("sa", "by", 1, t0+1), e.batchCall("by", "sa", t0+2),
		e.batchEvent("sb", "by", 2, t0+3), e.batchEvent("sb", "by", 3, t0+4)}, 0, e.clock.Now())
	release := make(chan struct{})
	held := make(chan struct{})
	var once sync.Once
	wa := e.worker("wa", withHook(func(step, key string) error {
		if step == "rows" && key == ka {
			once.Do(func() {
				close(held)
				<-release
			})
		}
		return nil
	}))
	wb := e.worker("wb")
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := wa.RunKey(e.ctx, ka); err != nil {
			t.Error(err)
		}
	}()
	<-held // A's rows are sent (its part, its lock taken), not committed
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := wb.RunKey(e.ctx, kb); err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-done:
		t.Fatal("B committed while A held the batch's lock")
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	wg.Wait()
	<-done
	e.drain(wa, wb)
	for _, sid := range []string{"sa"} {
		if got := e.requestDetail(sid, "by"); got != "3 events" {
			t.Fatalf("%s: %q, want the whole batch", sid, got)
		}
	}
	e.check()
}

// detailObject is a call's detail object as ingest writes it: its raw rows, gzipped.
func detailObject(t *testing.T, rows ...map[string]any) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	for _, r := range rows {
		line, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		zw.Write(line)
		zw.Write([]byte("\n"))
	}
	zw.Close()
	return b.Bytes()
}

func (e *env) drawerData(id string) map[string]any {
	e.t.Helper()
	var data string
	// As the drawer's statement rebuilds it (api pgstore.py _drawer_sql).
	if err := e.db.QueryRow(e.ctx, `SELECT replace(format(t.body, VARIADIC string_to_array(r.data_vals, E'\x1f')),
		'"stepCount":null', '"stepCount":' || coalesce(c.n::text, 'null'))
		FROM drawer_requests r JOIN drawer_templates t ON t.key = r.key AND t.id = r.data_tpl
		LEFT JOIN drawer_step_counts c ON c.session_id = r.step_session AND c.prompt_id = r.step_prompt AND c.key = r.key
		WHERE r.request_id = $1`, id).Scan(&data); err != nil {
		e.t.Fatalf("drawer row %s: %v", id, err)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(data), &out); err != nil {
		e.t.Fatal(err)
	}
	return out
}

func (e *env) putDetail(id string, start int64) {
	e.t.Helper()
	b := detailObject(e.t.(*testing.T),
		map[string]any{"correlation_id": id, "seq": 0, "timestamp_ns": start, "phase": "request_headers", "method": "POST",
			"path": "/v1/messages", "headers": map[string]string{"x-claude-code-session-id": "sa"}},
		map[string]any{"correlation_id": id, "seq": 1, "timestamp_ns": start + 5_000_000, "phase": "response_headers",
			"status": 200, "headers": map[string]string{"request-id": "req_x", "anthropic-ratelimit-unified-status": "allowed"}},
		map[string]any{"correlation_id": id, "seq": 2, "timestamp_ns": start + 9_000_000, "phase": "response_body_chunk", "bytes": 10},
		map[string]any{"correlation_id": id, "seq": 3, "timestamp_ns": start + 20_000_000, "phase": "complete", "status": 200,
			"model": "claude-opus-5-5", "output_tokens": 3, "thinking_type": "enabled"})
	if err := e.store.Put(e.ctx, records.DetailKey(prefix, id), b); err != nil {
		e.t.Fatal(err)
	}
}

func TestADetailObjectIsReadOnceAndKeptWithTheState(t *testing.T) {
	e := newEnv(t)
	t0 := e.clock.Now().UnixNano()
	ka := sessionKey("sa")
	c := ccCall("cx", "sa", t0)
	e.putDetail("cx", t0)
	e.putFile([]records.Record{e.end(c, t0+int64(20*time.Millisecond))}, 0, e.clock.Now())
	w := e.worker("w1")
	e.run(w, ka)
	d := e.drawerData("cx")
	ks := d["keyStrip"].(map[string]any)
	// What only the detail object holds: the first body byte's time, the thinking setting.
	if ks["proxyTtftMs"] != 9.0 || ks["thinking"] != true {
		t.Fatalf("drawer row %v", ks)
	}
	e.check()
	// Read once: neither a warm run nor a cold load reads it again, and nothing is rewritten.
	if err := e.store.Delete(e.ctx, records.DetailKey(prefix, "cx")); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Second)
	e.ingest("sa", 2)
	e.run(w, ka)
	e.clock.Advance(time.Second)
	e.ingest("sa", 2)
	e.run(e.worker("w2"), ka) // cold
	if got := e.drawerData("cx")["keyStrip"].(map[string]any)["proxyTtftMs"]; got != 9.0 {
		t.Fatalf("after the object went: %v", got)
	}
}

func TestADetailObjectNotThereYetIsWaitedFor(t *testing.T) {
	e := newEnv(t)
	t0 := e.clock.Now().UnixNano()
	ka := sessionKey("sa")
	c := ccCall("cy", "sa", t0)
	e.putFile([]records.Record{e.end(c, t0+int64(20*time.Millisecond))}, 0, e.clock.Now())
	w := e.worker("w1", func(c *Config) { c.DetailWait = 2 * time.Minute })
	e.run(w, ka)
	if !e.flagRender(ka) {
		t.Fatal("the run consumed its flag while the call's detail object is missing")
	}
	if got := e.drawerData("cy")["keyStrip"].(map[string]any)["proxyTtftMs"]; got != nil {
		t.Fatalf("from its row: no body time, got %v", got)
	}
	e.putDetail("cy", t0)
	e.clock.Advance(time.Second)
	if res := e.run(w, ka); !res.Committed {
		t.Fatalf("the render run %+v", res)
	}
	if got := e.drawerData("cy")["keyStrip"].(map[string]any)["proxyTtftMs"]; got != 9.0 {
		t.Fatalf("once the object landed: %v", got)
	}
	if e.flags() != 0 {
		t.Fatal("flag left")
	}
	e.check()
	// A call whose object never comes: rendered from its row once the wait is over.
	c2 := ccCall("cz", "sa", t0+int64(time.Second))
	e.putFile([]records.Record{e.end(c2, t0+int64(2*time.Second))}, 0, e.clock.Now())
	e.run(w, ka)
	e.clock.Advance(3 * time.Minute)
	e.run(w, ka)
	if e.flags() != 0 {
		t.Fatal("still waiting after the wait")
	}
}
