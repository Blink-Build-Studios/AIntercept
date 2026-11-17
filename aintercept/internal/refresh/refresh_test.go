package refresh

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/flush"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg/pgtest"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

const minute = minuteNS

// t0 is an hour boundary.
const t0 = int64(1_700_002_800) * 1e9

// --- records ------------------------------------------------------------------------------------

// call is a Claude Code model call of session s at `at`, ended, with output tokens.
func call(id, s string, at int64) records.Record {
	return records.Record{Kind: records.KindCallEnd, CallID: id, Call: &canon.Request{
		CallID: id, CorrelationID: id, SessionID: s, PromptID: "p-" + s, StartNS: at, EndNS: at + 1e9,
		Host: "api.anthropic.com", Method: "POST", Path: "/v1/messages", Product: canon.ProductClaudeCode,
		KeyHash: "kh1", RequestClass: "main", RequestID: "req-" + id, Status: 200, OutputTokens: 5, IP: "10.0.0.1",
	}}
}

// ev is a Claude Code analytics event of session s at `at`: name with
// metadata. Analytics events aren't loaded (docs/PIPELINE.md §3.2): a test
// writes them to show they change nothing.
func ev(batch string, n int64, s string, at int64, name, md string) records.Record {
	id := fmt.Sprintf("%s-%d", batch, n)
	return records.Record{Kind: records.KindClientEvent, BatchID: batch, BatchN: n, BatchCCN: n, Call: &canon.Request{KeyHash: "kh1"},
		Event: &canon.Event{EventKey: batch + "/" + id, CorrelationID: batch, RowNS: at, NS: at, Seq: n,
			Source: canon.ProductClaudeCode, Name: name, ClientEventID: id, SessionID: s, PromptID: "p-" + s, MetadataJSON: md}}
}

// --- Postgres -----------------------------------------------------------------------------------

func ctx(t testing.TB) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	return c
}

