package records

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

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
// live sink wrote to its raw file for the same rows: one code path.
func TestCanonMatchesTheLiveSink(t *testing.T) {
	rows := sampleRows()
	live := &memberLog{}
	s := NewSink(SinkConfig{Raw: live, Incarnation: "i", MaxAge: time.Hour,
		Now: func() time.Time { return time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC) }})
	for _, e := range rows {
		s.Emit(e)
	}
	s.Flush()

	dir := t.TempDir()
	writeRaw(t, dir, rows, 4) // file boundaries mid-transaction
	var off []Record
	st, err := CanonDirRecords(context.Background(), dir, func(rs []Record) error {
		off = append(off, rs...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.Rows != int64(len(rows)) || st.Records == 0 || st.Cases[CaseEndComplete] == 0 {
		t.Fatalf("stats: %+v", st)
	}
	got, _, err := ReadRawFile(live.buf)
	if err != nil {
		t.Fatal(err)
	}
	a, b := canonicalSet(got), canonicalSet(off)
	if strings.Join(a, "\n") != strings.Join(b, "\n") {
		t.Fatalf("canon differs from the live sink:\nlive\n%s\ncanon\n%s", strings.Join(a, "\n"), strings.Join(b, "\n"))
	}
}

// An analytics batch that's the last traffic for a while is written by the tick
// once it's older than MaxAge, not held until another row comes.
func TestTickReleasesAQuietHeldBatch(t *testing.T) {
	live := &memberLog{}
	now := t1.Add(7 * time.Second)
	s := NewSink(SinkConfig{Raw: live, Incarnation: "i", MaxAge: 10 * time.Second, Now: func() time.Time { return now }})
	for _, e := range append(analytics("a1"), clientEvent("a1", "e1", "s1", 0)) {
		s.Emit(e)
	}
	ended := func() bool {
		got, _, err := ReadRawFile(live.buf)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range got {
			if r.Kind == KindCallEnd && r.CallID == "a1" {
				return true
			}
		}
		return false
	}
	s.Tick()
	if ended() {
		t.Fatal("released before MaxAge: its batch's events may still be coming")
	}
	now = t1.Add(17 * time.Second)
	s.Tick()
	if !ended() {
		t.Fatal("the quiet batch is still held after MaxAge")
	}
}
