// Package tail is the workers' discovery (docs/PIPELINE.md §2, Singletons):
// one holder, every 2 s, reads ingest's recent index (keyflag) into
// dirty_keys. It replaces v5's sweep and ingest's Redis flags; ingest never
// talks to Postgres.
//
// A pass:
//  1. reads its cursor, the tail row: the newest index object read, and the
//     names inside the grace window behind it;
//  2. LISTs v1/recent/ after (the cursor's S3 time − Grace), skipping the
//     grace names (the keys' late-file rule), so an object that becomes
//     visible late, inside the window, is still read;
//  3. GETs each new object, in name order, at most MaxObjects a pass;
//  4. in one transaction: advances the cursor, guarded on the one it read
//     (first, so a second holder's pass rolls back before writing anything);
//     upserts a flag for every key named (seq + 1); and upserts each pod's
//     newest upload time into ingest_pods.
//
// The cursor advances only together with its flags, so Postgres down, S3
// failing, a crash or a dropped connection make the next pass read the same
// objects again, never skip one. Reading one twice only bumps a flag's seq.
//
// Scaling (§ Scaling budget): one LIST every 2 s, a GET per index object (a pod
// writes one every 5 s), one transaction a pass; its state is the cursor row.
package tail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/keyflag"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
)

// LockID is the tail's advisory lock: whichever worker holds it runs the tail.
const LockID int64 = 0x7461696c // "tail"

// Defaults.
const (
	DefaultEvery      = 2 * time.Second
	DefaultGrace      = 5 * time.Minute
	DefaultMaxObjects = 1000
	maxBackoff        = 30 * time.Second
	getParallel       = 8
)

// ErrCursorMoved is a pass that lost the guard: another tail advanced the
// cursor after this one read it. Nothing was written; the next pass reads
// from the new cursor.
var ErrCursorMoved = errors.New("tail: the cursor moved (another tail committed)")

// DB is what the tail needs of Postgres (a *pgxpool.Pool).
type DB interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Store is what the tail needs of S3.
type Store interface {
	ListAfter(ctx context.Context, prefix, after string) ([]process.Object, error)
	Get(ctx context.Context, key string) ([]byte, error)
}

// Config configures a Tail.
type Config struct {
	DB         DB
	Store      Store
	Prefix     string        // the object prefix v1/ is under
	Every      time.Duration // between passes (DefaultEvery)
	Grace      time.Duration // the late-object window (DefaultGrace)
	MaxObjects int           // index objects read per pass (DefaultMaxObjects)
	// Hook is called at each step ("begin": the objects are read, the
	// transaction not begun; "commit": everything written but COMMIT), and
	// fails the pass if it returns an error. Tests only.
	Hook func(step string) error
}

// Tail reads the recent index into dirty_keys.
type Tail struct{ cfg Config }

// New is a Tail.
func New(cfg Config) *Tail {
	if cfg.Every <= 0 {
		cfg.Every = DefaultEvery
	}
	if cfg.Grace <= 0 {
		cfg.Grace = DefaultGrace
	}
	if cfg.MaxObjects <= 0 {
		cfg.MaxObjects = DefaultMaxObjects
	}
	return &Tail{cfg: cfg}
}

// Result is what a pass did.
type Result struct {
	Objects int    // index objects read
	Skipped int    // ... of which unreadable (not an index object, or gone)
	Keys    int    // distinct keys flagged
	Cursor  string // the cursor after the pass
}

type cursor struct {
	last  string
	grace []string
}

type object struct {
	name string
	at   time.Time
	body keyflag.Object
	ok   bool
}

