package events

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// memTarget is a Target that keeps uploaded objects in memory. failRate makes a
// share of Puts fail; block, when set, makes every Put wait until it's closed.
type memTarget struct {
	name     string
	mu       sync.Mutex
	objects  map[string][]byte
	puts     map[string]int // successful puts per key
	attempts int
	failRate float64
	rng      *rand.Rand
	block    chan struct{}
}

func newMemTarget(name string) *memTarget {
	return &memTarget{name: name, objects: map[string][]byte{}, puts: map[string]int{}, rng: rand.New(rand.NewSource(1))}
}

func (m *memTarget) Name() string { return m.name }

func (m *memTarget) Put(ctx context.Context, o Object) error {
	if m.block != nil {
		select {
		case <-m.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.attempts++
	if m.failRate > 0 && m.rng.Float64() < m.failRate {
		return fmt.Errorf("synthetic put failure")
	}
	m.objects[o.Key] = append([]byte(nil), o.Body...)
	m.puts[o.Key]++
	return nil
}

func (m *memTarget) snapshot() (map[string][]byte, map[string]int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	objs := map[string][]byte{}
	for k, v := range m.objects {
		objs[k] = v
	}
	puts := map[string]int{}
	for k, v := range m.puts {
		puts[k] = v
	}
	return objs, puts
}

// manualTick lets a test fire the writer's ticker by hand.
type manualTick struct{ c chan time.Time }

func newManualTick() *manualTick                                      { return &manualTick{c: make(chan time.Time)} }
func (m *manualTick) fire()                                           { m.c <- time.Now() }
func (m *manualTick) ticker(time.Duration) (<-chan time.Time, func()) { return m.c, func() {} }

func testWriter(t *testing.T, cfg WriterConfig) *Writer {
	t.Helper()
	if cfg.SpoolDir == "" {
		cfg.SpoolDir = t.TempDir()
	}
	cfg.SkipFsync = true
	if cfg.RetryMin == 0 {
		cfg.RetryMin = time.Millisecond
	}
	if cfg.RetryMax == 0 {
		cfg.RetryMax = 5 * time.Millisecond
	}
	w, err := NewWriter(cfg)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	return w
}

func plainStream(w *Writer, target, prefix string) *Stream {
	return w.NewStream(StreamConfig{
		Target: target,
		Key:    func(seq uint64, _ time.Time) string { return fmt.Sprintf("%s/%06d", prefix, seq) },
	})
}

func lines(objs map[string][]byte) []string {
	var out []string
	for _, b := range objs {
		for _, l := range strings.Split(string(gunzipOrPlain(b)), "\n") {
			if l != "" {
				out = append(out, l)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Nothing is ever lost or duplicated, whatever fails: every written line lands
// in exactly one object, and every object is uploaded exactly once.
func TestWriterNeverDropsUnderRandomPutFailures(t *testing.T) {
	tgt := newMemTarget("mem")
	tgt.failRate = 0.5
	w := testWriter(t, WriterConfig{Targets: []Target{tgt}, MaxBufferBytes: 512})
	s := plainStream(w, "mem", "a")
	var want []string
	for i := 0; i < 2000; i++ {
		l := fmt.Sprintf("line-%05d", i)
		want = append(want, l)
		if err := s.Write([]byte(l)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	objs, puts := tgt.snapshot()
	for k, n := range puts {
		if n != 1 {
			t.Errorf("key %s uploaded %d times, want 1", k, n)
		}
	}
	sort.Strings(want)
	if got := lines(objs); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("uploaded %d lines, want %d (lost or duplicated)", len(got), len(want))
	}
	if st := w.Stats(); st.UploadFailures == 0 {
		t.Errorf("expected some failed attempts with failRate 0.5")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// The ticker seals a partial buffer: a quiet pod still flushes on time.
func TestWriterFlushesOnTicker(t *testing.T) {
	tgt := newMemTarget("mem")
	tick := newManualTick()
	w := testWriter(t, WriterConfig{Targets: []Target{tgt}, MaxBufferBytes: 1 << 20, NewTicker: tick.ticker})
	defer w.Close()
	s := plainStream(w, "mem", "a")
	_ = s.Write([]byte("one"))
	time.Sleep(20 * time.Millisecond)
	if objs, _ := tgt.snapshot(); len(objs) != 0 {
		t.Fatalf("sealed before the tick: %d objects", len(objs))
	}
	tick.fire()
	waitFor(t, func() bool { objs, _ := tgt.snapshot(); return len(objs) == 1 })
}

// The byte cap seals before the ticker.
func TestWriterFlushesOnByteCap(t *testing.T) {
	tgt := newMemTarget("mem")
	tick := newManualTick()
	w := testWriter(t, WriterConfig{Targets: []Target{tgt}, MaxBufferBytes: 100, NewTicker: tick.ticker})
	defer w.Close()
	s := plainStream(w, "mem", "a")
	for i := 0; i < 20; i++ {
		_ = s.Write([]byte("0123456789"))
	}
	waitFor(t, func() bool { objs, _ := tgt.snapshot(); return len(objs) >= 1 })
}

// A stuck object store never blocks writers: the loop seals to the spool and
// keeps accepting.
func TestWriterLoopNeverBlocksOnSlowUpload(t *testing.T) {
	tgt := newMemTarget("mem")
	tgt.block = make(chan struct{})
	w := testWriter(t, WriterConfig{Targets: []Target{tgt}, MaxBufferBytes: 64})
	s := plainStream(w, "mem", "a")
	done := make(chan struct{})
	go func() {
		for i := 0; i < 5000; i++ {
			_ = s.Write([]byte("0123456789abcdef"))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("writes blocked behind a stuck upload")
	}
	if st := w.Stats(); st.SpoolFiles == 0 {
		t.Fatalf("expected sealed files waiting in the spool")
	}
	close(tgt.block)
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
}

// Files sealed before a crash are uploaded by the next process on the same
// spool, exactly once.
func TestWriterSpoolSurvivesCrash(t *testing.T) {
	dir := t.TempDir()
	tgt := newMemTarget("mem")
	tgt.block = make(chan struct{}) // the first process never gets an upload through
	w1 := testWriter(t, WriterConfig{SpoolDir: dir, Targets: []Target{tgt}, MaxBufferBytes: 32})
	s1 := plainStream(w1, "mem", "a")
	for i := 0; i < 50; i++ {
		_ = s1.Write([]byte(fmt.Sprintf("crash-%02d-padding", i)))
	}
	waitFor(t, func() bool { return w1.Stats().SpoolFiles > 0 })
	w1.Abandon() // a crash: no flush, no close
	// What the crash left: the sealed files on disk.
	left, _ := filepath.Glob(filepath.Join(dir, "ready", "*.spool"))
	sealed := len(left)
	if sealed == 0 {
		t.Fatal("nothing sealed before the crash")
	}

	tgt2 := newMemTarget("mem")
	w2 := testWriter(t, WriterConfig{SpoolDir: dir, Targets: []Target{tgt2}})
	if err := w2.Flush(); err != nil {
		t.Fatal(err)
	}
	objs, puts := tgt2.snapshot()
	if len(objs) != sealed {
		t.Fatalf("after restart uploaded %d objects, want the %d sealed before the crash", len(objs), sealed)
	}
	for k, n := range puts {
		if n != 1 {
			t.Errorf("key %s uploaded %d times", k, n)
		}
	}
	if st := w2.Stats(); st.SpoolFiles != 0 || st.SpoolBytes != 0 {
		t.Fatalf("spool not empty after upload: %+v", st)
	}
	_ = w2.Close()
}

// Full turns on at the high watermark and off only below the low one.
func TestWriterFullHighLowWatermarks(t *testing.T) {
	tgt := newMemTarget("mem")
	tgt.block = make(chan struct{})
	w := testWriter(t, WriterConfig{Targets: []Target{tgt}, MaxBufferBytes: 1, SpoolMaxBytes: 1000})
	s := plainStream(w, "mem", "a")
	if w.Full() {
		t.Fatal("full when empty")
	}
	for !w.Full() {
		_ = s.Write([]byte(strings.Repeat("x", 50)))
		time.Sleep(time.Millisecond)
	}
	if b := w.Stats().SpoolBytes; b < 950 {
		t.Fatalf("full at %d spooled bytes, want >= 95%% of 1000", b)
	}
	close(tgt.block) // uploads drain the spool
	waitFor(t, func() bool { return !w.Full() })
	if b := w.Stats().SpoolBytes; b > 800 {
		t.Fatalf("not full at %d spooled bytes, want <= 80%% of 1000", b)
	}
	_ = w.Close()
}

// Flush returns only once everything written has been uploaded.
func TestWriterFlushWaitsForEmptySpool(t *testing.T) {
	tgt := newMemTarget("mem")
	tgt.failRate = 0.7
	w := testWriter(t, WriterConfig{Targets: []Target{tgt}, MaxBufferBytes: 16})
	s := plainStream(w, "mem", "a")
	for i := 0; i < 300; i++ {
		_ = s.Write([]byte(fmt.Sprintf("flush-%03d", i)))
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if st := w.Stats(); st.SpoolFiles != 0 {
		t.Fatalf("Flush returned with %d files still spooled", st.SpoolFiles)
	}
	objs, _ := tgt.snapshot()
	if n := len(lines(objs)); n != 300 {
		t.Fatalf("after Flush %d lines uploaded, want 300", n)
	}
	_ = w.Close()
}

// Every sink in the observer writes through one Writer: the S3, capture,
// local-directory and schema sinks are all streams of it, and nothing else in
// this package calls PutObject.
func TestEverySinkGoesThroughOneWriter(t *testing.T) {
	tgt := newMemTarget("store")
	local := newMemTarget("local")
	w := testWriter(t, WriterConfig{Targets: []Target{tgt, local}})
	sinks := []*S3Sink{
		NewS3Sink(w, S3Config{Target: "store", Prefix: "p", InstanceID: "pod"}),
		NewS3Sink(w, S3Config{Target: "store", Prefix: "p/capture", InstanceID: "pod", IncludeContent: true}),
		NewS3Sink(w, S3Config{Target: "local", Prefix: "p", InstanceID: "pod"}),
		NewS3Sink(w, S3Config{Target: "store", Prefix: "p/schema-samples", InstanceID: "pod"}),
	}
	for i, s := range sinks {
		if err := s.Emit(Event{CorrelationID: fmt.Sprint("c", i), Phase: PhaseComplete}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	a, _ := tgt.snapshot()
	b, _ := local.snapshot()
	if len(a) != 3 || len(b) != 1 {
		t.Fatalf("objects: store %d (want 3), local %d (want 1)", len(a), len(b))
	}

	// Structural guard: PutObject is called only by the writer's targets.
	files, _ := filepath.Glob("*.go")
	call := regexp.MustCompile(`\.PutObject\(`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "targets.go" {
			continue
		}
		src, _ := os.ReadFile(f)
		if call.Match(src) {
			t.Errorf("%s calls PutObject; uploads must go through the Writer's targets (targets.go)", f)
		}
	}
	_ = w.Close()
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within 5s")
}

func gunzipOrPlain(b []byte) []byte {
	if len(b) > 2 && b[0] == 0x1f && b[1] == 0x8b {
		r, err := gzip.NewReader(bytes.NewReader(b))
		if err == nil {
			out, _ := io.ReadAll(r)
			return out
		}
	}
	return b
}

// Whole objects (records files, call detail objects) go through the writer too:
// buffered, sealed together into one spool file on the tick or the byte cap,
// and each uploaded at its own key, exactly once.
func TestWriterUploadsWholeObjects(t *testing.T) {
	tgt := newMemTarget("store")
	tick := newManualTick()
	w := testWriter(t, WriterConfig{Targets: []Target{tgt}, NewTicker: tick.ticker})
	o := w.NewObjectStream(ObjectStreamConfig{Target: "store", ContentType: "application/vnd.apache.parquet"})
	for i := 0; i < 20; i++ {
		if err := o.Put(fmt.Sprintf("objs/%02d", i), []byte(fmt.Sprintf("body-%02d", i))); err != nil {
			t.Fatal(err)
		}
	}
	tick.fire()
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	objs, puts := tgt.snapshot()
	if len(objs) != 20 {
		t.Fatalf("uploaded %d objects", len(objs))
	}
	for i := 0; i < 20; i++ {
		k := fmt.Sprintf("objs/%02d", i)
		if string(objs[k]) != fmt.Sprintf("body-%02d", i) || puts[k] != 1 {
			t.Fatalf("%s: %q x%d", k, objs[k], puts[k])
		}
	}
	if st := w.Stats(); st.SpoolFiles != 0 || st.Uploads == 0 {
		t.Fatalf("stats %+v", st)
	}
	_ = w.Close()
}

// A bundle of objects sealed before a crash is uploaded by the next writer on
// the spool; one failing upload is retried without losing the others.
func TestWriterObjectBundlesSurviveCrashAndFailures(t *testing.T) {
	dir := t.TempDir()
	tgt := newMemTarget("store")
	tgt.block = make(chan struct{})
	w1 := testWriter(t, WriterConfig{SpoolDir: dir, Targets: []Target{tgt}, MaxBufferBytes: 64})
	o := w1.NewObjectStream(ObjectStreamConfig{Target: "store"})
	for i := 0; i < 30; i++ {
		_ = o.Put(fmt.Sprintf("k/%02d", i), []byte(strings.Repeat("x", 10)+fmt.Sprint(i)))
	}
	waitFor(t, func() bool { return w1.Stats().SpoolFiles > 0 })
	w1.Abandon()

	tgt2 := newMemTarget("store")
	tgt2.failRate = 0.3
	w2 := testWriter(t, WriterConfig{SpoolDir: dir, Targets: []Target{tgt2}})
	if err := w2.Flush(); err != nil {
		t.Fatal(err)
	}
	objs, _ := tgt2.snapshot()
	sealed := 0
	for i := 0; i < 30; i++ {
		if b, ok := objs[fmt.Sprintf("k/%02d", i)]; ok {
			sealed++
			if !strings.HasSuffix(string(b), fmt.Sprint(i)) {
				t.Fatalf("k/%02d body %q", i, b)
			}
		}
	}
	if sealed == 0 {
		t.Fatal("nothing from before the crash was uploaded")
	}
	if st := w2.Stats(); st.SpoolFiles != 0 || st.SpoolBytes != 0 {
		t.Fatalf("spool not empty: %+v", st)
	}
	_ = w2.Close()
}
