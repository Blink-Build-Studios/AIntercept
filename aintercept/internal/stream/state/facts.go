package state

import (
	"strings"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// The fact tables: the rows today's processor writes as requests, pending and
// analytics (canon's types, its columns), which the request drawer, /status and
// the directory's counts read. Each key stores its own: a call's ending under
// its own key (its copy under its start's key writes none), a call in flight
// under its start's key until the ending or its copy arrives there, an event
// under its own key.
const (
	TableFactRequests = "fact_requests"
	TableFactPending  = "fact_pending"
	TableFactEvents   = "fact_events"
	// TableFactProbes is a key's probe calls (synthetic, never shown) as one
	// row: what /status counts of them, as today's store reads its probes.
	TableFactProbes = "fact_probes"
)

// Probes is a key's fact_probes row (id ""): its probe calls' first time (a
// call's start, else its end) and newest (its end, else its start).
type Probes struct {
	FirstNS  int64 `parquet:"first_ns"`
	NewestNS int64 `parquet:"newest_ns"`
}

const (
	CaseFactRequest     = "fact.request"      // a non-synthetic call's ending, under its own key
	CaseFactPending     = "fact.pending"      // a call in flight (not a WebSocket turn), its ending not yet seen
	CaseFactPendingGone = "fact.pending_gone" // a call in flight ended (its ending, or its copy)
	CaseFactEvent       = "fact.event"        // a client event, under its own key
	CaseFactProbe       = "fact.probe"        // a synthetic call's ending: its key's probe times
)

func init() {
	AllCases = append(AllCases, CaseFactProbe, CaseFactRequest, CaseFactPending, CaseFactPendingGone, CaseFactEvent)
}

// facts folds a record into the fact tables.
func (s *KeyState) facts(r records.Record, d *Delta) {
	switch {
	case r.Kind == records.KindCallEnd || r.Kind == records.KindWSConnection:
		c := r.Call
		if _, ok := s.rows[TableFactPending][c.CallID]; ok {
			d.Cases = append(d.Cases, CaseFactPendingGone)
			s.rows.drop(d, TableFactPending, c.CallID)
		}
		if records.RoutingKey(&r) == s.Key && !c.Synthetic {
			d.Cases = append(d.Cases, CaseFactRequest)
			s.rows.put(d, TableFactRequests, c.CallID, *c)
		}
		if records.RoutingKey(&r) == s.Key && c.Synthetic {
			d.Cases = append(d.Cases, CaseFactProbe)
			s.probe(d, c)
		}
	case r.Kind == records.KindCallStart:
		c := r.Call
		if s.ses.ended[c.CallID] || strings.Contains(c.CallID, "#") {
			return
		}
		d.Cases = append(d.Cases, CaseFactPending)
		s.rows.put(d, TableFactPending, c.CallID, pendingOfStart(c))
	case r.Kind == records.KindClientEvent && r.Event != nil && records.RoutingKey(&r) == s.Key:
		id := r.EventKey // its record id; a record from before event keys is its own
		if id == "" {
			id = r.RecordID
		}
		d.Cases = append(d.Cases, CaseFactEvent)
		s.rows.put(d, TableFactEvents, id, *r.Event)
	}
}

// probe folds a probe call's times into the key's fact_probes row.
func (s *KeyState) probe(d *Delta, c *canon.Request) {
	first, newest := c.StartNS, c.EndNS
	if first == 0 {
		first = c.EndNS
	}
	if newest == 0 {
		newest = c.StartNS
	}
	p, _ := s.rows[TableFactProbes][""].(Probes)
	if first != 0 && (p.FirstNS == 0 || first < p.FirstNS) {
		p.FirstNS = first
	}
	if newest > p.NewestNS {
		p.NewestNS = newest
	}
	s.rows.put(d, TableFactProbes, "", p)
}