func fresh(t testing.TB) *pgxpool.Pool {
	t.Helper()
	p, err := pg.Open(ctx(t), pgtest.Fresh(t), 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	if err := pg.Apply(ctx(t), p); err != nil {
		t.Fatal(err)
	}
	return p
}

func write(t testing.TB, db *pgxpool.Pool, rs ...records.Record) {
	t.Helper()
	if err := flush.Write(ctx(t), db, "pod-1", rs); err != nil {
		t.Fatal(err)
	}
}

func drain(t testing.TB, db *pgxpool.Pool) {
	t.Helper()
	if _, err := Drain(ctx(t), db); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := db.QueryRow(ctx(t), "SELECT count(*) FROM dirty WHERE kind = ANY($1)", Kinds).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left > 0 {
		t.Fatalf("%d dirty rows left after a drain", left)
	}
}

// row is one row of a query as a map, jsonb and numbers decoded.
func row(t testing.TB, db *pgxpool.Pool, sql string, args ...any) map[string]any {
	t.Helper()
	rows, err := db.Query(ctx(t), "SELECT to_jsonb(x) FROM ("+sql+") x", args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("no row for %s %v", sql, args)
	}
	var b []byte
	if err := rows.Scan(&b); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func expect(t testing.TB, got map[string]any, want map[string]any) {
	t.Helper()
	for k, w := range want {
		g := got[k]
		if n, ok := w.(int); ok {
			w = float64(n)
		}
		if !reflect.DeepEqual(g, w) {
			t.Errorf("%s = %v, want %v", k, g, w)
		}
	}
}

func checkScratch(t testing.TB, db *pgxpool.Pool) {
	t.Helper()
	res, err := Check(ctx(t), db)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if !r.Equal() {
			t.Errorf("%s: %d rows, %d from scratch, %d missing, %d extra", r.Table, r.Rows, r.Scratch, r.Missing, r.Extra)
		}
	}
}

// --- rollups ------------------------------------------------------------------------------------

func TestSessionMinute(t *testing.T) {
	db := fresh(t)
	a, b := call("a", "s", t0+1e9), call("b", "s", t0+2e9)
	b.Call.RequestClass, b.Call.InputTokens, b.Call.CacheReadTokens, b.Call.OutputTokens = "subagent", 10, 20, 0
	b.Call.ToolUsesJSON = `[{"tool":"Bash","count":3},{"tool":"Read"}]`
	title := call("t", "s", t0+3e9)
	title.Call.HiddenKind = "title"
	count := call("n", "s", t0+4e9)
	count.Call.Path = "/v1/messages/count_tokens"
	post := call("post", "s", t0+5e9) // an analytics post: a call like any other
	post.Call.Path = "/api/event_logging/batch"
	other := call("o", "s", t0+minute) // the next minute
	write(t, db, a, b, title, count, post, other,
		ev("B", 1, "s", t0+6e9, "tengu_api_success", `{"requestId":"req-a","costUSD":0.5}`),
		ev("B", 2, "s", t0+8e9, "tengu_input_prompt", `{"is_negative":"true"}`),
		ev("B", 3, "s", t0+9e9, "tengu_cancel", `{}`),
	)
	drain(t, db)
	m := row(t, db, "SELECT * FROM session_minutes WHERE session_id = 's' AND minute_ns = $1", t0)
	expect(t, m, map[string]any{
		"model_calls": 2, "main_calls": 1, "subagent_calls": 1, "hidden_calls": 1,
		"input_tokens": 10, "cache_read_tokens": 20, "cache_write_tokens": nil, "output_tokens": 5, "outputs_known": 1,
		"context_sum": 30, "tool_calls": 4,
		"requests":        map[string]any{"model/kh1": float64(2), "count/kh1": float64(1), "analytics/kh1": float64(1)},
		"requests_hidden": map[string]any{"model/kh1": float64(1)},
	})
	for _, gone := range []string{"events", "unreported", "missed", "cost_usd", "frustrated"} {
		if _, ok := m[gone]; ok {
			t.Errorf("session_minutes still has %s (analytics)", gone)
		}
	}
	h := row(t, db, "SELECT * FROM session_hours WHERE session_id = 's' AND hour_ns = $1", t0)
	expect(t, h, map[string]any{"model_calls": 3, "output_tokens": 10,
		"requests": map[string]any{"model/kh1": float64(3), "count/kh1": float64(1), "analytics/kh1": float64(1)}})
	checkScratch(t, db)
}

// A Codex call that never really ended names no session today (its row is
// read from its headers): it isn't the thread's model call or Requests row.
func TestUnfinishedCodexCallsNameNoSession(t *testing.T) {
	db := fresh(t)
	codex := func(id string, at int64) records.Record {
		r := call(id, "x", at)
		r.Call.Product, r.Call.Host, r.Call.Path = canon.ProductCodex, "chatgpt.com", "/backend-api/codex/responses"
		return r
	}
	open, swept := codex("open", t0+1e9), codex("swept", t0+2e9)
	open.Kind, open.Call.EndNS = records.KindCallStart, 0
	swept.Kind, swept.Call.EndNS = records.KindCallStart, 0
	write(t, db, codex("done", t0), open, swept)
	if _, err := db.Exec(ctx(t), "UPDATE calls SET end_ns = start_ns + 1, swept = true WHERE call_id = 'swept'"); err != nil {
		t.Fatal(err)
	}
	drain(t, db)
	expect(t, row(t, db, "SELECT model_calls, requests FROM session_minutes WHERE session_id = 'x'"),
		map[string]any{"model_calls": 1, "requests": map[string]any{"model/kh1": float64(1)}})
	expect(t, row(t, db, "SELECT sum(n) AS n FROM cred_minutes"), map[string]any{"n": 3}) // the overview counts them
}

func TestSessionKeys(t *testing.T) {
	db := fresh(t)
	c := call("a", "s", t0)
	c.Call.ToolUsesJSON = `[{"kind":"mcp","server":"gh","tool":"pr"},{"kind":"builtin","tool":"Bash"}]`
	d := call("b", "s", t0+2*minute) // another minute of the hour, its tool calls recorded
	d.Call.DetailJSON = `{"calls":[{"id":"t1","tool":"Read","file":"a.go"},{"id":"t2","tool":"Bash","commands":["git commit"]}]}`
	write(t, db, c, d, ev("B", 1, "s", t0+1e9, "tengu_run_hook", `{"hookName":"pre"}`))
	drain(t, db)
	rows, err := db.Query(ctx(t), "SELECT kind || ' ' || key || ' ' || n FROM session_keys WHERE session_id = 's' AND hour_ns = $1 ORDER BY 1", t0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		got = append(got, s)
	}
	want := []string{
		"call_builtin Bash 1", `call_mcp ["gh", "pr"] 1`, "call_prompt p-s 2", "ip 10.0.0.1 2",
		// The work its steps' tool calls show (prompts.go stepItems): an MCP call and a Bash call
		// (content-free), a read of a.go and a git commit (recorded).
		"w:commit  1", "w:mcp gh 1", "w:read go 1", "w:read_file a.go 1", "w:run  1", "w:run git commit 1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("keys:\n got %q\nwant %q", got, want)
	}
	checkScratch(t, db)
}

func TestCredRollups(t *testing.T) {
	db := fresh(t)
	hidden := call("h", "s", t0+1e9)
	hidden.Call.HiddenKind = "prewarm"
	ws := call("ws", "x", t0+2e9) // a Codex WebSocket connection: an agent call, not a transaction
	ws.Call.Product, ws.Call.Host, ws.Call.Path, ws.Call.Method = canon.ProductCodex, "chatgpt.com", "/backend-api/codex/responses", "GET"
	ws.Call.KeyHash = "kh2"
	bare := call("bare", "s", t0+3e9) // no credential
	bare.Call.KeyHash = ""
	web := call("web", "", t0+4e9) // not a coding agent's
	web.Call.Product, web.Call.Host = "", "example.com"
	write(t, db, call("a", "s", t0), hidden, ws, bare, web, call("late", "s", t0+61*minute))
	drain(t, db)
	rows, err := db.Query(ctx(t), "SELECT key_hash || ' ' || hour_ns || ' ' || kind || ' ' || hidden || ' ' || agent || ' ' || n || ' ' || n_tx FROM cred_hours ORDER BY 1")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		got = append(got, s)
	}
	h1 := t0 + hourNS
	want := []string{
		fmt.Sprintf(" %d model false claude_code 1 1", t0),
		fmt.Sprintf("kh1 %d model false claude_code 1 1", t0),
		fmt.Sprintf("kh1 %d model true claude_code 1 1", t0),
		fmt.Sprintf("kh1 %d model false claude_code 1 1", h1),
		fmt.Sprintf("kh2 %d model false codex 1 0", t0),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cred_hours:\n got %q\nwant %q", got, want)
	}
	checkScratch(t, db)
}

