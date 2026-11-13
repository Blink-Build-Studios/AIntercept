package records

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// The request drawer opens one call by its id alone. Two things let it find
// the call's rows without scanning: a locator record per call and per
// reported call (routed by the drawer's own id, so its bucket is known), and a
// routing line at the end of the call's detail object naming every key its
// records went to (docs/STREAMING_PIPELINE.md §4.9).

func locators(rs []Record) []Record {
	var out []Record
	for _, r := range rs {
		if r.Kind == KindLocator {
			out = append(out, r)
		}
	}
	return out
}

func applyRecords(a *Assembler, rows []events.Event) []Record {
	var out []Record
	for i := range rows {
		out = append(out, a.Apply(&rows[i], line(rows[i]), "f")...)
	}
	return append(out, a.Flush()...)
}

// Every call's start writes one locator, l:<correlation id>, carrying the
// call's request: the key its start went to is the locator's target.
func TestEveryCallStartWritesALocator(t *testing.T) {
	a := NewAssembler("inc")
	rs := applyRecords(a, []events.Event{hdr("c1", true, ccHeaders), complete("c1", ccClient)})
	ls := locators(rs)
	if len(ls) != 1 {
		t.Fatalf("locators: %s", summary(rs))
	}
	l := ls[0]
	if l.RoutingKey != "l:c1" || l.CallID != "c1" || l.Call == nil || l.Bucket != int32(BucketOf("l:c1", DefaultBucketBits)) {
		t.Fatalf("locator %+v", l)
	}
	if got := LocatorTarget(&l); got != "s:s1" {
		t.Fatalf("target %q, want s:s1", got)
	}
}

// A WebSocket connection has one locator (its id), however many turns it has.
func TestOneLocatorPerConnection(t *testing.T) {
	a := NewAssembler("inc")
	rs := applyRecords(a, []events.Event{wsHdr("w1"), wsTurnStart("w1", 1, "t1"), wsTurn("w1", 1, "t1"),
		wsTurnStart("w1", 2, "t1"), wsTurn("w1", 2, "t1"), wsConn("w1")})
	ls := locators(rs)
	if len(ls) != 1 || ls[0].RoutingKey != "l:w1" {
		t.Fatalf("locators: %s", summary(ls))
	}
}

// A reported call (Claude Code's tengu_api_success) is opened as
// reported:<requestId | clientRequestId | ts>: its locator is routed by that
// id and points at the report's session. A report naming no session has none.
func TestReportedCallsHaveALocator(t *testing.T) {
	withRID := clientEvent("a1", "e1", "s1", 0)
	withCRID := clientEvent("a1", "e2", "s2", time.Second)
	withCRID.ClientEvent.Data = map[string]any{"additional_metadata": map[string]any{"clientRequestId": "cr_9"}}
	bare := clientEvent("a1", "e3", "s3", 2*time.Second)
	bare.ClientEvent.Data = map[string]any{"additional_metadata": map[string]any{}}
	noSession := clientEvent("a1", "e4", "", 3*time.Second)
	other := clientEvent("a1", "e5", "s1", 4*time.Second)
	other.ClientEvent.Name = "tengu_api_query"
	rows := append(analytics("a1"), withRID, withCRID, bare, noSession, other)
	a := NewAssembler("inc")
	rs := applyRecords(a, rows)
	got := map[string]string{}
	for _, l := range locators(rs) {
		if l.CallID == "a1" {
			continue // the batch request's own call locator
		}
		got[l.RoutingKey] = LocatorTarget(&l)
	}
	ns := func(d time.Duration) string {
		return "l:reported:" + strconvI(t1.Add(d).UnixNano())
	}
	want := map[string]string{"l:reported:req_1": "s:s1", "l:reported:cr_9": "s:s2", ns(2 * time.Second): "s:s3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("report locators %v, want %v", got, want)
	}
	if a.Cases()[CaseLocatorReport] != 3 || a.Cases()[CaseLocatorCall] != 1 {
		t.Fatalf("cases %v", a.Cases())
	}
}

