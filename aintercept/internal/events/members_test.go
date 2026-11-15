package events

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"sort"
	"strings"
	"testing"
	"time"
)

// v9's raw stream (docs/PIPELINE.md §2.1): each write is a whole gzip member,
// appended as is; the stream's file is sealed at its own size or age, not by
// the writer's tick or shared byte cap, and named by the hour it's uploaded in.

func member(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	_, _ = zw.Write([]byte(s))
	_ = zw.Close()
	return b.Bytes()
}

func membersStream(w *Writer, maxBytes int, maxAge time.Duration) *Stream {
	return w.NewStream(StreamConfig{Target: "mem", Members: true, AtUpload: true, MaxBytes: maxBytes, MaxAge: maxAge,
		ContentType: "application/gzip", Key: func(seq uint64, _ time.Time) string {
			return fmt.Sprintf("raw/%s/pod-1/0000000000000000001-%06d.ndjson.gz", UploadHour, seq)
		}})
}

func TestMembersStreamSealsAtItsSize(t *testing.T) {
	tgt := newMemTarget("mem")
	tick := newManualTick()
	w := testWriter(t, WriterConfig{Targets: []Target{tgt}, NewTicker: tick.ticker})
	defer w.Close()
	s := membersStream(w, 400, time.Hour)
	var want []byte
	for i := 0; i < 20; i++ {
		m := member(t, strings.Repeat("x", i)+"\n")
		want = append(want, m...)
		_ = s.Write(m)
	}
	waitFor(t, func() bool { objs, _ := tgt.snapshot(); return len(objs) >= 1 })
	w.Flush()
	objs, _ := tgt.snapshot()
	if len(objs) < 2 {
		t.Fatalf("want the stream split by size, got %d objects", len(objs))
	}
	var got []byte
	var keys []string
	for k := range objs {
		keys = append(keys, k)
	}
	for _, k := range sortedStrings(keys) {
		got = append(got, objs[k]...)
		if len(objs[k]) > 400+200 {
			t.Errorf("%s is %d bytes, over its size by more than a member", k, len(objs[k]))
		}
	}
	if !bytes.Equal(got, want) {
		t.Fatal("the files aren't the members, in order, unchanged")
	}
}

func TestMembersStreamSealsByItsAgeNotTheTick(t *testing.T) {
	tgt := newMemTarget("mem")
	tick := newManualTick()
	clk := &testClock{t: t0}
	w := testWriter(t, WriterConfig{Targets: []Target{tgt}, NewTicker: tick.ticker, Now: clk.now})
	defer w.Close()
	s := membersStream(w, 8<<20, 30*time.Second)
	_ = s.Write(member(t, "a\n"))
	time.Sleep(20 * time.Millisecond) // the loop has it
	for _, at := range []time.Duration{10 * time.Second, 20 * time.Second, 29 * time.Second} {
		clk.set(t0.Add(at))
		tick.fire()
		time.Sleep(10 * time.Millisecond)
		if objs, _ := tgt.snapshot(); len(objs) != 0 {
			t.Fatalf("sealed at %s, before its age", at)
		}
	}
	clk.set(t0.Add(30 * time.Second))
	tick.fire()
	waitFor(t, func() bool { objs, _ := tgt.snapshot(); return len(objs) == 1 })
}

func TestMembersStreamIgnoresTheSharedByteCap(t *testing.T) {
	tgt := newMemTarget("mem")
	tick := newManualTick()
	w := testWriter(t, WriterConfig{Targets: []Target{tgt}, MaxBufferBytes: 100, NewTicker: tick.ticker})
	defer w.Close()
	raw := membersStream(w, 8<<20, time.Hour)
	plain := plainStream(w, "mem", "plain")
	_ = raw.Write(member(t, "r\n"))
	for i := 0; i < 50; i++ {
		_ = plain.Write([]byte("0123456789"))
	}
	waitFor(t, func() bool { objs, _ := tgt.snapshot(); return len(objs) >= 1 })
	time.Sleep(20 * time.Millisecond)
	objs, _ := tgt.snapshot()
	for k := range objs {
		if strings.HasPrefix(k, "raw/") {
			t.Fatalf("the raw stream was sealed by another stream's bytes: %s", k)
		}
	}
	w.Flush() // Flush seals it
	objs, _ = tgt.snapshot()
	n := 0
	for k := range objs {
		if strings.HasPrefix(k, "raw/") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("Flush: want the raw file, got %d", n)
	}
}

func TestMembersStreamIsNamedByItsUploadHour(t *testing.T) {
	tgt := newMemTarget("mem")
	tick := newManualTick()
	clk := &testClock{t: time.Date(2026, 11, 14, 23, 59, 59, 0, time.UTC)}
	w := testWriter(t, WriterConfig{Targets: []Target{tgt}, NewTicker: tick.ticker, Now: clk.now})
	defer w.Close()
	s := membersStream(w, 8<<20, time.Hour)
	_ = s.Write(member(t, "a\n"))
	time.Sleep(20 * time.Millisecond)                      // the loop has it
	clk.set(time.Date(2026, 11, 15, 0, 0, 1, 0, time.UTC)) // uploaded an hour later than it began
	w.Flush()
	objs, _ := tgt.snapshot()
	want := "raw/2026-11-15/00/pod-1/0000000000000000001-000001.ndjson.gz"
	if _, ok := objs[want]; !ok || len(objs) != 1 {
		t.Fatalf("want %s, got %v", want, keysOf(objs))
	}
	zr, err := gzip.NewReader(bytes.NewReader(objs[want]))
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(zr); string(b) != "a\n" {
		t.Fatalf("body %q", b)
	}
}

func keysOf(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return sortedStrings(out)
}

func sortedStrings(s []string) []string { sort.Strings(s); return s }
