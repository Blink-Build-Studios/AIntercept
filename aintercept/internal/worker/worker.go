// Package worker is v7's worker core (docs/PIPELINE.md §2): the `rollup`
// Deployment's replicas, on Postgres. A worker claims flagged keys
// (dirty_keys) under advisory locks shared out by heartbeats, applies each
// key's new ingest files with the per-key state engine, and commits the key's
// rows and its next commit number in one transaction, guarded on the number it
// read. The guard is what makes it correct; the locks only save duplicate
// work. There's no Redis.
//
// A run, for one key:
//  1. reads the key row and its flag's seq in one round;
//  2. LISTs in/ after (last applied − grace), at most MaxFiles names;
//  3. with nothing new and no call due to expire, consumes the flag and stops;
//  4. loads the state (warm at the same n, else cold from its chunks);
//  5. GETs and applies the files, expires due calls, keeps each row's last change;
//  6. renders (Renderer: build step 3's seam);
//  7. writes the state chunk and compacts;
//  8. commits: the guard first (keys.n), then the stored inputs and the
//     rendered changes, then the flag, in one transaction;
//  9. deletes the garbage that's due.
package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/drawer"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/render"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/rollup"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream/state"
)

// Config is a worker's setup. Zero values take the defaults.
type Config struct {
	Store  stream.Store
	Prefix string // the bucket prefix; v1/ is under it
	Name   string // this worker's name: its heartbeat row, and its connections' application_name
	DSN    string // the worker login's: one lock connection and a pool of run connections
	Clock  stream.Clock
	// Render writes the rendered tables (nil: Tables, internal/render's).
	Render Renderer
	// Singletons run on whichever worker holds their advisory lock, besides
	// the built-in due calls (DueCalls).
	Singletons []Singleton

	Grace      time.Duration // late-file window on in/ names (5 min)
	Idle       time.Duration // a held key with no input this long is unlocked (60 s)
	GCGrace    time.Duration // a superseded chunk is deleted after this (10 min)
	Heartbeat  time.Duration // the workers row's period (10 s)
	Live       time.Duration // a worker seen this recently is live (30 s)
	Retry      time.Duration // a quarantined key is retried this often (1 h)
	Quarantine int           // consecutive failures that quarantine a key (5)
	Fanout     int           // state chunks of one tier merged together (8)
	MaxFiles   int           // in/ files applied per run at most (500)
	Parallel   int           // keys run at once (8)
	RunConns   int32         // run connections, shared by the runs (2)
	// DetailWait is how long after a call ended its detail object is waited
	// for (§3.7): a run that doesn't find it keeps its flag, to read it again.
	// Zero: a missing object is final at once (the call is rendered from its
	// row). The rollup service sets DefaultDetailWait.
	DetailWait time.Duration

	// Hook, if set, is called at each step of a run ("read-row", "listed",
	// "read", "state", "commit", "guarded", "rows", "commit-result",
	// "committed"); an error stops the run there, as a crash would. Tests only.
	Hook func(step, key string) error
}

func (c *Config) defaults() {
	set := func(d *time.Duration, v time.Duration) {
		if *d == 0 {
			*d = v
		}
	}
	set(&c.Grace, 5*time.Minute)
	set(&c.Idle, 60*time.Second)
	set(&c.GCGrace, 10*time.Minute)
	set(&c.Heartbeat, 10*time.Second)
	set(&c.Live, 30*time.Second)
	set(&c.Retry, time.Hour)
	if c.Quarantine <= 0 {
		c.Quarantine = 5
	}
	if c.Fanout < 2 {
		c.Fanout = 8
	}
	if c.MaxFiles <= 0 {
		c.MaxFiles = 500
	}
	if c.Parallel <= 0 {
		c.Parallel = 8
	}
	if c.RunConns <= 0 {
		c.RunConns = 2
	}
	if c.Clock == nil {
		c.Clock = stream.RealClock{}
	}
	if c.Render == nil {
		c.Render = Tables{}
	}
}

// Worker is one replica.
type Worker struct {
	cfg  Config
	pool *pgxpool.Pool // run connections
	// templates are the drawer templates its commits wrote (§3.7).
	templates templateSet

	mu   sync.Mutex
	keys map[string]*held

	lockMu     sync.Mutex // the lock connection: Tick's, one statement at a time
	lock       *pgx.Conn
	lockCfg    *pgx.ConnConfig
	lastBeat   time.Time
	singletons []*singleton
	killed     bool
}

