package process

import (
	"sort"
	"strings"
)

// The session_marks snapshot: every session's timeline marks, one row per mark,
// as the query API derived them (claude_code.py missed_calls, friction_marks and
// prompt_index; session.py compactions, permission and networks; the session
// timeline's context lane). Built with the prompt rows, from the same session
// parts, so a compaction here is the prompt list's between row.
//
// The lanes that bin (model calls, friction) or window (not captured) are SQL
// over these rows, since the bin, the range and the person's sessions are the
// request's. What depends on now is left to the API: a segment's end is the
// next one's start, and the last one's (to_ns null) is the timeline's end.

// markEventNames is the Claude Code events the marks read: friction, prompt
// numbers and the permission mode.
var markEventNames = map[string]bool{
	"tengu_input_prompt": true, "tengu_cancel": true, "tengu_bash_tool_command_executed": true,
	"tengu_api_query": true,
}

// markEvent is one of a session's markEventNames events, with the fields the API
// read: claude_code.py Ev's (json_extract_string, TRY_CAST) and, for the
// permission mode, session.py's (_str of the parsed metadata).
type markEvent struct {
	ts, seq, rowNS int64
	corr, name     string
	md             string // kept only until its batch is numbered
	n              int    // its number in its batch: orders events at the same time
	part           string // its events part, and its place among the part's Claude Code events
	pos            int

	promptID    string // $.cc_prompt_id
	index       *int64 // $.prompt_index
	negative    bool
	destructive string // $.destructive_category, "" for none
	scope       string
	mode        *string // tengu_api_query's permissionMode
}

func newMarkEvent(e eventCols) *markEvent {
	me := &markEvent{ts: e.NS, seq: e.Seq, rowNS: e.RowNS, corr: e.CorrelationID, name: e.Name, md: e.MetadataJSON}
	switch e.Name {
	case "tengu_api_query":
		me.mode = pyStr(pyObject(e.MetadataJSON)["permissionMode"])
	case "tengu_input_prompt":
		md := jsonFields(e.MetadataJSON, "cc_prompt_id", "prompt_index", "is_negative")
		me.promptID, _ = jsonString(md["cc_prompt_id"])
		me.index = jsonInt(md["prompt_index"], -1<<31, 1<<31-1)
		neg, _ := jsonString(md["is_negative"])
		me.negative = neg == "true"
	default:
		md := jsonFields(e.MetadataJSON, "cc_prompt_id", "destructive_category", "destructive_target_scope")
		me.promptID, _ = jsonString(md["cc_prompt_id"])
		if c, ok := jsonString(md["destructive_category"]); ok && c != "none" {
			me.destructive = c
		}
		me.scope, _ = jsonString(md["destructive_target_scope"])
	}
	return me
}

func (me *markEvent) key() batchKey {
	return batchKey{me.seq, me.rowNS, me.ts, firstStr(me.name, "(unnamed)"), me.md}
}

