package state

import (
	"sort"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// How long a call may wait for its ending, in data time, before it's written
// without one: today's processor's pendingTTL. A WebSocket connection lives
// far longer.
const (
	ExpireAfter   = 6 * time.Hour
	ExpireAfterWS = 7 * 24 * time.Hour
)

// openCalls is a key's calls still waiting for their ending (a start and no
// ending; never a WebSocket turn, whose connection is what waits): each one's
// start, ordered by when it expires.
type openCalls struct {
	byDue *omap[tsID, *canon.Request]
	due   map[string]int64 // call id → when it expires
}

func newOpenCalls() *openCalls {
	return &openCalls{byDue: newOmap[tsID, *canon.Request](lessTsID), due: map[string]int64{}}
}

// set records a call's start: it expires its wait after it.
func (o *openCalls) set(c *canon.Request) {
	o.drop(c.CallID)
	wait := ExpireAfter
	if c.Transport == events.TransportWebSocket {
		wait = ExpireAfterWS
	}
	at := c.StartNS + int64(wait)
	o.due[c.CallID] = at
	o.byDue.Set(tsID{at, c.CallID}, c)
}

func (o *openCalls) drop(id string) {
	if at, ok := o.due[id]; ok {
		o.byDue.Delete(tsID{at, id})
		delete(o.due, id)
	}
}

// Deadline is the data time the key's first call still waiting expires after
// (0: none waits).
func (s *KeyState) Deadline() int64 {
	k, _, ok := s.open.byDue.First()
	if !ok {
		return 0
	}
	return k.ts
}

// Expire writes the calls whose wait passed clockNS (data time: the newest row
// anywhere) without their ending, as today's processor does: each gets an
// ending built from its start (the transaction as ingest held it,
// canon.RequestFromPending), applied as a record, so it's in the key's
// checkpoint like any other. An ending that arrives later replaces it, as if it
// had come first: one row per call, whatever the order.
func (s *KeyState) Expire(clockNS int64) Delta {
	var due []*canon.Request
	s.open.byDue.All(func(k tsID, c *canon.Request) bool {
		if k.ts >= clockNS {
			return false
		}
		due = append(due, c)
		return true
	})
	var d Delta
	for _, c := range due {
		x := *c
		r := records.Record{Version: records.Version, RecordID: "expired-" + c.CallID, Kind: records.KindCallEnd,
			CallID: c.CallID, RoutingKey: s.Key, Bucket: int32(records.BucketOf(s.Key, records.DefaultBucketBits)), Call: &x}
		got := s.Apply(r)
		d.Changes = append(d.Changes, got.Changes...)
		d.Cases = append(d.Cases, got.Cases...)
	}
	d.Changes = lastChanges(d.Changes)
	sort.Slice(d.Changes, func(i, j int) bool {
		a, b := d.Changes[i], d.Changes[j]
		return a.Table < b.Table || a.Table == b.Table && a.ID < b.ID
	})
	return d
}
