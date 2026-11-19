package flush

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg/pgtest"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

const minute = int64(60e9)

// t0 is a minute boundary.
const t0 = int64(1_700_000_040) * 1e9

func start(id, session string, at int64) records.Record {
	return records.Record{Kind: records.KindCallStart, CallID: id, Call: &canon.Request{
		CallID: id, CorrelationID: id, SessionID: session, PromptID: "p-" + session, StartNS: at,
		Host: "api.anthropic.com", Method: "POST", Path: "/v1/messages", Product: canon.ProductClaudeCode, KeyHash: "kh1",
	}}
}

func end(id, session string, at, endAt int64) records.Record {
	r := start(id, session, at)
	r.Kind = records.KindCallEnd
	c := *r.Call
	c.EndNS, c.Status, c.Model, c.OutputTokens = endAt, 200, "claude-x", 5
	c.PersonID, c.DeviceID, c.RequestID, c.ClientVersion = "acct1", "dev1", "req-"+id, "2.0.1"
	r.Call = &c
	return r
}

func event(batch string, n int64, clientID, session string, ns int64) records.Record {
	return records.Record{Kind: records.KindClientEvent, BatchID: batch, BatchN: n, BatchCCN: n,
		Call: &canon.Request{KeyHash: "kh1"},
		Event: &canon.Event{
			EventKey: batch + "/" + clientID, CorrelationID: batch, RowNS: ns, NS: ns, Seq: n,
			Source: canon.ProductClaudeCode, Name: "tengu_input_prompt", ClientEventID: clientID,
			SessionID: session, PromptID: "p-" + session, PersonID: "acct1", DeviceID: "dev1",
			MetadataJSON: `{"is_negative":true,"subscription_type":"max"}`, EnvJSON: `{"platform":"darwin","terminal":"x"}`,
		}}
}

func sighting(acct, email, name string, ns int64) records.Record {
	return records.Record{Kind: records.KindAccount, Account: &records.Account{AccountID: acct, Email: email, Name: name, NS: ns}}
}

// --- collapse ---------------------------------------------------------------------------------

func TestCollapseEndWinsOverStart(t *testing.T) {
	for _, order := range [][]records.Record{
		{start("a", "s", t0), end("a", "s", t0, t0+5)},
		{end("a", "s", t0, t0+5), start("a", "s", t0)},
		{start("a", "s", t0), end("a", "s", t0, t0+5), start("a", "s", t0), end("a", "s", t0, t0+5)},
	} {
		rows := Collapse(order)
		if len(rows.Calls) != 1 {
			t.Fatalf("%d calls, want 1", len(rows.Calls))
		}
		if c := rows.Calls[0].R; c.EndNS != t0+5 || c.OutputTokens != 5 || c.StartNS != t0 {
			t.Errorf("got start %d end %d tokens %d, want the end's row", c.StartNS, c.EndNS, c.OutputTokens)
		}
	}
}

func TestCollapseKeepsTheEndsStartWhenItHasNone(t *testing.T) {
	e := end("a", "s", t0, t0+5)
	e.Call.StartNS = 0
	rows := Collapse([]records.Record{start("a", "s", t0), e})
	if got := rows.Calls[0].R.StartNS; got != t0 {
		t.Errorf("start_ns %d, want the start's %d", got, t0)
	}
}

func TestCollapseIdentity(t *testing.T) {
	rows := Collapse([]records.Record{
		end("a", "s", t0, t0+1), end("b", "s", t0+3*minute, t0+3*minute+1),
		sighting("acct1", "old@example.com", "Old", t0), sighting("acct1", "new@example.com", "", t0+minute),
		event("b1", 1, "e1", "s", t0+2*minute),
	})
	want := []Span{{A: "s", B: "acct1", First: t0, Last: t0 + 3*minute}}
	if !reflect.DeepEqual(rows.SessionAccounts, want) {
		t.Errorf("session_accounts %+v, want %+v", rows.SessionAccounts, want)
	}
	if want := []Span{{A: "s", B: "kh1", First: t0, Last: t0 + 3*minute}}; !reflect.DeepEqual(rows.SessionCredentials, want) {
		t.Errorf("session_credentials %+v, want %+v", rows.SessionCredentials, want)
	}
	if len(rows.Accounts) != 1 {
		t.Fatalf("accounts %+v", rows.Accounts)
	}
	a := rows.Accounts[0]
	if a.Email.V != "new@example.com" || a.Name.V != "Old" || a.Seen != t0+minute {
		t.Errorf("account %+v: want the newest email, the only name", a)
	}
	if len(rows.Devices) != 1 || rows.Devices[0].Version.V != "2.0.1" {
		t.Errorf("devices %+v", rows.Devices)
	}
}

