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
//  4. loads the state (warm at the same n, else cold: its snapshot decoded and
//     the journal's records after it applied);
//  5. GETs and applies the files, expires due calls, keeps each row's net change;
//  6. updates the running values (the wires of ended calls, the other keys'
//     parts of the batches its changes or its dirty inputs name), renders
//     (Renderer: the seam);
//  7. writes the journal chunk, and a snapshot when the journal holds as
//     many records since the last as it does, and compacts;
//  8. commits: the guard first (keys.n), then the stored inputs and the
//     rendered changes, then the flag, in one transaction;
//  9. deletes the garbage that's due.
package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
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
	Idle       time.Duration // a held key with no input this long is unlocked (60 min)
	GCGrace    time.Duration // a superseded chunk is deleted after this (10 min)
	Heartbeat  time.Duration // the workers row's period (10 s)
	Live       time.Duration // a worker seen this recently is live (30 s)
	Retry      time.Duration // a quarantined key is retried this often (1 h)
	Quarantine int           // consecutive failures that quarantine a key (5)
	Fanout     int           // state chunks of one tier merged together (8)
	MaxFiles   int           // in/ files applied per run at most (500)
	Parallel   int           // keys run at once (8)
	RunConns   int32         // run connections, shared by the runs (2)
	// WarmBytes caps the warm states' memory (512 MiB): over it, the least
	// recently fed keys are unlocked. A state's bytes are estimated as its
	// records times BytesPerRecord (7,000: a warm key's heap per record on
	// the capture, its state and the render's warm state: 6,600 built by its
	// runs, 5,900 decoded from its snapshot).
	WarmBytes      int64
	BytesPerRecord int64
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
	set(&c.Idle, 60*time.Minute)
	if c.WarmBytes <= 0 {
		c.WarmBytes = 512 << 20
	}
	if c.BytesPerRecord <= 0 {
		c.BytesPerRecord = 7000
	}
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
	// The warm state: the state as of commit n, the render's warm state, and
	// the key row that commit wrote (its chunks and snapshot).
	n       int64
	st      *state.KeyState
	warm    any
	row     KeyRow
	snapped bool // the commit wrote a snapshot: going cold needs none
	records int  // the records the state holds (its bytes' estimate)
}