// --- the refresh protocol -----------------------------------------------------------------------

func refreshAndRelease(t testing.TB, db *pgxpool.Pool, c Claim) {
	t.Helper()
	if err := Refresh(ctx(t), db, c); err != nil {
		t.Fatal(err)
	}
	if _, err := Release(ctx(t), db, c); err != nil {
		t.Fatal(err)
	}
}

// A row marked again while it's being refreshed stays dirty, unclaimed, and
// is refreshed again.
func TestMarkedDuringRefreshStaysDirty(t *testing.T) {
	db := fresh(t)
	write(t, db, call("a", "s", t0))
	claims, err := ClaimRows(ctx(t), db, 100)
	if err != nil {
		t.Fatal(err)
	}
	var mine Claim
	for _, c := range claims {
		if c.Kind == "minute" {
			mine = c
		} else {
			refreshAndRelease(t, db, c)
		}
	}
	if mine.Kind == "" {
		t.Fatalf("claimed %v, want the minute", claims)
	}
	again, _ := ClaimRows(ctx(t), db, 100)
	for _, c := range again {
		if c.Kind == mine.Kind && c.Key == mine.Key {
			t.Fatalf("claimed %v again while claimed", c)
		}
		refreshAndRelease(t, db, c)
	}
	write(t, db, call("b", "s", t0+1e9)) // marks the minute again
	if err := Refresh(ctx(t), db, mine); err != nil {
		t.Fatal(err)
	}
	gone, err := Release(ctx(t), db, mine)
	if err != nil {
		t.Fatal(err)
	}
	if gone {
		t.Fatal("released a row marked again during its refresh")
	}
	var claimed *time.Time
	if err := db.QueryRow(ctx(t), "SELECT claimed_at FROM dirty WHERE kind = $1 AND key = $2", mine.Kind, mine.Key).Scan(&claimed); err != nil {
		t.Fatal(err)
	}
	if claimed != nil {
		t.Error("the row is still claimed")
	}
	drain(t, db)
	expect(t, row(t, db, "SELECT model_calls FROM session_minutes WHERE session_id = 's'"), map[string]any{"model_calls": 2})
}

