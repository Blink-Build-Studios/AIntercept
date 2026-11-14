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
	Data    string // the /requests/{id} object; its stepCount null where it has a group
	Details string // the /details object
	// StepSession and StepPrompt are the (session, prompt) whose step count is
	// its stepCount (drawer_step_counts, filled in at read); "" for none.
	StepSession, StepPrompt string
}

// StepCount is one drawer_step_counts row: a session's steps of a prompt.
type StepCount struct {
	Session, Prompt string
	N               int64
}

// StepCounts is the key's sessions' step counts by prompt (the drawer's
// stepCount), from the memo a render returned.
func (m *Memo) StepCounts() []StepCount { return m.counts }

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
	reqs, evs, _ := RenderNext(in, nil, nil)
	return reqs, evs
}

// RenderNext is Render, deriving again only the rows whose inputs can have
// changed since prev's render, changed the state's rows changed since
// (incremental.go); every other row is prev's. With no prev (or no changed)
// it derives every row. It returns the memo for the next render, and consumes
// prev: a memo is never used twice.
func RenderNext(in Inputs, prev *Memo, changed Changed) ([]Request, []Event, *Memo) {
	if prev != nil && (prev.used || changed == nil) {
		prev = nil
	}
	if prev != nil {
		prev.used = true
	}
	s := newScope(in.Key, in.Rows, in.Parts, in.EventRows)
	next := newMemo()
	if prev != nil {
		for _, e := range s.events {
			if f := prev.md[e.rowKey]; f != nil && f.json == e.mdJSON {
				e.fx = f
			}
		}
	}
	d := s.inputs(in, prev, changed, next)
	var reqs []Request
	seen := map[string]bool{}
	for _, r := range s.calls {
		id := r.id
		if seen[id] {
			continue
		}
		if d != nil && !d.own[id] {
			m := prev.reqs[id]
			if m == nil {
				continue // it gave no row, and nothing of its own changed
			}
			if !d.hits(m.deps) {
				seen[id] = true
				next.reqs[id] = m
				reqs = append(reqs, m.req)
				continue
			}
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
		m := &memoReq{yield: true}
		m.deps = s.record(func() {
			c := s.detailOf(w)
			m.req = Request{ID: id, Data: compact(c.detail), Details: compact(s.detailsOf(c))}
			if c.pos.group {
				m.req.StepSession, m.req.StepPrompt = c.pos.sess, c.pos.groupPrompt
				if strings.Count(m.req.Data, `"stepCount":null`) != 1 {
					panic("drawer: a row's stepCount isn't its one null")
				}
			}
		})
		next.reqs[id] = m
		next.derived++
		reqs = append(reqs, m.req)
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
		m := prev.reqOf(id, d)
		if m == nil {
			m = &memoReq{}
			next.derived++
			m.deps = s.record(func() {
				if c := s.reportedDetail(id); c != nil {
					m.yield, m.req = true, Request{ID: id, Data: compact(c.detail), Details: compact(s.detailsOf(c))}
				}
			})
		}
		next.reqs[id] = m
		if m.yield {
			reqs = append(reqs, m.req)
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
		row, ok := Event{}, false
		if d != nil && !d.events[e.id] {
			row, ok = prev.events[e.id]
		}
		if !ok {
			data := obj("eventId", StrV(e.id), "t", dt(e.ts), "name", StrV(e.name), "sessionId", optS(e.session))
			row = Event{ID: e.id, Data: compact(data), CorrelationID: e.corr, EventKey: e.key}
		}
		next.events[e.id] = row
		evs = append(evs, row)
	}
	sort.Slice(evs, func(i, j int) bool { return evs[i].ID < evs[j].ID })
	for _, e := range s.events {
		if e.fx != nil {
			next.md[e.rowKey] = e.fx
		}
	}
	for sid := range next.exists {
		x := s.session(sid)
		for p, steps := range x.byPrompt {
			next.counts = append(next.counts, StepCount{sid, p, int64(len(steps))})
		}
	}
	sort.Slice(next.counts, func(i, j int) bool {
		a, b := next.counts[i], next.counts[j]
		return a.Session < b.Session || a.Session == b.Session && a.Prompt < b.Prompt
	})
	if VerifyIncremental && d != nil {
		verify(in, reqs, evs)
	}
	return reqs, evs, next
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
