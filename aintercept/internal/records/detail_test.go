package records

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// applyAll applies rows (with their lines) and returns the details written.
func applyAll(a *Assembler, rows []events.Event) []Detail {
	for i := range rows {
		a.Apply(&rows[i], line(rows[i]), "f")
	}
	a.Flush()
	return a.TakeDetails()
}

// A call's detail object is its rows, exactly as the raw log has them, in
// order, written once when it ends: what the request drawer reads.
func TestDetailIsTheCallsRows(t *testing.T) {
	rows := append(bodiesOf("d1", `{"messages":[]}`, "data: x\n\n"), complete("d1", ccClient))
	a := NewAssembler("inc")
	a.KeepDetail = true
	got := applyAll(a, rows)
	if len(got) != 1 || got[0].CorrelationID != "d1" || len(got[0].Lines) != len(rows) {
		t.Fatalf("details: %+v", got)
	}
	for i, l := range got[0].Lines {
		if string(l) != string(line(rows[i])) {
			t.Fatalf("line %d differs", i)
		}
	}
	if k := DetailKey("p", "d1"); k != "p/calls/d1.ndjson.gz" {
		t.Fatalf("key %q", k)
	}
}

// Each way a call ends writes its detail: a disconnect, a disconnect before its
// headers, a WebSocket's close (its turns included), an analytics request (its
// events included). A call that never ends writes none.
func TestEveryEndingWritesTheDetail(t *testing.T) {
	ana := append(analytics("a1"), clientEvent("a1", "e1", "s1", 0))
	for name, c := range map[string]struct {
		rows []events.Event
		id   string
		n    int
	}{
		"disconnect":                {[]events.Event{hdr("c1", true, ccHeaders), disconnect("c1")}, "c1", 2},
		"disconnect before headers": {[]events.Event{disconnect("c1"), hdr("c1", true, ccHeaders)}, "c1", 2},
		"websocket close":           {[]events.Event{wsHdr("w1"), wsTurnStart("w1", 1, "t1"), wsTurn("w1", 1, "t1"), wsConn("w1")}, "w1", 4},
		"analytics batch":           {ana, "a1", 4},
	} {
		a := NewAssembler("inc")
		a.KeepDetail = true
		got := applyAll(a, c.rows)
		if len(got) != 1 || got[0].CorrelationID != c.id || len(got[0].Lines) != c.n {
			t.Errorf("%s: %d details, %+v", name, len(got), got)
		}
	}
	a := NewAssembler("inc")
	a.KeepDetail = true
	if got := applyAll(a, []events.Event{hdr("open", true, ccHeaders)}); len(got) != 0 {
		t.Errorf("a call still open wrote a detail: %+v", got)
	}
	b := NewAssembler("inc") // off by default
	if got := applyAll(b, []events.Event{hdr("c1", true, ccHeaders), complete("c1", ccClient)}); len(got) != 0 {
		t.Errorf("details written though not kept: %+v", got)
	}
}

// Past the cap a call's rows are kept without their bodies, so memory per call
// in flight is bounded.
func TestDetailBodiesStopAtTheCap(t *testing.T) {
	a := NewAssembler("inc")
	a.KeepDetail = true
	a.DetailCap = 200
	big := bodiesOf("d2", strings.Repeat("x", 300), "y")
	got := applyAll(a, append(big, complete("d2", ccClient)))
	if len(got) != 1 {
		t.Fatalf("details: %d", len(got))
	}
	var withBody int
	for _, l := range got[0].Lines {
		if strings.Contains(string(l), "body_b64") {
			withBody++
		}
	}
	if withBody != 0 || len(got[0].Lines) != len(big)+1 {
		t.Fatalf("rows %d, with bodies %d", len(got[0].Lines), withBody)
	}
}

// The sink writes each detail object once, gzipped, under calls/.
func TestSinkWritesDetails(t *testing.T) {
	put := &memPut{}
	s := NewSink(SinkConfig{Putter: put, Prefix: "p", Incarnation: "i", Details: true, MaxAge: time.Hour,
		Now: func() time.Time { return time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC) }})
	for _, e := range []events.Event{hdr("c1", true, ccHeaders), complete("c1", ccClient)} {
		s.Emit(e)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	b, ok := put.objs["p/calls/c1.ndjson.gz"]
	if !ok {
		t.Fatalf("no detail object among %d", len(put.objs))
	}
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(zr)
	if n := strings.Count(string(raw), "\n"); n != 2 {
		t.Fatalf("%d lines", n)
	}
}

// canon writes the same detail objects as the live sink.
func TestCanonWritesTheSameDetails(t *testing.T) {
	rows := sampleRows()
	live := &memPut{}
	s := NewSink(SinkConfig{Putter: live, Prefix: "p", Incarnation: "i", Details: true, MaxAge: time.Hour,
		Now: func() time.Time { return time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC) }})
	for _, e := range rows {
		s.Emit(e)
	}
	s.Flush()
	dir := t.TempDir()
	writeRaw(t, dir, rows, 4)
	off := &memPut{}
	if _, err := CanonDirWith(context.Background(), dir, off, "p", CanonOptions{Details: true}); err != nil {
		t.Fatal(err)
	}
	n := 0
	for k, v := range live.objs {
		if !strings.HasPrefix(k, "p/calls/") {
			continue
		}
		n++
		if !bytes.Equal(gunzip(t, off.objs[k]), gunzip(t, v)) {
			t.Errorf("%s differs", k)
		}
	}
	if n < 3 {
		t.Fatalf("%d detail objects", n)
	}
}

func gunzip(t *testing.T, b []byte) []byte {
	t.Helper()
	if b == nil {
		return nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(zr)
	return out
}

// A row after the call ended (a disconnect after its complete) isn't added to
// the object already written.
func TestLateRowsAreNotInTheDetail(t *testing.T) {
	a := NewAssembler("inc")
	a.KeepDetail = true
	got := applyAll(a, []events.Event{hdr("c1", true, ccHeaders), complete("c1", ccClient), disconnect("c1")})
	if len(got) != 1 || len(got[0].Lines) != 2 {
		t.Fatalf("details: %+v", got)
	}
	if len(a.lines) != 0 {
		t.Fatalf("late row held: %d", len(a.lines))
	}
}

// Rows of a call with no state (a turn of a connection whose upgrade wasn't
// seen) are forgotten by the sweep with everything else.
func TestSweepForgetsOrphanRows(t *testing.T) {
	a := NewAssembler("inc")
	a.KeepDetail = true
	turn := wsTurnStart("w9", 1, "t1")
	a.Apply(&turn, line(turn), "f")
	if a.lines["w9"] == nil {
		t.Fatal("the turn's row wasn't kept")
	}
	a.sweep()
	if a.lines["w9"] != nil {
		t.Fatal("orphan rows survived a sweep")
	}
}
