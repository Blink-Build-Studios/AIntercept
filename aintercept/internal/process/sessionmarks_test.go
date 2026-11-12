package process

import (
	"reflect"
	"testing"
)

// The session marks against hand-worked cases (the cases a port gets wrong). The
// API tests cover the lanes end to end: both timelines read these rows.

func marksOf(marks []SessionMark, kind string) []SessionMark {
	var out []SessionMark
	for _, m := range marks {
		if m.Kind == kind {
			out = append(out, m)
		}
	}
	return out
}

// State bands: a segment per run of the same mode or IP, each ending where the
// next starts and the last one open; IPv4 mapped into IPv6 isn't IPv6. Events at
// the same time order by their place in the batch, not by table order.
func TestSessionMarksSegments(t *testing.T) {
	a := ccCall("a", "s", "acct", "k", 1000*msNS)
	a.ip = "::ffff:10.0.0.1"
	b := ccCall("b", "s", "acct", "k", 2000*msNS)
	b.ip = "::ffff:10.0.0.1"
	c := ccCall("c", "s", "acct", "k", 3000*msNS)
	c.ip = "2001:db8::1"
	q := func(seq int64, ts int64, mode string) eventCols {
		e := ccEvent("b1", "s", "acct", "tengu_api_query", ts, `{"permissionMode":`+mode+`}`)
		e.Seq = seq
		return e
	}
	evs := []eventCols{
		q(3, 1000*msNS, `"plan"`), // later in the batch than the next one, at the same time
		q(2, 1000*msNS, `"default"`),
		q(4, 1500*msNS, `"plan"`),
		q(5, 1600*msNS, `""`), // no mode: skipped
		q(6, 1700*msNS, `true`),
	}
	_, _, marks := buildMarks([]rowCall{a, b, c}, evs)

	type seg struct {
		from int64
		to   *int64
		v    string
		prev *string
	}
	var perm []seg
	for _, m := range marksOf(marks, "permission") {
		perm = append(perm, seg{m.TNS, m.ToNS, *m.Mode, m.PrevMode})
	}
	want := []seg{
		{1000 * msNS, ptr(int64(1000 * msNS)), "default", nil},
		{1000 * msNS, ptr(int64(1700 * msNS)), "plan", ptr("default")},
		{1700 * msNS, nil, "True", ptr("plan")}, // the API's str() of the parsed value
	}
	if !reflect.DeepEqual(perm, want) {
		t.Errorf("permission %+v, want %+v", perm, want)
	}

	nets := marksOf(marks, "network")
	if len(nets) != 2 || *nets[0].ToNS != 3000*msNS || nets[0].IPv6 || nets[1].ToNS != nil || !nets[1].IPv6 {
		t.Errorf("network %+v", nets)
	}
}

// Coverage: every tengu_api_success is reported; one no captured call matches
// (by request id, else client request id) is missed. A session with no Claude
// Code events has neither.
func TestSessionMarksCoverage(t *testing.T) {
	a := ccCall("a", "s", "acct", "k", 1000*msNS)
	a.reqID = "req_a"
	b := ccCall("b", "s", "acct", "k", 2000*msNS)
	b.clientReqID = "cli_b"
	quiet := ccCall("q", "quiet", "acct", "k", 2000*msNS)
	evs := []eventCols{
		ccEvent("b1", "s", "acct", "tengu_api_success", 1100*msNS, `{"requestId":"req_a"}`),
		ccEvent("b1", "s", "acct", "tengu_api_success", 2100*msNS, `{"clientRequestId":"cli_b"}`),
		ccEvent("b1", "s", "acct", "tengu_api_success", 3100*msNS, `{"requestId":"req_gone"}`),
		ccEvent("b1", "s", "acct", "tengu_api_success", 4100*msNS, `{}`),
	}
	_, _, marks := buildMarks([]rowCall{a, b, quiet}, evs)
	times := func(kind string) []int64 {
		var out []int64
		for _, m := range marksOf(marks, kind) {
			out = append(out, m.TNS)
		}
		return out
	}
	if g := times("reported"); len(g) != 4 {
		t.Errorf("reported %v", g)
	}
	if g, w := times("missed"), []int64{3100 * msNS, 4100 * msNS}; !reflect.DeepEqual(g, w) {
		t.Errorf("missed %v, want %v", g, w)
	}
	for _, m := range marks {
		if m.SessionID == "quiet" && m.Kind != "call" && m.Kind != "network" {
			t.Errorf("a session with no events has a %s mark", m.Kind)
		}
	}
}

// Friction: a negative prompt carries its own number (rounded, as TRY_CAST
// rounds); a cancel and a destructive command their prompt's, by cc_prompt_id
// (the newest tengu_input_prompt's number). A category of "none" or "" isn't
// destructive.
func TestSessionMarksFriction(t *testing.T) {
	call := ccCall("a", "s", "acct", "k", 1000*msNS)
	call.prompt = "p1"
	call.class = "main"
	call.input, call.output = 10, 5
	evs := []eventCols{
		ccEvent("b1", "s", "acct", "tengu_input_prompt", 900*msNS, `{"cc_prompt_id":"p1","prompt_index":"2.6","is_negative":true}`),
		ccEvent("b1", "s", "acct", "tengu_input_prompt", 950*msNS, `{"cc_prompt_id":"p1","prompt_index":5,"is_negative":"false"}`),
		ccEvent("b1", "s", "acct", "tengu_cancel", 1200*msNS, `{"cc_prompt_id":"p1"}`),
		ccEvent("b1", "s", "acct", "tengu_bash_tool_command_executed", 1300*msNS,
			`{"cc_prompt_id":"p1","destructive_category":"git_force","destructive_target_scope":"repo"}`),
		ccEvent("b1", "s", "acct", "tengu_bash_tool_command_executed", 1400*msNS, `{"destructive_category":"none"}`),
		ccEvent("b1", "s", "acct", "tengu_bash_tool_command_executed", 1500*msNS, `{"destructive_category":""}`),
	}
	_, _, marks := buildMarks([]rowCall{call}, evs)
	type fr struct {
		kind   string
		index  *int64
		detail *string
	}
	var got []fr
	for _, m := range marksOf(marks, "friction") {
		got = append(got, fr{*m.FrictionKind, m.PromptIndex, m.Detail})
	}
	want := []fr{
		{"frustrated", ptr(int64(3)), nil},
		{"interrupted", ptr(int64(5)), nil},
		{"destructive", ptr(int64(5)), ptr("git_force · repo")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("friction %+v, want %+v", got, want)
	}
	calls := marksOf(marks, "call")
	if len(calls) != 1 || *calls[0].Context != 10 || *calls[0].Output != 5 || *calls[0].PromptIndex != 5 {
		t.Errorf("call %+v", calls)
	}
}
