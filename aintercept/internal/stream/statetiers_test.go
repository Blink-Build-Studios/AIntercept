package stream

import (
	"context"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

// stateCounter counts the state bytes written (cycle checkpoints and folds) and
// the state files read.
type stateCounter struct {
	Store
	mu      sync.Mutex
	written int64
	reads   int
}

func (s *stateCounter) Put(ctx context.Context, key string, b []byte) error {
	if strings.Contains(key, "/state/") {
		s.mu.Lock()
		s.written += int64(len(b))
		s.mu.Unlock()
	}
	return s.Store.Put(ctx, key, b)
}

func (s *stateCounter) Get(ctx context.Context, key string) ([]byte, error) {
	if strings.Contains(key, "/state/") {
		s.mu.Lock()
		s.reads++
		s.mu.Unlock()
	}
	return s.Store.Get(ctx, key)
}

// grow is one key fed in cycles of 50 records, compacting as the processor
// does, sampled when it holds each of sizes: at each sample, the state files a
// cold load of the key reads, and the state bytes written per record so far
// (every cycle's checkpoint and every fold).
func grow(t *testing.T, sizes []int) (reads []int, perRecord []float64) {
	t.Helper()
	h, ctx := setup(t, 1)
	sc := &stateCounter{Store: h.s}
	h.s = sc
	h.m = StateModel{}
	w := h.worker("a", 1)
	w.CompactRuns = 4
	held := 0
	for _, n := range sizes {
		for held < n {
			k := min(50, n-held)
			h.upload(0, bigSession("big", held, k))
			h.uploaded = nil
			held += k
			h.clock.Advance(10 * time.Second)
			if _, err := w.Cycle(ctx); err != nil {
				t.Fatal(err)
			}
		}
		perRecord = append(perRecord, float64(sc.written)/float64(held))
		w.ResetCache()
		before := sc.reads
		h.upload(0, bigSession("big", held, 1)) // one more record: the key loads cold
		h.uploaded = nil
		held++
		h.clock.Advance(10 * time.Second)
		if _, err := w.Cycle(ctx); err != nil {
			t.Fatal(err)
		}
		reads = append(reads, sc.reads-before)
	}
	return reads, perRecord
}

// A cold load (a restart, a takeover) reads a number of checkpoint chunks that
// grows at most with log N of the key's records, and every record's state is
// rewritten at most O(log N) times over the key's life: the folds are in tiers.
func TestStateTiersBoundLoadsAndRewrites(t *testing.T) {
	heavy(t)
	sizes := tierSizes
	reads, per := grow(t, sizes)
	for i, n := range sizes {
		bound := 3*int(math.Ceil(math.Log(float64(n))/math.Log(4))) + 8
		t.Logf("holding %d: a cold load reads %d state files (bound %d); %.0f state bytes written per record", n, reads[i], bound, per[i])
		if reads[i] > bound {
			t.Errorf("holding %d: a cold load reads %d state files, more than %d", n, reads[i], bound)
		}
	}
	// Bytes written per record: the first sample's, times at most the number of
	// tiers a record can be folded through since.
	last := len(sizes) - 1
	tiers := 2 + math.Log(float64(sizes[last])/float64(sizes[1]))/math.Log(4)
	if per[last] > per[1]*tiers {
		t.Errorf("state bytes written per record grow faster than log N: %.0f at %d, %.0f at %d (allowed %.0f)",
			per[1], sizes[1], per[last], sizes[last], per[1]*tiers)
	}
}

// tierSizes is small in make check; -tags full grows the key to 100,000.
var tierSizes = []int{10, 100, 1_000, 3_000}
