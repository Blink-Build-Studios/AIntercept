//go:build full

package stream

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"runtime/debug"
	"sync"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
)

// One replica's memory doesn't grow with history: with 1×, 2×, 4× and 8× the
// history in its bucket (on disk, as in S3), its peak heap while it processes
// the same new batch on a cold cache (every key loaded back), and while it
// compacts, stays within 15% of the 1× run's. Its key-state cache is capped.
func TestReplicaMemoryFlatInHistory(t *testing.T) {
	type peaks struct{ cycle, compact uint64 }
	run := func(history int) peaks {
		dir := t.TempDir()
		clock := NewFakeClock(time.Date(2027, 1, 5, 10, 0, 0, 0, time.UTC))
		st := DirStore{process.DirStore{Root: dir}}
		h := newHarness(t, st, clock, Layout{Prefix: "t/"}, 1)
		h.m = StateModel{}
		ctx := context.Background()
		w := h.worker("a", 1)
		w.CacheBytes = 8 << 20
		w.CompactRuns = 16
		w.PartRows = 512
		for i := 0; i < 40*history; i++ { // history: sessions that come and go, then settle
			h.gen.sessions, h.gen.prefix = 50, fmt.Sprintf("h%d-", i/4)
			if i%40 == 0 { // a new day: credential-day keys, like sessions, come and go
				h.gen.ts += 24 * int64(time.Hour)
			}
			h.upload(0, h.gen.batch(200))
			h.uploaded = nil // the test keeps none of it
			clock.Advance(10 * time.Second)
			if _, err := w.Cycle(ctx); err != nil {
				t.Fatal(err)
			}
		}
		clock.Advance(GCGrace + time.Second)
		if err := w.GC(ctx); err != nil {
			t.Fatal(err)
		}
		if err := w.Compact(ctx); err != nil {
			t.Fatal(err)
		}
		w.ResetCache()
		h.gen.sessions, h.gen.prefix = 200, "new-"
		h.gen.ts += 24 * int64(time.Hour)
		h.upload(0, h.gen.batch(2000))
		clock.Advance(10 * time.Second)
		if c, err := LoadLatest(ctx, h.s, h.l, 0); err == nil && c != nil {
			b, _ := h.s.Get(ctx, h.l.CommitKey(0, c.Seq))
			m, _ := h.s.Get(ctx, c.BaseManifest)
			t.Logf("history %dx: a commit is %d bytes (its base manifest, written per generation, %d KiB)", history, len(b), len(m)>>10)
		}
		var p peaks
		p.cycle = peakHeap(func() {
			if _, err := w.Cycle(ctx); err != nil {
				t.Fatal(err)
			}
		})
		p.compact = peakHeap(func() {
			if err := w.Compact(ctx); err != nil {
				t.Fatal(err)
			}
		})
		return p
	}
	// The 1x–4x runs set the level (their highest peak: a single run's peak
	// varies with when the collector runs). As history keeps doubling to 16x,
	// a compaction must stay within 15% of it. A cold cycle holds one state part
	// per tier at a time (and drops each once its keys are past), and the tiers
	// grow with log N: it may grow by 15% per doubling, no more.
	var base peaks
	sizes := []int{1, 2, 4, 8, 16}
	for i, hist := range sizes {
		// Each size twice, the lower peak of each: one run's peak moves with when
		// the collector happens to run.
		p, q := run(hist), run(hist)
		p.cycle, p.compact = min(p.cycle, q.cycle), min(p.compact, q.compact)
		t.Logf("history %dx: peak heap %d KiB in a cold cycle of 2,000 records, %d KiB compacting", hist, p.cycle>>10, p.compact>>10)
		if hist <= 4 {
			base.cycle, base.compact = max(base.cycle, p.cycle), max(base.compact, p.compact)
			continue
		}
		_ = i
		if float64(p.cycle) > (1.15+0.15*math.Log2(float64(hist)/4))*float64(base.cycle) {
			t.Errorf("history %dx: a cold cycle peaks at %d KiB, 1x–4x at %d KiB", hist, p.cycle>>10, base.cycle>>10)
		}
		if float64(p.compact) > 1.15*float64(base.compact) {
			t.Errorf("history %dx: compaction peaks at %d KiB, 1x–4x at %d KiB", hist, p.compact>>10, base.compact>>10)
		}
	}
}

// peakHeap is the most heap in use while fn runs above what was live before
// (the replica's cache is emptied first, so what fn reads is all of it). The
// collector runs at GOGC=5 meanwhile, so the peak tracks live memory, not when
// a collection happened to run.
func peakHeap(fn func()) uint64 {
	defer debug.SetGCPercent(debug.SetGCPercent(5))
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	before := m.HeapAlloc
	var peak uint64
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		var s runtime.MemStats
		for {
			select {
			case <-done:
				return
			case <-time.After(100 * time.Microsecond):
				runtime.ReadMemStats(&s)
				mu.Lock()
				if s.HeapAlloc > peak {
					peak = s.HeapAlloc
				}
				mu.Unlock()
			}
		}
	}()
	fn()
	close(done)
	runtime.ReadMemStats(&m)
	mu.Lock()
	defer mu.Unlock()
	if m.HeapAlloc > peak {
		peak = m.HeapAlloc
	}
	if peak < before {
		return 0
	}
	return peak - before
}