// Result is what one run did.
type Result struct {
	Applied   int  // in/ files applied
	Committed bool // the key's rows and n + 1 landed
	Lost      bool // another worker committed first: this run wrote nothing
	// Cold is a load from the key's checkpoint: Decoded its snapshot, and
	// Replayed the journal's records after it (all of them with none).
	Cold, Decoded bool
	Replayed      int
	// PartsRead are the batches whose other keys' parts the run read.
	PartsRead []string
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
func (w *Worker) keep(key string, row KeyRow, st *state.KeyState, warm any, snapped, input bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	h := w.keys[key]
	if h == nil || !h.locked {
		return
	}
	h.n, h.st, h.warm, h.row, h.snapped, h.records = row.N, st, warm, row, snapped, st.Records()
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
	h.st, h.warm, h.n, h.records, h.row, h.snapped = nil, nil, 0, 0, KeyRow{}, false
}

// verifyRunning panics unless the run's running values are what walks over
// every row give (render.VerifyIncremental: the tests, and a gate's drain).
func verifyRunning(key string, st *state.KeyState, nowNS int64) {
	if got, want := rollup.FactsFrom(key, st.Facts(), nowNS), rollup.FactsOf(key, st.Rows(), nowNS); got != want {
		panic(fmt.Sprintf("worker: key %s: running facts %+v, the rows' %+v", key, got, want))
	}
	if got, want := st.HeldBatches(), drawer.Batches(st.Rows()); len(got)+len(want) > 0 && !slices.Equal(got, want) {
		panic(fmt.Sprintf("worker: key %s: held batches %v, the rows' %v", key, got, want))
	}
	want := map[string]int64{}
	for id, end := range drawer.EndedCalls(st.Rows()) {
		if _, ok := st.External(render.WireID(id)); !ok {
			want[id] = end
		}
	}
	if got := st.Unwired(); !maps.Equal(got, want) {
		panic(fmt.Sprintf("worker: key %s: unwired calls %v, the rows' %v", key, got, want))
	}
}

// snapshotCold writes the snapshot of each key about to go cold (idle,
// evicted, or on SIGTERM) whose journal holds a quarter of its records or more
// since its last snapshot (§2, v8): the next holder decodes it and applies
// that few at most. A smaller tail costs less to apply than a snapshot does
// to write (~41 µs a record applied, ~13 µs a record held written, on the
// capture), the catch-up render aside (~90 µs a tail record: docs/PIPELINE.md
// §2 has the numbers). Its key row names it, guarded on the n the warm state is at; a
// key another worker committed since keeps its row, and the snapshot is
// deleted. A failure only costs the next load its tail.
func (w *Worker) snapshotCold(ctx context.Context, keys []string) {
	for _, k := range keys {
		w.mu.Lock()
		h := w.keys[k]
		var st *state.KeyState
		var warm any
		var row KeyRow
		if h != nil && h.st != nil && !h.snapped && coldSnapshotDue(h.row, h.st.Records()) {
			st, warm, row = h.st, h.warm, h.row
		}
		w.mu.Unlock()
		if st == nil {
			continue
		}
		if err := w.writeSnapshot(ctx, k, st, warm, row); err != nil {
			slog.Warn("worker: snapshot on going cold", "key", k, "err", err)
		}
	}
}

// coldSnapshotDue is whether a key going cold writes a snapshot: it has none,
// or its journal holds a quarter of its records or more since.
func coldSnapshotDue(row KeyRow, records int) bool {
	return row.Snapshot == "" || 4*(int64(records)-row.SnapshotRecords) >= int64(records)
}

func (w *Worker) writeSnapshot(ctx context.Context, key string, st *state.KeyState, warm any, row KeyRow) error {
	b, err := state.EncodeSnapshot(st, warm)
	if err != nil {
		return err
	}
	dir := rollup.KeyDir(w.cfg.Prefix, key)
	p := rollup.StatePath(row.N, newNonce()+"s")
	if err := w.cfg.Store.Put(ctx, dir+p, b); err != nil {
		return err
	}
	garbage := []rollup.Garbage{}
	if row.Snapshot != "" {
		garbage = append(garbage, rollup.Garbage{Path: row.Snapshot, DeleteAfterNS: w.now().UnixNano() + w.cfg.GCGrace.Nanoseconds()})
	}
	g, err := json.Marshal(garbage)
	if err != nil {
		return err
	}
	tag, err := w.pool.Exec(ctx, `UPDATE keys SET snapshot = $3, snapshot_records = $4, garbage = coalesce(garbage, '[]'::jsonb) || $5::jsonb
		WHERE key = $1 AND n = $2`, key, row.N, p, int64(st.Records()), string(g))
	if err != nil || tag.RowsAffected() == 0 {
		_ = w.cfg.Store.Delete(ctx, dir+p)
	}
	return err
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
	if len(names) == 0 && !due && (!row.Render || row.N == 0) {
		// B1: the LIST reached the end with nothing new: consume the flag and
		// its inputs. A render flag on a key with no state (a batch's key that
		// hasn't committed yet) is consumed too: no key row is written for it
		// (§2, v8), and its first run reads every batch it holds.
		return res, w.consume(ctx, key, seq0, row.Inputs)
	}

	// Load the state: warm at the same n, else cold: its snapshot decoded and
	// the journal's records after it applied. Those records' rows are
	// committed already, so the render catches its warm state up to them
	// (writing nothing): then it holds what Postgres does, as a warm one.
	var st *state.KeyState
	var warm any
	if h := w.warm(key, row.N); h != nil {
		st, warm = h.st, h.warm
	} else {
		l, err := w.load(ctx, key, &row)
		if err != nil {
			return res, err
		}
		st, warm = l.State, l.Warm
		res.Cold, res.Decoded, res.Replayed = true, l.Snapshot, l.Replayed
		if warm != nil && (len(l.Changes) > 0 || len(l.Externals) > 0) {
			caught, err := w.cfg.Render.Render(RenderInput{Key: key, State: st, Prev: row, Warm: warm, Day: row.Day,
				Changes: l.Changes, ExtChanged: l.Externals})
			if err != nil {
				return res, err
			}
			warm = caught.Warm
		}
	}
	var changes state.ChangeSet
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
	delta := map[string]map[string]stream.Row{} // each (table, id)'s last change: the stored inputs'
	note := func(d state.Delta) {
		changes.Add(d.Changes)
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
	// The running values the render reads (§2, v8; §3.7): the ended calls'
	// detail objects, and the other keys' parts of the batches its changes
	// name, its dirty inputs name, or, with no warm state, every batch it holds.
	changed := changes.List()
	waiting, err := w.refreshWires(ctx, st, changed)
	if err != nil {
		return res, err
	}
	res.PartsRead = partsToRead(st, changed, row.Inputs, warm == nil)
	if err := w.refreshParts(ctx, key, st, res.PartsRead); err != nil {
		return res, err
	}

	nowNS := w.now().UnixNano()
	facts := rollup.FactsFrom(key, st.Facts(), nowNS)
	if render.VerifyIncremental {
		verifyRunning(key, st, nowNS)
	}
	rendered, err := w.cfg.Render.Render(RenderInput{Key: key, State: st, Prev: row, Warm: warm, Day: facts.Day,
		Changes: changed, ExtChanged: st.ExternalsChanged()})
	if err != nil {
		return res, err
	}
	if rendered.RenderVersion == 0 {
		rendered.RenderVersion = row.RenderVersion
	}

	// The state chunk, compaction and the key row this run commits.
	n, nonce := row.N+1, newNonce()
	next := KeyRow{Key: key, N: n, State: append([]string(nil), row.State...), StateLevels: padLevels(row.StateLevels, len(row.State)),
		StateRecords: append([]int64(nil), row.StateRecords...), Snapshot: row.Snapshot, SnapshotRecords: row.SnapshotRecords,
		RenderVersion: rendered.RenderVersion, UpdatedNS: nowNS, ExpiresNS: st.Deadline()}
	journaled := st.Journaled()
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
	if len(next.StateRecords) == len(row.State) {
		next.StateRecords = append(next.StateRecords, int64(journaled))
	} else {
		next.StateRecords = nil // a row from before the counts: unknown until a full load counts them
	}
	// A snapshot when the key has none, or the journal holds as many records
	// since its last as it does (§2, v8): a cold load then applies at most
	// that many. The old one is garbage.
	later := nowNS + w.cfg.GCGrace.Nanoseconds()
	snapped := false
	if held := int64(st.Records()); next.Snapshot == "" || held-next.SnapshotRecords >= next.SnapshotRecords {
		b, err := state.EncodeSnapshot(st, rendered.Warm)
		if err != nil {
			return res, err
		}
		p := rollup.StatePath(n, nonce+"s")
		if err := w.cfg.Store.Put(ctx, dir+p, b); err != nil {
			return res, err
		}
		if next.Snapshot != "" {
			next.Garbage = append(next.Garbage, rollup.Garbage{Path: next.Snapshot, DeleteAfterNS: later})
		}
		next.Snapshot, next.SnapshotRecords, snapped = p, held, true
	}
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
	compacted, err := w.compact(ctx, key, &next, nonce, later)
	if err != nil {
		return res, err
	}
	next.Day, next.FirstRequestNS, next.EarliestNS, next.ContentRetained = facts.Day, facts.FirstRequestNS, facts.EarliestNS, facts.ContentRetained
	next.NewestRowNS = max(newestRow, facts.RawNewestNS)
	if err := w.hook("state", key); err != nil {
		return res, err
	}

	stored := StoredChanges(key, flatten(delta))
	stored = append(stored, rendered.Changes...)
	committed, err := w.commit(ctx, row, next, stored, rendered.Queue, seq0, rest, start, waiting, rendered.ChangedBatches,
		callBatches(changed),
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
	w.keep(key, next, st, rendered.Warm, snapped, len(names) > 0)
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
	rest string, start time.Time, waiting bool, changedBatches, callBatches []string, seen func(string) (state.OtherParts, bool)) (bool, error) {
	inputs := prev.Inputs
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
	queueConsumeInputs(&b, next.Key, inputs)
	// A detail object waited for (§3.7): keep the key flagged, to render again.
	if waiting {
		b.Queue(`INSERT INTO dirty_keys (key, seq, since, render) VALUES ($1, 1, $2, true)
			ON CONFLICT (key) DO UPDATE SET render = true`, next.Key, start)
	}
	queueCrossKeys(&b, next.Key, changedBatches, callBatches, seen)
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

// load is key's state from its checkpoint (an empty state for a new key):
// its snapshot decoded and the journal chunks after the records it holds
// applied, or, with no snapshot it can read, the journal's every record. A
// full load counts each chunk's records into row, when it had no counts.
func (w *Worker) load(ctx context.Context, key string, row *KeyRow) (*state.Loaded, error) {
	if len(row.State) == 0 {
		return &state.Loaded{State: state.New(key)}, nil
	}
	dir := rollup.KeyDir(w.cfg.Prefix, key)
	get := func(paths []string) ([][]byte, error) {
		out := make([][]byte, 0, len(paths))
		for _, p := range paths {
			b, err := w.cfg.Store.Get(ctx, dir+p)
			if err != nil {
				return nil, fmt.Errorf("worker: %s state %s: %w", key, p, err)
			}
			out = append(out, b)
		}
		return out, nil
	}
	if row.Snapshot != "" && len(row.StateRecords) == len(row.State) {
		if first, skip, ok := chunksAfter(row.StateRecords, row.SnapshotRecords); ok {
			snap, err := get([]string{row.Snapshot})
			if err != nil {
				return nil, err
			}
			chunks, err := get(row.State[first:])
			if err != nil {
				return nil, err
			}
			l, err := state.Load(snap[0], skip, chunks...)
			if err == nil && int64(l.State.Records()) != row.SnapshotRecords+int64(l.Replayed) {
				err = fmt.Errorf("its %d records aren't the row's %d and the %d after", l.State.Records(), row.SnapshotRecords, l.Replayed)
			}
			if err == nil {
				l.State.Key = key
				return l, nil
			}
			// A snapshot another build wrote (or one it can't read): the journal is
			// the checkpoint, so load from it.
			slog.Warn("worker: snapshot not loaded; replaying the journal", "key", key, "err", err)
		}
	}
	chunks, err := get(row.State)
	if err != nil {
		return nil, err
	}
	l, err := state.Load(nil, 0, chunks...)
	if err != nil {
		return nil, err
	}
	l.State.Key = key
	if len(row.StateRecords) != len(row.State) {
		row.StateRecords = l.ChunkRecords
	}
	return l, nil
}

// chunksAfter is the first journal chunk a snapshot holding the journal's
// first held records doesn't hold all of, and how many of its records it
// does. A chunk with no records at the snapshot's end may be after it (a run
// that only set external inputs), so it's read (its inputs again are
// harmless: every one ends at its last value).
func chunksAfter(records []int64, held int64) (first, skip int, ok bool) {
	var start int64
	for i, n := range records {
		if start+n > held || n == 0 && start >= held {
			return i, int(held - start), held >= start
		}
		start += n
	}
	return len(records), 0, start == held
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
		if len(c.StateRecords) == len(c.State) {
			var sum int64
			for _, r := range c.StateRecords[lo:] {
				sum += r
			}
			c.StateRecords = append(c.StateRecords[:lo], sum)
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
	keep := map[string]bool{c.Snapshot: true}
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
