package worker

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Kill ends the worker as a crash would: the server ends its connections (its
// transactions abort, its locks are freed) and nothing is unlocked first.
func (w *Worker) Kill() {
	w.lockMu.Lock()
	if w.killed {
		w.lockMu.Unlock()
		return
	}
	w.killed = true
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if c, err := pgx.Connect(ctx, w.cfg.DSN); err == nil {
		_, _ = c.Exec(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name = ANY($1) AND datname = current_database()",
			[]string{runConnName(w.cfg.Name), "aintercept-lock-" + w.cfg.Name})
		_ = c.Close(ctx)
	}
	if w.lock != nil {
		_ = w.lock.Close(ctx)
		w.lock = nil
	}
	w.lockMu.Unlock()
	go w.pool.Close() // waits for a run still holding a connection to give it back
}
