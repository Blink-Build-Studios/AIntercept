package process

import (
	"encoding/json"
	"reflect"
	"testing"
)

// The session rows keep the API's rules exactly (claude_code.py load_sessions and
// rows.session_row); these cases are the ones a port gets wrong. The API tests
// cover the rest end to end: every session endpoint reads this table.

func buildSessions(calls []rowCall, parts [][]eventCols, sessions []Session) map[string]SessionRow {
	var aggs []*eventAgg
	for _, evs := range parts {
		part := newEventAgg()
		for _, e := range evs {
			part.add(e)
		}
		part.numberUnnamed()
		aggs = append(aggs, part)
	}
	ptrs := make([]*rowCall, len(calls))
	for i := range calls {
		ptrs[i] = &calls[i]
	}
	out := map[string]SessionRow{}
	for _, r := range newRowData(ptrs, aggs, nil).sessionRows(sessions) {
		out[r.SessionID] = r
	}
	return out
}

func TestSessionRowsClaudeCode(t *testing.T) {
	c1 := ccCall("c1", "s", "", "k", 3000*msNS)
	c1.prompt, c1.reqID, c1.ip, c1.client, c1.clientVersion = "p1", "req-1", "10.0.0.1", "cli", "2.1.0"
	c1.effort, c1.hasTools, c1.toolCalls = "low", true, 3
	c2 := ccCall("c2", "s", "acct-b", "k", 1000*msNS)
	c2.prompt, c2.device, c2.ip, c2.effort, c2.output = "p2", "dev-call", "10.0.0.2", "high", 0
	evs := []eventCols{
		ccEvent("b1", "s", "", "tengu_input_prompt", 2000*msNS, `{"cc_prompt_id":"p1","prompt_index":"3.5","is_negative":true}`),
		ccEvent("b1", "s", "", "tengu_input_prompt", 2500*msNS, `{"prompt_index":7}`), // no id: input:<ts>
		ccEvent("b1", "s", "acct-a", "tengu_api_query", 2600*msNS, `{"permissionMode":"bypassPermissions"}`),
		ccEvent("b1", "s", "acct-b", "tengu_api_query", 2700*msNS, `{"permissionMode":"default"}`),
		ccEvent("b1", "s", "", "tengu_api_query", 2800*msNS, `{"permissionMode":"default"}`),
		ccEvent("b1", "s", "", "tengu_cancel", 2900*msNS, `{}`),
		ccEvent("b1", "s", "", "tengu_bash_tool_command_executed", 3100*msNS, `{"destructive_category":"none"}`),
		ccEvent("b1", "s", "", "tengu_bash_tool_command_executed", 3200*msNS, `{"destructive_category":""}`),
		ccEvent("b1", "s", "", "tengu_bash_tool_command_executed", 3300*msNS, `{"destructive_category":"git"}`),
		ccEvent("b1", "s", "", "tengu_api_success", 3400*msNS, `{"requestId":"req-1"}`),
		ccEvent("b1", "s", "", "tengu_api_success", 3500*msNS, `{"requestId":"req-x"}`),
		ccEvent("b1", "s", "", "tengu_api_success", 3500*msNS+20*60*1000*msNS, `{"clientRequestId":"cr-y"}`),
	}
	evs[0].EnvJSON = `{"platform":"darwin","terminal":"old"}`
	evs[3].EnvJSON = `{"terminal":"iTerm","shell":null}`
	evs[5].DeviceID = "dev-cancel"                                             // the earliest of the Session's events that names a device
	other := ccEvent("b1", "s", "", "tengu_tool_use_success", 1500*msNS, `{}`) // not one of them
	other.DeviceID = "dev-other"
	evs = append(evs, other)
	r := buildSessions([]rowCall{c1, c2}, [][]eventCols{evs}, nil)["s"]

	if r.StartNS != 1000*msNS || r.LastNS != 3500*msNS+20*60*1000*msNS || r.ModelCalls != 2 || *r.Events != 13 {
		t.Errorf("times and counts: %+v", r)
	}
	if r.Agent != "claude-code" || *r.Client != "cli" || *r.ClientVersion != "2.1.0" || r.LabelText != "Session s" {
		t.Errorf("agent, client, label: %+v", r)
	}
	// p1 and p2 from the calls, input:<ts> for the prompt with no id; the first captured is #4 (3.5 rounds).
	if *r.Prompts != 3 || *r.CapturedFromPrompt != 4 {
		t.Errorf("prompts %d, captured from %d", *r.Prompts, *r.CapturedFromPrompt)
	}
	if r.Frustrated != 1 || r.Interrupted != 1 || r.Destructive != 1 || *r.BypassPct != 33.3 {
		t.Errorf("friction and bypass: %+v %v", r, *r.BypassPct)
	}
	if *r.CovReported != 3 || r.CovCaptured != 1 || r.CovNotCaptured != 2 ||
		!reflect.DeepEqual(r.CovWindowCount, []int64{1, 1}) {
		t.Errorf("coverage: %+v", r)
	}
	if r.FieldsMissing || *r.PersonID != "acct-a" || *r.DeviceID != "dev-cancel" || *r.DevicePlatform != "darwin" ||
		*r.DeviceTerminal != "iTerm" || r.DeviceShell != nil || *r.Networks != 2 {
		t.Errorf("header: %+v", r)
	}
	if *r.Effort != "low" || *r.TokensOutput != 10 || r.TokensInput != nil || r.TokensAccuracy != "estimated" || *r.ToolCalls != 3 {
		t.Errorf("usage: %+v", r)
	}
	if r.HasFacts || r.LimitsJSON != nil || r.Repo != nil {
		t.Errorf("Codex facts on a Claude Code session: %+v", r)
	}
}

