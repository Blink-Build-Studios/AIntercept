package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/refresh"
)

// Run is `aintercept worker`: v9's workers (docs/PIPELINE.md §2.2, §5). Every
// second each replica lists new raw files (when it holds the listing's lock
// and ListEvery has passed), loads unloaded files until none is left,
// refreshes dirty rollup rows until none is left to claim, and runs the
// housekeeping when it's due and the replica holds its lock.
// Configured by environment:
//
//	WORKER_POSTGRES_URL    the worker login's DSN (required)
//	WORKER_LOCAL_DIR       a local directory as the store (development), or
//	WORKER_S3_BUCKET, _REGION, _ENDPOINT, _PREFIX, _ACCESS_KEY_ID, _SECRET_ACCESS_KEY, _FORCE_PATH_STYLE
//	WORKER_LOADERS         files loaded at once (default 2)
//	WORKER_REFRESHERS      rollup rows refreshed at once (default 2)
//	WORKER_LIST_SECONDS    how often to list (default 5)
//	WORKER_RETENTION_DAYS  facts older than this are deleted (default 15; 0: never)
//	WORKER_SCHEMA_RESET    a database holding another build's schema is reset to
//	                       this one, and every fact reloads from the raw files
//	                       (default true; false: the worker refuses to start)
//	WORKER_HTTP_LISTEN     /healthz, /lag and /metrics (default 0.0.0.0:9005)
func Run() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	fail := func(msg string, err error) {
		slog.Error("worker: "+msg, "err", err)
		os.Exit(1)
	}
	dsn := os.Getenv("WORKER_POSTGRES_URL")
	if dsn == "" {
		fail("WORKER_POSTGRES_URL is required (the worker login's DSN)", nil)
	}
	store, prefix, err := storeFromEnv(ctx)
	if err != nil {
		fail("store", err)
	}
	loaders, refreshers := envInt("WORKER_LOADERS", 2), envInt("WORKER_REFRESHERS", 2)
	db, err := pg.Open(ctx, dsn, int32(loaders+refreshers+2))
	if err != nil {
		fail("Postgres", err)
	}
	defer db.Close()
	if os.Getenv("WORKER_SCHEMA_RESET") == "false" {
		if err := pg.Apply(ctx, db); err != nil {
			fail("schema", err) // a schema that isn't this build's refuses to start
		}
	} else if reset, err := pg.ApplyOrReset(ctx, db); err != nil {
		fail("schema", err)
	} else if reset {
		slog.Warn("ALARM worker: the database held another build's schema: reset it to this one; every fact reloads from the raw files",
			"schema", pg.SchemaHash)
	}
	addr := os.Getenv("WORKER_HTTP_LISTEN")
	if addr == "" {
		addr = "0.0.0.0:9005"
	}
	srv := &http.Server{Addr: addr, Handler: Handler(db), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("worker: http", "err", err)
		}
	}()
	days := 15
	if n, err := strconv.Atoi(os.Getenv("WORKER_RETENTION_DAYS")); err == nil && n >= 0 {
		days = n
	}
	slog.Info("worker: running", "loaders", loaders, "refreshers", refreshers, "retention_days", days, "prefix", prefix)
	hk := &refresh.Housekeeper{SweepEvery: 30 * time.Second, RetainEvery: time.Hour, RetentionDays: days}
	Loop(ctx, db, store, prefix, loaders, refreshers, hk, time.Duration(envInt("WORKER_LIST_SECONDS", 5))*time.Second)
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
}

// drain is refresh.Drain; tests stand in a slow one.
var drain = refresh.Drain

// Loop lists, loads, refreshes and keeps house until ctx ends. A failure
// (Postgres or the store down) is logged and retried on the next tick. hk nil:
// no housekeeping. The refreshers run on their own, beside listing and loading,
// so a slow refresh never holds up new data landing in Postgres.
func Loop(ctx context.Context, db *pgxpool.Pool, store process.Store, prefix string, loaders, refreshers int,
	hk *refresh.Housekeeper, listEvery time.Duration) {
	var refreshing sync.WaitGroup
	defer refreshing.Wait()
	for i := 0; i < refreshers; i++ {
		refreshing.Add(1)
		go func() {
			defer refreshing.Done()
			t := time.NewTicker(time.Second)
			defer t.Stop()
			for {
				if _, err := drain(ctx, db); err != nil && ctx.Err() == nil {
					slog.Warn("worker: refresh", "err", err)
				}
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			}
		}()
	}
	var listed time.Time
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		if time.Since(listed) >= listEvery {
			if n, ok, err := List(ctx, db, store, prefix); err != nil && ctx.Err() == nil {
				slog.Warn("worker: list", "err", err)
			} else if ok {
				listed = time.Now()
				if n > 0 {
					slog.Debug("worker: listed", "new", n)
				}
			}
		}
		var wg sync.WaitGroup
		for i := 0; i < max(loaders, 1); i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var failed []string
				for ctx.Err() == nil {
					key, err := Load(ctx, db, store, failed)
					if err != nil && ctx.Err() == nil {
						slog.Warn("ALARM worker: load failed; retried next pass", "key", key, "err", err)
					}
					if key == "" {
						return
					}
					if err != nil {
						failed = append(failed, key)
					}
				}
			}()
		}
		wg.Wait()
		if hk != nil {
			if err := hk.Run(ctx, db); err != nil && ctx.Err() == nil {
				slog.Warn("worker: housekeeping", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Handler serves /healthz, /lag ({"oldest_unloaded_seconds": s}: the oldest
// unloaded file or dirty rollup row) and /metrics. With Postgres unreachable
// /lag errors, and KEDA holds the count.
func Handler(db *pgxpool.Pool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(rw http.ResponseWriter, _ *http.Request) { _, _ = rw.Write([]byte("ok\n")) })
	mux.HandleFunc("/lag", func(rw http.ResponseWriter, r *http.Request) {
		lag, err := Lag(r.Context(), db)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusServiceUnavailable)
			return
		}
		rw.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(rw).Encode(map[string]any{"oldest_unloaded_seconds": lag.Seconds()})
	})
	mux.HandleFunc("/metrics", func(rw http.ResponseWriter, r *http.Request) {
		lag, err := Lag(r.Context(), db)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusServiceUnavailable)
			return
		}
		rw.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(rw, "# TYPE aintercept_ingest_lag_seconds gauge\naintercept_ingest_lag_seconds %g\n", lag.Seconds())
	})
	return mux
}

// storeFromEnv is the store and prefix the WORKER_* environment names.
func storeFromEnv(ctx context.Context) (process.Store, string, error) {
	if dir := os.Getenv("WORKER_LOCAL_DIR"); dir != "" {
		return process.DirStore{Root: dir}, "", nil
	}
	if os.Getenv("WORKER_S3_BUCKET") == "" {
		return nil, "", errors.New("WORKER_LOCAL_DIR or WORKER_S3_BUCKET is required")
	}
	s, err := process.NewS3Store(ctx, process.S3Config{
		Bucket: os.Getenv("WORKER_S3_BUCKET"), Region: os.Getenv("WORKER_S3_REGION"), Endpoint: os.Getenv("WORKER_S3_ENDPOINT"),
		AccessKeyID: os.Getenv("WORKER_S3_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("WORKER_S3_SECRET_ACCESS_KEY"),
		ForcePathStyle: os.Getenv("WORKER_S3_FORCE_PATH_STYLE") == "true"})
	if err != nil {
		return nil, "", err
	}
	return s, os.Getenv("WORKER_S3_PREFIX"), nil
}

func envInt(key string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(key)); err == nil && n > 0 {
		return n
	}
	return def
}
