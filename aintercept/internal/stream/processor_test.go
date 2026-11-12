package stream

import (
	"context"
	"testing"
	"time"
)

// The service: replicas run as goroutine pipelines (hopper pattern) with real
// timers, buckets in parallel. A short smoke run; the simulations cover the
// failure cases deterministically.
func TestProcessorRunDrainsAndMatches(t *testing.T) {
	clock := RealClock{}
	s := NewMemStore(clock)
	h := &harness{t: t, s: s, l: Layout{Prefix: "r/"}, bits: 2, gen: newGen(9, 20), m: CountModel{}}
	h.clock = nil
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var ps []*Processor
	for _, name := range []string{"a", "b"} {
		p := New(Config{Store: s, Layout: h.l, Model: h.m, Replica: name, BucketBits: 2,
			Cycle: 20 * time.Millisecond, Renew: 50 * time.Millisecond, TTL: 300 * time.Millisecond, Clock: clock})
		ps = append(ps, p)
		go func() { _ = p.Run(ctx) }()
	}
	for i := 0; i < 10; i++ {
		h.uploadAt(i%2, h.gen.batch(50), clock.Now())
		time.Sleep(10 * time.Millisecond)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		lag, err := ComputeLag(ctx, s, h.l, 4, clock.Now())
		if err == nil && lag.BacklogFiles == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never drained: %+v %v", lag, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	h.checkAll(ctx)
	cancel()
	for _, p := range ps {
		p.Wait()
	}
}
