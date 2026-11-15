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

func TestCollapseResentBatchKeepsTheEarliestCopy(t *testing.T) {
	first := []records.Record{event("b1", 1, "e1", "s", t0), event("b1", 2, "e2", "s", t0)}
	resent := []records.Record{event("b2", 1, "e1", "s", t0+minute), event("b2", 2, "e2", "s", t0+minute)}
	for _, rs := range [][]records.Record{append(append([]records.Record{}, first...), resent...), append(append([]records.Record{}, resent...), first...)} {
		rows := Collapse(rs)
		if len(rows.Events) != 2 {
			t.Fatalf("%d events, want 2", len(rows.Events))
		}
		for _, e := range rows.Events {
			if e.Key != "claude_code/"+e.E.ClientEventID || e.Batch != "b1" {
				t.Errorf("event %s from %s, want claude_code/<id> from b1", e.Key, e.Batch)
			}
		}
	}
}

func TestCollapseEventWithoutAClientIDKeepsCanonsKey(t *testing.T) {
	e := event("b1", 1, "", "s", t0)
	e.Event.EventKey = "b1/h123"
	rows := Collapse([]records.Record{e, e})
	if len(rows.Events) != 1 || rows.Events[0].Key != "b1/h123" {
		t.Fatalf("got %+v, want one event keyed b1/h123", rows.Events)
	}
}