// held is a key this worker holds the lock of, or has a warm state for.
type held struct {
	locked    bool
	lastInput time.Time
	// The warm state: the state as of commit n, and the render's baseline.
	n        int64
	st       *state.KeyState
	baseline any
}

// Result is what one run did.
type Result struct {
	Applied   int  // in/ files applied
	Committed bool // the key's rows and n + 1 landed
	Lost      bool // another worker committed first: this run wrote nothing
}

// New is a worker: its run pool and lock connection, the schema applied (or
// checked: a mismatch refuses to start).
func New(ctx context.Context, cfg Config) (*Worker, error) {
	cfg.defaults()
	pc, _, err := pg.ParseConfig(cfg.DSN, cfg.RunConns)
	if err != nil {
		return nil, err
	}
	pc.ConnConfig.RuntimeParams["application_name"] = runConnName(cfg.Name)
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, err
	}
	if err := pg.Apply(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	lc := pc.ConnConfig.Copy()
	lc.RuntimeParams["application_name"] = "aintercept-lock-" + cfg.Name
	w := &Worker{cfg: cfg, pool: pool, keys: map[string]*held{}, lockCfg: lc}
	w.singletons = append(w.singletons, &singleton{Singleton: DueCalls(w)})
	for _, s := range cfg.Singletons {
		w.singletons = append(w.singletons, &singleton{Singleton: s})
	}
	return w, nil
}

// runConnName is a worker's run connections' application_name.
func runConnName(worker string) string { return "aintercept-run-" + worker }

// Pool is the worker's run connections, for a singleton's statements.
func (w *Worker) Pool() *pgxpool.Pool { return w.pool }

func (w *Worker) hook(step, key string) error {
	if w.cfg.Hook != nil {
		return w.cfg.Hook(step, key)
	}
	return nil
}

func (w *Worker) now() time.Time { return w.cfg.Clock.Now() }

// warm is key's warm state at commit n (nil: none).
func (w *Worker) warm(key string, n int64) *held {
	w.mu.Lock()
	defer w.mu.Unlock()
	if h := w.keys[key]; h != nil && h.st != nil && h.n == n {
		return h
	}
	return nil
}

// Warm is whether the worker keeps a state for key.
func (w *Worker) Warm(key string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	h := w.keys[key]
	return h != nil && h.st != nil
}

// keep records key's state as of commit n, after its commit, while the worker
// holds the key's lock: a key it doesn't hold isn't kept warm.
func (w *Worker) keep(key string, n int64, st *state.KeyState, baseline any, input bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	h := w.keys[key]
	if h == nil || !h.locked {
		return
	}
	h.n, h.st, h.baseline = n, st, baseline
	if input {
		h.lastInput = w.now()
	}
}

// drop forgets key's warm state (a failed or lost run): the next run loads it
// cold from the key row. The lock, if held, stays.
func (w *Worker) drop(key string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	h := w.keys[key]
	if h == nil {
		return
	}
	if !h.locked {
		delete(w.keys, key)
		return
	}
	h.st, h.baseline, h.n = nil, nil, 0
}

// --- a run --------------------------------------------------------------------

// errUnknownResult is a COMMIT whose result never came back.
var errUnknownResult = errors.New("worker: the commit's result is unknown")

// RunKey runs key once, with data time read now (Tick reads it once per tick).
func (w *Worker) RunKey(ctx context.Context, key string) (Result, error) {
	clock, err := dataTime(ctx, w.pool)
	if err != nil {
		return Result{}, err
	}
	return w.runKey(ctx, key, clock)
}