// A session with only analytics: no calls, captured 0; with calls and no events,
// coverage has nothing reported; without account or device on any call, fieldsMissing.
func TestSessionRowsCoverageEdges(t *testing.T) {
	bare := ccCall("c", "calls-only", "", "k", 1000)
	rows := buildSessions([]rowCall{bare}, [][]eventCols{{ccEvent("b", "events-only", "acct", "tengu_cancel", 5000, `{}`)}}, nil)
	c, e := rows["calls-only"], rows["events-only"]
	if c.CovReported != nil || c.CovCaptured != 1 || c.Events != nil || !c.FieldsMissing || c.PersonID != nil {
		t.Errorf("calls only: %+v", c)
	}
	if *e.CovReported != 0 || e.CovCaptured != 0 || e.ModelCalls != 0 || e.FieldsMissing || e.Product != "" ||
		e.Agent != "claude-code" || e.StartNS != 5000 || e.LastNS != 5000 || e.TokensAccuracy != "missing" {
		t.Errorf("events only: %+v", e)
	}
}

// Events folded part by part give what one part gives, in either order; the
// earliest event's account wins (the API's any_value is arbitrary among several).
func TestSessionRowsMergeParts(t *testing.T) {
	a := ccEvent("b1", "s", "acct-1", "tengu_api_query", 2000, `{"permissionMode":"bypassPermissions"}`)
	a.EnvJSON = `{"platform":"linux"}`
	b := ccEvent("b2", "s", "acct-2", "tengu_api_query", 1000, `{"permissionMode":"default"}`)
	b.EnvJSON, b.DeviceID = `{"platform":"darwin"}`, "d"
	one := buildSessions(nil, [][]eventCols{{a, b}}, nil)["s"]
	two := buildSessions(nil, [][]eventCols{{a}, {b}}, nil)["s"]
	swapped := buildSessions(nil, [][]eventCols{{b}, {a}}, nil)["s"]
	if !reflect.DeepEqual(one, two) || !reflect.DeepEqual(one, swapped) {
		t.Errorf("parts differ:\n one %+v\n two %+v\n swapped %+v", one, two, swapped)
	}
	if *two.PersonID != "acct-2" || *two.DevicePlatform != "linux" || *two.BypassPct != 50 || two.StartNS != 1000 {
		t.Errorf("merged: %+v", two)
	}
}

func codexCall(id, session, account string, ts int64) rowCall {
	return rowCall{id: id, corrID: id, ts: ts, end: ts + msNS, host: "chatgpt.com", method: "POST",
		path: "/backend-api/codex/responses", status: 200, session: session, person: account, product: productCodex}
}

func TestSessionRowsCodex(t *testing.T) {
	c1 := codexCall("c1", "th", "acct", 1000)
	c1.ext = `{"workspaces":{"/home/me/repo/":{"latest_git_commit_hash":"abcdef123456"},"/other":{}}}`
	c1.sandbox = ptr("")
	c1.provider = `{"x-codex-primary-used-percent":"12.5","x-codex-primary-window-minutes":"300",` +
		`"x-codex-secondary-used-percent":"3","x-codex-secondary-window-minutes":"2880","x-codex-secondary-reset-at":"1700000000",` +
		`"x-codex-plan-type":"pro","x-codex-credits-balance":"50652.5"}`
	c2 := codexCall("c2", "th", "acct", 2000)
	c2.provider = `{"x-codex-primary-used-percent":"1","x-codex-primary-window-minutes":"0"}` // no window, no plan
	title := codexCall("t", "th", "acct", 1500)
	title.hidden, title.text = "title", ptr(" \"Fix the build\"\n")
	turn := eventCols{CorrelationID: "r", NS: 1800, Source: productCodex, Name: "codex_turn_event", SessionID: "th",
		MetadataJSON: `{"approval_policy":"never","sandbox_policy":"read-only"}`}
	r := buildSessions([]rowCall{c1, c2, title}, [][]eventCols{{turn}}, nil)["th"]
	if r.Agent != "codex" || *r.Repo != "repo" || *r.Commit != "abcdef1" || r.HiddenCalls != 1 ||
		r.LabelText != "Fix the build" || r.LabelSource != "title" || r.LabelTextAlone != "Fix the build" {
		t.Errorf("workspace and title: %+v", r)
	}
	// sandbox_mode "" is falsy: the turn's sandbox_policy shows.
	if !r.HasFacts || *r.FactsCwd != "/home/me/repo/" || *r.FactsApproval != "never" || *r.FactsSandbox != "read-only" {
		t.Errorf("facts: %+v", r)
	}
	if r.LimitsJSON != nil {
		t.Errorf("the newest response's limits (none): %s", *r.LimitsJSON)
	}
	r = buildSessions([]rowCall{c1}, nil, nil)["th"]
	want := `{"agent":"codex","plan":"pro","windows":[{"name":"5h","usedPct":12.5,"resetsAt":null},` +
		`{"name":"2d","usedPct":3,"resetsAt":"2023-11-14T22:13:20Z"}],"credits":"50,652","asOf":"1970-01-01T00:00:00.000001Z"}`
	if r.LimitsJSON == nil || *r.LimitsJSON != want {
		t.Errorf("limits:\n got %s\nwant %s", deref(r.LimitsJSON), want)
	}
}

