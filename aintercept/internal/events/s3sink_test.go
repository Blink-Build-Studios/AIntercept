package events

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// fakeS3 captures PutObject calls in memory, decompressing and recording each
// object so tests can assert on keys and contents.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte // key -> decompressed body
	puts    int
	failN   int // fail the first failN puts (to exercise retries)
}

func newFakeS3() *fakeS3 { return &fakeS3{objects: map[string][]byte{}} }

func (f *fakeS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts++
	if f.failN > 0 {
		f.failN--
		return nil, fmt.Errorf("synthetic put failure")
	}
	raw, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	gr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("object %q is not gzip: %w", *in.Key, err)
	}
	body, err := io.ReadAll(gr)
	if err != nil {
		return nil, err
	}
	f.objects[*in.Key] = body
	return &s3.PutObjectOutput{}, nil
}

func (f *fakeS3) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.objects))
	for k := range f.objects {
		out = append(out, k)
	}
	return out
}

func (f *fakeS3) totalLines() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, body := range f.objects {
		sc := bufio.NewScanner(bytes.NewReader(body))
		for sc.Scan() {
			if strings.TrimSpace(sc.Text()) != "" {
				n++
			}
		}
	}
	return n
}

// testSink builds an S3 sink on its own writer over a fake S3 bucket.
func testSink(t *testing.T, client s3PutObjectAPI, cfg S3Config, wcfg WriterConfig) (*S3Sink, *Writer) {
	t.Helper()
	wcfg.Targets = append(wcfg.Targets, newS3TargetWithClient("store", client, "test-bucket"))
	w := testWriter(t, wcfg)
	cfg.Target = "store"
	if cfg.InstanceID == "" {
		cfg.InstanceID = "pod-a"
	}
	return NewS3Sink(w, cfg), w
}

func ev(seq uint64) Event {
	return Event{CorrelationID: "cid", Seq: seq, Phase: PhaseComplete, Host: "api.anthropic.com"}
}

func TestS3Sink_FlushOnCount(t *testing.T) {
	f := newFakeS3()
	tick := newManualTick()
	s, w := testSink(t, f, S3Config{MaxEvents: 3}, WriterConfig{NewTicker: tick.ticker})
	defer w.Close()
	for i := 0; i < 3; i++ {
		if err := s.Emit(ev(uint64(i))); err != nil {
			t.Fatalf("emit: %v", err)
		}
	}
	waitFor(t, func() bool { return len(f.keys()) == 1 })
	if got := f.totalLines(); got != 3 {
		t.Fatalf("expected 3 NDJSON lines, got %d", got)
	}
}

func TestS3Sink_FlushOnBytes(t *testing.T) {
	f := newFakeS3()
	tick := newManualTick()
	// Tiny byte threshold: a single event exceeds it, so each Emit seals.
	s, w := testSink(t, f, S3Config{MaxBytes: 1, MaxEvents: 1000}, WriterConfig{NewTicker: tick.ticker})
	defer w.Close()
	_ = s.Emit(ev(0))
	_ = s.Emit(ev(1))
	waitFor(t, func() bool { return len(f.keys()) == 2 })
}

func TestS3Sink_FlushOnTick(t *testing.T) {
	f := newFakeS3()
	tick := newManualTick()
	s, w := testSink(t, f, S3Config{MaxEvents: 1000, MaxBytes: 1 << 30}, WriterConfig{NewTicker: tick.ticker})
	defer w.Close()
	_ = s.Emit(ev(0)) // below size/count thresholds
	time.Sleep(20 * time.Millisecond)
	if len(f.keys()) != 0 {
		t.Fatal("sealed before the tick")
	}
	tick.fire()
	waitFor(t, func() bool { return len(f.keys()) == 1 })
}

func TestS3Sink_KeyLayout(t *testing.T) {
	var mu sync.Mutex
	cur := time.Date(2026, 10, 22, 20, 5, 0, 0, time.UTC)
	restore := nowFn
	nowFn = func() time.Time { mu.Lock(); defer mu.Unlock(); return cur }
	defer func() { nowFn = restore }()

	f := newFakeS3()
	s, w := testSink(t, f, S3Config{Prefix: "aintercept", InstanceID: "observer-7c9", MaxEvents: 1}, WriterConfig{})
	defer w.Close()
	_ = s.Emit(ev(0))
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	keys := f.keys()
	if len(keys) != 1 {
		t.Fatalf("expected 1 key, got %v", keys)
	}
	// prefix/dt=YYYY-MM-DD/hour=HH/instance=<id>/<ns>-<seq>.ndjson.gz
	want := regexp.MustCompile(`^aintercept/dt=2026-10-22/hour=20/instance=observer-7c9/\d+-000001\.ndjson\.gz$`)
	if !want.MatchString(keys[0]) {
		t.Fatalf("key %q does not match expected layout %q", keys[0], want.String())
	}
}

