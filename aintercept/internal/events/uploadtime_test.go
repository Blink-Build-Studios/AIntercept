package events

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// v5 ingest files (docs/PIPELINE.md §1) are named by S3's clock, to the
// second, at upload; each one is handed to the writer durably (spooled before
// the call returns) together with the per-key buffer file it came from, which
// the writer removes once the spool holds it; and every landed object is
// reported (OnUploaded), so ingest can flag its key.

const inTmpl = "v1/keys/s%3Aabc/in/" + UploadTime + "-inc-000000000001.parquet"

func inKey(ts string) string { return "v1/keys/s%3Aabc/in/" + ts + "-inc-000000000001.parquet" }

// clockTarget is a scriptTarget whose server clock runs off the pod's.
type clockTarget struct {
	*scriptTarget
	offset time.Duration
}

func (c *clockTarget) ServerOffset() (time.Duration, bool) { return c.offset, true }

// Atomic: the name carries S3's clock, not the pod's, to the second.
func TestUploadTimeIsTheStoresClockToTheSecond(t *testing.T) {
	clock := &testClock{t: t0} // the pod's clock: 10:00:30
	tgt := &clockTarget{scriptTarget: newScriptTarget(), offset: 7*time.Minute + 5*time.Second}
	tick := newManualTick()
	w := testWriter(t, WriterConfig{Targets: []Target{tgt}, NewTicker: tick.ticker, Now: clock.now})
	o := w.NewObjectStream(ObjectStreamConfig{Target: "store"})
	if err := o.PutAtUpload(inTmpl, []byte("rows")); err != nil {
		t.Fatal(err)
	}
	tick.fire()
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	onlyObject(t, tgt.scriptTarget, inKey("20270301T100735Z"))
	_ = w.Close()
}

// Atomic: a target that can't tell its clock falls back to the pod's.
func TestUploadTimeWithoutAServerClockUsesThePods(t *testing.T) {
	clock := &testClock{t: t0}
	tgt := newScriptTarget()
	tick := newManualTick()
	w := testWriter(t, WriterConfig{Targets: []Target{tgt}, NewTicker: tick.ticker, Now: clock.now})
	o := w.NewObjectStream(ObjectStreamConfig{Target: "store"})
	_ = o.PutAtUpload(inTmpl, []byte("rows"))
	tick.fire()
	_ = w.Flush()
	onlyObject(t, tgt, inKey("20270301T100030Z"))
	_ = w.Close()
}

// Atomic: every object that lands is reported once, with the key it landed at,
// including objects a previous process left in the spool.
func TestOnUploadedReportsEachLandedObjectOnce(t *testing.T) {
	var mu sync.Mutex
	got := map[string]int{}
	on := func(target, key string) { mu.Lock(); got[target+" "+key]++; mu.Unlock() }
	clock := &testClock{t: t0}
	tgt := newScriptTarget("fail") // the first attempt fails: reported only once it lands
	tick := newManualTick()
	w := testWriter(t, WriterConfig{Targets: []Target{tgt}, NewTicker: tick.ticker, Now: clock.now, OnUploaded: on})
	o := w.NewObjectStream(ObjectStreamConfig{Target: "store"})
	_ = o.PutAtUpload(inTmpl, []byte("rows"))
	_ = o.Put("fixed/key", []byte("x"))
	tick.fire()
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got["store "+inKey("20270301T100030Z")] != 1 || got["store fixed/key"] != 1 {
		t.Fatalf("reported %v", got)
	}
}

// Atomic: a durable put is in the spool when it returns (no tick needed), and
// the source buffer file is gone.
func TestDurablePutIsSpooledOnReturnAndRemovesItsSource(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "buf.jsonl")
	if err := os.WriteFile(src, []byte("{}\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	spool := t.TempDir()
	tgt := newScriptTarget()
	block := make(chan struct{})
	tgt.after = func(int) { <-block } // hold the upload so the spool file stays
	w := testWriter(t, WriterConfig{SpoolDir: spool, Targets: []Target{tgt}, NewTicker: newManualTick().ticker})
	o := w.NewObjectStream(ObjectStreamConfig{Target: "store"})
	if err := o.PutAtUploadDurable(inTmpl, []byte("rows"), src); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("source buffer still there after a durable put: %v", err)
	}
	ready, _ := os.ReadDir(filepath.Join(spool, "ready"))
	if n := countSuffix(ready, ".spool"); n != 1 {
		t.Fatalf("%d spool files, want 1", n)
	}
	close(block)
	_ = w.Flush()
	_ = w.Close()
}

// Atomic: a crash after the spool file is written but before the source buffer
// is removed: the next writer removes the source, so the buffer is never
// uploaded a second time by ingest's recovery.
func TestRecoveryRemovesTheSourceOfASpooledBuffer(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "buf.jsonl")
	spool := t.TempDir()
	tgt := newScriptTarget()
	w := testWriter(t, WriterConfig{SpoolDir: spool, Targets: []Target{tgt}, NewTicker: newManualTick().ticker})
	w.Abandon() // nothing uploads
	o := w.NewObjectStream(ObjectStreamConfig{Target: "store"})
	_ = os.WriteFile(src, []byte("{}\n"), 0o640)
	hdr := spoolHeader{Target: "store", Bundle: []bundledEntry{{Key: inTmpl, Len: 4, AtUpload: true, Src: src}}}
	if _, _, err := w.spool(hdr, []byte("rows")); err != nil { // spooled, then "crashed" before removing src
		t.Fatal(err)
	}
	_ = o
	w2 := testWriter(t, WriterConfig{SpoolDir: spool, Targets: []Target{tgt}, NewTicker: newManualTick().ticker})
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("recovery left the spooled buffer's source in place: %v", err)
	}
	_ = w2.Flush()
	_ = w2.Close()
	onlyObject(t, tgt, inKey(time.Now().UTC().Format("20060102T150405Z")[:0]+keyTime(t, tgt)))
}

func keyTime(t *testing.T, tgt *scriptTarget) string {
	t.Helper()
	puts, _ := tgt.snapshot()
	for k := range puts {
		if strings.HasPrefix(k, "v1/keys/s%3Aabc/in/") {
			return strings.SplitN(strings.TrimPrefix(k, "v1/keys/s%3Aabc/in/"), "-", 2)[0]
		}
	}
	t.Fatalf("no ingest file uploaded: %v", puts)
	return ""
}

func countSuffix(es []os.DirEntry, suf string) int {
	n := 0
	for _, e := range es {
		if strings.HasSuffix(e.Name(), suf) {
			n++
		}
	}
	return n
}

var _ = context.Background