// Pass runs one pass. It returns ErrCursorMoved if another tail committed
// first; any error leaves the cursor and the flags as they were.
func (t *Tail) Pass(ctx context.Context) (Result, error) {
	var c cursor
	if err := t.cfg.DB.QueryRow(ctx, `SELECT coalesce(last_name, ''), coalesce(grace, '{}') FROM tail`).Scan(&c.last, &c.grace); err != nil {
		return Result{}, fmt.Errorf("tail: read cursor: %w", err)
	}
	if c.grace == nil {
		c.grace = []string{} // never NULL in the guard's comparison
	}
	names, err := t.newNames(ctx, c)
	if err != nil {
		return Result{}, err
	}
	objs, err := t.read(ctx, names)
	if err != nil {
		return Result{}, err
	}
	next := t.advance(c, names)
	res := Result{Objects: len(objs), Cursor: next.last}
	if err := t.hook("begin"); err != nil {
		return Result{}, err
	}

	// The flags: one per key, since the earliest object naming it.
	since := map[string]time.Time{}
	type pod struct {
		lastNS int64
		at     time.Time
	}
	pods := map[string]pod{}
	for _, o := range objs {
		if !o.ok {
			res.Skipped++
			continue
		}
		for _, k := range o.body.Keys {
			if s, ok := since[k]; k != "" && (!ok || o.at.Before(s)) {
				since[k] = o.at
			}
		}
		if o.body.LastNS > 0 && o.body.Incarnation != "" { // a crash's leftovers carry no upload time
			p := pods[o.body.Incarnation]
			p.lastNS = max(p.lastNS, o.body.LastNS)
			if o.at.After(p.at) {
				p.at = o.at
			}
			pods[o.body.Incarnation] = p
		}
	}
	keys := make([]string, 0, len(since))
	for k := range since {
		keys = append(keys, k)
	}
	sort.Strings(keys) // a fixed order: concurrent upserts never deadlock
	sinces := make([]time.Time, len(keys))
	for i, k := range keys {
		sinces[i] = since[k]
	}
	incs := make([]string, 0, len(pods))
	for inc := range pods {
		incs = append(incs, inc)
	}
	sort.Strings(incs)
	lastNS, ats := make([]int64, len(incs)), make([]time.Time, len(incs))
	for i, inc := range incs {
		lastNS[i], ats[i] = pods[inc].lastNS, pods[inc].at
	}
	res.Keys = len(keys)

	tx, err := t.cfg.DB.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("tail: begin: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // a no-op after COMMIT
	b := &pgx.Batch{}
	// The guard first: it takes the row's lock, so a concurrent pass waits here
	// and then finds the cursor moved.
	b.Queue(`UPDATE tail SET last_name = $1, grace = $2, checked_at = now()
		WHERE coalesce(last_name, '') = $3 AND coalesce(grace, '{}') = $4::text[]`, next.last, next.grace, c.last, c.grace)
	if len(keys) > 0 {
		b.Queue(`INSERT INTO dirty_keys (key, seq, since)
			SELECT k, 1, least(s, now()) FROM unnest($1::text[], $2::timestamptz[]) AS f(k, s)
			ON CONFLICT (key) DO UPDATE SET seq = dirty_keys.seq + 1, since = least(dirty_keys.since, EXCLUDED.since)`,
			keys, sinces)
	}
	if len(incs) > 0 {
		b.Queue(`INSERT INTO ingest_pods (incarnation, last_ns, at)
			SELECT * FROM unnest($1::text[], $2::bigint[], $3::timestamptz[])
			ON CONFLICT (incarnation) DO UPDATE SET last_ns = greatest(ingest_pods.last_ns, EXCLUDED.last_ns),
				at = greatest(ingest_pods.at, EXCLUDED.at)`, incs, lastNS, ats)
	}
	br := tx.SendBatch(ctx, b)
	tag, err := br.Exec()
	if err == nil && tag.RowsAffected() != 1 {
		br.Close()
		return Result{}, ErrCursorMoved
	}
	for i := 1; err == nil && i < b.Len(); i++ {
		_, err = br.Exec()
	}
	if cerr := br.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Result{}, fmt.Errorf("tail: write: %w", err)
	}
	if err := t.hook("commit"); err != nil {
		return Result{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("tail: commit: %w", err)
	}
	return res, nil
}

// newNames is the index objects not read yet: after (the cursor's time −
// Grace), minus the grace names, in name order, at most MaxObjects.
func (t *Tail) newNames(ctx context.Context, c cursor) ([]string, error) {
	dir := keyflag.Prefix(t.cfg.Prefix)
	after := ""
	if c.last != "" {
		at, err := keyflag.NameTime(c.last)
		if err != nil {
			return nil, fmt.Errorf("tail: cursor %q: %w", c.last, err)
		}
		after = dir + at.Add(-t.cfg.Grace).UTC().Format(keyflag.TimeFormat)
	}
	objs, err := t.cfg.Store.ListAfter(ctx, dir, after)
	if err != nil {
		return nil, fmt.Errorf("tail: list: %w", err)
	}
	seen := make(map[string]bool, len(c.grace))
	for _, g := range c.grace {
		seen[g] = true
	}
	var out []string
	for _, o := range objs {
		name := strings.TrimPrefix(o.Key, dir)
		if !strings.HasSuffix(name, ".json") || seen[name] {
			continue
		}
		if _, err := keyflag.NameTime(name); err != nil {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	if len(out) > t.cfg.MaxObjects {
		out = out[:t.cfg.MaxObjects]
	}
	return out, nil
}

// read GETs the objects. One that's gone or isn't an index object is
// skipped (ok false); any other failure fails the pass.
func (t *Tail) read(ctx context.Context, names []string) ([]object, error) {
	dir := keyflag.Prefix(t.cfg.Prefix)
	out := make([]object, len(names))
	errs := make([]error, len(names))
	sem := make(chan struct{}, getParallel)
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			at, _ := keyflag.NameTime(name)
			out[i] = object{name: name, at: at}
			b, err := t.cfg.Store.Get(ctx, dir+name)
			if errors.Is(err, process.ErrNotFound) {
				slog.Warn("tail: an index object listed is gone; skipped", "name", name)
				return
			}
			if err != nil {
				errs[i] = fmt.Errorf("tail: get %s: %w", name, err)
				return
			}
			if err := json.Unmarshal(b, &out[i].body); err != nil {
				slog.Error("ALARM tail: an object under v1/recent/ isn't an index object; skipped", "name", name, "err", err)
				return
			}
			out[i].ok = true
		}()
	}
	wg.Wait()
	return out, errors.Join(errs...)
}

// advance is the cursor after reading names: the newest name, and the names
// (old and new) whose time is within Grace of it.
func (t *Tail) advance(c cursor, names []string) cursor {
	next := cursor{last: c.last}
	for _, n := range names {
		if n > next.last {
			next.last = n
		}
	}
	if next.last == "" {
		return cursor{grace: []string{}}
	}
	last, _ := keyflag.NameTime(next.last)
	next.grace = []string{}
	for _, n := range append(append([]string(nil), c.grace...), names...) {
		if at, err := keyflag.NameTime(n); err == nil && !at.Before(last.Add(-t.cfg.Grace)) {
			next.grace = append(next.grace, n)
		}
	}
	sort.Strings(next.grace)
	return next
}

func (t *Tail) hook(step string) error {
	if t.cfg.Hook != nil {
		return t.cfg.Hook(step)
	}
	return nil
}

// Run runs a pass every Every until ctx ends. A failed pass is retried with
// backoff (up to 30 s); a lost guard is retried at once.
func (t *Tail) Run(ctx context.Context) {
	wait := time.Duration(0)
	backoff := t.cfg.Every
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		r, err := t.Pass(ctx)
		switch {
		case err == nil:
			wait, backoff = t.cfg.Every, t.cfg.Every
			if r.Keys > 0 {
				slog.Debug("tail: pass", "objects", r.Objects, "keys", r.Keys, "cursor", r.Cursor)
			}
		case errors.Is(err, ErrCursorMoved):
			wait = 0
		case ctx.Err() != nil:
			return
		default:
			slog.Warn("tail: pass failed; the cursor holds, retrying", "err", err, "in", backoff)
			wait = backoff
			backoff = min(2*backoff, maxBackoff)
		}
	}
}

