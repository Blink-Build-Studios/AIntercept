package stream

import (
	"context"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

func setup(t *testing.T, buckets int) (*harness, context.Context) {
	clock := NewFakeClock(time.Date(2027, 1, 5, 10, 0, 0, 0, time.UTC))
	return newHarness(t, NewMemStore(clock), clock, Layout{Prefix: "t/"}, buckets), context.Background()
}

// Atomic: one upload, one cycle.
func TestCycleAppliesAnUpload(t *testing.T) {
	h, ctx := setup(t, 1)
	h.upload(0, h.gen.batch(30))
	w := h.worker("a", 1)
	st, err := w.Cycle(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Committed || st.Files != 1 || st.Records != 30 {
		t.Fatalf("stats %+v, want one committed file of 30 records", st)
	}
	h.checkAll(ctx)
}

// Atomic: nothing new, no commit.
func TestCycleWithNothingNewDoesNotCommit(t *testing.T) {
	h, ctx := setup(t, 1)
	h.upload(0, h.gen.batch(5))
	w := h.worker("a", 1)
	if _, err := w.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	st, err := w.Cycle(ctx)
	if err != nil || st.Committed {
		t.Fatalf("idle cycle committed: %+v %v", st, err)
	}
}

// Atomic: the same file name uploaded twice (an ingest retry) counts once.
func TestCycleReuploadedFileCountsOnce(t *testing.T) {
	h, ctx := setup(t, 1)
	rs := h.gen.batch(10)
	h.upload(0, rs)
	w := h.worker("a", 1)
	if _, err := w.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	// Re-put the identical object (same key, same body): the uploader retried
	// after a timeout that had in fact succeeded.
	all, _ := h.s.List(ctx, records.ObjectKeyPrefix(h.l.Prefix, 0))
	b, _ := h.s.Get(ctx, all[0].Key)
	if err := h.s.Put(ctx, all[0].Key, b); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	h.checkAll(ctx)
}

// Atomic: a file that lands in an older minute (a spool replayed after an
// outage, pod clock skew) inside the grace window is still read, once.
func TestCycleReadsLateFileInsideGrace(t *testing.T) {
	h, ctx := setup(t, 1)
	h.upload(0, h.gen.batch(5))
	w := h.worker("a", 1)
	if _, err := w.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(3 * time.Minute)
	h.upload(0, h.gen.batch(5)) // watermark moves to this minute
	if _, err := w.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	// A pod whose clock runs 2 minutes behind uploads now.
	h.clock.Advance(-2 * time.Minute)
	h.upload(1, h.gen.batch(5))
	h.clock.Advance(2 * time.Minute)
	if _, err := w.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	h.checkAll(ctx)
}

// Atomic: within a cycle each key's records are applied in (ts, record id)
// order, whatever order the files and rows came in.
func TestCycleAppliesInTimeThenIDOrder(t *testing.T) {
	h, ctx := setup(t, 1)
	om := &orderModel{}
	h.m = om
	rs := h.gen.batch(200)
	h.upload(0, rs[100:])
	h.upload(1, rs[:100])
	w := h.worker("a", 1)
	if _, err := w.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	if om.violations > 0 {
		t.Fatalf("%d records applied out of (ts, record id) order", om.violations)
	}
}

// Sequence: many uploads and cycles, in random sizes, equal one pass at every
// cycle.
func TestCycleSequenceEqualsOnePassThroughout(t *testing.T) {
	heavy(t)
	h, ctx := setup(t, 4)
	ws := []*Bucket{}
	for b := 0; b < 4; b++ {
		w := h.worker("a", 1)
		w.B = b
		ws = append(ws, w)
	}
	for i := 0; i < 40; i++ {
		h.upload(i%3, h.gen.batch(1+h.gen.rng.Intn(40)))
		h.clock.Advance(time.Duration(h.gen.rng.Intn(50)) * time.Second)
		for _, w := range ws {
			if _, err := w.Cycle(ctx); err != nil {
				t.Fatal(err)
			}
		}
		h.checkAll(ctx)
	}
}

// Sequence: a tiny state cache forces eviction; evicted keys are read back
// from state files and the tables still equal one pass.
func TestCycleEvictedStateIsReloaded(t *testing.T) {
	h, ctx := setup(t, 1)
	w := h.worker("a", 1)
	w.CacheBytes = 64 // a few keys at most
	for i := 0; i < 15; i++ {
		h.upload(0, h.gen.batch(20))
		if _, err := w.Cycle(ctx); err != nil {
			t.Fatal(err)
		}
	}
	h.checkAll(ctx)
	if w.Loads() == 0 {
		t.Fatal("no state was reloaded: the cache never evicted")
	}
}

// Sequence: a new owner (failover) resumes from the last commit exactly.
func TestCycleNewOwnerResumes(t *testing.T) {
	h, ctx := setup(t, 1)
	a := h.worker("a", 1)
	for i := 0; i < 5; i++ {
		h.upload(0, h.gen.batch(10))
		if _, err := a.Cycle(ctx); err != nil {
			t.Fatal(err)
		}
	}
	b := h.worker("b", 2)
	for i := 0; i < 5; i++ {
		h.upload(0, h.gen.batch(10))
		if _, err := b.Cycle(ctx); err != nil {
			t.Fatal(err)
		}
	}
	h.checkAll(ctx)
}

// orderModel checks the order Apply sees records in, within each key and cycle.
type orderModel struct {
	CountModel
	last       map[string][2]any
	violations int
}

func (m *orderModel) Apply(st any, r *records.Record) Delta {
	if m.last == nil {
		m.last = map[string][2]any{}
	}
	ts := RecordTS(r)
	if p, ok := m.last[r.RoutingKey]; ok {
		if pt := p[0].(int64); ts < pt || (ts == pt && r.RecordID < p[1].(string)) {
			m.violations++
		}
	}
	m.last[r.RoutingKey] = [2]any{ts, r.RecordID}
	return m.CountModel.Apply(st, r)
}
