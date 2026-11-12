package stream

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// Lag is how far behind the processor is: the age of the oldest record file
// no commit has read, across every bucket (0: caught up), and how many such
// files there are. KEDA scales the processor on Seconds (§4.10).
type Lag struct {
	Seconds      float64 `json:"lag_seconds"`
	BacklogFiles int     `json:"backlog_files"`
}

// ComputeLag reads each bucket's newest commit and lists only the record
// files after its watermark.
func ComputeLag(ctx context.Context, s Store, l Layout, buckets int, now time.Time) (Lag, error) {
	var lag Lag
	for b := 0; b < buckets; b++ {
		c, err := LoadLatest(ctx, s, l, b)
		if err != nil {
			return lag, err
		}
		files, err := listNewInfo(ctx, s, l, b, c, DefaultGrace)
		if err != nil {
			return lag, err
		}
		lag.BacklogFiles += len(files)
		for _, f := range files {
			if age := now.Sub(f.Modified).Seconds(); age > lag.Seconds {
				lag.Seconds = age
			}
		}
	}
	return lag, nil
}

// LagHandler serves GET /lag as JSON, recomputed at most every 10 s: every
// replica answers the same, so KEDA can ask any of them.
type LagHandler struct {
	s       Store
	l       Layout
	buckets int
	clock   Clock

	mu   sync.Mutex
	at   time.Time
	last Lag
}

const lagCache = 10 * time.Second

func NewLagHandler(s Store, l Layout, buckets int, clock Clock) *LagHandler {
	return &LagHandler{s: s, l: l, buckets: buckets, clock: clock}
}

func (h *LagHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.clock.Now()
	if h.at.IsZero() || now.Sub(h.at) >= lagCache {
		lag, err := ComputeLag(r.Context(), h.s, h.l, h.buckets, now)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		h.last, h.at = lag, now
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(h.last)
}
