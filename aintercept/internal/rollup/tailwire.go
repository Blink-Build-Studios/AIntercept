package rollup

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/tail"
)

// startTail runs the tail singleton (docs/PIPELINE.md §2: discovery, the
// recent index into dirty_keys) on whichever replica holds its lock, when
// ROLLUP_POSTGRES_URL names the workers' Postgres. It returns once the tail
// has stopped (ctx ended).
//
// TODO(v7 step 2): the worker's singleton hook and its Postgres pool replace
// this wiring: register tail.New(...).Run under tail.LockID there, and /lag
// takes the larger of the flags' age and tail.Age.
func startTail(ctx context.Context, s stream.Store, prefix string) {
	dsn := os.Getenv("ROLLUP_POSTGRES_URL")
	if dsn == "" {
		slog.Info("rollup: no ROLLUP_POSTGRES_URL; the tail (discovery into dirty_keys) isn't running")
		return
	}
	pool, err := pg.Open(ctx, dsn, 2)
	if err != nil {
		slog.Error("rollup: the tail's Postgres", "err", err)
		return
	}
	defer pool.Close()
	if err := pg.Apply(ctx, pool); err != nil {
		slog.Error("rollup: the tail's Postgres schema", "err", err)
		return
	}
	tail.RunLocked(ctx, pool, tail.New(tail.Config{DB: pool, Store: s, Prefix: prefix}), 5*time.Second)
}
