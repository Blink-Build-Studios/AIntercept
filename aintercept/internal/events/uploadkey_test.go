package events

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// Records files are named by the minute they're uploaded in, at each upload
// attempt, and never written twice (docs/STREAMING_PIPELINE.md §3.3).

const tmplKey = "records/v1/bucket=03/minute=" + UploadMinute + "/pod-1-000000000001.parquet"

func minuteKey(m string) string {
	return "records/v1/bucket=03/minute=" + m + "/pod-1-000000000001.parquet"
}

// testClock is a settable clock.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *testClock) set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

// scriptTarget is a Target with Exists. Each Put runs the next step of a
// script: "ok" lands, "fail" doesn't land and errors, "ambiguous" lands and
// errors (a timeout after success). After a step, before is called, so a test
// can move the clock between attempts. Past the script, puts succeed.
type scriptTarget struct {
	mu      sync.Mutex
	objects map[string][]byte
	puts    map[string]int
	script  []string
	after   func(attempt int)
	heads   int
	keys    []string // the key of each attempt
}

func newScriptTarget(script ...string) *scriptTarget {
	return &scriptTarget{objects: map[string][]byte{}, puts: map[string]int{}, script: script}
}

func (s *scriptTarget) Name() string { return "store" }

func (s *scriptTarget) Put(_ context.Context, o Object) error {
	s.mu.Lock()
	n := len(s.keys)
	s.keys = append(s.keys, o.Key)
	step := "ok"
	if n < len(s.script) {
		step = s.script[n]
	}
	if step == "ok" || step == "ambiguous" {
		s.objects[o.Key] = append([]byte(nil), o.Body...)
		s.puts[o.Key]++
	}
	after := s.after
	s.mu.Unlock()
	if after != nil {
		after(n + 1)
	}
	if step == "ok" {
		return nil
	}
	return errors.New("synthetic " + step)
}

func (s *scriptTarget) Exists(_ context.Context, key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.heads++
	_, ok := s.objects[key]
	return ok, nil
}

func (s *scriptTarget) snapshot() (map[string]int, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := map[string]int{}
	for k, v := range s.puts {
		p[k] = v
	}
	return p, append([]string(nil), s.keys...)
}

var t0 = time.Date(2027, 3, 1, 10, 0, 30, 0, time.UTC)

func atMinute(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }

func putOne(t *testing.T, w *Writer) {
	t.Helper()
	o := w.NewObjectStream(ObjectStreamConfig{Target: "store"})
	if err := o.PutAtUpload(tmplKey, []byte("records")); err != nil {
		t.Fatal(err)
	}
}

func onlyObject(t *testing.T, tgt *scriptTarget, want string) {
	t.Helper()
	puts, keys := tgt.snapshot()
	if len(puts) != 1 || puts[want] != 1 {
		t.Fatalf("objects %v (attempt keys %v), want exactly one at %s", puts, keys, want)
	}
}

// Atomic: a clean upload is named by the minute it's uploaded in, not sealed in.
func TestUploadKeyCleanUploadGetsItsUploadMinute(t *testing.T) {
	clock := &testClock{t: atMinute(0)}
	tgt := newScriptTarget()
	tick := newManualTick()
	w := testWriter(t, WriterConfig{Targets: []Target{tgt}, NewTicker: tick.ticker, Now: clock.now})
	putOne(t, w)
	clock.set(atMinute(2)) // sealed and uploaded two minutes after the put
	tick.fire()
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	onlyObject(t, tgt, minuteKey("2027-03-01T10:02"))
	_ = w.Close()
}

// Atomic: a failed attempt, then a later success: the object is named by the
// successful attempt's minute.
func TestUploadKeyRetryUsesTheSuccessfulAttemptsMinute(t *testing.T) {
	clock := &testClock{t: atMinute(0)}
	tgt := newScriptTarget("fail")
	tgt.after = func(n int) {
		if n == 1 {
			clock.set(atMinute(3))
		}
	}
	w := testWriter(t, WriterConfig{Targets: []Target{tgt}, Now: clock.now})
	putOne(t, w)
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	onlyObject(t, tgt, minuteKey("2027-03-01T10:03"))
	_ = w.Close()
}

// Atomic: an ambiguous failure that in fact landed: one object, under the first
// attempt's key, never a second under a new minute.
func TestUploadKeyAmbiguousLandedIsNeverWrittenTwice(t *testing.T) {
	clock := &testClock{t: atMinute(0)}
	tgt := newScriptTarget("ambiguous")
	tgt.after = func(n int) {
		if n == 1 {
			clock.set(atMinute(2))
		}
	}
	w := testWriter(t, WriterConfig{Targets: []Target{tgt}, Now: clock.now})
	putOne(t, w)
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	onlyObject(t, tgt, minuteKey("2027-03-01T10:00"))
	_ = w.Close()
}

// Atomic: an ambiguous failure that didn't land: retried under the new minute.
func TestUploadKeyAmbiguousNotLandedGetsTheNextMinute(t *testing.T) {
	clock := &testClock{t: atMinute(0)}
	tgt := newScriptTarget("fail")
	tgt.after = func(n int) {
		if n == 1 {
			clock.set(atMinute(1))
		}
	}
	w := testWriter(t, WriterConfig{Targets: []Target{tgt}, Now: clock.now})
	putOne(t, w)
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	onlyObject(t, tgt, minuteKey("2027-03-01T10:01"))
	if tgt.heads == 0 {
		t.Fatal("retried under a new minute without checking the earlier attempt")
	}
	_ = w.Close()
}

