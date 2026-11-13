package records

import (
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// A call's key marks name every routing key its records went to, once each, as
// soon as the record that first goes there is made: what the request drawer
// reads to find the call's rows (calls/<id>.keys/<key>) without scanning.

// keysByCall is the routing keys each call's records went to.
func keysByCall(rs []Record) map[string][]string {
	set := map[string]map[string]bool{}
	for _, r := range rs {
		corr := corrOf(&r)
		if corr == "" {
			continue
		}
		if set[corr] == nil {
			set[corr] = map[string]bool{}
		}
		set[corr][r.RoutingKey] = true
	}
	out := map[string][]string{}
	for c, ks := range set {
		for k := range ks {
			out[c] = append(out[c], k)
		}
		sort.Strings(out[c])
	}
	return out
}

func marksByCall(ms []KeyMark) map[string][]string {
	out := map[string][]string{}
	for _, m := range ms {
		out[m.CorrelationID] = append(out[m.CorrelationID], m.Key)
	}
	for c := range out {
		sort.Strings(out[c])
	}
	return out
}

func TestKeyMarksNameEveryKeyOfACall(t *testing.T) {
	two := append(analytics("a1"), clientEvent("a1", "e1", "s1", 0), clientEvent("a1", "e2", "s2", 1))
	for name, rows := range map[string][]events.Event{
		"a call":                     {hdr("c1", true, ccHeaders), complete("c1", ccClient)},
		"a call still open":          {hdr("c1", true, ccHeaders)},
		"a websocket's turns":        {wsHdr("w1"), wsTurnStart("w1", 1, "t1"), wsTurn("w1", 1, "t1"), wsConn("w1")},
		"a batch across two sessions": two,
	} {
		t.Run(name, func(t *testing.T) {
			a := NewAssembler("inc")
			a.KeepDetail = true
			var rs []Record
			for i := range rows {
				rs = append(rs, a.Apply(&rows[i], line(rows[i]), "f")...)
			}
			rs = append(rs, a.Flush()...)
			marks := a.TakeKeyMarks()
			want, got := keysByCall(rs), marksByCall(marks)
			if len(want) == 0 {
				t.Fatal("no records")
			}
			for c, ks := range want {
				if strings.Join(got[c], ",") != strings.Join(ks, ",") {
					t.Fatalf("%s: marks %v, want %v (each key once)", c, got[c], ks)
				}
			}
			if len(got) != len(want) {
				t.Fatalf("marks for %d calls, records for %d", len(got), len(want))
			}
		})
	}
}

// A mark is made when the key is first seen: a call in flight is findable.
func TestKeyMarkMadeAtTheCallsStart(t *testing.T) {
	a := NewAssembler("inc")
	a.KeepDetail = true
	h := hdr("c1", true, ccHeaders)
	a.Apply(&h, line(h), "f")
	if ms := a.TakeKeyMarks(); len(ms) != 1 || ms[0].CorrelationID != "c1" || ms[0].Key != "s:s1" {
		t.Fatalf("marks at start: %+v", ms)
	}
	c := complete("c1", ccClient)
	a.Apply(&c, line(c), "f")
	if ms := a.TakeKeyMarks(); len(ms) != 0 {
		t.Fatalf("the same key marked twice: %+v", ms)
	}
}

func TestNoKeyMarksWithoutDetails(t *testing.T) {
	a := NewAssembler("inc")
	rows := []events.Event{hdr("c1", true, ccHeaders), complete("c1", ccClient)}
	for i := range rows {
		a.Apply(&rows[i], line(rows[i]), "f")
	}
	if ms := a.TakeKeyMarks(); len(ms) != 0 {
		t.Fatalf("marks though details are off: %+v", ms)
	}
}

// The sink writes each mark as calls/<id>.keys/<escaped key>.
func TestSinkWritesKeyMarks(t *testing.T) {
	put := &memPut{}
	s := NewSink(SinkConfig{Putter: put, Prefix: "p", Incarnation: "i", Details: true, MaxAge: time.Hour,
		Now: func() time.Time { return time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC) }})
	for _, e := range []events.Event{hdr("c1", true, ccHeaders), complete("c1", ccClient)} {
		s.Emit(e)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	want := CallKeyMark("p", "c1", "s:s1")
	if want != "p/calls/c1.keys/s:s1" {
		t.Fatalf("mark key %q", want)
	}
	if _, ok := put.objs[want]; !ok {
		t.Fatalf("no mark %s among %d objects", want, len(put.objs))
	}
	if got := CallKeyMark("p", "c1", "c:ab/cd:2026-11-12"); got != "p/calls/c1.keys/c:ab%2Fcd:2026-11-12" {
		t.Fatalf("escaped mark %q", got)
	}
}
