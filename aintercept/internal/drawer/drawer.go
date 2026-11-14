// Package drawer renders the request drawer (docs/PIPELINE.md v7 §3.7): for
// every drawer entity a key holds, the finished /requests/{id} and /details
// objects, and each Claude Code event's /events/{id} fields. It's
// request_detail.py ported to Go, over what today's drawer loads for a call:
// its key's fact rows (requests, analytics), the call's detail object (its
// Wire, read once by the worker and kept as the key's external input), and
// what other keys hold of its analytics batch (BatchParts).
//
// The output is JSON the API validates through today's Pydantic models: a
// value's Python type is kept (an int an int, a float a float by its repr), so
// the response is the bytes today's code serializes.
package drawer

import (
	"sort"
	"strings"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream/state"
)

// BatchParts is what the other keys hold of an analytics batch (state.OtherParts).
type BatchParts = state.OtherParts

// Inputs is one key's state as the drawer reads it.
type Inputs struct {
	Key  string
	Rows state.Rows
	// Wire is a call's wire from its detail object (call id); nil where the
	// drawer builds the call from its request row (no object, or none read).
	Wire func(callID string) *Wire
	// Parts is the other keys' parts of the key's batches, by batch.
	Parts map[string]BatchParts
	// EventRows are the key's session's rendered event_rows (session.py's
	// link, summary and marker of each event the drawer lists).
	EventRows []process.EventRow
}

// Request is one drawer_requests row.
type Request struct {
	ID      string
	Data    string // the /requests/{id} object
	Details string // the /details object
}

// Event is one drawer_events row: /events/{id} but its raw row, and where the
// API finds that (the batch's detail object, the event's key).
type Event struct {
	ID            string
	Data          string
	CorrelationID string
	EventKey      string
}

func compact(v Value) string { return Dumps(v, ",", ":", -1) }

// Render is the key's drawer rows: a request row for each ended call (a
// WebSocket turn's id is <connection>~<turn>) and each of its sessions'
// reported calls, and an event row for each Claude Code event, by id.
func Render(in Inputs) ([]Request, []Event) {
	s := newScope(in.Key, in.Rows, in.Parts, in.EventRows)
	var reqs []Request
	seen := map[string]bool{}
	for _, r := range s.calls {
		id := r.id
		if seen[id] {
			continue
		}
		var w *Wire
		if !strings.Contains(id, "~") && in.Wire != nil {
			w = in.Wire(r.c.CallID)
		}
		if w == nil {
			w = s.wireFromTables(id)
		}
		if w == nil {
			continue
		}
		seen[id] = true
		c := s.detailOf(w)
		reqs = append(reqs, Request{ID: id, Data: compact(c.detail), Details: compact(s.detailsOf(c))})
	}
	var reported []string
	for _, v := range in.Rows[state.TableRequestMissed] {
		if m, ok := v.(state.MissedRow); ok && strings.HasPrefix(m.Row.RequestID, "reported:") && !seen[m.Row.RequestID] {
			seen[m.Row.RequestID] = true
			reported = append(reported, m.Row.RequestID)
		}
	}
	sort.Strings(reported)
	for _, id := range reported {
		if c := s.reportedDetail(id); c != nil {
			reqs = append(reqs, Request{ID: id, Data: compact(c.detail), Details: compact(s.detailsOf(c))})
		}
	}
	sort.Slice(reqs, func(i, j int) bool { return reqs[i].ID < reqs[j].ID })
	var evs []Event
	have := map[string]bool{}
	for _, e := range s.events {
		if have[e.id] {
			continue
		}
		have[e.id] = true
		data := obj("eventId", StrV(e.id), "t", dt(e.ts), "name", StrV(e.name), "sessionId", optS(e.session))
		evs = append(evs, Event{ID: e.id, Data: compact(data), CorrelationID: e.corr, EventKey: e.key})
	}
	sort.Slice(evs, func(i, j int) bool { return evs[i].ID < evs[j].ID })
	return reqs, evs
}

// Batches is the analytics batches a key's rows name: its events' batches and
// its analytics calls, whose other keys' parts the render reads.
func Batches(rows state.Rows) []string {
	set := map[string]bool{}
	for _, v := range rows[state.TableBatchMembers] {
		if m, ok := v.(state.BatchMember); ok {
			set[m.BatchID] = true
		}
	}
	for _, v := range rows[state.TableRequestTxs] {
		if t, ok := v.(state.ReqTx); ok && t.Kind == "analytics" {
			set[t.CallID] = true
		}
	}
	out := make([]string, 0, len(set))
	for b := range set {
		out = append(out, b)
	}
	sort.Strings(out)
	return out
}

// EndedCalls is the key's ended calls whose drawer reads their detail object
// (every one but a WebSocket turn), by call id, with when each ended (its
// start when it has no end).
func EndedCalls(rows state.Rows) map[string]int64 {
	out := map[string]int64{}
	for id, v := range rows[state.TableFactRequests] {
		c, ok := v.(canon.Request)
		if !ok || strings.Contains(id, "#") {
			continue
		}
		end := c.EndNS
		if end == 0 {
			end = c.StartNS
		}
		out[c.CallID] = end
	}
	return out
}