// Atomic: retries within one minute reuse the key (an idempotent overwrite of
// the same bytes), without a check.
func TestUploadKeySameMinuteRetriesReuseTheKey(t *testing.T) {
	clock := &testClock{t: atMinute(0)}
	tgt := newScriptTarget("fail", "fail", "ambiguous")
	w := testWriter(t, WriterConfig{Targets: []Target{tgt}, Now: clock.now})
	putOne(t, w)
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	onlyObject(t, tgt, minuteKey("2027-03-01T10:00"))
	_, keys := tgt.snapshot()
	for _, k := range keys {
		if k != minuteKey("2027-03-01T10:00") {
			t.Fatalf("attempt under %s", k)
		}
	}
}

// Crash between attempts: the attempted keys survive in the spool, so the next
// process checks them first and still writes the object at most once.
func TestUploadKeyCrashBetweenAttemptsKeepsTheAttemptedKeys(t *testing.T) {
	for _, landed := range []bool{true, false} {
		dir := t.TempDir()
		clock := &testClock{t: atMinute(0)}
		step := "fail"
		if landed {
			step = "ambiguous"
		}
		tgt := newScriptTarget(step, "fail", "fail", "fail", "fail", "fail", "fail", "fail")
		crashed := make(chan struct{})
		var once sync.Once
		tgt.after = func(n int) { once.Do(func() { close(crashed) }) }
		w1 := testWriter(t, WriterConfig{SpoolDir: dir, Targets: []Target{tgt}, Now: clock.now, RetryMin: time.Hour, RetryMax: time.Hour})
		putOne(t, w1)
		go func() { _ = w1.Flush() }()
		<-crashed
		w1.Abandon() // the process dies between attempts

		clock.set(atMinute(5))
		tgt.mu.Lock()
		tgt.script, tgt.keys, tgt.after = nil, nil, nil // the store is healthy again
		tgt.mu.Unlock()
		w2 := testWriter(t, WriterConfig{SpoolDir: dir, Targets: []Target{tgt}, Now: clock.now})
		if err := w2.Flush(); err != nil {
			t.Fatal(err)
		}
		if landed {
			onlyObject(t, tgt, minuteKey("2027-03-01T10:00"))
		} else {
			onlyObject(t, tgt, minuteKey("2027-03-01T10:05"))
		}
		_ = w2.Close()
	}
}

// A bundle half uploaded when the process died: the objects already up are
// not uploaded again under a new minute.
func TestUploadKeyHalfUploadedBundleIsNotRewritten(t *testing.T) {
	dir := t.TempDir()
	clock := &testClock{t: atMinute(0)}
	tgt := newScriptTarget("ok", "fail")
	crashed := make(chan struct{})
	var once sync.Once
	tgt.after = func(n int) {
		if n == 2 {
			once.Do(func() { close(crashed) })
		}
	}
	tick := newManualTick()
	w1 := testWriter(t, WriterConfig{SpoolDir: dir, Targets: []Target{tgt}, Now: clock.now, NewTicker: tick.ticker, RetryMin: time.Hour, RetryMax: time.Hour})
	o := w1.NewObjectStream(ObjectStreamConfig{Target: "store"})
	_ = o.PutAtUpload("r/minute="+UploadMinute+"/a", []byte("a"))
	_ = o.PutAtUpload("r/minute="+UploadMinute+"/b", []byte("b"))
	tick.fire()
	<-crashed
	w1.Abandon()

	clock.set(atMinute(4))
	tgt.mu.Lock()
	tgt.script, tgt.keys, tgt.after = nil, nil, nil
	tgt.mu.Unlock()
	w2 := testWriter(t, WriterConfig{SpoolDir: dir, Targets: []Target{tgt}, Now: clock.now})
	if err := w2.Flush(); err != nil {
		t.Fatal(err)
	}
	puts, _ := tgt.snapshot()
	want := map[string]int{"r/minute=2027-03-01T10:00/a": 1, "r/minute=2027-03-01T10:04/b": 1}
	if len(puts) != len(want) || puts["r/minute=2027-03-01T10:00/a"] != 1 || puts["r/minute=2027-03-01T10:04/b"] != 1 {
		t.Fatalf("objects %v, want %v", puts, want)
	}
	_ = w2.Close()
}

// Fixed keys (raw log, call detail objects) are untouched: uploaded as given.
func TestUploadKeyFixedKeysAreUnchanged(t *testing.T) {
	clock := &testClock{t: atMinute(0)}
	tgt := newScriptTarget("fail")
	tgt.after = func(int) { clock.set(atMinute(2)) }
	w := testWriter(t, WriterConfig{Targets: []Target{tgt}, Now: clock.now})
	o := w.NewObjectStream(ObjectStreamConfig{Target: "store"})
	_ = o.Put("calls/abc.ndjson.gz", []byte("x"))
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	onlyObject(t, tgt, "calls/abc.ndjson.gz")
}
