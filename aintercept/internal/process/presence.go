package process

import (
	"math"
	"sort"
)

// The presence_segments snapshot: each session's presence inputs as segments
// (claude_code.py presence). Focus is per session: its open span, start to last,
// cut into focused and away by its tengu_return_to_session blur windows. Sleep is
// per device, so a session's sleep windows (tengu_event_loop_stall likely_sleep)
// are rows of their own, wherever they fall.
//
// A person's presence is these rows of the sessions a request shows, combined in
// SQL: asleep where any sleep row covers, else focused where any open session is
// focused, else away where one is open. Which sessions are shown and which are
// live are the request's (its window and now), so the combining is too. A live
// session stays open, focused, from last to now: no blur window ends after its
// last event.

// presenceEventNames is the events claude_code.py loads (_EVENT_NAMES); blur and
// sleep are read from any of them.
var presenceEventNames = map[string]bool{
	"tengu_api_success": true, "tengu_api_query": true, "tengu_input_prompt": true, "tengu_cancel": true,
	"tengu_bash_tool_command_executed": true, "tengu_return_to_session": true, "tengu_event_loop_stall": true,
}

// span is a half-open window [a, b).
type span struct{ a, b int64 }

type presenceEvs struct{ blur, sleep []span }

func (a *eventAgg) presenceOf(s string) *presenceEvs {
	x := a.presence[s]
	if x == nil {
		x = &presenceEvs{}
		a.presence[s] = x
	}
	return x
}

// addPresence keeps an event's blur and sleep windows: blurDurationMs, and
// clock_jump_ms when likely_sleep, each ending at the event (TRY_CAST AS BIGINT;
// zero or none is no window). A window that ends before it starts covers nothing.
func (a *eventAgg) addPresence(s string, e eventCols) {
	md := jsonFields(e.MetadataJSON, "blurDurationMs", "likely_sleep", "clock_jump_ms")
	if ms := jsonInt(md["blurDurationMs"], math.MinInt64, math.MaxInt64); ms != nil && *ms > 0 {
		a.presenceOf(s).blur = append(a.presenceOf(s).blur, span{msBefore(e.NS, *ms), e.NS})
	}
	if v, ok := jsonString(md["likely_sleep"]); ok && v == "true" {
		if ms := jsonInt(md["clock_jump_ms"], math.MinInt64, math.MaxInt64); ms != nil && *ms > 0 {
			a.presenceOf(s).sleep = append(a.presenceOf(s).sleep, span{msBefore(e.NS, *ms), e.NS})
		}
	}
}

// msBefore is ts - ms milliseconds, the earliest time when that's out of range.
func msBefore(ts, ms int64) int64 {
	if uint64(ms) > (uint64(ts)+1<<63)/1_000_000 { // ts - MinInt64, as unsigned
		return math.MinInt64
	}
	return ts - ms*1_000_000
}

// mergeSpans is claude_code.py _merge: sorted, with overlapping or touching
// windows joined.
func mergeSpans(in []span) []span {
	s := append([]span(nil), in...)
	sort.Slice(s, func(i, j int) bool { return s[i].a < s[j].a || s[i].a == s[j].a && s[i].b < s[j].b })
	var out []span
	for _, x := range s {
		if n := len(out); n > 0 && x.a <= out[n-1].b {
			out[n-1].b = max(out[n-1].b, x.b)
		} else {
			out = append(out, x)
		}
	}
	return out
}

// presenceRows is the session's segments: its open span, start to last, cut
// into focused and away, then its sleep windows.
func (d *rowData) presenceRows(r SessionRow) []PresenceSegment {
	var out []PresenceSegment
	add := func(a, b int64, state string) {
		out = append(out, PresenceSegment{SessionID: r.SessionID, Seq: int64(len(out)), FromNS: a, ToNS: b, State: state})
	}
	x := d.presence[r.SessionID]
	if x == nil {
		x = &presenceEvs{}
	}
	at := r.StartNS
	for _, w := range mergeSpans(x.blur) {
		a, b := max(w.a, r.StartNS), min(w.b, r.LastNS)
		if a >= b {
			continue
		}
		if a > at {
			add(at, a, "focused")
		}
		add(a, b, "away")
		at = b
	}
	if at < r.LastNS {
		add(at, r.LastNS, "focused")
	}
	for _, w := range mergeSpans(x.sleep) {
		add(w.a, w.b, "asleep")
	}
	return out
}

// presenceSegments is every session's presence rows, by session.
func (d *rowData) presenceSegments(rows []SessionRow) []PresenceSegment {
	var out []PresenceSegment
	for _, r := range rows {
		out = append(out, d.presenceRows(r)...)
	}
	return out
}

// EventWindows is an event's blur and sleep windows as the presence rows read
// them (addPresence), for the streaming processor: one parsing, so the two
// can't drift. ok is false when the event has none.
func EventWindows(name, metadataJSON string, ns int64) (blur, sleep [2]int64, blurOK, sleepOK bool) {
	if !presenceEventNames[name] {
		return
	}
	a := newEventAgg()
	a.addPresence("s", eventCols{MetadataJSON: metadataJSON, NS: ns})
	if x := a.presence["s"]; x != nil {
		if len(x.blur) > 0 {
			blur, blurOK = [2]int64{x.blur[0].a, x.blur[0].b}, true
		}
		if len(x.sleep) > 0 {
			sleep, sleepOK = [2]int64{x.sleep[0].a, x.sleep[0].b}, true
		}
	}
	return
}