// Analytics events aren't loaded (docs/PIPELINE.md §3.2): an event names no
// session, account, credential or device, and marks nothing.
func TestCollapseSkipsAnalyticsEvents(t *testing.T) {
	ev := event("b1", 1, "e1", "s2", t0+minute)
	ev.Event.OrgID = "org-1"
	rows := Collapse([]records.Record{ev})
	if len(rows.Calls)+len(rows.Accounts)+len(rows.SessionAccounts)+len(rows.SessionCredentials)+len(rows.Devices)+len(rows.Dirty) != 0 {
		t.Errorf("an analytics event left rows: %+v", rows)
	}
}

func TestCollapseDirty(t *testing.T) {
	rows := Collapse([]records.Record{end("a", "s", t0+5, t0+minute+5), event("b1", 1, "e1", "s", t0+2*minute)})
	var got []string
	for _, d := range rows.Dirty {
		got = append(got, d.Kind+" "+d.Key)
	}
	want := []string{
		fmt.Sprintf("cred_minute kh1/%d", t0),
		fmt.Sprintf("minute s/%d", t0),
		"step a",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("dirty %v, want %v", got, want)
	}
}

// --- Postgres ---------------------------------------------------------------------------------

func ctx(t testing.TB) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	return c
}

func fresh(t testing.TB) *pgxpool.Pool {
	t.Helper()
	p, err := pg.Open(ctx(t), pgtest.Fresh(t), 4)
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
	if err := Write(ctx(t), db, "pod-1", rs); err != nil {
		t.Fatal(err)
	}
}

func TestStartThenEndAndEndThenStart(t *testing.T) {
	db := fresh(t)
	write(t, db, start("a", "s", t0))
	write(t, db, end("a", "s", t0, t0+5))
	write(t, db, end("b", "s", t0, t0+7))
	write(t, db, start("b", "s", t0))
	for _, id := range []string{"a", "b"} {
		var endNS, out *int64
		if err := db.QueryRow(ctx(t), "SELECT end_ns, output_tokens FROM calls WHERE call_id = $1", id).Scan(&endNS, &out); err != nil {
			t.Fatal(err)
		}
		if endNS == nil || out == nil || *out != 5 {
			t.Errorf("call %s: end %v tokens %v, want its end's row", id, endNS, out)
		}
	}
}

// A call's tool list (handoff §11S) is stored once, by its hash: the call names it, tool_lists holds it.
func TestToolListIsStoredOnceByHash(t *testing.T) {
	db := fresh(t)
	list := `["Bash","Read","mcp__linear__list_issues"]`
	a, b := end("a", "s", t0, t0+5), end("b", "s", t0+10, t0+15)
	a.Call.ToolsOffered, b.Call.ToolsOffered = list, list
	write(t, db, a, b, end("c", "s", t0+20, t0+25))
	var lists, named, unnamed int
	if err := db.QueryRow(ctx(t), `SELECT (SELECT count(*) FROM tool_lists WHERE names = $1::jsonb),
		(SELECT count(DISTINCT tools_list) FROM calls WHERE tools_list IS NOT NULL),
		(SELECT count(*) FROM calls WHERE tools_list IS NULL)`, list).Scan(&lists, &named, &unnamed); err != nil {
		t.Fatal(err)
	}
	if lists != 1 || named != 1 || unnamed != 1 {
		t.Errorf("tool_lists %d, distinct lists on calls %d, calls with none %d; want 1, 1, 1", lists, named, unnamed)
	}
}

func TestSweptCallTakesItsRealEnd(t *testing.T) {
	db := fresh(t)
	write(t, db, start("a", "s", t0))
	if _, err := db.Exec(ctx(t), "UPDATE calls SET end_ns = $1, swept = true", t0+int64(6*time.Hour)); err != nil {
		t.Fatal(err)
	}
	write(t, db, end("a", "s", t0, t0+5))
	var endNS int64
	var swept bool
	if err := db.QueryRow(ctx(t), "SELECT end_ns, swept FROM calls").Scan(&endNS, &swept); err != nil {
		t.Fatal(err)
	}
	if endNS != t0+5 || swept {
		t.Errorf("end %d swept %v, want the real end", endNS, swept)
	}
}

