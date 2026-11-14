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
	"slices"
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
	// Part is the other keys' parts of one of the key's batches (false: none).
	Part func(batch string) (BatchParts, bool)
	// EventRows are the key's session's rendered event_rows (session.py's
	// link, summary and marker of each event the drawer lists): read whole
	// only by a render without a memo.
	EventRows []process.EventRow
	// EventRowsChanged are the event rows that changed since the memo's
	// render, and ExternalsChanged the external inputs (WireID, PartsID ids)
	// set since: what a render with a memo reads of them.
	EventRowsChanged []process.EventRow
	ExternalsChanged []string
}

// Request is one drawer_requests row. Its /requests/{id} object (its
// stepCount null where it has a group) and its /details object are each a
// template (by id: the memo's Template holds it) and its values (template.go).
type Request struct {
	ID                      string
	DataTpl, DataVals       string
	DetailsTpl, DetailsVals string
	// StepSession and StepPrompt are the (session, prompt) whose step count is
	// its stepCount (drawer_step_counts, filled in at read); "" for none.
	StepSession, StepPrompt string
}

// StepCount is one drawer_step_counts row: a session's steps of a prompt.
type StepCount struct {
	Session, Prompt string
	N               int64
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
// reported calls, an event row for each Claude Code event, and its sessions'
// step counts by prompt, each kind by id.
func Render(in Inputs) ([]Request, []Event, []StepCount) { return RenderNext(in, nil, nil).All() }

// RenderNext is the key's drawer rows after prev's render, changed the
// state's rows changed since (a run's deltas; incremental.go): it applies them
// to prev's scope and derives again only the rows they can reach. The memo it
// returns holds every row and, with prev, Delta: what changed. With no prev it
// renders every row. prev is consumed: a memo is the next render's once.
func RenderNext(in Inputs, prev *Memo, changed Changed) *Memo {
	if prev == nil {
		return whole(in)
	}
	if prev.used || changed == nil {
		panic("drawer: a memo rendered from twice, or no changes given")
	}
	prev.used = true
	m := *prev
	m.used, m.derived, m.delta = false, 0, &Delta{}
	d := m.apply(in, changed)
	for _, id := range d.rows() {
		m.rederive(in, id)
	}
	for id := range d.events {
		old, had := m.events[id]
		es := m.scope.evByID[id]
		if len(es) == 0 {
			if had {
				delete(m.events, id)
				m.delta.GoneEvents = append(m.delta.GoneEvents, id)
			}
			continue
		}
		if row := eventRowOf(es[0]); !had || row != old {
			m.events[id] = row
			m.delta.Events = append(m.delta.Events, row)
		}
	}
	for key := range d.counts {
		sid, pk, _ := strings.Cut(key, "\x00")
		old, had := m.counts[key]
		var cur StepCount
		has := false
		if p, ok := strings.CutPrefix(pk, "s"); ok {
			if l := m.scope.sessions[sid].byPrompt[p]; len(l) > 0 {
				cur, has = StepCount{sid, p, int64(len(l))}, true
			}
		}
		switch {
		case has && (!had || old != cur):
			m.counts[key] = cur
			m.delta.Counts = append(m.delta.Counts, cur)
		case !has && had:
			delete(m.counts, key)
			m.delta.GoneCounts = append(m.delta.GoneCounts, old)
		}
	}
	m.delta.sort()
	if VerifyIncremental {
		verify(in, &m)
	}
	return &m
}

// whole is every row of the key, from a scope built whole.
func whole(in Inputs) *Memo {
	m := newMemo()
	s := newScope(in.Key, in.Rows, in.Part, in.EventRows)
	m.scope = s
	for k, v := range in.Rows[state.TableFactEvents] {
		e, ok := v.(canon.Event)
		if !ok {
			continue
		}
		f := fileEvent(&e, s.evs[k])
		if x := s.evs[k]; x != nil {
			r, ok := s.rows[x.id]
			f.id, f.rowHash = x.id, eventRowHash(r, ok)
		}
		m.evIn[k] = f
	}
	for id, r := range s.calls {
		m.callIn[id] = &filed{id: r.id}
	}
	for id, v := range in.Rows[state.TableRequestMissed] {
		if mr, ok := v.(state.MissedRow); ok {
			m.missed[id] = mr.Row.RequestID
			if strings.HasPrefix(mr.Row.RequestID, "reported:") {
				m.reported[mr.Row.RequestID]++
			}
		}
	}
	ids := make([]string, 0, len(s.drawer)+len(m.reported))
	for id := range s.drawer {
		ids = append(ids, id)
	}
	for id := range m.reported {
		if len(s.drawer[id]) == 0 {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		m.rederive(in, id)
	}
	for id, es := range s.evByID {
		m.events[id] = eventRowOf(es[0])
	}
	for sid, x := range s.sessions {
		for p, l := range x.byPrompt {
			m.counts[sid+"\x00s"+p] = StepCount{sid, p, int64(len(l))}
		}
	}
	return m
}

// rederive derives request id's row again (a call's, or a reported id's when
// no call row gives one), and records what changed in the memo's delta.
func (m *Memo) rederive(in Inputs, id string) {
	s := m.scope
	var r *memoReq
	for _, row := range s.drawer[id] {
		var w *Wire
		if !strings.Contains(id, "~") && in.Wire != nil {
			w = in.Wire(row.c.CallID)
		}
		if w == nil {
			w = s.wireFromTables(id)
		}
		if w == nil {
			continue
		}
		r = &memoReq{yield: true, call: true}
		r.deps = s.record(func() {
			c := s.detailOf(w)
			r.req = m.request(id, c.detail, s.detailsOf(c))
			if c.pos.group {
				r.req.StepSession, r.req.StepPrompt = c.pos.sess, c.pos.groupPrompt
				if strings.Count(m.tpls[r.req.DataTpl], `"stepCount":null`) != 1 {
					panic("drawer: a row's stepCount isn't its one null")
				}
			}
		})
		break
	}
	if r == nil && m.reported[id] > 0 {
		r = &memoReq{}
		r.deps = s.record(func() {
			if c := s.reportedDetail(id); c != nil {
				r.yield, r.req = true, m.request(id, c.detail, s.detailsOf(c))
			}
		})
	}
	m.derived++
	old := m.reqs[id]
	if m.delta != nil {
		switch {
		case r != nil && r.yield && (old == nil || !old.yield || old.req != r.req):
			m.delta.Requests = append(m.delta.Requests, r.req)
		case (r == nil || !r.yield) && old != nil && old.yield:
			m.delta.GoneRequests = append(m.delta.GoneRequests, id)
		}
	}
	m.keep(id, r)
}

func eventRowOf(e *ev) Event {
	data := obj("eventId", StrV(e.id), "t", dt(e.ts), "name", StrV(e.name), "sessionId", optS(e.session))
	return Event{ID: e.id, Data: compact(data), CorrelationID: e.corr, EventKey: e.key}
}

func (d *Delta) sort() {
	slices.SortFunc(d.Requests, func(a, b Request) int { return strings.Compare(a.ID, b.ID) })
	slices.Sort(d.GoneRequests)
	slices.SortFunc(d.Events, func(a, b Event) int { return strings.Compare(a.ID, b.ID) })
	slices.Sort(d.GoneEvents)
	cmp := func(a, b StepCount) int {
		if c := strings.Compare(a.Session, b.Session); c != 0 {
			return c
		}
		return strings.Compare(a.Prompt, b.Prompt)
	}
	slices.SortFunc(d.Counts, cmp)
	slices.SortFunc(d.GoneCounts, cmp)
}

// Delta is what the render changed of its prev's rows (nil: it rendered
// every row, which All holds).
func (m *Memo) Delta() *Delta { return m.delta }

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