// Two minutes of one hour refreshed at once: the hour, refreshed from a
// snapshot that lacks the second minute, stays dirty and ends right.
func TestNoLostUpdateBetweenTwoMinutesOfAnHour(t *testing.T) {
	db := fresh(t)
	write(t, db, call("a", "s", t0), call("b", "s", t0+minute))
	all, err := ClaimRows(ctx(t), db, 100)
	if err != nil {
		t.Fatal(err)
	}
	var m1, m2 Claim
	for _, c := range all {
		switch {
		case c.Kind != "minute":
			refreshAndRelease(t, db, c)
		case c.Key == fmt.Sprintf("s/%d", t0):
			m1 = c
		default:
			m2 = c
		}
	}
	// Worker A refreshes minute 1, which marks the hour; worker B claims the
	// hour and refreshes it while worker C's refresh of minute 2 commits.
	if err := Refresh(ctx(t), db, m1); err != nil {
		t.Fatal(err)
	}
	Release(ctx(t), db, m1)
	claimed, err := ClaimRows(ctx(t), db, 100)
	var hour []Claim
	for _, c := range claimed {
		if c.Kind == "hour" {
			hour = append(hour, c)
		} else {
			refreshAndRelease(t, db, c)
		}
	}
	if err != nil || len(hour) != 1 {
		t.Fatalf("claimed %v %v, want the hour", claimed, err)
	}
	if err := Refresh(ctx(t), db, hour[0]); err != nil {
		t.Fatal(err)
	}
	if err := Refresh(ctx(t), db, m2); err != nil { // commits after B's read, before B releases
		t.Fatal(err)
	}
	Release(ctx(t), db, m2)
	if gone, _ := Release(ctx(t), db, hour[0]); gone {
		t.Fatal("the hour was released though minute 2 marked it during its refresh")
	}
	drain(t, db)
	expect(t, row(t, db, "SELECT model_calls FROM session_hours WHERE session_id = 's'"), map[string]any{"model_calls": 2})
}

// Two workers drain while flushes keep landing in one hour: no update is
// lost, and the rollups end equal to the facts from scratch.
func TestConcurrentWorkers(t *testing.T) {
	db := fresh(t)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := Drain(ctx(t), db); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 60; i++ {
		at := t0 + int64(r.Intn(60))*minute + int64(i)
		c := call(fmt.Sprintf("c%d", i), "s", at)
		c.Call.PromptID = fmt.Sprintf("p%d", i%4) // four prompts, their steps refreshed by both workers
		c.Call.DetailJSON = fmt.Sprintf(`{"calls":[{"id":"tu%d","tool":"Bash","commands":["go test"]}],"results":{"tu%d":{"exit":[%d]}}}`, i, i-1, i%2)
		write(t, db, c)
	}
	close(stop)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	drain(t, db)
	expect(t, row(t, db, "SELECT model_calls FROM session_hours WHERE session_id = 's' AND hour_ns = $1", t0), map[string]any{"model_calls": 60})
	checkScratch(t, db)
}

