package rollup

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream"
)

// Run is `aintercept rollup`: one rollup replica (docs/PIPELINE.md §2).
// Configured by environment:
//
//	ROLLUP_LOCAL_DIR                      a local directory as the store (development), or
//	ROLLUP_S3_BUCKET, _REGION, _ENDPOINT, _PREFIX, _ACCESS_KEY_ID, _SECRET_ACCESS_KEY, _FORCE_PATH_STYLE
//	ROLLUP_REDIS_ADDR                     host:port of the in-cluster Redis (default redis:6379)
//	ROLLUP_REPLICA                        this replica's name (default the hostname)
//	ROLLUP_PARALLEL                       keys run at once (default 8)
//	ROLLUP_HTTP_LISTEN                    /healthz and /lag (default 0.0.0.0:9004)
func Run() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	s, prefix, err := storeFromEnv(ctx)
	if err != nil {
		slog.Error("rollup: store", "err", err)
		os.Exit(1)
	}
	host, _ := os.Hostname()
	rdb := redis.NewClient(&redis.Options{Addr: envOr("ROLLUP_REDIS_ADDR", "redis:6379")})
	defer rdb.Close()
	w := New(Config{Store: s, Redis: rdb, Prefix: prefix, Replica: envOr("ROLLUP_REPLICA", host),
		Model: stream.StateModel{}, Parallel: envInt("ROLLUP_PARALLEL", 8)})
	srv := &http.Server{Addr: envOr("ROLLUP_HTTP_LISTEN", "0.0.0.0:9004"), Handler: w.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("rollup: http", "err", err)
		}
	}()
	slog.Info("rollup: running", "replica", w.cfg.Replica)
	if err := w.Run(ctx); err != nil {
		slog.Error("rollup: run", "err", err)
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
}

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

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func envInt(k string, d int) int {
	if n, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return n
	}
	return d
}
