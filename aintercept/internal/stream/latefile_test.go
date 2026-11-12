package stream

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// storeTarget is the observer writer's Target over a stream Store, with an
// outage switch (writes fail and don't land while down).
type storeTarget struct {
	s    Store
	down atomic.Bool
}

func (t *storeTarget) Name() string { return "store" }

func (t *storeTarget) Put(ctx context.Context, o events.Object) error {
	if t.down.Load() {
		return errors.New("store unreachable")
	}
	return t.s.Put(ctx, o.Key, o.Body)
}

func (t *storeTarget) Exists(ctx context.Context, key string) (bool, error) {
	if t.down.Load() {
		return false, errors.New("store unreachable")
	}
	_, err := t.s.Stat(ctx, key)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, process.ErrNotFound) {
		return false, nil
	}
	return false, err
}

// A store outage three times the processor's grace window, while a pod keeps
// writing records through the observer's writer: when the store comes back
// and the spool drains, every record is applied exactly once. The files are
// named by their upload minute, so they land after the watermark, not behind
// it where the processor no longer looks.
func TestRecordsWrittenThroughALongOutageAreAppliedOnce(t *testing.T) {
	h, ctx := setup(t, 1)
	tgt := &storeTarget{s: h.s}
	ticks := make(chan time.Time)
	w, err := events.NewWriter(events.WriterConfig{
		SpoolDir: t.TempDir(), SkipFsync: true, Targets: []events.Target{tgt},
		RetryMin: time.Millisecond, RetryMax: 2 * time.Millisecond,
		NewTicker: func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} },
		Now:       h.clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	put := records.WriterPutter(w, "store")
	proc := h.worker("a", 1)

	// Before the outage: both pods upload and the processor keeps up.
	h.upload(1, h.gen.batch(10))
	if _, err := proc.Cycle(ctx); err != nil {
		t.Fatal(err)
	}

	// The outage. Pod 0 writes through its writer; the uploads fail and
	// retry. Pod 1, unaffected, keeps the watermark moving minute by minute.
	tgt.down.Store(true)
	held := h.gen.batch(25)
	for i, r := range held {
		held[i].Bucket = int32(records.BucketOf(r.RoutingKey, h.bits))
	}
	body, err := records.Encode(held)
	if err != nil {
		t.Fatal(err)
	}
	if err := records.PutBucketFile(ctx, put, h.l.Prefix, 0, "pod0", 1, body, h.clock.Now()); err != nil {
		t.Fatal(err)
	}
	ticks <- time.Now() // sealed into the spool now, at the outage's first minute
	outage := 3 * DefaultGrace
	for d := time.Duration(0); d < outage; d += time.Minute {
		h.clock.Advance(time.Minute)
		h.upload(1, h.gen.batch(3))
		if _, err := proc.Cycle(ctx); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond) // let the uploader retry in this minute
	}

	// The store comes back; the spool drains; the processor cycles.
	tgt.down.Store(false)
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	h.uploaded = append(h.uploaded, held...)
	if _, err := proc.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	h.checkAll(ctx)

	// And the file exists once.
	all, err := h.s.List(ctx, records.ObjectKeyPrefix(h.l.Prefix, 0))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, o := range all {
		if strings.Contains(o.Key, "/pod0-") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("pod0's records file exists %d times", n)
	}
}
