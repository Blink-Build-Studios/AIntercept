package worker

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/rollup"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/tail"
)

// Run is `aintercept rollup` with ROLLUP_POSTGRES_URL set: one v7 worker
// (docs/PIPELINE.md §2), with the tail and the due calls as singletons.
// Configured by environment:
//
//	ROLLUP_POSTGRES_URL   the worker login's DSN (aintercept_worker)
//	ROLLUP_LOCAL_DIR, or ROLLUP_S3_* as rollup.Run: the store
//	ROLLUP_REPLICA        this worker's name (default the hostname)
//	ROLLUP_PARALLEL       keys run at once (default 8)
//	ROLLUP_HTTP_LISTEN    /healthz, /lag and /metrics (default 0.0.0.0:9004)
func Run() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	s, prefix, err := rollup.StoreFromEnv(ctx)
	if err != nil {
		slog.Error("worker: store", "err", err)
		os.Exit(1)
	}
	host, _ := os.Hostname()
	name := os.Getenv("ROLLUP_REPLICA")
	if name == "" {
		name = host
	}
	parallel, _ := strconv.Atoi(os.Getenv("ROLLUP_PARALLEL"))
	w, err := New(ctx, Config{Store: s, Prefix: prefix, Name: name, DSN: os.Getenv("ROLLUP_POSTGRES_URL"), Parallel: parallel})
	if err != nil {
		slog.Error("worker: Postgres", "err", err) // a schema mismatch refuses to start
		os.Exit(1)
	}
	w.AddSingleton(TailSingleton(tail.New(tail.Config{DB: w.Pool(), Store: s, Prefix: prefix})))
	addr := os.Getenv("ROLLUP_HTTP_LISTEN")
	if addr == "" {
		addr = "0.0.0.0:9004"
	}
	srv := &http.Server{Addr: addr, Handler: w.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("worker: http", "err", err)
		}
	}()
	slog.Info("worker: running", "name", name)
	w.Run(ctx)
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	w.pool.Close()
}

// AddSingleton registers a singleton before Run.
func (w *Worker) AddSingleton(s Singleton) {
	w.lockMu.Lock()
	defer w.lockMu.Unlock()
	w.singletons = append(w.singletons, &singleton{Singleton: s})
}

// Run ticks every second until ctx ends (Postgres down: each tick fails and
// the next retries), then unlocks every key and singleton it holds.
func (w *Worker) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		if err := w.Tick(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("worker: tick", "err", err)
		}
		select {
		case <-ctx.Done():
			rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			w.Release(rctx)
			cancel()
			return
		case <-t.C:
		}
	}
}