// marks builds the session's marks. compactions is its prompt list's.
func (s *promptSession) marks(d *rowData, comps []compaction) []SessionMark {
	var out []SessionMark
	add := func(m SessionMark) {
		m.SessionID, m.Seq = s.id, int64(len(out))
		out = append(out, m)
	}

	evs := d.markEvs[s.id]
	sort.SliceStable(evs, func(i, j int) bool {
		a, b := evs[i], evs[j]
		switch {
		case a.ts != b.ts:
			return a.ts < b.ts
		case a.corr != b.corr:
			return a.corr < b.corr
		}
		return a.n < b.n
	})
	// claude_code.py prompt_index: cc_prompt_id -> its number, the newest event's.
	index := map[string]int64{}
	for _, e := range evs {
		if e.name == "tengu_input_prompt" && e.promptID != "" && e.index != nil {
			index[e.promptID] = *e.index
		}
	}
	at := func(pid string) *int64 {
		if n, ok := index[pid]; ok {
			return ptr(n)
		}
		return nil
	}

	// The visible model calls (the model-call and context lanes; the person summary's sums).
	reported := map[string]bool{}
	for _, e := range d.reports[s.id] {
		if e.reqID != "" {
			reported["r:"+e.reqID] = true
		}
		if e.clientReqID != "" {
			reported["c:"+e.clientReqID] = true
		}
	}
	for _, x := range s.steps {
		m := SessionMark{Kind: "call", TNS: x.ts, RequestID: ptr(x.id), RequestClass: nz(x.class), Model: nz(x.model),
			Context: x.context(), Output: nzi(x.output),
			Input: nzi(x.input), CacheRead: nzi(x.cacheRead), CacheWrite: nzi(x.cacheWrite), PromptID: nz(x.prompt),
			Unreported: !reported["r:"+x.reqID] && !reported["c:"+x.clientReqID]}
		if x.prompt != "" {
			m.PromptIndex = at(x.prompt)
		}
		add(m)
	}

	// Reported calls, and those that weren't captured (claude_code.py missed_calls).
	if d.eventCount[s.id] > 0 {
		rids, crids := map[string]bool{}, map[string]bool{}
		for _, x := range s.steps {
			if x.reqID != "" {
				rids[x.reqID] = true
			}
			if x.clientReqID != "" {
				crids[x.clientReqID] = true
			}
		}
		for _, e := range d.reports[s.id] {
			add(SessionMark{Kind: "reported", TNS: e.ts})
			if !rids[e.reqID] && !crids[e.clientReqID] {
				add(SessionMark{Kind: "missed", TNS: e.ts})
			}
		}
	}

	// Friction (claude_code.py friction_marks), each with its prompt's number.
	for _, e := range evs {
		m := SessionMark{Kind: "friction", TNS: e.ts}
		switch {
		case e.name == "tengu_input_prompt" && e.negative:
			m.FrictionKind, m.PromptIndex = ptr("frustrated"), e.index
		case e.name == "tengu_cancel":
			m.FrictionKind, m.PromptIndex = ptr("interrupted"), at(e.promptID)
		case e.name == "tengu_bash_tool_command_executed" && e.destructive != "":
			m.FrictionKind, m.PromptIndex = ptr("destructive"), at(e.promptID)
			var parts []string
			for _, p := range []string{e.destructive, e.scope} {
				if p != "" {
					parts = append(parts, p)
				}
			}
			m.Detail = ptr(strings.Join(parts, " · "))
		default:
			continue
		}
		add(m)
	}

	for _, c := range comps {
		add(SessionMark{Kind: "compaction", TNS: c.t, TokensFrom: c.from, TokensTo: c.to, Idle: c.idle, WaitMS: c.wait,
			RequestID: nz(c.requestID)})
	}

	// State bands: a segment per run of the same permission mode (session.py
	// permission), of the same client IP (networks). Each ends where the next
	// starts; the last one's end is the request's.
	var seg *SessionMark
	flush := func() {
		if seg != nil {
			add(*seg)
		}
	}
	for _, e := range evs {
		if e.name != "tengu_api_query" || e.mode == nil || *e.mode == "" || seg != nil && *seg.Mode == *e.mode {
			continue
		}
		var prev *string
		if seg != nil {
			seg.ToNS, prev = ptr(e.ts), seg.Mode
			flush()
		}
		seg = &SessionMark{Kind: "permission", TNS: e.ts, Mode: e.mode, PrevMode: prev}
	}
	flush()
	seg = nil
	for _, x := range s.steps {
		if x.ip == "" || seg != nil && *seg.IP == x.ip {
			continue
		}
		if seg != nil {
			seg.ToNS = ptr(x.ts)
			flush()
		}
		ip := x.ip
		v6 := strings.Contains(ip, ":") && !strings.HasPrefix(strings.ToLower(ip), "::ffff:")
		seg = &SessionMark{Kind: "network", TNS: x.ts, IP: &ip, IPv6: v6}
	}
	flush()
	return out
}
