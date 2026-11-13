package worker

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg/pgtest"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/render"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/rollup"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream/state"
)

// The harness (docs/PIPELINE.md, Testing): each test gets its own Postgres
// database (pgtest.Fresh), an in-memory S3 with S3's semantics, a fake clock,
// and every record ingested so far, the one-pass reference's input. The
// check writes that reference with the same writer into a schema of its own
// (ref) and compares every stored table with it, row for row, in SQL.

const prefix = "p"

var errCrash = errors.New("crash")

type env struct {
	t     testing.TB
	ctx   context.Context
	clock *stream.FakeClock
	store *stream.MemStore
	dsn   string
	db    *pgxpool.Pool // the test's own connections: flags, reads, the check
	all   []records.Record
	// storedOnly checks the stored inputs only (a test with a renderer of its own).
	storedOnly bool
	seq        int64
	n          int64
	ref        bool
}

func newEnv(t testing.TB) *env {
	t.Helper()
	dsn := pgtest.Fresh(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	db, err := pg.Open(ctx, dsn, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := pg.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	clock := stream.NewFakeClock(time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC))
	return &env{t: t, ctx: ctx, clock: clock, store: stream.NewMemStore(clock), dsn: dsn, db: db}
}

func (e *env) cfg(name string) Config {
	return Config{Store: e.store, Prefix: prefix, Name: name, DSN: e.dsn, Clock: e.clock, Parallel: 2}
}

// worker is a worker named name, with set applied to its config. It's killed
// (its connections closed, as a crash would) when the test ends.
func (e *env) worker(name string, set ...func(*Config)) *Worker {
	e.t.Helper()
	c := e.cfg(name)
	for _, f := range set {
		f(&c)
	}
	w, err := New(e.ctx, c)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(w.Kill)
	return w
}

func withHook(h func(step, key string) error) func(*Config) { return func(c *Config) { c.Hook = h } }

func crashAt(step string) func(string, string) error {
	return func(s, _ string) error {
		if s == step {
			return errCrash
		}
		return nil
	}
}

// --- records ------------------------------------------------------------------

func (e *env) id() string {
	e.n++
	return fmt.Sprintf("inc-%020d", e.n)
}

func (e *env) finish(r records.Record) records.Record {
	r.Version = records.Version
	if r.RecordID == "" {
		r.RecordID = e.id()
	}
	records.Clean(&r)
	r.RoutingKey = records.RoutingKey(&r)
	r.Bucket = int32(records.BucketOf(r.RoutingKey, records.DefaultBucketBits))
	return r
}

// ccCall is a Claude Code model call of session sid that started at start.
func ccCall(id, sid string, start int64) *canon.Request {
	return &canon.Request{CallID: id, CorrelationID: id, StartNS: start, Host: "api.anthropic.com", Method: "POST",
		Path: "/v1/messages", SessionID: sid, KeyHash: "k1", Product: canon.ProductClaudeCode}
}

func (e *env) callRec(kind string, c *canon.Request) records.Record {
	x := *c
	return e.finish(records.Record{Kind: kind, CallID: c.CallID, Call: &x})
}

func (e *env) start(c *canon.Request) records.Record { return e.callRec(records.KindCallStart, c) }

func (e *env) end(c *canon.Request, endNS int64) records.Record {
	x := *c
	x.EndNS, x.Status = endNS, 200
	return e.callRec(records.KindCallEnd, &x)
}

// call is an ended call of session sid at ts, under one of a few prompts.
func (e *env) call(sid string, ts int64) records.Record {
	c := ccCall(fmt.Sprintf("c%d", e.n+1), sid, ts-int64(time.Second))
	c.PromptID, c.RequestID, c.PersonID = fmt.Sprintf("p%d", e.n%3), fmt.Sprintf("req_%d", e.n+1), "acct-1"
	return e.end(c, ts)
}

// event is a Claude Code analytics event of session sid at ts.
func (e *env) event(sid, name string, ts int64, md string) records.Record {
	key := fmt.Sprintf("ev-%d", e.n+1)
	b := "batch-" + sid
	ev := &canon.Event{EventKey: key, CorrelationID: b, RowNS: ts, NS: ts, Seq: e.n + 1, Source: canon.ProductClaudeCode,
		Name: name, SessionID: sid, PersonID: "acct-1", PromptID: fmt.Sprintf("p%d", e.n%3), MetadataJSON: md}
	return e.finish(records.Record{Kind: records.KindClientEvent, EventKey: key, BatchID: b, BatchN: e.n + 1, BatchCCN: e.n + 1, Event: ev})
}

// rec is the i-th record of a session's mix: calls, prompts, API reports and
// tool events.
func (e *env) rec(sid string, ts int64) records.Record {
	switch e.n % 4 {
	case 0:
		return e.call(sid, ts)
	case 1:
		return e.event(sid, "tengu_input_prompt", ts, `{"prompt_length":"12"}`)
	case 2:
		return e.event(sid, "tengu_api_success", ts, fmt.Sprintf(`{"requestId":"req_%d","ttftMs":42}`, e.n))
	default:
		return e.event(sid, "tengu_tool_use_success", ts, `{"toolName":"Read"}`)
	}
}

// sessionKey is a session's routing key.
func sessionKey(sid string) string {
	r := records.Record{Kind: records.KindCallEnd, Call: &canon.Request{SessionID: sid}}
	return records.RoutingKey(&r)
}

// --- ingest -------------------------------------------------------------------

// putFile writes rs as one ingest file per key they route to, named at time
// at and carrying newest (its pod's newest raw row), and flags those keys, as
// ingest and the tail do. It returns the files' names (relative to in/).
func (e *env) putFile(rs []records.Record, newest int64, at time.Time) []string {
	e.t.Helper()
	by := map[string][]records.Record{}
	for _, r := range rs {
		by[r.RoutingKey] = append(by[r.RoutingKey], r)
	}
	keys := make([]string, 0, len(by))
	for k := range by {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var names []string
	for _, k := range keys {
		b, err := records.EncodeWithNewest(by[k], newest)
		if err != nil {
			e.t.Fatal(err)
		}
		e.seq++
		name := rollup.InName(at, "inc", e.seq)
		if err := e.store.Put(e.ctx, rollup.InPrefix(prefix, k)+name, b); err != nil {
			e.t.Fatal(err)
		}
		names = append(names, name)
	}
	e.all = append(e.all, rs...)
	e.flag(keys...)
	return names
}

// ingestAt ingests n records of session sid at time at.
func (e *env) ingestAt(sid string, n int, at time.Time) string {
	e.t.Helper()
	rs := make([]records.Record, n)
	for i := range rs {
		rs[i] = e.rec(sid, at.UnixNano()+int64(i))
	}
	return e.putFile(rs, 0, at)[0]
}

func (e *env) ingest(sid string, n int) string { return e.ingestAt(sid, n, e.clock.Now()) }

// flag flags keys as the tail does.
func (e *env) flag(keys ...string) {
	e.t.Helper()
	if len(keys) == 0 {
		return
	}
	if _, err := e.db.Exec(e.ctx, `INSERT INTO dirty_keys (key, seq, since) SELECT k, 1, $2 FROM unnest($1::text[]) k
		ON CONFLICT (key) DO UPDATE SET seq = dirty_keys.seq + 1`, keys, e.clock.Now()); err != nil {
		e.t.Fatal(err)
	}
}

type flagRow struct {
	seq      int64
	since    time.Time
	failures int
	ok       bool
}

func (e *env) flagOf(key string) flagRow {
	e.t.Helper()
	var f flagRow
	err := e.db.QueryRow(e.ctx, "SELECT seq, since, failures FROM dirty_keys WHERE key = $1", key).Scan(&f.seq, &f.since, &f.failures)
	if errors.Is(err, pgx.ErrNoRows) {
		return f
	}
	if err != nil {
		e.t.Fatal(err)
	}
	f.ok = true
	return f
}

func (e *env) flags() int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRow(e.ctx, "SELECT count(*) FROM dirty_keys").Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) keyRow(key string) KeyRow {
	e.t.Helper()
	row, _, err := readRow(e.ctx, e.db, key)
	if err != nil {
		e.t.Fatal(err)
	}
	return row
}

// count is a table's rows of key.
func (e *env) count(table, key string) int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRow(e.ctx, "SELECT count(*) FROM "+table+" WHERE key = $1", key).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// terminate closes a worker's lock connection from the server's side, as
// keepalives do for a stalled or partitioned worker.
func (e *env) terminate(pid uint32) {
	e.t.Helper()
	if _, err := e.db.Exec(e.ctx, "SELECT pg_terminate_backend($1)", int32(pid)); err != nil {
		e.t.Fatal(err)
	}
	// Wait until it's gone (pg_terminate_backend only signals it).
	for i := 0; i < 200; i++ {
		var n int
		if err := e.db.QueryRow(e.ctx, "SELECT count(*) FROM pg_stat_activity WHERE pid = $1", int32(pid)).Scan(&n); err != nil {
			e.t.Fatal(err)
		}
		if n == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.t.Fatal("the backend didn't end")
}

// --- driving --------------------------------------------------------------------

// drain ticks the workers until no key is flagged (bounded).
func (e *env) drain(ws ...*Worker) {
	e.t.Helper()
	for i := 0; i < 200; i++ {
		if e.flags() == 0 {
			return
		}
		for _, w := range ws {
			_ = w.Tick(e.ctx) // a failed run keeps its flag: the drain's bound catches what never settles
		}
		e.clock.Advance(time.Second)
	}
	e.t.Fatal("drain: keys stayed flagged")
}

func (e *env) run(w *Worker, key string) Result {
	e.t.Helper()
	res, err := w.RunKey(e.ctx, key)
	if err != nil {
		e.t.Fatalf("run %s: %v", key, err)
	}
	return res
}

// --- the check ----------------------------------------------------------------

// check fails unless every key's stored inputs and rendered rows equal one
// pass over every record ingested for it (rendered under the day its key row
// says: the day's own rule is TestFactsOfEqualsTheMerge's).
func (e *env) check() { e.t.Helper(); e.checkAt(0) }

// checkAt is check with each key's calls still waiting expired at clock.
func (e *env) checkAt(clock int64, only ...string) {
	e.t.Helper()
	e.ensureRef()
	in := e.all
	if len(only) > 0 {
		in = nil
		for _, r := range e.all {
			if contains(only, r.RoutingKey) {
				in = append(in, r)
			}
		}
	}
	byKey := map[string]map[string][]stream.Row{}
	for table, rows := range stream.ReferenceAt(stream.StateModel{}, in, clock) {
		for _, r := range rows {
			if r.Tomb {
				continue
			}
			if byKey[r.Key] == nil {
				byKey[r.Key] = map[string][]stream.Row{}
			}
			byKey[r.Key][table] = append(byKey[r.Key][table], r)
		}
	}
	var cs []pg.Changes
	var diffs []*render.Diff
	owners := append([]string(nil), only...)
	for key, tables := range byKey {
		if len(only) > 0 && !contains(only, key) {
			continue
		}
		cs = append(cs, StoredChanges(key, tables)...)
		rows := state.Rows{}
		for t, rs := range tables {
			rows[t] = map[string]any{}
			for _, r := range rs {
				rows[t][r.ID] = r.Value
			}
		}
		r, err := render.Render(key, rows, e.keyRow(key).Day)
		if err != nil {
			e.t.Fatal(err)
		}
		diffs = append(diffs, r.Diff(nil))
		if sid, ok := strings.CutPrefix(key, "s:"); ok {
			owners = append(owners, sid)
		}
	}
	all := storedTables()
	if !e.storedOnly {
		all = append(all, render.Tables...)
	}
	tx, err := e.db.Begin(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	defer tx.Rollback(e.ctx)
	var clear []string
	for _, t := range all {
		clear = append(clear, "DELETE FROM ref."+t.Name)
	}
	if _, err := tx.Exec(e.ctx, "SET LOCAL search_path = ref; "+strings.Join(clear, "; ")); err != nil {
		e.t.Fatal(err)
	}
	var b pgx.Batch
	if err := pg.QueueChanges(&b, cs); err != nil {
		e.t.Fatal(err)
	}
	for _, d := range diffs {
		d.Queue(&b)
	}
	if err := tx.SendBatch(e.ctx, &b).Close(); err != nil {
		e.t.Fatal(err)
	}
	if _, err := tx.Exec(e.ctx, "SET LOCAL search_path = public"); err != nil {
		e.t.Fatal(err)
	}
	args := []any{}
	if len(only) > 0 {
		args = []any{owners}
	}
	// One statement: each table's rows on one side only, and the one pass's row count.
	var parts []string
	for _, t := range all {
		filter := "true"
		if len(only) > 0 {
			owner := "key"
			if t.Kind != pg.Stored {
				owner = render.OwnerColumn(t.Name)
			}
			filter = owner + " = ANY($1)"
		}
		parts = append(parts, fmt.Sprintf(`(SELECT '%[1]s only stored: ' || x::text FROM (SELECT * FROM public.%[1]s WHERE %[2]s
			EXCEPT ALL SELECT * FROM ref.%[1]s) x LIMIT 3)
			UNION ALL (SELECT '%[1]s only one pass: ' || x::text FROM (SELECT * FROM ref.%[1]s
			EXCEPT ALL SELECT * FROM public.%[1]s WHERE %[2]s) x LIMIT 3)
			UNION ALL SELECT '#' || count(*) FROM ref.%[1]s`, t.Name, filter))
	}
	rows, err := tx.Query(e.ctx, strings.Join(parts, " UNION ALL "), args...)
	if err != nil {
		e.t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		e.t.Fatal(err)
	}
	refRows := 0
	var diff []string
	for _, g := range got {
		if n, ok := strings.CutPrefix(g, "#"); ok {
			var c int
			fmt.Sscan(n, &c)
			refRows += c
			continue
		}
		diff = append(diff, g)
	}
	if len(diff) > 0 {
		e.t.Fatalf("the stored inputs or rendered rows differ from one pass (expired at %d):\n%s", clock, strings.Join(diff, "\n"))
	}
	if len(in) > 0 && refRows == 0 {
		e.t.Fatal("check: the one pass stored nothing (vacuous)")
	}
}

// ensureRef makes the ref schema: the same tables, for the one pass.
func (e *env) ensureRef() {
	e.t.Helper()
	if e.ref {
		return
	}
	err := pgx.BeginFunc(e.ctx, e.db, func(tx pgx.Tx) error {
		_, err := tx.Exec(e.ctx, "CREATE SCHEMA ref; SET LOCAL search_path = ref; "+pg.Schema)
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
	e.ref = true
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