// TestS3Sink_NoClobberAcrossInstances is the core multi-pod guarantee: many
// instances flushing concurrently must never produce the same object key.
func TestS3Sink_NoClobberAcrossInstances(t *testing.T) {
	var mu sync.Mutex
	cur := time.Date(2026, 10, 22, 20, 0, 0, 0, time.UTC)
	restore := nowFn
	nowFn = func() time.Time { mu.Lock(); defer mu.Unlock(); return cur }
	defer func() { nowFn = restore }()

	f := newFakeS3() // shared "bucket" across all instances
	const instances = 25
	const batchesEach = 8
	var wg sync.WaitGroup
	for i := 0; i < instances; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			s, w := testSink(t, f, S3Config{InstanceID: fmt.Sprintf("pod-%02d", id), MaxEvents: 1}, WriterConfig{})
			for b := 0; b < batchesEach; b++ {
				_ = s.Emit(ev(uint64(b)))
			}
			_ = w.Close() // flushes + drains this instance
		}(i)
	}
	wg.Wait()
	if got, want := len(f.keys()), instances*batchesEach; got != want {
		t.Fatalf("expected %d distinct keys (no clobbering), got %d", want, got)
	}
	if got, want := f.totalLines(), instances*batchesEach; got != want {
		t.Fatalf("expected %d total events persisted, got %d", want, got)
	}
}

func TestS3Sink_GzipRoundTripsEvents(t *testing.T) {
	f := newFakeS3()
	s, w := testSink(t, f, S3Config{MaxEvents: 2}, WriterConfig{})
	defer w.Close()
	for _, e := range []Event{ev(0), ev(1)} {
		_ = s.Emit(e)
	}
	_ = w.Flush()
	keys := f.keys()
	if len(keys) != 1 {
		t.Fatalf("expected 1 object, got %d", len(keys))
	}
	var got []Event
	sc := bufio.NewScanner(bytes.NewReader(f.objects[keys[0]]))
	for sc.Scan() {
		var e Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("object line is not valid JSON event: %v", err)
		}
		got = append(got, e)
	}
	if len(got) != 2 || got[0].Seq != 0 || got[1].Seq != 1 {
		t.Fatalf("round-trip mismatch: got %+v", got)
	}
}

func TestS3Sink_CloseFlushesPartialBatch(t *testing.T) {
	f := newFakeS3()
	tick := newManualTick()
	s, w := testSink(t, f, S3Config{MaxEvents: 1000, MaxBytes: 1 << 30}, WriterConfig{NewTicker: tick.ticker})
	_ = s.Emit(ev(0))
	_ = s.Emit(ev(1))
	if got := len(f.keys()); got != 0 {
		t.Fatalf("expected no objects before Close, got %d", got)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := f.totalLines(); got != 2 {
		t.Fatalf("expected 2 events persisted on Close, got %d", got)
	}
}

// A failed PUT is retried until it succeeds: nothing is shed.
func TestS3Sink_PutErrorIsRetriedNotDropped(t *testing.T) {
	f := newFakeS3()
	f.failN = 3
	s, w := testSink(t, f, S3Config{MaxEvents: 1}, WriterConfig{})
	_ = s.Emit(ev(0))
	_ = s.Emit(ev(1))
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := len(f.objects); got != 2 {
		t.Fatalf("expected both objects persisted after retries, got %d", got)
	}
	if f.puts != 5 {
		t.Fatalf("expected 5 PUT attempts (3 failures + 2 successes), got %d", f.puts)
	}
}

func TestS3Sink_EmitAfterCloseErrors(t *testing.T) {
	f := newFakeS3()
	s, w := testSink(t, f, S3Config{}, WriterConfig{})
	_ = w.Close()
	if err := s.Emit(ev(0)); err == nil {
		t.Fatalf("expected error emitting after close")
	}
}
