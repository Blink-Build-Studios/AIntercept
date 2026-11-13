package state

import (
	"strings"

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
)

const (
	CaseFactRequest     = "fact.request"      // a non-synthetic call's ending, under its own key
	CaseFactPending     = "fact.pending"      // a call in flight (not a WebSocket turn), its ending not yet seen
	CaseFactPendingGone = "fact.pending_gone" // a call in flight ended (its ending, or its copy)
	CaseFactEvent       = "fact.event"        // a client event, under its own key
)

func init() {
	AllCases = append(AllCases, CaseFactRequest, CaseFactPending, CaseFactPendingGone, CaseFactEvent)
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