func TestDuplicateRecordsInOneFlush(t *testing.T) {
	db := fresh(t)
	e := event("b1", 1, "e1", "s", t0)
	write(t, db, start("a", "s", t0), end("a", "s", t0, t0+5), end("a", "s", t0, t0+5), e, e,
		sighting("acct1", "x@example.com", "X", t0), sighting("acct1", "x@example.com", "X", t0))
	for table, want := range map[string]int{"calls": 1, "accounts": 1, "session_accounts": 1} {
		if n := count(t, db, table); n != want {
			t.Errorf("%s: %d rows, want %d", table, n, want)
		}
	}
}

// A call carrying back a tool call's result marks the step of the call that
// made the tool call, whatever its prompt (§5.3).
func TestResultMarksTheStepThatMadeTheToolCall(t *testing.T) {
	db := fresh(t)
	a := end("a", "s", t0, t0+5)
	a.Call.DetailJSON = `{"calls":[{"id":"tu1","tool":"Bash","commands":["go test"]}]}`
	write(t, db, a)
	if _, err := db.Exec(ctx(t), "DELETE FROM dirty"); err != nil {
		t.Fatal(err)
	}
	b := end("b", "s2", t0+minute, t0+minute+5)
	b.Call.DetailJSON = `{"results":{"tu1":{"exit":[1]}}}`
	write(t, db, b)
	var keys []string
	if err := db.QueryRow(ctx(t), "SELECT array_agg(key ORDER BY key) FROM dirty WHERE kind = 'step'").Scan(&keys); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(keys, []string{"a", "b"}) {
		t.Errorf("steps marked %v, want the result's call and the call that made the tool call", keys)
	}
}

// The credential rollups count coding agents' calls, with or without a
// credential; other calls mark none.
func TestCollapseMarksCredentialsOfAgentCalls(t *testing.T) {
	bare := end("b", "s", t0, t0+5)
	bare.Call.KeyHash = ""
	web := end("w", "", t0, t0+5)
	web.Call.Product, web.Call.Host = "", "example.com"
	var got []string
	for _, d := range Collapse([]records.Record{bare, web}).Dirty {
		if d.Kind == "cred_minute" {
			got = append(got, d.Key)
		}
	}
	if want := []string{fmt.Sprintf("/%d", t0)}; !reflect.DeepEqual(got, want) {
		t.Errorf("cred_minute %v, want %v", got, want)
	}
}

func TestDirtyIsMarkedAgain(t *testing.T) {
	db := fresh(t)
	write(t, db, start("a", "s", t0))
	seq := func() (s int64) {
		db.QueryRow(ctx(t), "SELECT seq FROM dirty WHERE kind = 'step'").Scan(&s)
		return
	}
	before := seq()
	write(t, db, end("a", "s", t0, t0+5))
	if after := seq(); after <= before {
		t.Errorf("seq %d then %d: marking again must change it", before, after)
	}
}

// corpus is a few sessions' worth of records: calls whose start and end come
// apart, a resent batch, repeated sightings, a Codex subagent.
func corpus() []records.Record {
	var rs []records.Record
	for s := 0; s < 4; s++ {
		sid := fmt.Sprintf("s%d", s)
		for c := 0; c < 12; c++ {
			id := fmt.Sprintf("%s-c%d", sid, c)
			at := t0 + int64(c)*minute/3
			rs = append(rs, start(id, sid, at), end(id, sid, at, at+int64(c+1)*1e9))
		}
		batch := "batch-" + sid
		for n := int64(1); n <= 10; n++ {
			e := event(batch, n, fmt.Sprintf("%s-e%d", sid, n), sid, t0+n*minute/2)
			e.Event.RequestID = fmt.Sprintf("%s-c%d", sid, n)
			rs = append(rs, e)
			r := event(batch+"-resent", n, fmt.Sprintf("%s-e%d", sid, n), sid, t0+n*minute/2+minute)
			rs = append(rs, r)
		}
		rs = append(rs, sighting("acct1", fmt.Sprintf("p%d@example.com", s), "P", t0+int64(s)*minute))
		// Credentials: one per session, two sightings each, and two sightings
		// of one at the same time (the greater account wins, in any order).
		rs = append(rs, credential("kh-"+sid, "acct1", "lookup", t0+int64(s)*minute), credential("kh-"+sid, "acct1", "observed", t0+int64(s+5)*minute),
			credential("kh-tie", fmt.Sprintf("acct-%d", s), "lookup", t0))
	}
	cx := end("codex-1", "child", t0, t0+9)
	cx.Call.Product, cx.Call.Host, cx.Call.Path = canon.ProductCodex, "chatgpt.com", "/backend-api/codex/responses"
	cx.Call.ExtJSON, cx.Call.ProviderJSON = `{"parent_thread_id":"s0"}`, `{"x-codex-plan-type":"plus"}`
	return append(rs, cx)
}