// runKey runs key once: applies its new in/ files (and expires its calls due
// by clock) and commits. A run that loses the guard returns Lost and no
// error. A failed run records its error on the key's flag (failures + 1).
func (w *Worker) runKey(ctx context.Context, key string, clock int64) (res Result, err error) {
	start := w.now()
	defer func() {
		if err != nil {
			w.drop(key)
			if ctx.Err() == nil {
				w.recordFailure(context.WithoutCancel(ctx), key, err)
			}
		}
	}()
	row, seq0, err := readRow(ctx, w.pool, key)
	if err != nil {
		return res, err
	}
	if err := w.hook("read-row", key); err != nil {
		return res, err
	}
	names, rest, err := w.newInput(ctx, key, row)
	if err != nil {
		return res, err
	}
	if err := w.hook("listed", key); err != nil {
		return res, err
	}
	due := row.ExpiresNS != 0 && row.ExpiresNS < clock
	if len(names) == 0 && !due && !row.Render {
		// B1: the LIST reached the end with nothing new: consume the flag.
		if seq0 > 0 {
			if _, err := w.pool.Exec(ctx, "DELETE FROM dirty_keys WHERE key = $1 AND seq = $2", key, seq0); err != nil {
				return res, err
			}
		}
		return res, nil
	}

	// Load the state: warm at the same n, else cold from the chunks the row names.
	var st *state.KeyState
	var baseline any
	if h := w.warm(key, row.N); h != nil {
		st, baseline = h.st, h.baseline
	} else {
		if st, err = w.load(ctx, key, row); err != nil {
			return res, err
		}
		// A cold load's baseline: the render of the state as committed, when
		// its rows are this render's (else the render rewrites them all).
		if row.N > 0 {
			if baseline, err = w.cfg.Render.Baseline(key, st, row.Day); err != nil {
				return res, err
			}
		}
	}
	w.drop(key) // the state is about to move past commit n: until it commits, it's no one's
	res.Applied = len(names)

	var rs []records.Record
	newestRow := row.NewestRowNS
	for _, name := range names {
		b, err := w.cfg.Store.Get(ctx, rollup.InPrefix(w.cfg.Prefix, key)+name)
		if err != nil {
			return res, fmt.Errorf("worker: %s %s: %w", key, name, err)
		}
		got, newest, err := records.DecodeWithNewest(b)
		if err != nil {
			return res, fmt.Errorf("worker: %s %s: %w", key, name, err)
		}
		rs = append(rs, got...)
		newestRow = max(newestRow, newest)
	}
	sort.SliceStable(rs, func(i, j int) bool {
		ti, tj := stream.RecordTS(&rs[i]), stream.RecordTS(&rs[j])
		if ti != tj {
			return ti < tj
		}
		return rs[i].RecordID < rs[j].RecordID
	})
	delta := map[string]map[string]stream.Row{} // each (table, id)'s last change
	note := func(d state.Delta) {
		for _, c := range d.Changes {
			if delta[c.Table] == nil {
				delta[c.Table] = map[string]stream.Row{}
			}
			delta[c.Table][c.ID] = stream.Row{Key: key, ID: c.ID, Tomb: c.Row == nil, Value: c.Row}
		}
	}
	for i := range rs {
		note(st.Apply(rs[i]))
	}
	if d := st.Deadline(); d != 0 && d < clock {
		note(st.Expire(clock))
	}
	if err := w.hook("read", key); err != nil {
		return res, err
	}
	// The render's external inputs (§3.7): the ended calls' detail objects, the
	// other keys' parts of its batches.
	waiting, err := w.refreshWires(ctx, st)
	if err != nil {
		return res, err
	}
	if err := w.refreshParts(ctx, key, st); err != nil {
		return res, err
	}

	nowNS := w.now().UnixNano()
	facts := rollup.FactsOf(key, st.Rows(), nowNS)
	changed := drawer.Changed{}
	for table, rows := range delta {
		changed[table] = make(map[string]bool, len(rows))
		for id := range rows {
			changed[table][id] = true
		}
	}
	rendered, err := w.cfg.Render.Render(RenderInput{Key: key, State: st, Prev: row, Baseline: baseline, Day: facts.Day, Changed: changed})
	if err != nil {
		return res, err
	}
	if rendered.RenderVersion == 0 {
		rendered.RenderVersion = row.RenderVersion
	}

	// The state chunk, compaction and the key row this run commits.
	n, nonce := row.N+1, newNonce()
	next := KeyRow{Key: key, N: n, State: append([]string(nil), row.State...), StateLevels: padLevels(row.StateLevels, len(row.State)),
		RenderVersion: rendered.RenderVersion, UpdatedNS: nowNS, ExpiresNS: st.Deadline()}
	chunk, err := state.Encode(st)
	if err != nil {
		return res, err
	}
	dir := rollup.KeyDir(w.cfg.Prefix, key)
	sp := rollup.StatePath(n, nonce)
	if err := w.cfg.Store.Put(ctx, dir+sp, chunk); err != nil {
		return res, err
	}
	next.State, next.StateLevels = append(next.State, sp), append(next.StateLevels, 0)
	next.LastApplied = row.LastApplied
	for _, name := range names {
		if name > next.LastApplied {
			next.LastApplied = name
		}
	}
	last, _ := rollup.InTime(next.LastApplied)
	for _, name := range append(append([]string(nil), row.Grace...), names...) {
		if t, err := rollup.InTime(name); err == nil && !t.Before(last.Add(-w.cfg.Grace)) {
			next.Grace = append(next.Grace, name)
		}
	}
	sort.Strings(next.Grace)
	next.Grace = dedupe(next.Grace)
	var dueGarbage []rollup.Garbage
	for _, g := range row.Garbage {
		if g.DeleteAfterNS <= nowNS {
			dueGarbage = append(dueGarbage, g)
		} else {
			next.Garbage = append(next.Garbage, g)
		}
	}
	compacted, err := w.compact(ctx, key, &next, nonce, nowNS+w.cfg.GCGrace.Nanoseconds())
	if err != nil {
		return res, err
	}
	next.Day, next.FirstRequestNS, next.EarliestNS, next.ContentRetained = facts.Day, facts.FirstRequestNS, facts.EarliestNS, facts.ContentRetained
	next.NewestRowNS = max(newestRow, facts.RawNewestNS)
	if err := w.hook("state", key); err != nil {
		return res, err
	}

	changes := StoredChanges(key, flatten(delta))
	changes = append(changes, rendered.Changes...)
	var newTemplates, bodies []string
	for _, t := range rendered.Templates {
		if id := t[0].(string); !w.templates.has(id) {
			newTemplates, bodies = append(newTemplates, id), append(bodies, t[1].(string))
		}
	}
	queue := rendered.Queue
	if len(newTemplates) > 0 {
		queue = func(b *pgx.Batch) {
			if rendered.Queue != nil {
				rendered.Queue(b)
			}
			b.Queue(`INSERT INTO drawer_templates (id, body) SELECT * FROM unnest($1::text[], $2::text[])
				ON CONFLICT (id) DO NOTHING`, newTemplates, bodies)
		}
	}
	committed, err := w.commit(ctx, row, next, changes, queue, seq0, rest, start, waiting, rendered.ChangedBatches,
		func(batch string) (state.OtherParts, bool) {
			v, ok := st.External(render.PartsID(batch))
			p, _ := v.(state.OtherParts)
			return p, ok
		})
	if err != nil {
		if errors.Is(err, errUnknownResult) {
			w.reflag(context.WithoutCancel(ctx), key)
		}
		return res, err
	}
	if !committed {
		res.Lost = true // another worker committed: drop the state and its baseline, move on
		return res, nil
	}
	res.Committed = true
	w.templates.add(newTemplates)
	w.keep(key, n, st, rendered.Baseline, len(names) > 0)
	for _, g := range dueGarbage {
		_ = w.cfg.Store.Delete(ctx, dir+g.Path)
	}
	if compacted {
		w.deleteOrphans(ctx, key, next)
	}
	if err := w.hook("committed", key); err != nil {
		return res, err
	}
	return res, nil
}

