package observer

import (
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/membudget"
)

// setupMemory sizes the memory budget and the Go runtime's soft limit from the
// container's memory limit (cgroup), unless set explicitly:
//   - OBSERVER_MEMORY_BUDGET (e.g. "128Mi"): bytes in-flight traffic may hold
//     before the pod counts as full; default half the container limit, or
//     128 MiB without one.
//   - GOMEMLIMIT: the Go runtime reads it itself; if unset, 90% of the
//     container limit, so the GC works harder before the kernel OOM-kills.
func setupMemory() *membudget.Budget {
	limit := cgroupMemoryLimit()
	if os.Getenv("GOMEMLIMIT") == "" && limit > 0 {
		debug.SetMemoryLimit(limit / 10 * 9)
	}
	budget := int64(128 << 20)
	if limit > 0 {
		budget = limit / 2
	}
	if v := os.Getenv("OBSERVER_MEMORY_BUDGET"); v != "" {
		if n, err := parseSize(v); err == nil && n > 0 {
			budget = n
		} else {
			slog.Warn("invalid OBSERVER_MEMORY_BUDGET, using default", "value", v, "default", budget)
		}
	}
	slog.Info("memory budget", "budget_bytes", budget, "container_limit_bytes", limit)
	return membudget.New(budget)
}

// cgroupMemoryLimit is the container's memory limit in bytes, or 0 if unknown
// or unlimited (cgroup v2 memory.max, then v1 limit_in_bytes).
func cgroupMemoryLimit() int64 {
	for _, p := range []string{"/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes"} {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
		if err != nil || n <= 0 || n > 1<<50 { // "max", or v1's "unlimited" sentinel
			return 0
		}
		return n
	}
	return 0
}

// parseSize reads a byte count: plain, or with a Ki/Mi/Gi (or K/M/G) suffix.
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	mult := int64(1)
	for _, u := range []struct {
		suffix string
		mult   int64
	}{{"Ki", 1 << 10}, {"Mi", 1 << 20}, {"Gi", 1 << 30}, {"K", 1 << 10}, {"M", 1 << 20}, {"G", 1 << 30}} {
		if strings.HasSuffix(s, u.suffix) {
			s, mult = strings.TrimSuffix(s, u.suffix), u.mult
			break
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("size %q: %w", s, err)
	}
	return n * mult, nil
}

// budgetStats is the memory budget as served on /stats.
type budgetStats struct {
	HeldBytes   int64   `json:"held_bytes"`
	PeakBytes   int64   `json:"peak_bytes"`
	LimitBytes  int64   `json:"limit_bytes"`
	Utilization float64 `json:"utilization"`
}

func statsOf(b *membudget.Budget) budgetStats {
	return budgetStats{HeldBytes: b.Held(), PeakBytes: b.Peak(), LimitBytes: b.Limit(), Utilization: b.Utilization()}
}
