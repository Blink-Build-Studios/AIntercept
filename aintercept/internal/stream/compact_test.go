package stream

import (
	"testing"
	"time"
)

func TestCompactionMergesRunsIntoABase(t *testing.T) {
	h, ctx := setup(t, 1)
	w := h.worker("a", 1)
	w.CompactRuns = 8
	for i := 0; i < 20; i++ {
		h.upload(0, h.gen.batch(10))
		h.clock.Advance(5 * time.Second)
		if _, err := w.Cycle(ctx); err != nil {
			t.Fatal(err)
		}
	}
	c, err := LoadLatest(ctx, h.s, h.l, 0)
	if err != nil {
		t.Fatal(err)
	}
	if c.Gen == 0 {
		t.Fatal("never compacted")
	}
	for table, runs := range c.Runs {
		if len(runs) >= 8 {
			t.Fatalf("%s has %d runs after compaction at 8", table, len(runs))
		}
	}
	for table, parts := range c.Base {
		if len(parts) == 0 {
			t.Fatalf("%s has an empty base", table)
		}
		checkShape(t, c, table, w.Fanout) // per segment: key-ranged parts, in order, never overlapping
	}
	h.checkAll(ctx)
}

func TestGCWaitsForTheGracePeriod(t *testing.T) {
	h, ctx := setup(t, 1)
	w := h.worker("a", 1)
	w.CompactRuns = 2
	for i := 0; i < 6; i++ {
		h.upload(0, h.gen.batch(10))
		if _, err := w.Cycle(ctx); err != nil {
			t.Fatal(err)
		}
	}
	c, _ := LoadLatest(ctx, h.s, h.l, 0)
	if len(c.Garbage) == 0 {
		t.Fatal("compaction left no garbage to collect")
	}
	if err := w.GC(ctx); err != nil {
		t.Fatal(err)
	}
	for _, g := range c.Garbage {
		if _, err := h.s.Get(ctx, g.Key); err != nil {
			t.Fatalf("%s deleted before its grace period: a reader mid-read could lose it", g.Key)
		}
	}
	h.clock.Advance(GCGrace + time.Second)
	if _, err := w.Cycle(ctx); err != nil { // GC runs within the cycle too
		t.Fatal(err)
	}
	if err := w.GC(ctx); err != nil {
		t.Fatal(err)
	}
	for _, g := range c.Garbage {
		if _, err := h.s.Get(ctx, g.Key); err == nil {
			t.Fatalf("%s still there after the grace period", g.Key)
		}
	}
	h.checkAll(ctx)
}