// commit is the run's transaction (§2 Run 8): the guard, the rows, the flag.
// It reports false when the guard found another worker's commit.
func (w *Worker) commit(ctx context.Context, prev, next KeyRow, changes []pg.Changes, queue func(*pgx.Batch), seq0 int64,
	rest string, start time.Time, waiting bool, changedBatches []string, seen func(string) (state.OtherParts, bool)) (bool, error) {
	if err := w.hook("commit", next.Key); err != nil {
		return false, err
	}
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	ok, err := guard(ctx, tx, prev, next)
	if err != nil || !ok {
		return false, err
	}
	if err := w.hook("guarded", next.Key); err != nil {
		return false, err
	}
	var b pgx.Batch
	if err := pg.QueueChanges(&b, changes); err != nil {
		return false, err
	}
	if queue != nil {
		queue(&b)
	}
	// The flag: kept (since the first file left) when the cap cut the run short;
	// else consumed if its seq hasn't moved; else kept, since the run's start.
	if rest != "" {
		left, err := rollup.InTime(rest)
		if err != nil {
			return false, err
		}
		b.Queue(`INSERT INTO dirty_keys (key, seq, since) VALUES ($1, 1, $2)
			ON CONFLICT (key) DO UPDATE SET since = excluded.since, failures = 0, last_error = NULL, failed_at = NULL`, next.Key, left)
	} else {
		b.Queue("DELETE FROM dirty_keys WHERE key = $1 AND seq = $2", next.Key, seq0)
		b.Queue("UPDATE dirty_keys SET since = $2, failures = 0, last_error = NULL, failed_at = NULL WHERE key = $1", next.Key, start)
	}
	// A detail object waited for (§3.7): keep the key flagged, to render again.
	if waiting {
		b.Queue(`INSERT INTO dirty_keys (key, seq, since, render) VALUES ($1, 1, $2, true)
			ON CONFLICT (key) DO UPDATE SET render = true`, next.Key, start)
	}
	queueCrossKeys(&b, next.Key, changedBatches, seen)
	if err := tx.SendBatch(ctx, &b).Close(); err != nil {
		return false, err
	}
	if err := w.hook("rows", next.Key); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		if ctx.Err() == nil {
			return false, fmt.Errorf("%w: %w", errUnknownResult, err)
		}
		return false, err
	}
	if err := w.hook("commit-result", next.Key); err != nil {
		return false, err
	}
	return true, nil
}

