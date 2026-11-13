package stream

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
)

// Run is `aintercept stream`: one processor replica. Configured by environment:
//
//	STREAM_LOCAL_DIR                      a local directory as the store (development), or
//	STREAM_S3_BUCKET, _REGION, _ENDPOINT, _PREFIX, _ACCESS_KEY_ID, _SECRET_ACCESS_KEY, _FORCE_PATH_STYLE
//	STREAM_BUCKET_BITS                    key buckets = 2^bits (default 6, matching ingest)
//	STREAM_CYCLE_SECONDS                  how often an owned bucket looks for new records (default 5)
//	STREAM_REPLICA                        this replica's name (the pod's; default the hostname)
//	STREAM_HTTP_LISTEN                    /healthz and /lag (default 0.0.0.0:9003)
//
// It runs the derived tables' per-key state (StateModel, §4.5).
func Run() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	s, prefix, err := storeFromEnv(ctx)
	if err != nil {
		slog.Error("stream: store", "err", err)
		os.Exit(1)
	}
	host, _ := os.Hostname()
	cfg := Config{Store: s, Layout: Layout{Prefix: prefix}, Model: StateModel{},
		Replica: envOr("STREAM_REPLICA", host), BucketBits: envInt("STREAM_BUCKET_BITS", 6),
		Cycle: time.Duration(envInt("STREAM_CYCLE_SECONDS", 5)) * time.Second}
	p := New(cfg)
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })
	mux.Handle("/lag", NewLagHandler(s, cfg.Layout, p.Buckets(), RealClock{}))
	srv := &http.Server{Addr: envOr("STREAM_HTTP_LISTEN", "0.0.0.0:9003"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("stream: http", "err", err)
		}
	}()
	slog.Info("stream: running", "replica", cfg.Replica, "buckets", p.Buckets())
	if err := p.Run(ctx); err != nil {
		slog.Error("stream: run", "err", err)
	}
	p.Wait()
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
}

func storeFromEnv(ctx context.Context) (Store, string, error) {
	if dir := os.Getenv("STREAM_LOCAL_DIR"); dir != "" {
		return DirStore{process.DirStore{Root: dir}}, "", nil
	}
	s, err := process.NewS3Store(ctx, process.S3Config{
		Bucket: os.Getenv("STREAM_S3_BUCKET"), Region: os.Getenv("STREAM_S3_REGION"), Endpoint: os.Getenv("STREAM_S3_ENDPOINT"),
		AccessKeyID: os.Getenv("STREAM_S3_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("STREAM_S3_SECRET_ACCESS_KEY"),
		ForcePathStyle: os.Getenv("STREAM_S3_FORCE_PATH_STYLE") == "true"})
	if err != nil {
		return nil, "", err
	}
	return S3Store{s}, os.Getenv("STREAM_S3_PREFIX"), nil
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
