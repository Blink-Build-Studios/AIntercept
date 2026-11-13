package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/tail"
)

// Singletons (docs/PIPELINE.md §2): each runs on whichever worker holds its
// advisory lock, a fixed id, on that worker's lock connection; no new
// component. A worker tries to take each one every tick and keeps it while its
// lock connection lives. The due calls and the tail (TailSingleton) are the
// service's; retention registers its own in Config.Singletons.

// Singleton is a periodic job one worker runs at a time.
type Singleton struct {
	Name  string
	Lock  int64         // its advisory lock id: fixed, distinct from every other's
	Every time.Duration // how often it runs
	// Run does one pass. A failure is retried on the next tick.
	Run func(ctx context.Context, now time.Time) error
}

type singleton struct {
	Singleton
	held    bool
	lastRun time.Time
}

// runSingletons takes the singletons' locks it can and runs the due ones.
func (w *Worker) runSingletons(ctx context.Context, conn *pgx.Conn) error {
	now := w.now()
	var errs []error
	for _, s := range w.singletons {
		if !s.held {
			if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", s.Lock).Scan(&s.held); err != nil {
				return w.lockErr(err)
			}
			if !s.held {
				continue
			}
			s.lastRun = time.Time{}
		}
		if !s.lastRun.IsZero() && now.Sub(s.lastRun) < s.Every {
			continue
		}
		if err := s.Run(ctx, now); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.Name, err))
			continue
		}
		s.lastRun = now
	}
	return errors.Join(errs...)
}

// Singletons is the names of the singletons this worker holds.
func (w *Worker) Singletons() []string {
	w.lockMu.Lock()
	defer w.lockMu.Unlock()
	var out []string
	for _, s := range w.singletons {
		if s.held {
			out = append(out, s.Name)
		}
	}
	return out
}

// Advisory lock ids of the singletons ("aintercept" in ASCII, then a number;
// pg.Apply's schema lock is …0001).
// The tail's is tail.LockID.
const (
	DueCallsLock  int64 = 0x61696e7465720010
	RetentionLock int64 = 0x61696e7465720012
)

// DueCalls is the due calls singleton (§2, every 30 s): it flags every key
// whose first call waiting for its ending is due by data time, so a quiet
// key's calls expire. It replaces the merge's flagging.
func DueCalls(w *Worker) Singleton {
	return Singleton{Name: "due-calls", Lock: DueCallsLock, Every: 30 * time.Second,
		Run: func(ctx context.Context, now time.Time) error { return w.flagDueCalls(ctx, now) }}
}

// FlagDueCalls runs the due calls' pass once.
func (w *Worker) FlagDueCalls(ctx context.Context) error { return w.flagDueCalls(ctx, w.now()) }

func (w *Worker) flagDueCalls(ctx context.Context, now time.Time) error {
	_, err := w.pool.Exec(ctx, `INSERT INTO dirty_keys (key, seq, since)
		SELECT key, 1, $1 FROM keys WHERE expires_ns > 0 AND expires_ns < (SELECT coalesce(max(newest_row_ns), 0) FROM keys)
		ON CONFLICT (key) DO UPDATE SET seq = dirty_keys.seq + 1`, now)
	return err
}

// TailSingleton is the tail (internal/tail, §2 discovery) as a singleton: a
// pass every 2 s on whichever worker holds tail.LockID. A pass that lost the
// cursor's guard to another tail isn't a failure.
func TailSingleton(t *tail.Tail) Singleton {
	return Singleton{Name: "tail", Lock: tail.LockID, Every: tail.DefaultEvery, Run: func(ctx context.Context, _ time.Time) error {
		if _, err := t.Pass(ctx); err != nil && !errors.Is(err, tail.ErrCursorMoved) {
			return err
		}
		return nil
	}}
}