// A prompt, session or account-day row is claimed only once it's been dirty
// for Coalesce.
func TestCoalescedKindsWait(t *testing.T) {
	db := fresh(t)
	defer func(c time.Duration) { Coalesce = c }(Coalesce)
	Coalesce = 5 * time.Second
	if _, err := db.Exec(ctx(t), "INSERT INTO dirty (kind, key) VALUES ('session', 's'), ('hour', 's/0')"); err != nil {
		t.Fatal(err)
	}
	got, err := ClaimRows(ctx(t), db, 10)
	if err != nil || len(got) != 1 || got[0].Kind != "hour" {
		t.Fatalf("claimed %v %v, want only the hour", got, err)
	}
	db.Exec(ctx(t), "UPDATE dirty SET since = now() - interval '6 seconds' WHERE kind = 'session'")
	if got, err := ClaimRows(ctx(t), db, 10); err != nil || len(got) != 1 || got[0].Kind != "session" {
		t.Fatalf("claimed %v %v, want the session after 5 s", got, err)
	}
}

// A claim whose worker died is taken again once it's stale.
func TestStaleClaimIsTakenAgain(t *testing.T) {
	db := fresh(t)
	write(t, db, call("a", "s", t0))
	if _, err := ClaimRows(ctx(t), db, 100); err != nil {
		t.Fatal(err)
	}
	db.Exec(ctx(t), "UPDATE dirty SET claimed_at = now() - interval '2 minutes'")
	drain(t, db)
	expect(t, row(t, db, "SELECT model_calls FROM session_minutes WHERE session_id = 's'"), map[string]any{"model_calls": 1})
}

// corpus is three sessions' calls, their starts and ends apart: prompts of
// several steps (some subagents'), recorded tool calls whose results a later
// call carries back (one a later prompt's), a turn after an interrupt, a
// title side request, and analytics events (not loaded).
func corpus() []records.Record {
	var rs []records.Record
	for s := 0; s < 3; s++ {
		sid := fmt.Sprintf("s%d", s)
		for c := 0; c < 20; c++ {
			at := t0 + int64(c)*7*minute + int64(s)
			x := call(fmt.Sprintf("%s-c%d", sid, c), sid, at)
			x.Call.PromptID = fmt.Sprintf("%s-p%d", sid, c/6)
			switch {
			case c%5 == 0:
				x.Call.HiddenKind = "title"
			case c%4 == 1:
				x.Call.RequestClass = "subagent"
			}
			cmd := []string{"go test", "npm test", "git commit"}[c%3]
			detail := fmt.Sprintf(`{"calls":[{"id":"%s-t%d","tool":"Bash","commands":[%q]},{"id":"%s-r%d","tool":"Read","file":"f%d.go"}]`,
				sid, c, cmd, sid, c, c%4)
			if c > 0 {
				detail += fmt.Sprintf(`,"results":{"%s-t%d":{"exit":[%d]},"%s-r%d":{"error":true}}`, sid, c-1, c%3, sid, c-1)
			}
			if c%6 == 0 {
				detail += fmt.Sprintf(`,"said":[{"role":"user","text":"turn %d"}],"prompt":"turn %d","promptChars":%d`, c, c, 6+len(fmt.Sprint(c)))
				if c == 12 {
					detail += `,"interrupted":true`
				}
			}
			x.Call.DetailJSON = detail + "}"
			st := x
			st.Kind, st.Call = records.KindCallStart, &canon.Request{CallID: x.CallID, CorrelationID: x.CallID, SessionID: sid,
				PromptID: x.Call.PromptID, StartNS: at, Host: x.Call.Host, Path: x.Call.Path, Method: "POST", Product: x.Call.Product, KeyHash: "kh1"}
			rs = append(rs, st, x)
			if c%2 == 0 {
				rs = append(rs, ev("B"+sid, int64(c+1), sid, at+int64(c)*minute, "tengu_tool_use_success", `{"toolName":"Edit"}`))
			}
		}
	}
	return rs
}

