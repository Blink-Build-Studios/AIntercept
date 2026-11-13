package worker

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// The claim (docs/PIPELINE.md §2 Claim): each tick a worker runs the flagged
// keys it already holds, then takes new ones, at most its share (claimable
// flags over live workers), trying the oldest few and a few dozen in its own
// hash order with pg_try_advisory_lock on its lock connection. Live workers
// come from heartbeats. The lock is for affinity only: correctness is the
// guard's.

// claimable is a flag that isn't quarantined ($q failures, retried after $retry).
const claimable = "(failures < $1 OR failed_at < $2)"

// lockOf is the advisory lock id of a key expression: its 64-bit hash.
func lockOf(expr string) string { return "hashtextextended(" + expr + ", 0)" }

// Tick is one pass: heartbeat, claim, run, release idle keys, singletons.
func (w *Worker) Tick(ctx context.Context) error {
	todo, clock, err := w.claim(ctx)
	if err != nil {
		return err
	}
	// Run them, Parallel at once, on the run connections.
	keys := make([]string, 0, len(todo))
	for k := range todo {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var (
		wg    sync.WaitGroup
		sem   = make(chan struct{}, w.cfg.Parallel)
		errMu sync.Mutex
		first error
	)
	for _, k := range keys {
		wg.Add(1)
		sem <- struct{}{}
		go func(k string) {
			defer wg.Done()
			defer func() { <-sem }()
			if _, err := w.runKey(ctx, k, clock); err != nil {
				errMu.Lock()
				if first == nil {
					first = fmt.Errorf("%s: %w", k, err)
				}
				errMu.Unlock()
			}
		}(k)
	}
	wg.Wait()

	w.lockMu.Lock()
	defer w.lockMu.Unlock()
	conn, err := w.lockConn(ctx)
	if err != nil {
		return errors.Join(first, err)
	}
	// Release the held keys with no input for Idle that weren't flagged.
	if err := w.releaseIdle(ctx, conn, todo); err != nil {
		return errors.Join(first, w.lockErr(err))
	}
	// The singletons whose lock it holds, when due.
	if err := w.runSingletons(ctx, conn); err != nil {
		return errors.Join(first, err)
	}
	return first
}

// claim heartbeats, reads data time, and returns the keys to run: the flagged
// keys it holds, and the new ones it locked.
func (w *Worker) claim(ctx context.Context) (map[string]bool, int64, error) {
	now := w.now()
	retry := now.Add(-w.cfg.Retry)
	w.lockMu.Lock()
	defer w.lockMu.Unlock()
	conn, err := w.lockConn(ctx)
	if err != nil {
		return nil, 0, err
	}
	if now.Sub(w.lastBeat) >= w.cfg.Heartbeat || w.lastBeat.IsZero() {
		if err := w.heartbeat(ctx, conn, now); err != nil {
			return nil, 0, w.lockErr(err)
		}
	}
	clock, err := dataTime(ctx, w.pool)
	if err != nil {
		return nil, 0, err
	}

	// 1. The keys it holds that are flagged.
	heldKeys := w.Held()
	todo := map[string]bool{}
	if len(heldKeys) > 0 {
		rows, err := conn.Query(ctx, "SELECT key FROM dirty_keys WHERE key = ANY($3) AND "+claimable, w.cfg.Quarantine, retry, heldKeys)
		if err != nil {
			return nil, 0, w.lockErr(err)
		}
		ks, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return nil, 0, w.lockErr(err)
		}
		for _, k := range ks {
			todo[k] = true
		}
	}
	// 2. New keys, at most its share.
	var dirty, live int
	if err := conn.QueryRow(ctx, "SELECT (SELECT count(*) FROM dirty_keys WHERE "+claimable+"), "+
		"(SELECT count(*) FROM workers WHERE seen > $3)", w.cfg.Quarantine, retry, now.Add(-w.cfg.Live)).Scan(&dirty, &live); err != nil {
		return nil, 0, w.lockErr(err)
	}
	share := int(math.Ceil(float64(dirty) / float64(max(live, 1))))
	if share > 0 {
		rows, err := conn.Query(ctx, `(SELECT key FROM dirty_keys WHERE `+claimable+` ORDER BY since LIMIT 4)
			UNION ALL (SELECT key FROM dirty_keys WHERE `+claimable+` ORDER BY hashtext(key || $3) LIMIT 64)`,
			w.cfg.Quarantine, retry, w.cfg.Name)
		if err != nil {
			return nil, 0, w.lockErr(err)
		}
		cands, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return nil, 0, w.lockErr(err)
		}
		mine := map[string]bool{}
		for _, k := range heldKeys {
			mine[k] = true
		}
		var try []string
		for _, k := range cands {
			if !mine[k] {
				mine[k] = true
				try = append(try, k)
			}
		}
		if len(try) > 0 {
			rows, err := conn.Query(ctx, "SELECT k FROM unnest($1::text[]) AS k WHERE pg_try_advisory_lock("+lockOf("k")+") LIMIT $2",
				try, share)
			if err != nil {
				return nil, 0, w.lockErr(err)
			}
			got, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return nil, 0, w.lockErr(err)
			}
			for _, k := range got {
				w.markLocked(k, now)
				todo[k] = true
			}
		}
	}

	return todo, clock, nil
}

