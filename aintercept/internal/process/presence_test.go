package process

import (
	"math"
	"reflect"
	"testing"
)

// The presence segments against hand-worked cases. The API tests cover how a
// timeline combines them across sessions, its range and now.

func buildPresence(calls []rowCall, evs []eventCols) []PresenceSegment {
	part := newEventAgg()
	for _, e := range evs {
		part.add(e)
	}
	ptrs := make([]*rowCall, len(calls))
	for i := range calls {
		ptrs[i] = &calls[i]
	}
	d := newRowData(ptrs, []*eventAgg{part}, nil)
	return d.presenceSegments(d.sessionRows(nil))
}

type seg struct {
	a, b  int64
	state string
}

func segsOf(rows []PresenceSegment, session string) []seg {
	var out []seg
	for _, r := range rows {
		if r.SessionID == session {
			out = append(out, seg{r.FromNS / msNS, r.ToNS / msNS, r.State})
		}
	}
	return out
}

// A session's open span is cut by its merged blur windows (clipped to the span);
// its sleep windows are kept merged, wherever they fall. A zero, negative or
// missing duration is no window, and only the events the API loads count.
func TestPresenceSegments(t *testing.T) {
	ev := func(ts int64, name, md string) eventCols { return ccEvent("b1", "s", "acct", name, ts*msNS, md) }
	evs := []eventCols{
		ev(1000, "tengu_input_prompt", `{}`),
		ev(3000, "tengu_return_to_session", `{"blurDurationMs":1000}`),   // away 2000-3000
		ev(3500, "tengu_return_to_session", `{"blurDurationMs":"1000"}`), // 2500-3500: joins it
		ev(5000, "tengu_return_to_session", `{"blurDurationMs":0}`),
		ev(5100, "tengu_return_to_session", `{"blurDurationMs":-50}`),
		ev(5200, "tengu_some_other_event", `{"blurDurationMs":100}`),                     // not an event the API loads
		ev(1200, "tengu_return_to_session", `{"blurDurationMs":900}`),                    // 300-1200: clipped to 1000-1200
		ev(6000, "tengu_event_loop_stall", `{"likely_sleep":true,"clock_jump_ms":5500}`), // 500-6000
		ev(6100, "tengu_event_loop_stall", `{"likely_sleep":false,"clock_jump_ms":100}`),
		ev(6200, "tengu_event_loop_stall", `{"likely_sleep":"true","clock_jump_ms":"100.4"}`), // 6100-6200
		ev(7000, "tengu_api_success", `{}`),
	}
	got := segsOf(buildPresence(nil, evs), "s")
	want := []seg{
		{1000, 1200, "away"},
		{1200, 2000, "focused"},
		{2000, 3500, "away"},
		{3500, 7000, "focused"},
		{500, 6000, "asleep"},
		{6100, 6200, "asleep"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// One event, or one call: an open span of no length, so nothing until the API
// keeps a live session open to now. A sleep that reaches before the session's
// start is still the session's (it belongs to the device).
func TestPresenceOfAMomentarySession(t *testing.T) {
	evs := []eventCols{ccEvent("b1", "s", "acct", "tengu_event_loop_stall", 9000*msNS, `{"likely_sleep":true,"clock_jump_ms":4000}`)}
	got := segsOf(buildPresence([]rowCall{ccCall("c", "t", "acct", "k", 100*msNS)}, evs), "s")
	if want := []seg{{5000, 9000, "asleep"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := segsOf(buildPresence([]rowCall{ccCall("c", "t", "acct", "k", 100*msNS)}, nil), "t"); got != nil {
		t.Errorf("a one-call session: %v", got)
	}
}

func TestMsBefore(t *testing.T) {
	if got := msBefore(5*msNS, 2); got != 3*msNS {
		t.Errorf("msBefore(5ms, 2) = %d", got)
	}
	if got := msBefore(0, math.MaxInt64/1000); got != math.MinInt64 {
		t.Errorf("out of range: %d", got)
	}
	if got := msBefore(math.MinInt64+5*msNS, 5); got != math.MinInt64 {
		t.Errorf("at the edge: %d", got)
	}
}
