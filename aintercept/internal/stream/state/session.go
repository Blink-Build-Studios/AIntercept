package state

import (
	"strings"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// tsID orders by time, then id: every tie in this package breaks this way.
type tsID struct {
	ts int64
	id string
}

func lessTsID(a, b tsID) bool { return a.ts < b.ts || a.ts == b.ts && a.id < b.id }

// session is the inputs every session table shares: the times its span is made
// of, and its presence windows. A session's span runs from its first to its last
// visible model call or Claude Code event (process sessionRow).
type session struct {
	// span holds each visible call's time (by call id) and each Claude Code
	// event's (by event key). A call in flight can count and then not (a hidden
	// call is visible until its ending says it's hidden), so it's a set, not a
	// running min and max.
	span   *omap[tsID, bool]
	callAt map[string]int64 // a visible call's time in span, by call id
	ended  map[string]bool  // calls whose ending was applied: a start can't count again
	blur   *spans
	sleep  *spans
	// before is the span as the presence rows were last written (exists, first, last).
	before spanOf
}

type spanOf struct {
	ok          bool
	first, last int64
}

func newSession() *session {
	return &session{span: newOmap[tsID, bool](lessTsID), callAt: map[string]int64{}, ended: map[string]bool{},
		blur: newSpans(), sleep: newSpans()}
}

// spanNow is the span as it stands.
func (s *session) spanNow() spanOf {
	if s.span.Len() == 0 {
		return spanOf{}
	}
	var first, last int64
	s.span.All(func(k tsID, _ bool) bool { first = k.ts; return false })
	last = s.lastTs()
	return spanOf{true, first, last}
}

func (s *session) lastTs() int64 {
	c := s.span.chunks[len(s.span.chunks)-1].e
	return c[len(c)-1].k.ts
}

// changed is what one record changed in the session's inputs.
type changed struct {
	blur      []Span // merged blur windows a record made or grew
	sleep     *Span  // the merged sleep window a record made or grew
	sleepGone []Span // the sleep windows that merged into it
}

func (s *session) apply(r records.Record, d *Delta) changed {
	var ch changed
	switch {
	case r.Kind == records.KindClientEvent && r.Event != nil:
		e := r.Event
		if e.Source != canon.ProductClaudeCode || e.SessionID == "" {
			d.Cases = append(d.Cases, CaseEventNotSession)
			return ch
		}
		s.span.Set(tsID{e.NS, "e:" + e.EventKey}, true)
		d.Cases = append(d.Cases, CaseEventSpan)
		blur, sleep, blurOK, sleepOK := process.EventWindows(e.Name, e.MetadataJSON, e.NS)
		if blurOK {
			d.Cases = append(d.Cases, CaseEventBlur)
			if m, ok := s.blur.add(Span{blur[0], blur[1]}); ok {
				ch.blur = append(ch.blur, m)
			}
		}
		if sleepOK {
			d.Cases = append(d.Cases, CaseEventSleep)
			ch.sleepGone, ch.sleep = s.sleep.addTrack(Span{sleep[0], sleep[1]})
		}
	case r.Kind == records.KindCallEnd || r.Kind == records.KindWSConnection:
		c := r.Call
		s.ended[c.CallID] = true
		s.unspanCall(c.CallID)
		if visibleEnded(c) {
			d.Cases = append(d.Cases, CaseCallVisible)
			s.spanCall(c.CallID, firstNZ(c.StartNS, c.EndNS))
		} else {
			d.Cases = append(d.Cases, CaseCallNotVisible)
		}
	case r.Kind == records.KindCallStart:
		c := r.Call
		switch {
		case s.ended[c.CallID]:
			d.Cases = append(d.Cases, CaseStartAfterEnd)
		case visibleStart(c):
			d.Cases = append(d.Cases, CaseStartVisible)
			s.spanCall(c.CallID, c.StartNS)
		default:
			d.Cases = append(d.Cases, CaseStartNotVisible)
		}
	default:
		d.Cases = append(d.Cases, CaseOtherRecord)
	}
	return ch
}

func (s *session) spanCall(id string, ts int64) {
	s.callAt[id] = ts
	s.span.Set(tsID{ts, "c:" + id}, true)
}

func (s *session) unspanCall(id string) {
	if ts, ok := s.callAt[id]; ok {
		s.span.Delete(tsID{ts, "c:" + id})
		delete(s.callAt, id)
	}
}

func firstNZ(a, b int64) int64 {
	if a != 0 {
		return a
	}
	return b
}

// agentCall is process rowCall.agentCall: a coding agent's model call with a session.
func agentCall(product, host, method, path, session string, wsTurn int64, synthetic bool) bool {
	if synthetic || session == "" {
		return false
	}
	switch product {
	case canon.ProductClaudeCode:
		return host == "api.anthropic.com" && strings.HasPrefix(path, "/v1/messages") &&
			!strings.HasPrefix(path, "/v1/messages/count_tokens")
	case canon.ProductCodex:
		return host == "chatgpt.com" && path == "/backend-api/codex/responses" && (wsTurn != 0 || method == "POST")
	}
	return false
}

// visibleEnded is an ended call the session counts: a visible model call
// (process ccCall).
func visibleEnded(c *canon.Request) bool {
	return c.HiddenKind == "" && agentCall(c.Product, c.Host, c.Method, c.Path, c.SessionID, c.WSTurn, c.Synthetic)
}

// visibleStart is a call in flight the session counts: as process callFromPending
// reads one, a Claude Code model call with its session header. A WebSocket turn
// has no in-flight row there (the connection is in flight, not the turn).
func visibleStart(c *canon.Request) bool {
	if strings.Contains(c.CallID, "#") {
		return false
	}
	product := ""
	if c.Host == "api.anthropic.com" {
		product = canon.ProductClaudeCode
	}
	return agentCall(product, c.Host, c.Method, c.Path, c.SessionID, 0, c.Synthetic)
}

type sessionSnap struct {
	Span   []SpanEntry
	CallAt map[string]int64
	Ended  []string
	Blur   []Span
	Sleep  []Span
}

// SpanEntry is one time in a session's span, for Encode.
type SpanEntry struct {
	TS int64
	ID string
}

func (s *session) snap() sessionSnap {
	x := sessionSnap{CallAt: s.callAt, Blur: s.blur.list(), Sleep: s.sleep.list()}
	s.span.All(func(k tsID, _ bool) bool { x.Span = append(x.Span, SpanEntry{k.ts, k.id}); return true })
	for id := range s.ended {
		x.Ended = append(x.Ended, id)
	}
	return x
}

func sessionFrom(x sessionSnap) *session {
	s := newSession()
	for _, e := range x.Span {
		s.span.Set(tsID{e.TS, e.ID}, true)
	}
	for id, ts := range x.CallAt {
		s.callAt[id] = ts
	}
	for _, id := range x.Ended {
		s.ended[id] = true
	}
	for _, w := range x.Blur {
		s.blur.add(w)
	}
	for _, w := range x.Sleep {
		s.sleep.add(w)
	}
	return s
}
