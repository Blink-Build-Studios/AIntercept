package state

import (
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
	_, _ = canon.Request{}, r
}
