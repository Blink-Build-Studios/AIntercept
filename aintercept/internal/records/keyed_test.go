package records_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// v5 ingest (docs/PIPELINE.md §1): one buffer per routing key, flushed on a
// byte size or a timer (from the buffer's first record, ±10% jitter, at most
// 60 s), oldest first over a memory cap; each buffer a spool file, handed to
// the writer durably, so a crash loses nothing that reached the disk and
// nothing is uploaded twice.

// store is an in-memory object store Target (with Exists) that can fail.
type store struct {
	mu      sync.Mutex
	objs    map[string][]byte
	puts    map[string]int
	failPct int
	rng     *rand.Rand
}

func newStore() *store { return &store{objs: map[string][]byte{}, puts: map[string]int{}} }

func (s *store) Name() string { return "s3" }
func (s *store) Put(_ context.Context, o events.Object) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failPct > 0 && s.rng.Intn(100) < s.failPct {
		if s.rng.Intn(2) == 0 { // ambiguous: it landed, then the call failed
			s.objs[o.Key] = append([]byte(nil), o.Body...)
			s.puts[o.Key]++
		}
		return errors.New("synthetic store failure")
	}
	s.objs[o.Key] = append([]byte(nil), o.Body...)
	s.puts[o.Key]++
	return nil
}
func (s *store) Exists(_ context.Context, k string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.objs[k]
	return ok, nil
}

// inFiles is every ingest file in the store, by routing key: its records' ids.
func (s *store) inFiles(t *testing.T) map[string][][]string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string][][]string{}
	for k, b := range s.objs {
		rest, ok := strings.CutPrefix(k, "v1/keys/")
		if !ok {
			continue
		}
		esc, _, ok := strings.Cut(rest, "/in/")
		if !ok {
			continue
		}
		key, _ := url.PathUnescape(esc)
		rs, err := records.Decode(b)
		if err != nil {
			t.Fatalf("decode %s: %v", k, err)
		}
		var ids []string
		for _, r := range rs {
			if r.RoutingKey != key {
				t.Fatalf("record %s (key %s) in %s's folder", r.RecordID, r.RoutingKey, key)
			}
			ids = append(ids, r.RecordID)
		}
		out[key] = append(out[key], ids)
	}
	return out
}

func (s *store) puttedTwice() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for k, n := range s.puts {
		if n > 1 && strings.Contains(k, "/in/") {
			out = append(out, k)
		}
	}
	return out
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

var start = time.Date(2027, 3, 1, 10, 0, 0, 0, time.UTC)

type rig struct {
	st     *store
	w      *events.Writer
	s      *records.Sink
	clock  *clock
	spool  string
	keys   string
	inc    string
	jitter func(time.Duration) time.Duration
}

func tmpl(inc string) func(key string, seq int64) string {
	return func(key string, seq int64) string {
		return fmt.Sprintf("v1/keys/%s/in/%s-%s-%012d.parquet", url.PathEscape(key), events.UploadTime, inc, seq)
	}
}