// recordFailure counts a failed run on the key's flag (B4), flagging it if it
// had none, so it's tried again.
func (w *Worker) recordFailure(ctx context.Context, key string, cause error) {
	msg := cause.Error()
	if len(msg) > 2000 {
		msg = msg[:2000]
	}
	now := w.now()
	_, _ = w.pool.Exec(ctx, `INSERT INTO dirty_keys (key, seq, since, failures, last_error, failed_at) VALUES ($1, 1, $2, 1, $3, $2)
		ON CONFLICT (key) DO UPDATE SET failures = dirty_keys.failures + 1, last_error = excluded.last_error, failed_at = excluded.failed_at`,
		key, now, strings.ToValidUTF8(strings.ReplaceAll(msg, "\x00", "�"), "�"))
}

// reflag flags key again after a commit whose result is unknown: the next run
// reads whichever n landed.
func (w *Worker) reflag(ctx context.Context, key string) {
	_, _ = w.pool.Exec(ctx, `INSERT INTO dirty_keys (key, seq, since) VALUES ($1, 1, $2)
		ON CONFLICT (key) DO UPDATE SET seq = dirty_keys.seq + 1`, key, w.now())
}

// load is key's state from the chunks its row names (an empty state for a new key).
func (w *Worker) load(ctx context.Context, key string, row KeyRow) (*state.KeyState, error) {
	if len(row.State) == 0 {
		return state.New(key), nil
	}
	chunks := make([][]byte, 0, len(row.State))
	for _, p := range row.State {
		b, err := w.cfg.Store.Get(ctx, rollup.KeyDir(w.cfg.Prefix, key)+p)
		if err != nil {
			return nil, fmt.Errorf("worker: %s state %s: %w", key, p, err)
		}
		chunks = append(chunks, b)
	}
	st, err := state.Decode(chunks...)
	if err != nil {
		return nil, err
	}
	st.Key = key
	return st, nil
}