func load(t testing.TB, db *pgxpool.Pool, rs []records.Record, chunk int) {
	for i := 0; i < len(rs); i += chunk {
		write(t, db, rs[i:min(i+chunk, len(rs))]...)
	}
}

func TestReloadAndShuffleGiveIdenticalTables(t *testing.T) {
	rs := corpus()
	db := fresh(t)
	load(t, db, rs, 7)
	want := checksums(t, db)
	load(t, db, rs, 7)
	if got := checksums(t, db); !reflect.DeepEqual(got, want) {
		t.Errorf("reloading changed the tables:\n got %v\nwant %v", got, want)
	}
	for seed := int64(1); seed <= 3; seed++ {
		shuffled := append([]records.Record(nil), rs...)
		rand.New(rand.NewSource(seed)).Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		db := fresh(t)
		load(t, db, shuffled, int(seed)*5)
		if got := checksums(t, db); !reflect.DeepEqual(got, want) {
			t.Errorf("seed %d: shuffled order gave other tables:\n got %v\nwant %v", seed, got, want)
		}
	}
	if n := count(t, db, "session_edges"); n != 1 {
		t.Errorf("%d session parents, want the Codex subagent's", n)
	}
}

// TestFlushCostIsFlat times a flush of 1,000 model calls (each carrying a tool
// result, whose step it looks up) into a session that already has 1,000
// calls, then 50,000 (docs/PIPELINE.md §9: flat). Timing: run on demand with
// AINTERCEPT_FLUSH_COST=1.
func TestFlushCostIsFlat(t *testing.T) {
	if os.Getenv("AINTERCEPT_FLUSH_COST") == "" {
		t.Skip("set AINTERCEPT_FLUSH_COST=1 to time flushes")
	}
	db := fresh(t)
	batch := func(n int) []records.Record {
		rs := make([]records.Record, 1000)
		for i := range rs {
			k := n*1000 + i
			rs[i] = end(fmt.Sprintf("c%d", k), "big", t0+int64(k)*1e9, t0+int64(k)*1e9+5)
			rs[i].Call.DetailJSON = fmt.Sprintf(`{"calls":[{"id":"tu%d","tool":"Bash"}],"results":{"tu%d":{"exit":[0]}}}`, k, k-1)
		}
		return rs
	}
	timed := func(n int) time.Duration {
		var best time.Duration
		for i := 0; i < 3; i++ { // three flushes at this size: the fastest
			rs := batch(n + i)
			start := time.Now()
			write(t, db, rs...)
			if d := time.Since(start); best == 0 || d < best {
				best = d
			}
		}
		return best
	}
	write(t, db, batch(0)...)
	n := 1
	at1k := timed(n)
	for n += 3; n < 50; n++ {
		write(t, db, batch(n)...)
	}
	at50k := timed(n)
	t.Logf("per record: %.1f µs at 1,000 calls in the session, %.1f µs at 50,000",
		float64(at1k.Microseconds())/1000, float64(at50k.Microseconds())/1000)
	if at50k > 3*at1k {
		t.Errorf("a flush into a 50,000-call session took %v, %v at 1,000", at50k, at1k)
	}
}

func count(t testing.TB, db *pgxpool.Pool, table string) (n int) {
	t.Helper()
	if err := db.QueryRow(ctx(t), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func checksums(t testing.TB, db *pgxpool.Pool) map[string]string {
	t.Helper()
	ss, err := Summarize(ctx(t), db)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, s := range ss {
		out[s.Table] = fmt.Sprintf("%d %s", s.Rows, s.Checksum)
	}
	keys := make([]string, 0, len(out))
	for k := range out {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) != len(Tables) {
		t.Fatalf("summarized %v, want %v", keys, Tables)
	}
	return out
}