// lockConn is the lock connection, (re)connected. Every key it held was
// forgotten when it dropped (lockErr).
func (w *Worker) lockConn(ctx context.Context) (*pgx.Conn, error) {
	if w.killed {
		return nil, errors.New("worker: killed")
	}
	if w.lock != nil && !w.lock.IsClosed() {
		return w.lock, nil
	}
	w.forgetAll()
	conn, err := pgx.ConnectConfig(ctx, w.lockCfg)
	if err != nil {
		return nil, err
	}
	w.lock = conn
	return conn, nil
}

// lockErr handles an error on the lock connection: if the connection is gone,
// Postgres has freed its locks, so the worker forgets every key it holds,
// with their warm states (another worker may hold them now), and its
// singletons.
func (w *Worker) lockErr(err error) error {
	if w.lock != nil && w.lock.IsClosed() {
		w.forgetAll()
		w.lock = nil
		return fmt.Errorf("%w: %w", ErrLockLost, err)
	}
	return err
}

// ErrLockLost is a tick that found its lock connection gone: the worker forgot
// its keys, and reconnects on the next tick.
var ErrLockLost = errors.New("worker: the lock connection dropped")

func (w *Worker) forgetAll() {
	w.mu.Lock()
	w.keys = map[string]*held{}
	w.mu.Unlock()
	for _, s := range w.singletons {
		s.held = false
	}
}

// Heartbeat upserts the worker's row in workers: it's live for Live.
func (w *Worker) Heartbeat(ctx context.Context) error {
	w.lockMu.Lock()
	defer w.lockMu.Unlock()
	conn, err := w.lockConn(ctx)
	if err != nil {
		return err
	}
	return w.lockErr(w.heartbeat(ctx, conn, w.now()))
}

func (w *Worker) heartbeat(ctx context.Context, conn *pgx.Conn, now time.Time) error {
	_, err := conn.Exec(ctx, `INSERT INTO workers (worker, seen) VALUES ($1, $2)
		ON CONFLICT (worker) DO UPDATE SET seen = excluded.seen`, w.cfg.Name, now)
	if err == nil {
		w.lastBeat = now
	}
	return err
}

// Held is the keys whose lock the worker holds, sorted.
func (w *Worker) Held() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	for k, h := range w.keys {
		if h.locked {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func (w *Worker) markLocked(k string, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	h := w.keys[k]
	if h == nil {
		h = &held{}
		w.keys[k] = h
	}
	h.locked, h.lastInput = true, now
}

// releaseIdle unlocks the held keys with no input for Idle that weren't just
// flagged, and drops their warm states.
func (w *Worker) releaseIdle(ctx context.Context, conn *pgx.Conn, flagged map[string]bool) error {
	now := w.now()
	var idle []string
	w.mu.Lock()
	for k, h := range w.keys {
		if h.locked && !flagged[k] && now.Sub(h.lastInput) >= w.cfg.Idle {
			idle = append(idle, k)
		}
	}
	w.mu.Unlock()
	return w.unlock(ctx, conn, idle)
}

func (w *Worker) unlock(ctx context.Context, conn *pgx.Conn, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_unlock("+lockOf("k")+") FROM unnest($1::text[]) AS k", keys); err != nil {
		return err
	}
	w.mu.Lock()
	for _, k := range keys {
		delete(w.keys, k)
	}
	w.mu.Unlock()
	return nil
}

// Release unlocks every key and singleton the worker holds (on SIGTERM).
func (w *Worker) Release(ctx context.Context) {
	w.lockMu.Lock()
	defer w.lockMu.Unlock()
	if w.lock == nil || w.lock.IsClosed() {
		w.forgetAll()
		return
	}
	if err := w.unlock(ctx, w.lock, w.Held()); err != nil {
		_ = w.lockErr(err)
	}
	for _, s := range w.singletons {
		if s.held {
			_, _ = w.lock.Exec(ctx, "SELECT pg_advisory_unlock($1)", s.Lock)
			s.held = false
		}
	}
}

// LockPID is the lock connection's server process (0: none). Tests only.
func (w *Worker) LockPID() uint32 {
	w.lockMu.Lock()
	defer w.lockMu.Unlock()
	if w.lock == nil || w.lock.IsClosed() {
		return 0
	}
	return w.lock.PgConn().PID()
}
