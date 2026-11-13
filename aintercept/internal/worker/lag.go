package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/tail"
)

// /lag (docs/PIPELINE.md §2 Scaling): the larger of the oldest flag's wait
// (quarantined keys aside) and the tail's, the same on every replica. KEDA's
// metrics-api trigger reads it. With Postgres unreachable it errors, and KEDA
// holds the replica count.

// Lag is how long the oldest work has waited: the oldest claimable flag's
// since, and the tail's last pass (tail.Age, 0 before its first).
func (w *Worker) Lag(ctx context.Context) (time.Duration, error) {
	var secs float64
	err := w.pool.QueryRow(ctx, `SELECT greatest(0,
		coalesce(extract(epoch FROM $1::timestamptz - (SELECT min(since) FROM dirty_keys WHERE failures < $2)), 0))::float8`,
		w.now(), w.cfg.Quarantine).Scan(&secs)
	if err != nil {
		return 0, err
	}
	age, err := tail.Age(ctx, w.pool)
	if err != nil {
		return 0, err
	}
	return max(time.Duration(math.Round(secs*1e6))*time.Microsecond, age), nil
}

// Quarantined is how many keys have failed Quarantine times in a row: the
// quarantined_keys gauge.
func (w *Worker) Quarantined(ctx context.Context) (int, error) {
	var n int
	err := w.pool.QueryRow(ctx, "SELECT count(*) FROM dirty_keys WHERE failures >= $1", w.cfg.Quarantine).Scan(&n)
	return n, err
}

// Handler serves /healthz, /lag ({"oldest_unapplied_seconds": s,
// "quarantined_keys": n}) and /metrics (the gauges, Prometheus text).
func (w *Worker) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(rw http.ResponseWriter, _ *http.Request) { _, _ = rw.Write([]byte("ok\n")) })
	gauges := func(r *http.Request) (time.Duration, int, error) {
		lag, err := w.Lag(r.Context())
		if err != nil {
			return 0, 0, err
		}
		q, err := w.Quarantined(r.Context())
		return lag, q, err
	}
	mux.HandleFunc("/lag", func(rw http.ResponseWriter, r *http.Request) {
		lag, q, err := gauges(r)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusServiceUnavailable)
			return
		}
		rw.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(rw).Encode(map[string]any{"oldest_unapplied_seconds": lag.Seconds(), "quarantined_keys": q})
	})
	mux.HandleFunc("/metrics", func(rw http.ResponseWriter, r *http.Request) {
		lag, q, err := gauges(r)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusServiceUnavailable)
			return
		}
		rw.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(rw, "# TYPE aintercept_lag_seconds gauge\naintercept_lag_seconds %g\n", lag.Seconds())
		fmt.Fprintf(rw, "# TYPE aintercept_quarantined_keys gauge\naintercept_quarantined_keys %d\n", q)
	})
	return mux
}
