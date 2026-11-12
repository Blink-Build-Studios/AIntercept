package stream

import (
	"context"
	"testing"
	"time"
)

// The distributed replay: N ingest pods upload records, R processor replicas
// own buckets through leases, with chaos, against the in-memory emulator.
// At every checkpoint (ingest paused, replicas drained) and at the end, every
// table equals one pass over everything uploaded.

func simCfg(seed int64, n, r int) SimConfig {
	return SimConfig{
		Seed: seed, IngestPods: n, Processors: r, BucketBits: 3,
		Records: 3000, Steps: 400, Checkpoints: 4,
		Chaos: Chaos{
			KillHard: 0.01, KillGraceful: 0.01, StealLease: 0.01, Pause: 0.01,
			PutFailure: 0.05, DelayUpload: 0.05, AddRemove: 0.01,
		},
	}
}

func TestSimulateSmoke(t *testing.T) {
	res, err := Simulate(context.Background(), simCfg(1, 3, 3))
	if err != nil {
		t.Fatal(err)
	}
	if res.Checkpoints < 4 || res.Mismatches != 0 {
		t.Fatalf("%+v", res)
	}
	if res.Kills == 0 || res.Steals == 0 || res.PutFailures == 0 {
		t.Fatalf("no chaos happened: %+v", res)
	}
}

// Without chaos, any N and R give the same tables (the plain path).
func TestSimulateNoChaos(t *testing.T) {
	for _, nr := range [][2]int{{1, 1}, {3, 1}, {1, 3}} {
		cfg := simCfg(2, nr[0], nr[1])
		cfg.Chaos = Chaos{}
		res, err := Simulate(context.Background(), cfg)
		if err != nil || res.Mismatches != 0 {
			t.Fatalf("N=%d R=%d: %+v %v", nr[0], nr[1], res, err)
		}
	}
}

// Flat cost: the work one cycle does (store requests and bytes read) for a
// fixed batch of new records doesn't grow with how much history the bucket
// already holds.
func TestCycleCostIsFlatInHistory(t *testing.T) {
	var base CycleCost
	for i, hist := range []int{1, 2, 4, 8} {
		cost := measureCycle(t, hist)
		if i == 0 {
			base = cost
			continue
		}
		if cost.Requests > base.Requests+2 || float64(cost.BytesRead) > 1.25*float64(base.BytesRead)+4096 {
			t.Fatalf("history %dx: %+v, vs 1x %+v", hist, cost, base)
		}
	}
}

func measureCycle(t *testing.T, history int) CycleCost {
	t.Helper()
	h, ctx := setup(t, 1)
	w := h.worker("a", 1)
	w.CompactRuns = 16
	for i := 0; i < 40*history; i++ { // history: settled sessions, then compacted
		h.gen.sessions = 1000 + i // new sessions over time, as people come and go
		h.upload(0, h.gen.batch(50))
		h.clock.Advance(10 * time.Second)
		if _, err := w.Cycle(ctx); err != nil {
			t.Fatal(err)
		}
	}
	h.clock.Advance(GCGrace + time.Second)
	if err := w.GC(ctx); err != nil {
		t.Fatal(err)
	}
	w.ResetCache() // a cold replica: worst case
	h.gen.sessions = 12
	h.upload(0, h.gen.batch(100))
	ms := h.s.(*MemStore)
	ms.ResetCounters()
	if _, err := w.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	return ms.Cost()
}

// Lag: a 10× burst drives lag past the target with one replica; adding
// replicas brings it back under; nothing is lost or applied twice.
func TestLagBurstScalesOut(t *testing.T) {
	res, err := SimulateBurst(context.Background(), BurstConfig{
		Seed: 3, BucketBits: 3, BaseRate: 20, Burst: 10, FilesPerCycle: 4,
		Target: 30 * time.Second, MaxReplicas: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.PeakLag <= res.Target || res.FinalLag > res.Target || res.MaxReplicas < 2 {
		t.Fatalf("%+v", res)
	}
	if res.Mismatches != 0 {
		t.Fatalf("tables differ after the burst: %+v", res)
	}
}
