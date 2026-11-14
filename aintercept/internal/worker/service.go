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

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/tail"
)

// Run is `aintercept rollup`: one v7 worker (docs/PIPELINE.md §2), with the
// tail, the due calls and retention as singletons. Configured by environment:
//
//	ROLLUP_POSTGRES_URL        the worker login's DSN (aintercept_worker; required)
//	ROLLUP_LOCAL_DIR           a local directory as the store (development), or
//	ROLLUP_S3_BUCKET, _REGION, _ENDPOINT, _PREFIX, _ACCESS_KEY_ID, _SECRET_ACCESS_KEY, _FORCE_PATH_STYLE
//	ROLLUP_REPLICA             this worker's name (default the hostname)
//	ROLLUP_PARALLEL            keys run at once (default 8)
//	ROLLUP_RETENTION_DAYS      keys whose day is older are forgotten (default 15; 0: never)
//	ROLLUP_RETENTION_RAW_DAYS  the local raw cap (default 0: off; on a local directory it covers calls/ too)
//	ROLLUP_HTTP_LISTEN         /healthz, /lag and /metrics (default 0.0.0.0:9004)
func Run() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	dsn := os.Getenv("ROLLUP_POSTGRES_URL")
	if dsn == "" {
		slog.Error("worker: ROLLUP_POSTGRES_URL is required (the worker login's DSN)")
		os.Exit(1)
	}
	s, prefix, err := storeFromEnv(ctx)
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
	w, err := New(ctx, Config{Store: s, Prefix: prefix, Name: name, DSN: dsn, Parallel: parallel})
	if err != nil {
		slog.Error("worker: Postgres", "err", err) // a schema mismatch refuses to start
		os.Exit(1)
	}
	w.AddSingleton(TailSingleton(tail.New(tail.Config{DB: w.Pool(), Store: s, Prefix: prefix})))
	r := retentionFromEnv(os.Getenv)
	r.Calls = os.Getenv("ROLLUP_LOCAL_DIR") != ""
	w.AddSingleton(RetentionSingleton(w, r))
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

// storeFromEnv is the store and prefix the ROLLUP_* environment names.
func storeFromEnv(ctx context.Context) (stream.Store, string, error) {
	if dir := os.Getenv("ROLLUP_LOCAL_DIR"); dir != "" {
		return stream.DirStore{DirStore: process.DirStore{Root: dir}}, "", nil
	}
	s, err := process.NewS3Store(ctx, process.S3Config{
		Bucket: os.Getenv("ROLLUP_S3_BUCKET"), Region: os.Getenv("ROLLUP_S3_REGION"), Endpoint: os.Getenv("ROLLUP_S3_ENDPOINT"),
		AccessKeyID: os.Getenv("ROLLUP_S3_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("ROLLUP_S3_SECRET_ACCESS_KEY"),
		ForcePathStyle: os.Getenv("ROLLUP_S3_FORCE_PATH_STYLE") == "true"})
	if err != nil {
		return nil, "", err
	}
	return stream.S3Store{S3Store: s}, os.Getenv("ROLLUP_S3_PREFIX"), nil
}
