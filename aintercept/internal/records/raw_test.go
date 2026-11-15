package records

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// v9's raw files (docs/PIPELINE.md §2.1): the sink writes every record and,
// for each completed call, its rows, as gzip members back to back; each
// completed call's member is readable alone by its byte range.

type memberLog struct {
	mu  sync.Mutex
	buf []byte
	n   int
}

func (m *memberLog) Write(b []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.buf = append(m.buf, b...)
	m.n++
	return nil
}

func rawSink(t *testing.T, raw *memberLog) *Sink {
	return NewSink(SinkConfig{Putter: &memPut{}, Prefix: "p", Incarnation: "pod-a-1-ab", MaxAge: time.Hour,
		KeySpoolDir: t.TempDir(), Raw: raw, Now: func() time.Time { return t1 }})
}

func TestRawFileHoldsEveryRecordTheSinkWrites(t *testing.T) {
	raw := &memberLog{}
	s := rawSink(t, raw)
	put := s.cfg.Putter.(*memPut)
	for _, e := range sampleRows() {
		if err := s.Emit(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	got, _, err := ReadRawFile(raw.buf)
	if err != nil {
		t.Fatal(err)
	}
	want := put.records(t)
	if len(got) == 0 || !reflect.DeepEqual(ids(got), ids(want)) {
		t.Fatalf("the raw file's records %v, the sink's %v", ids(got), ids(want))
	}
	// The whole file is one gzip stream to any reader.
	zr, err := gzip.NewReader(bytes.NewReader(raw.buf))
	if err != nil {
		t.Fatal(err)
	}
	all, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(all), "\n"); n < len(got) {
		t.Fatalf("whole-file read: %d lines for %d records", n, len(got))
	}
}

func TestRawMembersAreReadableByRange(t *testing.T) {
	raw := &memberLog{}
	s := rawSink(t, raw)
	rows := sampleRows()
	for _, e := range rows {
		if err := s.Emit(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	_, ranges, err := ReadRawFile(raw.buf)
	if err != nil {
		t.Fatal(err)
	}
	byCall := map[string]RawRange{}
	for _, r := range ranges {
		byCall[r.CallID] = r
	}
	// c1 (complete), c2 (a disconnect), w1 (its connection) and a1 (an analytics batch) completed.
	for _, id := range []string{"c1", "c2", "w1", "a1"} {
		r, ok := byCall[id]
		if !ok {
			t.Fatalf("no range for %s: %v", id, ranges)
		}
		zr, err := gzip.NewReader(bytes.NewReader(raw.buf[r.Offset : r.Offset+r.Len]))
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		zr.Multistream(false)
		var rowsSeen, ends int
		sc := bufio.NewScanner(zr)
		for sc.Scan() {
			l := sc.Bytes()
			if IsRawRecordLine(l) {
				var rl struct{ Record Record }
				if err := json.Unmarshal(l, &rl); err != nil {
					t.Fatal(err)
				}
				if rl.Record.CallID == id && (rl.Record.Kind == KindCallEnd || rl.Record.Kind == KindWSConnection) {
					ends++
				}
				continue
			}
			var e events.Event
			if err := json.Unmarshal(l, &e); err != nil || e.CorrelationID != id {
				t.Fatalf("%s's member has a row of %q (%v)", id, e.CorrelationID, err)
			}
			rowsSeen++
		}
		if rowsSeen == 0 || ends == 0 {
			t.Fatalf("%s's member: %d rows, %d end records", id, rowsSeen, ends)
		}
	}
	if _, ok := byCall["w1#1"]; ok {
		t.Fatal("a WebSocket turn has no member of its own")
	}
}

func TestRawKeyLayout(t *testing.T) {
	start := time.Date(2026, 11, 14, 9, 0, 0, 5, time.UTC)
	got := events.ResolveUploadKey(RawKey("pre/", "pod-a")(3, start), time.Date(2026, 11, 14, 10, 2, 0, 0, time.UTC))
	if want := "pre/raw/2026-11-14/10/pod-a/1794646800000000005-000003.ndjson.gz"; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
	if got := RawPrefix(""); got != "raw/" {
		t.Fatalf("RawPrefix: %s", got)
	}
	if pod, ok := RawPod("pre/raw/2026-11-14/10/pod-a/1-1.ndjson.gz"); !ok || pod != "pod-a" {
		t.Fatalf("RawPod: %q %v", pod, ok)
	}
}

func ids(rs []Record) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.RecordID + "/" + r.RoutingKey
	}
	sort.Strings(out)
	return out
}

// CanonDirRaw (canon -raw: a capture converted to raw files) holds exactly the
// records CanonDirRecords (canon -load) hands over, each pod in its own stream.
func TestCanonDirRawMatchesCanonDirRecords(t *testing.T) {
	rows := sampleRows()
	dir := t.TempDir()
	writeRaw(t, dir, rows, 4)
	var want []Record
	if _, err := CanonDirRecords(context.Background(), dir, func(rs []Record) error {
		want = append(want, rs...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	streams := map[string]*memberLog{}
	st, err := CanonDirRaw(context.Background(), dir, func(pod string) RawWriter {
		if streams[pod] == nil {
			streams[pod] = &memberLog{}
		}
		return streams[pod]
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []Record
	ranged := 0
	for _, m := range streams {
		rs, ranges, err := ReadRawFile(m.buf)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, rs...)
		ranged += len(ranges)
	}
	if st.Records != int64(len(want)) || len(streams) == 0 || ranged == 0 {
		t.Fatalf("records %d, want %d; %d streams, %d ranges", st.Records, len(want), len(streams), ranged)
	}
	a, b := canonicalSet(want), canonicalSet(got)
	if strings.Join(a, "\n") != strings.Join(b, "\n") {
		t.Fatalf("records differ:\nCanonDirRecords\n%s\nCanonDirRaw\n%s", strings.Join(a, "\n"), strings.Join(b, "\n"))
	}
}