// Loaded in any order, with refreshes in between, the rollups (each step
// refreshed alone and added to its prompt) equal the facts from scratch (each
// prompt computed from all its calls at once).
func TestShuffledLoadsEqualScratch(t *testing.T) {
	rs := corpus()
	for seed := int64(1); seed <= 3; seed++ {
		shuffled := append([]records.Record(nil), rs...)
		rand.New(rand.NewSource(seed)).Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		db := fresh(t)
		for i := 0; i < len(shuffled); i += 7 {
			write(t, db, shuffled[i:min(i+7, len(shuffled))]...)
			if i%21 == 0 {
				drain(t, db)
			}
		}
		drain(t, db)
		checkScratch(t, db)
	}
}

// --- housekeeping -------------------------------------------------------------------------------

func TestSweepClosesOpenCallsInDataTime(t *testing.T) {
	db := fresh(t)
	open := call("a", "s", t0)
	open.Kind, open.Call.EndNS = records.KindCallStart, 0
	ws := call("w", "s", t0)
	ws.Kind, ws.Call.EndNS, ws.Call.Transport = records.KindCallStart, 0, "websocket"
	write(t, db, open, ws)
	drain(t, db)
	sweep := func(clock int64) int64 {
		if _, err := db.Exec(ctx(t), "UPDATE ingest_pods SET last_ns = $1", clock); err != nil {
			t.Fatal(err)
		}
		n, err := Sweep(ctx(t), db)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := sweep(t0 + int64(5*time.Hour)); n != 0 {
		t.Fatalf("swept %d before 6 hours of data", n)
	}
	if n := sweep(t0 + int64(7*time.Hour)); n != 1 {
		t.Fatalf("swept %d after 7 hours, want the HTTP call", n)
	}
	expect(t, row(t, db, "SELECT end_ns, swept FROM calls WHERE call_id = 'a'"), map[string]any{"end_ns": float64(t0 + int64(6*time.Hour)), "swept": true})
	var dirty int
	db.QueryRow(ctx(t), "SELECT count(*) FROM dirty WHERE kind = 'minute' AND key = $1", fmt.Sprintf("s/%d", t0)).Scan(&dirty)
	if dirty != 1 {
		t.Error("the swept call's minute isn't dirty")
	}
	if n := sweep(t0 + int64(8*24*time.Hour)); n != 1 {
		t.Fatalf("swept %d after 8 days, want the WebSocket", n)
	}
	write(t, db, call("a", "s", t0)) // its real end replaces the sweep's
	expect(t, row(t, db, "SELECT end_ns, swept FROM calls WHERE call_id = 'a'"), map[string]any{"end_ns": float64(t0 + 1e9), "swept": false})
	drain(t, db)
	checkScratch(t, db)
}

func TestRetainDeletesOldFactsAndRollups(t *testing.T) {
	db := fresh(t)
	now := time.Unix(0, t0+20*24*hourNS)
	write(t, db, call("old", "s", t0), ev("B", 1, "s", t0, "tengu_api_success", `{}`), call("new", "s", now.UnixNano()-hourNS))
	drain(t, db)
	if _, err := Retain(ctx(t), db, 15, now); err != nil {
		t.Fatal(err)
	}
	for table, want := range map[string]int{"calls": 1, "session_minutes": 1, "session_hours": 1,
		"session_minute_keys": 2, "session_keys": 2, "cred_minutes": 1, "cred_hours": 1} {
		var n int
		db.QueryRow(ctx(t), "SELECT count(*) FROM "+table).Scan(&n)
		if n != want {
			t.Errorf("%s: %d rows after retention, want %d", table, n, want)
		}
	}
}

func TestLagCoversDirtyRows(t *testing.T) {
	db := fresh(t)
	db.Exec(ctx(t), "INSERT INTO dirty (kind, key, since) VALUES ('prompt', 'p', now() - interval '1 hour'), ('minute', 's/0', now() - interval '20 seconds')")
	lag, err := Lag(ctx(t), db)
	if err != nil {
		t.Fatal(err)
	}
	if lag < time.Hour || lag > time.Hour+time.Minute {
		t.Errorf("lag %v, want the prompt row's ~1 h", lag)
	}
}

// TestRefreshCostIsFlat times the refresh one new call causes (its step, its
// prompt's row, its minute, hour and credential rows, the session and its
// account's day) in a prompt that already has 1,000 steps, then 25,000
// (docs/PIPELINE.md §9: flat). Each step has recorded tool calls, and carries
// back the previous one's results. A step refresh reads its own call and its
// tool calls' results; its prompt's row reads the prompt's keys (its distinct
// tools, files and commands) and a few indexed rows of its steps, never the
// prompt's other steps. Timing: run on demand with AINTERCEPT_REFRESH_COST=1.
func TestRefreshCostIsFlat(t *testing.T) {
	if os.Getenv("AINTERCEPT_REFRESH_COST") == "" {
		t.Skip("set AINTERCEPT_REFRESH_COST=1 to time refreshes")
	}
	db := fresh(t)
	n := 0 // steps so far
	step := func(i int) records.Record {
		c := call(fmt.Sprintf("g%06d", i), "big", t0+int64(i)*1e9)
		c.Call.PromptID, c.Call.PersonID = "big-prompt", "acct"
		c.Call.DetailJSON = fmt.Sprintf(`{"calls":[{"id":"t%d","tool":"Bash","commands":["go test","rg"]},{"id":"r%d","tool":"Read","file":"src/f%d.go"}],`+
			`"results":{"t%d":{"exit":[%d]},"r%d":{}}}`, i, i, i%200, i-1, i%2, i-1)
		return c
	}
	grow := func(to int) {
		for n < to {
			var rs []records.Record
			for i := 0; i < 1000 && n < to; i, n = i+1, n+1 {
				rs = append(rs, step(n))
			}
			write(t, db, rs...)
			drain(t, db)
		}
		if _, err := db.Exec(ctx(t), "ANALYZE"); err != nil { // as autovacuum keeps a live database's statistics
			t.Fatal(err)
		}
	}
	timed := func() time.Duration {
		var best time.Duration
		var kinds map[string]time.Duration
		for k := 0; k < 5; k++ {
			write(t, db, step(n))
			n++
			start := time.Now()
			byKind := map[string]time.Duration{}
			for {
				claims, err := ClaimRows(ctx(t), db, 100)
				if err != nil {
					t.Fatal(err)
				}
				if len(claims) == 0 {
					break
				}
				for _, c := range claims {
					s := time.Now()
					refreshAndRelease(t, db, c)
					byKind[c.Kind] += time.Since(s)
				}
			}
			if d := time.Since(start); best == 0 || d < best {
				best, kinds = d, byKind
			}
		}
		t.Logf("at %d steps: %v by kind", n, kinds)
		return best
	}
	grow(1000)
	at1k := timed()
	grow(25000)
	at25k := timed()
	t.Logf("refresh after one new call: %v at 1,000 steps in the prompt, %v at 25,000", at1k, at25k)
	if at25k > 3*at1k {
		t.Errorf("a refresh in a 25,000-step prompt took %v, %v at 1,000", at25k, at1k)
	}
}
