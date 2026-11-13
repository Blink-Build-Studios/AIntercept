package records

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

type memPut struct {
	mu   sync.Mutex
	objs map[string][]byte
}

func (m *memPut) Put(_ context.Context, key string, b []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.objs == nil {
		m.objs = map[string][]byte{}
	}
	m.objs[key] = append([]byte(nil), b...)
	return nil
}

func (m *memPut) records(t *testing.T) []Record {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Record
	keys := make([]string, 0, len(m.objs))
	for k := range m.objs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		rs, err := Decode(m.objs[k])
		if err != nil {
			t.Fatalf("%s: %v", k, err)
		}
		for _, r := range rs {
			if want := "p/v1/keys/" + url.PathEscape(r.RoutingKey) + "/in/"; !strings.HasPrefix(k, want) {
				t.Fatalf("record of %s in %s", r.RoutingKey, k)
			}
		}
		out = append(out, rs...)
	}
	return out
}

func sampleRows() []events.Event {
	rows := []events.Event{hdr("c1", false, ccHeaders), reqChunk("c1", 10, true), respHdr("c1", "req_1"), complete("c1", ccClient),
		wsHdr("w1"), wsTurnStart("w1", 1, "t1"), wsTurn("w1", 1, "t1"), wsConn("w1"), hdr("c2", true, ccHeaders), disconnect("c2")}
	rows = append(rows, analytics("a1")...)
	rows = append(rows, clientEvent("a1", "e1", "s1", 0), clientEvent("a1", "", "s2", time.Second))
	for i := range rows {
		rows[i].ObserverInstance = "pod-a"
	}
	return rows
}

// The sink writes each routing key's records as their own ingest file, under
// the key's folder, named by S3 time at upload, once the key's buffer is due.
func TestSinkWritesPerKeyOnAge(t *testing.T) {
	now := time.Date(2026, 11, 12, 9, 7, 0, 0, time.UTC)
	put := &memPut{}
	s := NewSink(SinkConfig{Putter: put, Prefix: "p", Incarnation: "pod-a-1-ab", MaxAge: 10 * time.Second,
		FlushAge: 30 * time.Second, KeySpoolDir: t.TempDir(),
		Jitter: func(d time.Duration) time.Duration { return d }, Now: func() time.Time { return now }})
	for _, e := range sampleRows() {
		if err := s.Emit(e); err != nil {
			t.Fatal(err)
		}
	}
	if len(put.objs) != 0 {
		t.Fatalf("written before its timer: %v", len(put.objs))
	}
	now = now.Add(31 * time.Second)
	s.Tick()
	s.Wait()
	if len(put.objs) == 0 {
		t.Fatal("nothing written after FlushAge")
	}
	for k, b := range put.objs {
		if !strings.HasPrefix(k, "p/v1/keys/") || !strings.Contains(k, "/in/20261112T090731Z-pod-a-1-ab-") {
			t.Errorf("object key %s", k)
		}
		rs, err := Decode(b)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rs {
			if !strings.Contains(k, "/keys/"+url.PathEscape(r.RoutingKey)+"/in/") {
				t.Errorf("record of %s in %s", r.RoutingKey, k)
			}
		}
	}
	got := put.records(t)
	if len(got) < 6 {
		t.Fatalf("records: %d", len(got))
	}
}

func TestFlushSealsEverything(t *testing.T) {
	put := &memPut{}
	s := NewSink(SinkConfig{Putter: put, Prefix: "p", Incarnation: "i", MaxAge: time.Hour,
		Now: func() time.Time { return time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC) }})
	for _, e := range sampleRows() {
		s.Emit(e)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	var calls int
	for _, r := range put.records(t) {
		if r.Kind == KindCallEnd && r.CallID == "a1" {
			calls++
		}
	}
	if calls != 2 { // under its batch's session, and under its start's credential key
		t.Fatalf("the held analytics call must be released by Flush: %d", calls)
	}
}

// writeRaw writes rows as an observer's raw files (ndjson.gz) under dir.
func writeRaw(t *testing.T, dir string, rows []events.Event, perFile int) {
	t.Helper()
	for i := 0; i < len(rows); i += perFile {
		end := min(i+perFile, len(rows))
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		for _, r := range rows[i:end] {
			b, _ := json.Marshal(r)
			zw.Write(append(b, '\n'))
		}
		zw.Close()
		at := time.Unix(0, rows[i].TimestampNS).UTC()
		p := filepath.Join(dir, "dt="+at.Format("2006-01-02"), "hour="+at.Format("15"), "instance=pod-a",
			strings.ReplaceAll(at.Format("20060102150405.000000000"), ".", "")+"-"+string(rune('0'+i/perFile))+".ndjson.gz")
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The canon tool over the raw files a pod wrote gives exactly the records the
// live sink gave for the same rows: one code path.
func TestCanonMatchesTheLiveSink(t *testing.T) {
	rows := sampleRows()
	live := &memPut{}
	s := NewSink(SinkConfig{Putter: live, Prefix: "p", Incarnation: "i", MaxAge: time.Hour,
		Now: func() time.Time { return time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC) }})
	for _, e := range rows {
		s.Emit(e)
	}
	s.Flush()

	dir := t.TempDir()
	writeRaw(t, dir, rows, 4) // file boundaries mid-transaction
	off := &memPut{}
	st, err := CanonDir(context.Background(), dir, off, "p")
	if err != nil {
		t.Fatal(err)
	}
	if st.Rows != int64(len(rows)) || st.Records == 0 || st.Cases[CaseEndComplete] == 0 {
		t.Fatalf("stats: %+v", st)
	}
	a, b := canonicalSet(live.records(t)), canonicalSet(off.records(t))
	if strings.Join(a, "\n") != strings.Join(b, "\n") {
		t.Fatalf("canon differs from the live sink:\nlive\n%s\ncanon\n%s", strings.Join(a, "\n"), strings.Join(b, "\n"))
	}
}

// A case the code doesn't know (no atomic test) seen in real data fails the run.
func TestCanonFailsOnAnUntestedCase(t *testing.T) {
	st := Stats{Cases: map[string]int64{CaseEndComplete: 3, "made.up": 1}}
	if err := st.CheckCoverage(); err == nil || !strings.Contains(err.Error(), "made.up") {
		t.Fatalf("want an error naming the untested case, got %v", err)
	}
	st.Cases[CaseUnknownPhase] = 1
	delete(st.Cases, "made.up")
	if err := st.CheckCoverage(); err == nil {
		t.Fatal("an unknown phase in real data must fail the run")
	}
}
