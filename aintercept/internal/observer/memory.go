package observer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/orca"

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

// minReportedUtilization floors the utilization reported to Envoy, which
// ignores a zero report; it also bounds how strongly an idle pod is favored.
const minReportedUtilization = 0.05

// startLoadReports serves ORCA out-of-band load reports on the gRPC server so
// Envoy's client-side weighted round robin sends new streams to the observer
// pods with the most budget left. Envoy weighs a pod by rps / utilization;
// streams here last from milliseconds to minutes, so a stream rate says
// little about load, and a constant rate makes the weight inversely
// proportional to budget utilization. grpc-go reports at most every 30s,
// ample for load that builds over minutes. Returns a stop function.
func startLoadReports(s *grpc.Server, budget *membudget.Budget) func() {
	rec := orca.NewServerMetricsRecorder()
	rec.SetQPS(1)
	update := func() { rec.SetApplicationUtilization(math.Max(budget.Utilization(), minReportedUtilization)) }
	update()
	if err := orca.Register(s, orca.ServiceOptions{ServerMetricsProvider: rec}); err != nil {
		fatal("orca load reports", "err", err)
	}
	t := time.NewTicker(5 * time.Second)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case <-t.C:
				update()
			}
		}
	}()
	return func() { t.Stop(); close(done) }
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

// fleetHandler serves the memory-budget utilization averaged over every
// observer pod (found through the headless Service named by
// OBSERVER_PEERS_DNS), as a percentage, for the autoscaler: KEDA polls one
// URL, and any pod answers for the fleet. Peers that don't answer are left
// out; without a peers name it reports this pod alone.
func fleetHandler(budget *membudget.Budget, peersDNS, port string) http.HandlerFunc {
	client := &http.Client{Timeout: 2 * time.Second}
	return func(w http.ResponseWriter, r *http.Request) {
		utils := []float64{budget.Utilization()}
		if peersDNS != "" {
			ips, err := net.DefaultResolver.LookupHost(r.Context(), peersDNS)
			if err == nil {
				utils = utils[:0]
				var mu sync.Mutex
				var wg sync.WaitGroup
				for _, ip := range ips {
					wg.Add(1)
					go func() {
						defer wg.Done()
						u, ok := peerUtilization(r.Context(), client, net.JoinHostPort(ip, port))
						if ok {
							mu.Lock()
							utils = append(utils, u)
							mu.Unlock()
						}
					}()
				}
				wg.Wait()
				if len(utils) == 0 {
					utils = []float64{budget.Utilization()}
				}
			}
		}
		sum := 0.0
		for _, u := range utils {
			sum += u
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"pods":                    len(utils),
			"avg_utilization_percent": math.Round(sum / float64(len(utils)) * 100),
		})
	}
}

func peerUtilization(ctx context.Context, c *http.Client, addr string) (float64, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/stats", nil)
	if err != nil {
		return 0, false
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	var s struct {
		Memory budgetStats `json:"memory"`
	}
	if json.NewDecoder(resp.Body).Decode(&s) != nil || s.Memory.LimitBytes == 0 {
		return 0, false
	}
	return s.Memory.Utilization, true
}