// Locators aren't the call's records: the batch's events still rank 1..n,
// and an event's records are unchanged by its report locator.
func TestLocatorsLeaveEventsAlone(t *testing.T) {
	rows := append(analytics("a1"), clientEvent("a1", "e1", "s1", 0), clientEvent("a1", "e2", "s1", time.Second))
	a := NewAssembler("inc")
	var n []int64
	for _, r := range applyRecords(a, rows) {
		if r.Kind == KindClientEvent {
			n = append(n, r.BatchCCN)
		}
	}
	if !reflect.DeepEqual(n, []int64{1, 2}) {
		t.Fatalf("ranks %v", n)
	}
}

// A detail object names every key the call's records went to: a split call
// (its start under its credential's key, its end under its session's) both;
// an analytics batch, its request's key and every event's session; a
// WebSocket connection, its own key and its turns'. Locators aren't routing.
func TestDetailNamesItsRoutingKeys(t *testing.T) {
	split := hdr("c2", true, map[string]string{"user-agent": "claude-cli/2.1.3 (external, cli)"})
	splitEnd := complete("c2", ccClient)
	ana := append(analytics("a1"), clientEvent("a1", "e1", "s1", 0), clientEvent("a1", "e2", "s2", time.Second))
	day := time.Unix(0, at(0)).UTC().Format("2006-01-02")
	for name, c := range map[string]struct {
		rows []events.Event
		id   string
		keys []string
	}{
		"session call":    {[]events.Event{hdr("c1", true, ccHeaders), complete("c1", ccClient)}, "c1", []string{"s:s1"}},
		"split call":      {[]events.Event{split, splitEnd}, "c2", []string{"c:" + kh + ":" + day, "s:s1"}},
		"analytics batch": {ana, "a1", []string{"s:s1", "s:s2"}},
		"websocket":       {[]events.Event{wsHdr("w1"), wsTurnStart("w1", 1, "t1"), wsTurn("w1", 1, "t1"), wsConn("w1")}, "w1", []string{"c:" + kh + ":" + day, "s:t1"}},
	} {
		t.Run(name, func(t *testing.T) {
			a := NewAssembler("inc")
			a.KeepDetail = true
			ds := applyAll(a, c.rows)
			var d *Detail
			for i := range ds {
				if ds[i].CorrelationID == c.id {
					d = &ds[i]
				}
			}
			if d == nil {
				t.Fatalf("no detail for %s", c.id)
			}
			if !reflect.DeepEqual(d.RoutingKeys, c.keys) {
				t.Fatalf("routing keys %v, want %v", d.RoutingKeys, c.keys)
			}
			if len(d.Lines) != len(c.rows) {
				t.Fatalf("lines %d, want %d (the routing isn't a row)", len(d.Lines), len(c.rows))
			}
		})
	}
}

type memPutter struct {
	mu   sync.Mutex
	objs map[string][]byte
}

func (m *memPutter) Put(_ context.Context, key string, body []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.objs == nil {
		m.objs = map[string][]byte{}
	}
	m.objs[key] = append([]byte(nil), body...)
	return nil
}

// The written object is the call's rows, then one routing line:
// {"phase":"routing","correlation_id":…,"routing_keys":[…]}.
func TestDetailObjectEndsWithItsRouting(t *testing.T) {
	p := &memPutter{}
	s := NewSink(SinkConfig{Putter: p, Incarnation: "inc", Details: true, MaxAge: time.Hour})
	rows := []events.Event{hdr("c1", true, ccHeaders), complete("c1", ccClient)}
	for i := range rows {
		if err := s.Emit(rows[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	body := p.objs[DetailKey("", "c1")]
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	all, _ := io.ReadAll(zr)
	lines := strings.Split(strings.TrimRight(string(all), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines %d: %s", len(lines), all)
	}
	var r struct {
		Phase         string   `json:"phase"`
		CorrelationID string   `json:"correlation_id"`
		RoutingKeys   []string `json:"routing_keys"`
	}
	if err := json.Unmarshal([]byte(lines[2]), &r); err != nil || r.Phase != "routing" || r.CorrelationID != "c1" ||
		!reflect.DeepEqual(r.RoutingKeys, []string{"s:s1"}) {
		t.Fatalf("routing line %q (%v)", lines[2], err)
	}
}

func strconvI(n int64) string { return strconv.FormatInt(n, 10) }
