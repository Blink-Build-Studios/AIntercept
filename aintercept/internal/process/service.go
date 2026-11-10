package process

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Run is `aintercept process`: polls the raw partitions and keeps the shaped
// tables current. Configured by environment:
//
//	PROCESS_LOCAL_DIR / PROCESS_OUT_DIR   a local raw directory and where to write (default <dir>/../shaped)
//	PROCESS_S3_BUCKET, _REGION, _ENDPOINT, _PREFIX, _ACCESS_KEY_ID, _SECRET_ACCESS_KEY, _FORCE_PATH_STYLE
//	                                      or an S3-compatible bucket (tables under <prefix>/shaped)
//	PROCESS_RAW_PREFIX                    the raw stream's prefix, when not <prefix> (full interception: <prefix>/capture)
//	PROCESS_POLL_SECONDS                  between polls (default 10)
//	PROCESS_FULL_MINUTES                  between full listings (default 60; one also runs at start)
//	PROCESS_HTTP_LISTEN                   /healthz and /stats (default 0.0.0.0:9002)
//	PROCESS_ONCE=true                     one full pass, then exit
//	PROCESS_SWEEP_ALL=true                include sealed days (for a raw file that landed days late)
//	PROCESS_COMPACT_AT                    run parts per table before compacting into day files (default 64)
func Run() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	cfg, err := configFromEnv(ctx)
	if err != nil {
		slog.Error("process: config", "err", err)
		os.Exit(1)
	}
	cfg.SweepAll = os.Getenv("PROCESS_SWEEP_ALL") == "true"
	if n := envInt("PROCESS_COMPACT_AT", 0); n > 0 {
		cfg.CompactAt = n
	}
	p := New(cfg)
	if err := p.Open(ctx); err != nil {
		slog.Error("process: open", "err", err)
		os.Exit(1)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("/stats", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		json.NewEncoder(w).Encode(p.Stats())
	})
	srv := &http.Server{Addr: envOr("PROCESS_HTTP_LISTEN", "0.0.0.0:9002"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("process: http", "err", err)
		}
	}()
	if os.Getenv("PROCESS_ONCE") == "true" { // one full pass, then exit (tests, backfills)
		if _, err := p.Poll(ctx, true); err != nil {
			slog.Error("process: poll", "err", err)
			os.Exit(1)
		}
		srv.Close()
		return
	}
	poll := time.Duration(envInt("PROCESS_POLL_SECONDS", 10)) * time.Second
	full := time.Duration(envInt("PROCESS_FULL_MINUTES", 60)) * time.Minute
	slog.Info("process: started", "run", p.Stats().Run, "poll", poll, "full", full)
	lastFull := time.Time{}
	for {
		isFull := time.Since(lastFull) >= full
		n, err := p.Poll(ctx, isFull)
		if err != nil {
			slog.Warn("process: poll failed; retrying next poll", "err", err) // nothing was committed
		} else {
			if isFull {
				lastFull = time.Now()
			}
			if n > 0 {
				s := p.Stats()
				slog.Info("process: committed", "run", s.Run, "files", n, "lag_seconds", int(s.LagSeconds), "sessions", s.Sessions)
			}
		}
		select {
		case <-ctx.Done():
			srv.Close()
			return
		case <-time.After(poll):
		}
	}
}

func configFromEnv(ctx context.Context) (Config, error) {
	if dir := os.Getenv("PROCESS_LOCAL_DIR"); dir != "" {
		out := envOr("PROCESS_OUT_DIR", strings.TrimRight(dir, "/")+"/../shaped")
		if err := SeparateDirs(dir, out); err != nil {
			return Config{}, err
		}
		return Config{Raw: DirStore{Root: dir}, Out: DirStore{Root: out}}, nil
	}
	s, err := NewS3Store(ctx, S3Config{
		Bucket: os.Getenv("PROCESS_S3_BUCKET"), Region: os.Getenv("PROCESS_S3_REGION"), Endpoint: os.Getenv("PROCESS_S3_ENDPOINT"),
		AccessKeyID: os.Getenv("PROCESS_S3_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("PROCESS_S3_SECRET_ACCESS_KEY"),
		ForcePathStyle: os.Getenv("PROCESS_S3_FORCE_PATH_STYLE") == "true",
	})
	if err != nil {
		return Config{}, err
	}
	prefix := strings.Trim(os.Getenv("PROCESS_S3_PREFIX"), "/")
	out := "shaped"
	if prefix != "" {
		out = prefix + "/shaped"
	}
	// Full interception (the observer's DEV_CAPTURE_FULL_BODIES) writes the raw stream with
	// bodies under <prefix>/capture: read that one instead (the tables stay under <prefix>/shaped).
	raw := prefix
	if r := strings.Trim(os.Getenv("PROCESS_RAW_PREFIX"), "/"); r != "" {
		raw = r
	}
	return Config{Raw: s, RawPrefix: raw, Out: s, OutPrefix: out}, nil
}

// SeparateDirs refuses an output directory that is, holds or sits inside the
// raw one: the processor deletes files in its output that no commit names.
func SeparateDirs(raw, out string) error {
	a, err := filepath.Abs(raw)
	if err != nil {
		return err
	}
	b, err := filepath.Abs(out)
	if err != nil {
		return err
	}
	if a == b || strings.HasPrefix(a+"/", b+"/") || strings.HasPrefix(b+"/", a+"/") {
		return fmt.Errorf("output directory %s must be separate from the raw directory %s", b, a)
	}
	return nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(k)); err == nil && n > 0 {
		return n
	}
	return def
}
