package state

import (
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
)

// SessionTables is one session's finished API-shaped rows, rendered from its
// key's rows by the query side (query.go): what the session's pages read, each
// table in today's columns, order and seq. It's whole-session: every value
// that crosses hours or prompts (link labels, prompt numbers and Codex turn
// ranks, permission and network to_ns, open_tail, events_pending_if_live) is
// computed over the whole session. How the rows are stored for the API is a
// later step (docs/PIPELINE.md §3).
//
// What the session's own key can't know isn't here: children_json (its
// children's keys), a Codex thread's label from a title thread (another key:
// session_rows carries the session's own label), and a Codex thread's client
// calls no thread names (the credential's c: key).
type SessionTables struct {
	SessionRows        []process.SessionRow // its own row
	PresenceSegments   []process.PresenceSegment
	SessionTools       []process.SessionTool
	EventRows          []process.EventRow
	SessionMarks       []process.SessionMark
	PromptRows         []process.PromptRow
	PromptSteps        []process.PromptStep
	SessionRequestRows []process.SessionRequestRow // its own
	CallToolUses       []process.CallToolUse
}

// OtherParts is what the other keys hold of an analytics batch: their
// batch_parts summed (the count, the least non-empty name, the earliest
// (ns, session) that has a session). The render reads them (docs/PIPELINE.md
// v7 §3.7) so a batch spanning sessions counts the whole batch.
type OtherParts struct {
	N            int64
	Name         string
	HasName      bool
	FirstNS      int64
	FirstSession string
	HasSession   bool
}

// Add folds one key's part into the sum.
func (p *OtherParts) Add(n int64, name *string, firstNS *int64, firstSession *string) {
	p.N += n
	if name != nil && *name != "" && (!p.HasName || *name < p.Name) {
		p.Name, p.HasName = *name, true
	}
	if firstNS != nil && firstSession != nil &&
		(!p.HasSession || *firstNS < p.FirstNS || *firstNS == p.FirstNS && *firstSession < p.FirstSession) {
		p.FirstNS, p.FirstSession, p.HasSession = *firstNS, *firstSession, true
	}
}

// RenderSession is session sid's tables from its key's rows (a session key's
// KeyState.Rows), its batches as its own key holds them.
func RenderSession(sid string, rows Rows) *SessionTables { return RenderSessionWith(sid, rows, nil) }

// RenderSessionWith is RenderSession with what other keys hold of its batches
// (by batch): an analytics batch's count, name and session are every key's.
func RenderSessionWith(sid string, rows Rows, others map[string]OtherParts) *SessionTables {
	own := func(s string) bool { return s == sid }
	prompts, steps, marks := PromptViewOf(rows)
	return &SessionTables{
		SessionRows:        filter(SessionRowsOf(rows), func(r process.SessionRow) bool { return own(r.SessionID) }),
		PresenceSegments:   filter(PresenceSegmentsOf(rows), func(r process.PresenceSegment) bool { return own(r.SessionID) }),
		SessionTools:       filter(SessionToolsOf(rows), func(r process.SessionTool) bool { return own(r.SessionID) }),
		EventRows:          filter(EventRowsOf(rows), func(r process.EventRow) bool { return own(r.SessionID) }),
		SessionMarks:       filter(marks, func(r process.SessionMark) bool { return own(r.SessionID) }),
		PromptRows:         filter(prompts, func(r process.PromptRow) bool { return own(r.SessionID) }),
		PromptSteps:        filter(steps, func(r process.PromptStep) bool { return own(r.SessionID) }),
		SessionRequestRows: viewOf(rows, nil).withOthers(others).ownSessionRows(sid),
		CallToolUses:       filter(CallToolUsesOf(rows), func(r process.CallToolUse) bool { return own(r.SessionID) }),
	}
}

// Tables is each table's rows by its API name.
func (t *SessionTables) Tables() map[string]any {
	return map[string]any{
		"session_rows":         t.SessionRows,
		"presence_segments":    t.PresenceSegments,
		"session_tools":        t.SessionTools,
		"event_rows":           t.EventRows,
		"session_marks":        t.SessionMarks,
		"prompt_rows":          t.PromptRows,
		"prompt_steps":         t.PromptSteps,
		"session_request_rows": t.SessionRequestRows,
		"call_tool_uses":       t.CallToolUses,
	}
}

// ownSessionRows is session sid's Requests tab rows of its own: its
// transactions and not-captured calls, in today's order (seq from 0). A Codex
// thread's unthreaded client calls aren't its own (the credential's c: key).
func (v *rqView) ownSessionRows(sid string) []process.SessionRequestRow {
	var kept []process.RequestRow
	for _, x := range v.scopeRows([]string{sid}, sid) {
		if x.SessionID != nil && *x.SessionID == sid {
			kept = append(kept, x)
		}
	}
	sortRows(kept)
	out := make([]process.SessionRequestRow, len(kept))
	for i, x := range kept {
		out[i] = process.SessionRequestRow{Scope: sid, Seq: int64(i), Row: x}
	}
	return out
}

func filter[T any](xs []T, keep func(T) bool) []T {
	var out []T
	for _, x := range xs {
		if keep(x) {
			out = append(out, x)
		}
	}
	return out
}

// withOthers folds the other keys' parts into the view's batches.
func (v *rqView) withOthers(others map[string]OtherParts) *rqView {
	for id, o := range others {
		b := v.batches[id]
		if b == nil {
			b = &rqBatch{}
			v.batches[id] = b
		}
		b.n += o.N
		if o.HasName && (!b.hasName || o.Name < b.name) {
			b.name, b.hasName = o.Name, true
		}
		if o.HasSession && (!b.hasSess || o.FirstNS < b.sessionNS || o.FirstNS == b.sessionNS && o.FirstSession < b.session) {
			b.session, b.sessionNS, b.hasSess = o.FirstSession, o.FirstNS, true
		}
	}
	return v
}
