package probe

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Monitor probes a fixed set of hosts on an interval and serves the latest
// verdict over HTTP: 200 when every host passed its last probe, else 503, with
// per-host results as JSON. It is the signal a monitor or automatic kill
// switch polls.
type Monitor struct {
	Prober   *Prober
	Targets  []Target
	Interval time.Duration

	mu      sync.RWMutex
	results []Result // nil until the first round completes
}

// Run probes until ctx is done.
func (m *Monitor) Run(ctx context.Context) {
	t := time.NewTicker(m.Interval)
	defer t.Stop()
	for {
		m.round(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (m *Monitor) round(ctx context.Context) {
	out := make([]Result, len(m.Targets))
	var wg sync.WaitGroup
	for i, t := range m.Targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = m.Prober.Check(ctx, t.Host, t.Path)
		}()
	}
	wg.Wait()
	for _, r := range out {
		if !r.OK {
			slog.Error("path probe failed", "host", r.Host, "error", r.Error, "status", r.Status)
		}
	}
	m.mu.Lock()
	m.results = out
	m.mu.Unlock()
}

// ServeHTTP reports the latest round.
func (m *Monitor) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	m.mu.RLock()
	rs := m.results
	m.mu.RUnlock()
	healthy := rs != nil
	for _, r := range rs {
		healthy = healthy && r.OK
	}
	w.Header().Set("Content-Type", "application/json")
	if !healthy {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"healthy": healthy, "target": m.Prober.Target, "hosts": rs})
}
