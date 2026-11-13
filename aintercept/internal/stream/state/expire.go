package state

import (
	"sort"
	"strings"
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

// expiredID prefixes the record id of an expiry's ending. Ingest's record ids
// begin with their incarnation's start time, so it's never one of theirs.
const expiredID = "expired-"

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
// anywhere) without their ending, as today's processor does: each gets the
// ending todaysRow builds from its start, applied as a record under this key,
// so it's in the key's checkpoint like any other. An ending that arrives later
// replaces it, as if it had come first: one row per call, whatever the order.
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
		x := todaysRow(c)
		r := records.Record{Version: records.Version, RecordID: expiredID + c.CallID, Kind: records.KindCallEnd,
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

// todaysRow is the row today's processor writes for a call that never ended:
// canon.RequestFromPending of the transaction ingest held when it wrote the
// call's start. That's the start without what ingest adds to it (a Codex call's
// turn identity: today's row names no session).
func todaysRow(c *canon.Request) canon.Request {
	p := canon.PendingOfStart(c)
	p.SourceFile, p.WebSocket = c.SourceFile, c.Transport == events.TransportWebSocket
	return canon.RequestFromPending(&p)
}

// own is whether a call's ending is the key's own rather than its copy under
// its start's key: its routing names the key, or it's this key's expiry of a
// call it held (whose row, like today's, may name no session where its start
// named one).
func (s *KeyState) own(r *records.Record) bool {
	return strings.HasPrefix(r.RecordID, expiredID) || records.RoutingKey(r) == s.Key
}