// Querier is what Age needs (a pool or a connection).
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Age is how long ago the tail last completed a pass, on Postgres's clock:
// /lag's tail part (§2 Scaling), so a stuck tail shows as lag. It's 0 before
// the first pass.
func Age(ctx context.Context, q Querier) (time.Duration, error) {
	var us int64
	if err := q.QueryRow(ctx, `SELECT coalesce((extract(epoch FROM now() - checked_at) * 1e6)::bigint, 0) FROM tail`).Scan(&us); err != nil {
		return 0, fmt.Errorf("tail: age: %w", err)
	}
	return max(time.Duration(us)*time.Microsecond, 0), nil
}

// RunLocked runs t on whichever worker holds the tail's advisory lock: it
// tries for the lock every retry, and runs the tail while it holds it. If the
// lock's connection drops, Postgres has freed the lock, so it stops and tries
// again. Correctness never depends on the lock (the cursor is guarded); it
// saves duplicate LISTs and GETs.
func RunLocked(ctx context.Context, pool *pgxpool.Pool, t *Tail, retry time.Duration) {
	for ctx.Err() == nil {
		held, release := tryLock(ctx, pool)
		if held != nil {
			slog.Info("tail: holding the lock; tailing the recent index")
			runCtx, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			go func() { defer close(done); t.Run(runCtx) }()
			watch(runCtx, held, retry)
			cancel()
			<-done
			release()
		}
		select {
		case <-ctx.Done():
		case <-time.After(retry):
		}
	}
}

// tryLock takes the lock on a connection of its own, or returns nil.
func tryLock(ctx context.Context, pool *pgxpool.Pool) (*pgxpool.Conn, func()) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, nil
	}
	var ok bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, LockID).Scan(&ok); err != nil || !ok {
		conn.Release()
		return nil, nil
	}
	return conn, func() {
		uctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(uctx, `SELECT pg_advisory_unlock($1)`, LockID); err != nil {
			conn.Conn().Close(uctx) //nolint:errcheck // closing frees the lock
		}
		conn.Release()
	}
}

// watch returns when ctx ends or the lock's connection stops answering.
func watch(ctx context.Context, conn *pgxpool.Conn, every time.Duration) {
	every = max(every, time.Second)
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
		if err := conn.Ping(ctx); err != nil {
			if ctx.Err() == nil {
				slog.Warn("tail: the lock's connection dropped; the lock is free", "err", err)
			}
			return
		}
	}
}
