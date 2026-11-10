package process

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// Mirror is the query API's sidecar in the cluster (`aintercept mirror`). The API
// reads the processor's tables from local disk, and raw files only for the
// drawer. So the mirror:
//   - keeps a local copy of the shaped tables: the parts the newest manifest
//     names first, then the manifest, then deletions, so the API never reads a
//     manifest naming files it doesn't have;
//   - serves raw objects on demand over localhost HTTP (/raw/list, /raw/get),
//     so the bucket's raw partitions are never copied in bulk.
type Mirror struct {
	Src    Store
	Shaped string // the tables' prefix in Src ("<prefix>/shaped")
	Raw    string // the raw partitions' prefix in Src ("<prefix>"); /raw serves only under it
	Dir    string // the local copy of the tables

	last  []byte          // the manifest last mirrored
	prev  map[string]bool // the local files it named: kept one more commit
	ready atomic.Bool
}

// Sync mirrors the newest commit. Returns whether anything changed.
func (m *Mirror) Sync(ctx context.Context) (bool, error) {
	b, err := m.Src.Get(ctx, m.Shaped+"/manifest.json")
	if errors.Is(err, ErrNotFound) {
		m.ready.Store(true) // nothing processed yet: the API serves empty tables
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read manifest: %w", err)
	}
	if bytes.Equal(b, m.last) {
		return false, nil
	}
	var man Manifest
	if err := json.Unmarshal(b, &man); err != nil {
		return false, fmt.Errorf("parse manifest: %w", err)
	}
	parts := man.allParts()
	want := map[string]bool{}
	// Parts are immutable (each run writes new names), so one already here is current.
	for k := range parts {
		p := m.local(k)
		want[p] = true
		if _, err := os.Stat(p); err == nil {
			continue
		}
		data, err := m.Src.Get(ctx, k)
		if err != nil {
			return false, fmt.Errorf("read %s: %w", k, err)
		}
		if err := writeFileAtomic(p, data); err != nil {
			return false, err
		}
	}
	manifest := filepath.Join(m.Dir, "manifest.json")
	if err := writeFileAtomic(manifest, b); err != nil {
		return false, err
	}
	// Files only the previous commit named stay until the next one: the API may
	// be loading that commit right now.
	err = filepath.WalkDir(m.Dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || p == manifest || want[p] || m.prev[p] {
			return err
		}
		return os.Remove(p)
	})
	if err != nil {
		return false, fmt.Errorf("prune: %w", err)
	}
	m.last, m.prev = b, want
	m.ready.Store(true)
	return true, nil
}

func (man Manifest) allParts() map[string]bool {
	out := map[string]bool{}
	for _, keys := range man.Facts {
		for _, k := range keys {
			out[k] = true
		}
	}
	for _, k := range man.Snapshots {
		out[k] = true
	}
	return out
}

func (m *Mirror) local(key string) string {
	return filepath.Join(m.Dir, filepath.FromSlash(strings.TrimPrefix(key, m.Shaped+"/")))
}

func writeFileAtomic(p string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// rawKey reports whether a key or folder is a raw partition the mirror may serve.
func (m *Mirror) rawKey(k string) bool {
	return strings.HasPrefix(k, m.Raw+"/") && !strings.HasPrefix(k, m.Shaped+"/") && !strings.Contains(k, "..")
}

// Handler serves /healthz (ready once the tables are mirrored) and the raw objects.
func (m *Mirror) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		if !m.ready.Load() {
			http.Error(w, "not mirrored yet", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok\n"))
	})
	// The keys directly in a folder ("<prefix>/dt=…/hour=…/<pod>/"), one per line.
	mux.HandleFunc("/raw/list", func(w http.ResponseWriter, r *http.Request) {
		prefix := r.URL.Query().Get("prefix")
		if !m.rawKey(prefix) || !strings.HasSuffix(prefix, "/") {
			http.Error(w, "not a raw folder", http.StatusBadRequest)
			return
		}
		objs, err := m.Src.ListAfter(r.Context(), prefix, "")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		for _, o := range objs {
			fmt.Fprintln(w, o.Key)
		}
	})
	mux.HandleFunc("/raw/get", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		if !m.rawKey(key) {
			http.Error(w, "not a raw key", http.StatusBadRequest)
			return
		}
		b, err := m.Src.Get(r.Context(), key)
		switch {
		case errors.Is(err, ErrNotFound):
			http.NotFound(w, r)
		case err != nil:
			http.Error(w, err.Error(), http.StatusBadGateway)
		default:
			w.Header().Set("content-type", "application/gzip")
			w.Write(b)
		}
	})
	return mux
}

// RunMirror is `aintercept mirror`. Configured by environment:
//
//	PROCESS_S3_*            the bucket, as for `aintercept process`
//	MIRROR_DIR              where the tables are copied (the API's AINTERCEPT_SHAPED_DIR)
//	MIRROR_POLL_SECONDS     between manifest checks (default 10)
//	MIRROR_HTTP_LISTEN      /healthz and /raw (default 127.0.0.1:9003: the pod only)
func RunMirror() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	cfg, err := configFromEnv(ctx)
	if err != nil {
		slog.Error("mirror: config", "err", err)
		os.Exit(1)
	}
	dir := os.Getenv("MIRROR_DIR")
	s3, ok := cfg.Raw.(*S3Store)
	if dir == "" || !ok || cfg.RawPrefix == "" {
		slog.Error("mirror: needs MIRROR_DIR and a bucket with a prefix (PROCESS_S3_*)")
		os.Exit(1)
	}
	m := &Mirror{Src: s3, Shaped: cfg.OutPrefix, Raw: cfg.RawPrefix, Dir: dir}
	srv := &http.Server{Addr: envOr("MIRROR_HTTP_LISTEN", "127.0.0.1:9003"), Handler: m.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("mirror: http", "err", err)
		}
	}()
	poll := time.Duration(envInt("MIRROR_POLL_SECONDS", 10)) * time.Second
	slog.Info("mirror: started", "dir", dir, "poll", poll)
	for {
		changed, err := m.Sync(ctx)
		if err != nil {
			slog.Warn("mirror: sync failed; retrying next poll", "err", err)
		} else if changed {
			slog.Info("mirror: synced")
		}
		select {
		case <-ctx.Done():
			srv.Close()
			return
		case <-time.After(poll):
		}
	}
}