// A title thread (a title call in a thread of its own) names the same account's
// thread that started last before it, within five minutes, when every thread
// loads together; a thread loaded alone takes the first that fits it.
func TestSessionRowsTitleThreads(t *testing.T) {
	min := int64(60_000) * msNS
	early, late := codexCall("e", "early", "acct", 0), codexCall("l", "late", "acct", 1*min)
	t1 := codexCall("t1", "title-1", "acct", 2*min)
	t1.hidden, t1.text = "title", ptr("First")
	t2 := codexCall("t2", "title-2", "acct", 3*min)
	t2.hidden, t2.text = "title", ptr("Second")
	other := codexCall("t3", "title-3", "someone-else", 2*min)
	other.hidden, other.text = "title", ptr("Not theirs")
	rows := buildSessions([]rowCall{early, late, t1, t2, other}, nil, nil)
	e, l := rows["early"], rows["late"]
	if l.LabelText != "First" || e.LabelText != "Second" {
		t.Errorf("together: early %q, late %q", e.LabelText, l.LabelText)
	}
	if e.LabelTextAlone != "First" || l.LabelTextAlone != "First" {
		t.Errorf("alone: early %q, late %q", e.LabelTextAlone, l.LabelTextAlone)
	}
	if _, ok := rows["title-1"]; ok {
		t.Error("a thread with only a hidden call has no row")
	}
}

func TestSessionRowsOriginAndChildren(t *testing.T) {
	calls := []rowCall{codexCall("p", "parent", "a", 1000), codexCall("k", "kid", "a", 2000), codexCall("r", "resumed", "a", 3000)}
	snap := []Session{
		{SessionID: "kid", FirstNS: 2000, OriginKind: "subagent", OriginSessionID: "parent", OriginTurn: 2, AgentName: "explorer", Init: "warm_fork"},
		{SessionID: "rev", FirstNS: 1500, OriginKind: "review", OriginSessionID: "parent"},
		{SessionID: "resumed", FirstNS: 3000, Init: "cold_resume"},
	}
	rows := buildSessions(calls, nil, snap)
	if p := rows["parent"]; deref(p.ChildrenJSON) != `{"subagent":1,"review":1,"sessionIds":["rev","kid"]}` || p.OriginJSON != nil {
		t.Errorf("parent: %+v", p)
	}
	if k := rows["kid"]; deref(k.OriginJSON) != `{"kind":"subagent","sessionId":"parent","turn":2,"init":"warm_fork","agentName":"explorer"}` ||
		*k.OriginKind != "subagent" || *k.OriginSessionID != "parent" {
		t.Errorf("kid: %+v", k)
	}
	if r := rows["resumed"]; deref(r.OriginJSON) != `{"kind":"resume","sessionId":null,"turn":null,"init":"cold_resume","agentName":null}` {
		t.Errorf("resumed: %s", deref(r.OriginJSON))
	}
}

func TestSessionRowsParquet(t *testing.T) {
	rows := []SessionRow{{SessionID: "s", Agent: "codex", LabelText: "x", LabelSource: "none", LabelTextAlone: "x",
		LabelSourceAlone: "none", TokensAccuracy: "missing", CovWindowFrom: []int64{1}, CovWindowTo: []int64{2},
		CovWindowCount: []int64{1}, BypassPct: ptr(0.0), Prompts: ptr(int64(0)),
		PromptIDs: []string{}, Devices: []string{}, CallAccounts: []string{}}}
	s := newMem()
	s.objs["r"] = encodeParquet(rows)
	got, err := readParquet[SessionRow](t.Context(), s, "r")
	if err != nil {
		t.Fatal(err)
	}
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(rows)
	if string(g) != string(w) {
		t.Errorf("round trip:\n got %s\nwant %s", g, w)
	}
}

func TestCredits(t *testing.T) {
	for in, want := range map[string]string{"50652.4": "50,652", "2.5": "2", "-1234567": "-1,234,567", "x": "x", "999": "999"} {
		if g := credits(in); g != want {
			t.Errorf("credits(%q) = %q, want %q", in, g, want)
		}
	}
}