// newInput is key's in/ files not yet applied, oldest first, at most MaxFiles:
// from (last applied − grace) on, minus the names the row records as applied
// in that window. rest is the first name left by the cap ("" when the LIST
// reached the end).
func (w *Worker) newInput(ctx context.Context, key string, row KeyRow) (names []string, rest string, err error) {
	in := rollup.InPrefix(w.cfg.Prefix, key)
	after := in
	if row.LastApplied != "" {
		t, err := rollup.InTime(row.LastApplied)
		if err != nil {
			return nil, "", err
		}
		after = in + t.Add(-w.cfg.Grace).UTC().Format(rollup.TimeFormat)
	}
	objs, err := w.cfg.Store.ListAfter(ctx, in, after)
	if err != nil {
		return nil, "", err
	}
	seen := make(map[string]bool, len(row.Grace))
	for _, g := range row.Grace {
		seen[g] = true
	}
	for _, o := range objs {
		name := strings.TrimPrefix(o.Key, in)
		if !strings.HasSuffix(name, ".parquet") || seen[name] {
			continue
		}
		if _, err := rollup.InTime(name); err != nil {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > w.cfg.MaxFiles {
		rest = names[w.cfg.MaxFiles]
		names = names[:w.cfg.MaxFiles]
	}
	return names, rest, nil
}

// --- the stored inputs ----------------------------------------------------------

// storedTables are the §3.4 tables, by the state table they store.
func storedTables() []*pg.Table {
	var out []*pg.Table
	for i := range pg.Tables {
		if pg.Tables[i].Kind == pg.Stored {
			out = append(out, &pg.Tables[i])
		}
	}
	return out
}

var byState = func() map[string]*pg.Table {
	m := map[string]*pg.Table{}
	for _, t := range storedTables() {
		m[t.State] = t
	}
	return m
}()

// StoredChanges is a key's state rows as the stored inputs' writes (§3.4):
// an upsert for each row, a delete for each tombstone, keyed by (key, the
// state's row id). State tables that aren't stored (the render's inputs) are
// left out.
func StoredChanges(key string, tables map[string][]stream.Row) []pg.Changes {
	names := make([]string, 0, len(tables))
	for t := range tables {
		if byState[t] != nil {
			names = append(names, t)
		}
	}
	sort.Strings(names)
	out := make([]pg.Changes, 0, len(names))
	for _, name := range names {
		t := byState[name]
		c := pg.Changes{Table: t.Name}
		for _, r := range tables[name] {
			if r.Tomb || r.Value == nil {
				c.Deletes = append(c.Deletes, []any{key, []byte(r.ID)})
				continue
			}
			c.Upserts = append(c.Upserts, append([]any{key, []byte(r.ID)}, t.RowValues(r.Value)...))
		}
		out = append(out, c)
	}
	return out
}

func flatten(delta map[string]map[string]stream.Row) map[string][]stream.Row {
	out := make(map[string][]stream.Row, len(delta))
	for t, byID := range delta {
		ids := make([]string, 0, len(byID))
		for id := range byID {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			out[t] = append(out[t], byID[id])
		}
	}
	return out
}

// --- state chunks ---------------------------------------------------------------

// compact folds the newest Fanout state chunks of one tier into one of the
// next tier, repeatedly, so the row names O(Fanout · log N) chunks; the folded
// ones become garbage. It reports whether it folded anything.
func (w *Worker) compact(ctx context.Context, key string, c *KeyRow, nonce string, later int64) (bool, error) {
	dir := rollup.KeyDir(w.cfg.Prefix, key)
	folded := false
	for i := 0; ; i++ {
		lo, ok := trailingRun(c.StateLevels, w.cfg.Fanout)
		if !ok {
			break
		}
		chunks := make([][]byte, 0, len(c.State)-lo)
		for _, p := range c.State[lo:] {
			b, err := w.cfg.Store.Get(ctx, dir+p)
			if err != nil {
				return false, err
			}
			chunks = append(chunks, b)
		}
		b, err := state.Merge(chunks...)
		if err != nil {
			return false, err
		}
		p := rollup.StatePath(c.N, fmt.Sprintf("%sc%d", nonce, i))
		if err := w.cfg.Store.Put(ctx, dir+p, b); err != nil {
			return false, err
		}
		level := c.StateLevels[len(c.StateLevels)-1] + 1
		for _, old := range c.State[lo:] {
			c.Garbage = append(c.Garbage, rollup.Garbage{Path: old, DeleteAfterNS: later})
		}
		c.State, c.StateLevels = append(c.State[:lo], p), append(c.StateLevels[:lo], level)
		folded = true
	}
	return folded, nil
}

// trailingRun finds the newest entries sharing one level; it reports their
// start when there are at least fanout of them.
func trailingRun(levels []int, fanout int) (int, bool) {
	if len(levels) < fanout {
		return 0, false
	}
	l := levels[len(levels)-1]
	i := len(levels) - 1
	for i > 0 && levels[i-1] == l {
		i--
	}
	return i, len(levels)-i >= fanout
}

func padLevels(ls []int, n int) []int {
	out := append([]int(nil), ls...)
	for len(out) < n {
		out = append(out, 0)
	}
	return out[:n]
}

// deleteOrphans removes the state chunks of runs that never committed (a
// crash, a lost guard): numbered at or before c.N, named neither by c nor as
// garbage. No row can ever name them.
func (w *Worker) deleteOrphans(ctx context.Context, key string, c KeyRow) {
	dir := rollup.KeyDir(w.cfg.Prefix, key)
	keep := map[string]bool{}
	for _, p := range c.State {
		keep[p] = true
	}
	for _, g := range c.Garbage {
		keep[g.Path] = true
	}
	infos, err := w.cfg.Store.ListInfo(ctx, dir+"state/")
	if err != nil {
		return
	}
	for _, in := range infos {
		rel := strings.TrimPrefix(in.Key, dir)
		var n int64
		if _, err := fmt.Sscanf(strings.TrimPrefix(rel, "state/"), "%012d", &n); err != nil || keep[rel] || n > c.N {
			continue
		}
		_ = w.cfg.Store.Delete(ctx, in.Key)
	}
}

func newNonce() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func dedupe(xs []string) []string {
	out := xs[:0]
	for i, x := range xs {
		if i == 0 || x != xs[i-1] {
			out = append(out, x)
		}
	}
	return out
}