// open starts a writer and sink on the rig's spool directories (a restart
// when called again).
func (r *rig) open(t *testing.T, cfg records.SinkConfig) {
	t.Helper()
	w, err := events.NewWriter(events.WriterConfig{SpoolDir: r.spool, Targets: []events.Target{r.st}, SkipFsync: true,
		RetryMin: time.Millisecond, RetryMax: 2 * time.Millisecond, Now: r.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	r.w = w
	r.inc = records.NewIncarnation("pod")
	cfg.Putter = records.WriterPutter(w, "s3")
	cfg.Incarnation = r.inc
	cfg.KeySpoolDir = r.keys
	cfg.KeyObject = tmpl(r.inc)
	cfg.Now = r.clock.Now
	if cfg.Jitter == nil {
		cfg.Jitter = func(d time.Duration) time.Duration { return d }
	}
	r.s = records.NewSink(cfg)
}

func newRig(t *testing.T, cfg records.SinkConfig) *rig {
	r := &rig{st: newStore(), clock: &clock{t: start}, spool: t.TempDir(), keys: t.TempDir()}
	r.open(t, cfg)
	return r
}

func (r *rig) drain(t *testing.T) {
	t.Helper()
	if err := r.w.Flush(); err != nil {
		t.Fatal(err)
	}
}

var nrec int

func rec(key string) records.Record {
	nrec++
	ns := start.UnixNano()
	id := fmt.Sprintf("r%06d", nrec)
	r := records.Record{Version: records.Version, RecordID: id, Kind: records.KindCallEnd, CallID: id,
		Call: &canon.Request{CallID: id, SessionID: strings.TrimPrefix(key, "s:"), KeyHash: "k", StartNS: ns}}
	r.RoutingKey = records.RoutingKey(&r)
	return r
}

// Atomic: a buffer flushes once it reaches its byte size; other keys don't.
func TestKeyedFlushesOnSize(t *testing.T) {
	r := newRig(t, records.SinkConfig{FlushBytes: 600, FlushAge: 30 * time.Second})
	for i := 0; i < 3; i++ {
		r.s.AddRecords(rec("s:big"))
	}
	r.s.AddRecords(rec("s:small"))
	r.drain(t)
	got := r.st.inFiles(t)
	if len(got["s:big"]) != 1 || len(got["s:small"]) != 0 {
		t.Fatalf("files by key %v, want one for s:big only", got)
	}
}

// Atomic: the timer runs from the buffer's first record, with its jitter.
func TestKeyedFlushesOnTimerWithJitter(t *testing.T) {
	r := newRig(t, records.SinkConfig{FlushBytes: 1 << 20, FlushAge: 30 * time.Second,
		Jitter: func(d time.Duration) time.Duration { return d + d/10 }}) // +10%: 33 s
	r.s.AddRecords(rec("s:a"))
	r.clock.add(20 * time.Second)
	r.s.AddRecords(rec("s:a")) // a later record doesn't restart the timer
	r.clock.add(12 * time.Second)
	r.s.Tick()
	r.drain(t)
	if n := len(r.st.inFiles(t)["s:a"]); n != 0 {
		t.Fatalf("flushed at 32 s, before the jittered 33 s: %d files", n)
	}
	r.clock.add(2 * time.Second)
	r.s.Tick()
	r.drain(t)
	if got := r.st.inFiles(t)["s:a"]; len(got) != 1 || len(got[0]) != 2 {
		t.Fatalf("after 34 s: %v, want one file with both records", got)
	}
}

// Atomic: the timer never exceeds 60 s, whatever is configured.
func TestKeyedTimerIsCappedAt60s(t *testing.T) {
	r := newRig(t, records.SinkConfig{FlushBytes: 1 << 20, FlushAge: 5 * time.Minute})
	r.s.AddRecords(rec("s:a"))
	r.clock.add(61 * time.Second)
	r.s.Tick()
	r.drain(t)
	if n := len(r.st.inFiles(t)["s:a"]); n != 1 {
		t.Fatalf("%d files after 61 s with a 5 min setting, want 1 (capped at 60 s)", n)
	}
}

// Atomic: over the memory cap, the oldest buffers flush first.
func TestKeyedMemoryCapFlushesOldestFirst(t *testing.T) {
	r := newRig(t, records.SinkConfig{FlushBytes: 1 << 20, FlushAge: 60 * time.Second, MaxBufferBytes: 900})
	r.s.AddRecords(rec("s:old"))
	r.clock.add(time.Second)
	r.s.AddRecords(rec("s:mid"))
	r.clock.add(time.Second)
	r.s.AddRecords(rec("s:new"), rec("s:new")) // pushes the total over the cap
	r.drain(t)
	got := r.st.inFiles(t)
	if len(got["s:old"]) != 1 || len(got["s:new"]) != 0 {
		t.Fatalf("files by key %v: want s:old flushed first, s:new still buffered", got)
	}
}

// Atomic: Flush (SIGTERM) writes every buffer.
func TestKeyedFlushWritesEveryBuffer(t *testing.T) {
	r := newRig(t, records.SinkConfig{FlushBytes: 1 << 20, FlushAge: 60 * time.Second})
	for _, k := range []string{"s:a", "s:b", "s:c"} {
		r.s.AddRecords(rec(k))
	}
	if err := r.s.Flush(); err != nil {
		t.Fatal(err)
	}
	r.drain(t)
	if got := r.st.inFiles(t); len(got) != 3 {
		t.Fatalf("files by key %v, want all 3 keys", got)
	}
	if left, _ := filepath.Glob(filepath.Join(r.keys, "*", "*")); len(left) != 0 {
		t.Fatalf("buffer files left after Flush: %v", left)
	}
}

// Atomic: a crash with buffers on disk: the next process uploads them, every
// record exactly once, and no object twice.
func TestKeyedCrashRecoveryUploadsEachRecordOnce(t *testing.T) {
	r := newRig(t, records.SinkConfig{FlushBytes: 1 << 20, FlushAge: 60 * time.Second})
	var want []string
	for _, k := range []string{"s:a", "s:b"} {
		for i := 0; i < 3; i++ {
			x := rec(k)
			want = append(want, x.RecordID)
			r.s.AddRecords(x)
		}
	}
	r.s.Sync()   // the buffers reach the disk (the once-a-second fsync)
	r.w.Abandon() // crash: nothing flushed, nothing uploaded
	r.open(t, records.SinkConfig{FlushBytes: 1 << 20, FlushAge: 60 * time.Second})
	_ = r.s.Flush()
	r.drain(t)
	assertExactlyOnce(t, r.st, want)
}

// Atomic: incarnations differ across restarts of the same pod, and sort by start.
func TestIncarnationsAreUniqueAcrossRestarts(t *testing.T) {
	a := records.NewIncarnation("pod-x")
	time.Sleep(time.Millisecond)
	b := records.NewIncarnation("pod-x")
	if a == b || !(a < b) {
		t.Fatalf("incarnations %q, %q: want distinct, in start order", a, b)
	}
}

// Generated: N pods, random crashes (with and without the buffers synced) and
// store failures (some ambiguous): every record lands in exactly one ingest
// file, under its own key, and no ingest object is written twice.
func TestKeyedChaosEveryRecordExactlyOnce(t *testing.T) {
	for seed := int64(1); seed <= 6; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			st := newStore()
			st.rng, st.failPct = rand.New(rand.NewSource(seed*7)), 20
			keys := []string{"s:a", "s:b", "s:c", "s:d", "s:e"}
			var want []string
			for pod := 0; pod < 3; pod++ {
				r := &rig{st: st, clock: &clock{t: start}, spool: t.TempDir(), keys: t.TempDir()}
				cfg := records.SinkConfig{FlushBytes: 400 + rng.Intn(800), FlushAge: 30 * time.Second, MaxBufferBytes: 3000}
				r.open(t, cfg)
				for i := 0; i < 120; i++ {
					x := rec(keys[rng.Intn(len(keys))])
					want = append(want, x.RecordID)
					r.s.AddRecords(x)
					r.clock.add(time.Duration(rng.Intn(4000)) * time.Millisecond)
					if rng.Intn(5) == 0 {
						r.s.Tick()
					}
					if rng.Intn(40) == 0 { // crash and restart
						r.s.Sync()
						r.w.Abandon()
						r.open(t, cfg)
					}
				}
				if err := r.s.Flush(); err != nil {
					t.Fatal(err)
				}
				st.mu.Lock()
				st.failPct = 0
				st.mu.Unlock()
				r.drain(t)
				_ = r.w.Close()
				st.mu.Lock()
				st.failPct = 20
				st.mu.Unlock()
			}
			assertExactlyOnce(t, st, want)
		})
	}
}

func assertExactlyOnce(t *testing.T, st *store, want []string) {
	t.Helper()
	seen := map[string]int{}
	for _, files := range st.inFiles(t) {
		for _, ids := range files {
			for _, id := range ids {
				seen[id]++
			}
		}
	}
	var missing, dup []string
	for _, id := range want {
		switch seen[id] {
		case 0:
			missing = append(missing, id)
		case 1:
		default:
			dup = append(dup, id)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 || len(dup) > 0 || len(seen) != len(want) {
		t.Fatalf("%d records: %d missing %v, %d duplicated %v, %d seen", len(want), len(missing), head(missing), len(dup), head(dup), len(seen))
	}
}

func head(s []string) []string {
	if len(s) > 5 {
		return s[:5]
	}
	return s
}

var _ = os.Remove