func TestCollapseEventDerivations(t *testing.T) {
	tick := event("b1", 2, "e2", "s", t0)
	tick.Event.Name, tick.Event.MetadataJSON = "tengu_tool_use_progress", `{"toolName":"Bash"}`
	rows := Collapse([]records.Record{event("b1", 1, "e1", "s", t0), tick})
	byKey := map[string]Event{}
	for _, e := range rows.Events {
		byKey[e.Key] = e
	}
	if e := byKey["claude_code/e1"]; e.Marker != "frustrated" || e.Internal || e.Summary == "" {
		t.Errorf("input prompt: marker %q internal %v summary %q", e.Marker, e.Internal, e.Summary)
	}
	if e := byKey["claude_code/e2"]; !e.Internal {
		t.Error("a tool-progress tick isn't internal (#69)")
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
	if a.Email.V != "new@example.com" || a.Name.V != "Old" || a.Plan.V != "max" || a.Seen != t0+2*minute {
		t.Errorf("account %+v: want the newest email, the only name, the event's plan", a)
	}
	if len(rows.Devices) != 1 || !rows.Devices[0].OnEvents || rows.Devices[0].Platform.V != "darwin" || rows.Devices[0].Version.V != "2.0.1" {
		t.Errorf("devices %+v", rows.Devices)
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
		fmt.Sprintf("minute s/%d", t0+2*minute),
		"prompt p-s",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("dirty %v, want %v", got, want)
	}
	if !reflect.DeepEqual(rows.ReportIDs, []string{"req-a"}) {
		t.Errorf("report ids %v, want the model call's request id", rows.ReportIDs)
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
	for table, want := range map[string]int{"calls": 1, "events": 1, "accounts": 1, "session_accounts": 1} {
		if n := count(t, db, table); n != want {
			t.Errorf("%s: %d rows, want %d", table, n, want)
		}
	}
}

func TestResentAnalyticsBatchCountsOnce(t *testing.T) {
	first := []records.Record{event("b1", 1, "e1", "s", t0), event("b1", 2, "e2", "s", t0)}
	resent := []records.Record{event("b2", 1, "e1", "s", t0+minute), event("b2", 2, "e2", "s", t0+minute)}
	var sums []map[string]string
	for _, order := range [][][]records.Record{{first, resent}, {resent, first}} {
		db := fresh(t)
		for _, rs := range order {
			write(t, db, rs...)
		}
		if n := count(t, db, "events"); n != 2 {
			t.Errorf("%d events, want 2", n)
		}
		var batches []string
		if err := db.QueryRow(ctx(t), "SELECT array_agg(DISTINCT batch_call_id) FROM events").Scan(&batches); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(batches, []string{"b1"}) {
			t.Errorf("events from %v, want the first copy's batch", batches)
		}
		sums = append(sums, checksums(t, db))
	}
	if !reflect.DeepEqual(sums[0], sums[1]) {
		t.Errorf("the order the copies came in changed the tables:\n%v\n%v", sums[0], sums[1])
	}
}

func TestEqualEnvsAreStoredOnce(t *testing.T) {
	db := fresh(t)
	a, b, c := event("b1", 1, "e1", "s", t0), event("b1", 2, "e2", "s", t0), event("b1", 3, "e3", "s", t0)
	b.Event.EnvJSON = `{ "terminal": "x", "platform": "darwin" }` // a's, written otherwise
	c.Event.EnvJSON = ""
	write(t, db, a, b, c)
	write(t, db, event("b2", 1, "e4", "s", t0)) // a later file with the same env
	if n := count(t, db, "event_envs"); n != 1 {
		t.Errorf("%d envs, want 1", n)
	}
	var shared, none int
	if err := db.QueryRow(ctx(t), `SELECT count(*) FILTER (WHERE env_hash = (SELECT env_hash FROM event_envs)),
		count(*) FILTER (WHERE env_hash IS NULL) FROM events`).Scan(&shared, &none); err != nil {
		t.Fatal(err)
	}
	if shared != 3 || none != 1 {
		t.Errorf("%d events point at the env and %d at none, want 3 and 1", shared, none)
	}
	var platform string
	if err := db.QueryRow(ctx(t), "SELECT env->>'platform' FROM event_envs").Scan(&platform); err != nil || platform != "darwin" {
		t.Errorf("env platform %q (%v)", platform, err)
	}
}

func TestModelCallMarksItsReportsMinutes(t *testing.T) {
	db := fresh(t)
	rep := event("b1", 1, "e1", "s", t0+10*minute)
	rep.Event.RequestID = "req-a"
	write(t, db, rep)
	if _, err := db.Exec(ctx(t), "DELETE FROM dirty"); err != nil {
		t.Fatal(err)
	}
	write(t, db, end("a", "other", t0, t0+5))
	var n int
	if err := db.QueryRow(ctx(t), "SELECT count(*) FROM dirty WHERE kind = 'minute' AND key = $1",
		fmt.Sprintf("s/%d", t0+10*minute)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("the report's minute isn't marked dirty")
	}
}

func TestReportMarksItsCallsMinutes(t *testing.T) {
	db := fresh(t)
	write(t, db, end("a", "s", t0, t0+5))
	if _, err := db.Exec(ctx(t), "DELETE FROM dirty"); err != nil {
		t.Fatal(err)
	}
	rep := event("b1", 1, "e1", "s", t0+10*minute)
	rep.Event.Name, rep.Event.RequestID = "tengu_api_success", "req-a"
	write(t, db, rep)
	var n int
	if err := db.QueryRow(ctx(t), "SELECT count(*) FROM dirty WHERE kind = 'minute' AND key = $1",
		fmt.Sprintf("s/%d", t0)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("the reported call's minute isn't marked dirty")
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
		db.QueryRow(ctx(t), "SELECT seq FROM dirty WHERE kind = 'prompt'").Scan(&s)
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
	if n := count(t, db, "session_parents"); n != 1 {
		t.Errorf("%d session parents, want the Codex subagent's", n)
	}
}

// TestFlushCostIsFlat times a flush of 1,000 events and 50 model calls (whose
// reports it looks up) into a session that already has 1,000 events, then
// 50,000 (docs/PIPELINE.md §9: flat). Timing: run on demand with
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
			rs[i] = event(fmt.Sprintf("b%d", n), int64(i+1), fmt.Sprintf("e%d", k), "big", t0+int64(k)*1e9)
			rs[i].Event.RequestID = fmt.Sprintf("r%d", k)
		}
		for i := 0; i < 50; i++ {
			k := n*1000 + i*20
			c := end(fmt.Sprintf("c%d", k), "big", t0+int64(k)*1e9, t0+int64(k)*1e9+5)
			c.Call.RequestID = fmt.Sprintf("r%d", k)
			rs = append(rs, c)
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
	t.Logf("per record: %.1f µs at 1,000 events in the session, %.1f µs at 50,000",
		float64(at1k.Microseconds())/1050, float64(at50k.Microseconds())/1050)
	if at50k > 3*at1k {
		t.Errorf("a flush into a 50,000-event session took %v, %v at 1,000", at50k, at1k)
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
