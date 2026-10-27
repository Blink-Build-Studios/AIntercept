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
	failN   int // fail the first failN puts (to exercise error handling)
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

func testSink(client s3PutObjectAPI, cfg S3Config) *S3Sink {
	if cfg.Bucket == "" {
		cfg.Bucket = "test-bucket"
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	if cfg.InstanceID == "" {
		cfg.InstanceID = "pod-a"
	}
	return newS3SinkWithClient(client, cfg)
}

func ev(seq uint64) Event {
	return Event{CorrelationID: "cid", Seq: seq, Phase: PhaseComplete, Host: "api.anthropic.com"}
}

func TestS3Sink_FlushOnCount(t *testing.T) {
	f := newFakeS3()
	s := testSink(f, S3Config{MaxEvents: 3, MaxAge: time.Hour})
	defer s.Close()

	for i := 0; i < 3; i++ {
		if err := s.Emit(ev(uint64(i))); err != nil {
			t.Fatalf("emit: %v", err)
		}
	}
	s.Flush()

	if got := len(f.keys()); got != 1 {
		t.Fatalf("expected 1 object after count-triggered flush, got %d (keys=%v)", got, f.keys())
	}
	if got := f.totalLines(); got != 3 {
		t.Fatalf("expected 3 NDJSON lines, got %d", got)
	}
}

func TestS3Sink_FlushOnBytes(t *testing.T) {
	f := newFakeS3()
	// Tiny byte threshold: a single event exceeds it, so each Emit seals.
	s := testSink(f, S3Config{MaxBytes: 1, MaxEvents: 1000, MaxAge: time.Hour})
	defer s.Close()

	s.Emit(ev(0))
	s.Emit(ev(1))
	s.Flush()

	if got := len(f.keys()); got != 2 {
		t.Fatalf("expected 2 objects (byte-triggered per event), got %d", got)
	}
}

func TestS3Sink_FlushOnAge(t *testing.T) {
	// Drive the clock manually so the age flusher fires deterministically.
	var mu sync.Mutex
	cur := time.Date(2026, 10, 22, 20, 0, 0, 0, time.UTC)
	restore := nowFn
	nowFn = func() time.Time { mu.Lock(); defer mu.Unlock(); return cur }
	defer func() { nowFn = restore }()
	advance := func(d time.Duration) { mu.Lock(); cur = cur.Add(d); mu.Unlock() }

	f := newFakeS3()
	s := testSink(f, S3Config{MaxEvents: 1000, MaxBytes: 1 << 30, MaxAge: 100 * time.Millisecond})
	defer s.Close()

	s.Emit(ev(0)) // opens a batch below size/count thresholds
	advance(200 * time.Millisecond)

	// Wait for the ageLoop ticker (interval = maxAge/2 = 50ms) to observe the
	// advanced clock and seal the batch.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(f.keys()) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := len(f.keys()); got != 1 {
		t.Fatalf("expected 1 object after age-triggered flush, got %d", got)
	}
}

func TestS3Sink_KeyLayout(t *testing.T) {
	var mu sync.Mutex
	cur := time.Date(2026, 10, 22, 20, 5, 0, 0, time.UTC)
	restore := nowFn
	nowFn = func() time.Time { mu.Lock(); defer mu.Unlock(); return cur }
	defer func() { nowFn = restore }()

	f := newFakeS3()
	s := testSink(f, S3Config{Prefix: "aintercept", InstanceID: "observer-7c9", MaxEvents: 1, MaxAge: time.Hour})
	defer s.Close()

	s.Emit(ev(0))
	s.Flush()

	keys := f.keys()
	if len(keys) != 1 {
		t.Fatalf("expected 1 key, got %v", keys)
	}
	key := keys[0]
	// prefix/dt=YYYY-MM-DD/hour=HH/instance=<id>/<ns>-<seq>.ndjson.gz
	want := regexp.MustCompile(`^aintercept/dt=2026-10-22/hour=20/instance=observer-7c9/\d+-000001\.ndjson\.gz$`)
	if !want.MatchString(key) {
		t.Fatalf("key %q does not match expected layout %q", key, want.String())
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
			s := testSink(f, S3Config{
				InstanceID: fmt.Sprintf("pod-%02d", id),
				MaxEvents:  1, // each Emit seals => batchesEach objects per instance
				MaxAge:     time.Hour,
			})
			for b := 0; b < batchesEach; b++ {
				s.Emit(ev(uint64(b)))
			}
			s.Close() // flushes + drains this instance
		}(i)
	}
	wg.Wait()

	gotKeys := len(f.keys())
	wantKeys := instances * batchesEach
	if gotKeys != wantKeys {
		t.Fatalf("expected %d distinct keys (no clobbering), got %d — a collision overwrote an object",
			wantKeys, gotKeys)
	}
	if gotLines := f.totalLines(); gotLines != wantKeys {
		t.Fatalf("expected %d total events persisted, got %d", wantKeys, gotLines)
	}
}

func TestS3Sink_GzipRoundTripsEvents(t *testing.T) {
	f := newFakeS3()
	s := testSink(f, S3Config{MaxEvents: 2, MaxAge: time.Hour})
	defer s.Close()

	in := []Event{ev(0), ev(1)}
	for _, e := range in {
		s.Emit(e)
	}
	s.Flush()

	keys := f.keys()
	if len(keys) != 1 {
		t.Fatalf("expected 1 object, got %d", len(keys))
	}
	body := f.objects[keys[0]]
	var got []Event
	sc := bufio.NewScanner(bytes.NewReader(body))
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
	s := testSink(f, S3Config{MaxEvents: 1000, MaxBytes: 1 << 30, MaxAge: time.Hour})

	// Emit fewer than any threshold; only Close should flush the remainder.
	s.Emit(ev(0))
	s.Emit(ev(1))
	if got := len(f.keys()); got != 0 {
		t.Fatalf("expected no objects before Close, got %d", got)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := len(f.keys()); got != 1 {
		t.Fatalf("expected 1 object after Close-flush, got %d", got)
	}
	if got := f.totalLines(); got != 2 {
		t.Fatalf("expected 2 events persisted on Close, got %d", got)
	}
}

func TestS3Sink_PutErrorDoesNotBlockOrPanic(t *testing.T) {
	f := newFakeS3()
	f.failN = 1 // first PUT fails
	s := testSink(f, S3Config{MaxEvents: 1, MaxAge: time.Hour})

	s.Emit(ev(0)) // this batch's PUT fails
	s.Emit(ev(1)) // this one succeeds
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// One succeeded object persisted; the failed one is shed, not retried-forever.
	if got := len(f.objects); got != 1 {
		t.Fatalf("expected 1 persisted object after 1 failure, got %d", got)
	}
	if f.puts != 2 {
		t.Fatalf("expected 2 PUT attempts, got %d", f.puts)
	}
}

func TestS3Sink_EmitAfterCloseErrors(t *testing.T) {
	f := newFakeS3()
	s := testSink(f, S3Config{MaxEvents: 1000, MaxAge: time.Hour})
	s.Close()
	if err := s.Emit(ev(0)); err == nil {
		t.Fatalf("expected error emitting after close")
	}
}
