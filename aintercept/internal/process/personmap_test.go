package process

import (
	"reflect"
	"testing"
)

// The person tables keep the API's rules (claude_code.py linked_accounts,
// email_groups, person_sessions; person.py header_facts; directory.py people);
// these are the cases a port gets wrong. The API tests cover the rest end to end.

func buildPeople(calls []rowCall, parts [][]eventCols, accounts []Account, ids []string) (*rowData, personTables) {
	var aggs []*eventAgg
	for _, evs := range parts {
		part := newEventAgg()
		for _, e := range evs {
			part.add(e)
		}
		aggs = append(aggs, part)
	}
	ptrs := make([]*rowCall, len(calls))
	for i := range calls {
		ptrs[i] = &calls[i]
	}
	d := newRowData(ptrs, aggs, accounts)
	return d, d.personTables(ids)
}

func TestPersonMap(t *testing.T) {
	// a and b share an email (any case); c shares it too but has no session; d is alone.
	accounts := []Account{
		{AccountID: "b", Email: "Pat@x.test"}, {AccountID: "a", Email: "pat@x.test"},
		{AccountID: "c", Email: "PAT@x.test"}, {AccountID: "d", Email: "dee@x.test"},
	}
	calls := []rowCall{ccCall("c1", "s1", "b", "k", 1000*msNS), ccCall("c2", "s3", "", "k", 1000*msNS)}
	evs := []eventCols{ccEvent("e1", "s2", "a", "tengu_api_query", 2000*msNS, `{}`), ccEvent("e2", "s4", "d", "x", 1, `{}`)}
	_, pt := buildPeople(calls, [][]eventCols{evs}, accounts, []string{"a", "b", "c", "d", unattributed})

	want := []PersonMapRow{
		{"a", "a", true}, {"a", "b", true}, {"a", "c", false}, // the row's id is the group's first account with a session
		{"b", "a", false}, {"b", "b", false}, {"b", "c", false}, // b is linked, but not a row
		{"c", "a", false}, {"c", "b", false}, {"c", "c", false},
		{"d", "d", true},
	}
	if !reflect.DeepEqual(pt.Map, want) {
		t.Errorf("person_map:\n got %v\nwant %v", pt.Map, want)
	}
	sessions := map[string][]string{}
	for _, s := range pt.Sessions {
		sessions[s.PersonID] = append(sessions[s.PersonID], s.SessionID)
	}
	if !reflect.DeepEqual(sessions, map[string][]string{
		"a": {"s1", "s2"}, "b": {"s1", "s2"}, "c": {"s1", "s2"}, "d": {"s4"}, unattributed: {"s3"},
	}) {
		t.Errorf("person_sessions: %v", sessions)
	}
}

func TestAccountFacts(t *testing.T) {
	ev := func(ts int64, org, dev, md, env string) eventCols {
		e := ccEvent("b", "s", "acct", "tengu_api_query", ts, md)
		e.OrgID, e.DeviceID, e.EnvJSON = org, dev, env
		return e
	}
	// Two parts, the later one first in time: the picks mustn't depend on the order they're read in.
	late := []eventCols{
		ev(3000, "org-late", "dev-1", `{"subscription_type":"max"}`, `{"platform":"linux","terminal":null}`),
		ev(3000, "", "dev-2", `{"subscription_type":1}`, ``), // a number reads as its text
	}
	early := []eventCols{
		ev(1000, "org-b", "dev-1", `{"subscription_type":"pro"}`, `{"platform":"darwin","terminal":"iTerm","shell":"zsh"}`),
		ev(1000, "org-a", "", `{}`, ``), // the least org at the earliest time
	}
	codex := ccCall("x1", "t", "acct", "k", 2000*msNS)
	codex.product, codex.host, codex.path, codex.method = productCodex, "chatgpt.com", "/backend-api/codex/responses", "POST"
	codex.provider = `{"x-codex-plan-type":"plus"}`
	call := ccCall("c1", "s", "acct", "k", 500)
	call.device = "dev-call"
	_, pt := buildPeople([]rowCall{codex, call}, [][]eventCols{late, early}, nil, nil)

	if len(pt.Facts) != 1 || *pt.Facts[0].Org != "org-a" || *pt.Facts[0].Plan != "max" || *pt.Facts[0].CodexPlan != "plus" {
		t.Errorf("facts: %+v", pt.Facts)
	}
	got := map[string]AccountDevice{}
	for _, d := range pt.Devices {
		got[d.DeviceID] = d
	}
	d1 := got["dev-1"]
	if !d1.OnEvents || *d1.Platform != "linux" || *d1.Terminal != "iTerm" || *d1.Shell != "zsh" {
		t.Errorf("dev-1: %+v", d1)
	}
	if d2 := got["dev-2"]; !d2.OnEvents || d2.Platform != nil {
		t.Errorf("dev-2: %+v", d2)
	}
	if dc := got["dev-call"]; dc.OnEvents || len(got) != 3 { // a call's device: the people search's, not the header's
		t.Errorf("devices: %+v", got)
	}
}

func TestSessionPersonFacts(t *testing.T) {
	c1 := ccCall("c1", "s", "acct-a", "k", 1000*msNS)
	c1.cli, c1.device = ptr("2.0.1"), "dev-a"
	c1.rl = &rateLimit{at: 5000 * msNS, status: "allowed", util: ptr(0.25)}
	c2 := ccCall("c2", "s", "acct-b", "k", 2000*msNS)
	c2.cli = ptr("") // a claude-cli user-agent with no version: the newest still counts
	c2.rl = &rateLimit{at: 4000 * msNS, status: "rejected"}
	c2.reqID = "req-2"
	evs := []eventCols{
		ccEvent("b", "s", "", "tengu_bash_tool_command_executed", 1500*msNS, `{"sandbox_enabled":true}`),
		ccEvent("b", "s", "", "tengu_bash_tool_command_executed", 1600*msNS, `{"sandbox_enabled":"false"}`),
		ccEvent("b", "s", "", "tengu_bash_tool_command_executed", 1700*msNS, `{}`), // no value: the earlier one stands
		ccEvent("b", "s", "", "tengu_api_success", 1800*msNS, `{"requestId":"req-2","gzipSkipReason":"custom_ca"}`),
		ccEvent("b", "s", "", "tengu_cancel", 1900*msNS, `{}`),
	}
	evs[4].DeviceID = "dev-e"
	r := buildSessions([]rowCall{c1, c2}, [][]eventCols{evs}, nil)["s"]
	if !reflect.DeepEqual(r.Devices, []string{"dev-a", "dev-e"}) || !reflect.DeepEqual(r.CallAccounts, []string{"acct-a", "acct-b"}) {
		t.Errorf("devices %v, accounts %v", r.Devices, r.CallAccounts)
	}
	if *r.CLIVersion != "" || *r.CLIVersionNS != 2000*msNS {
		t.Errorf("cli version %q at %d", *r.CLIVersion, *r.CLIVersionNS)
	}
	if *r.RLStatus != "allowed" || *r.RLUtil != 0.25 || r.RLOverage != nil || *r.RLNS != 5000*msNS {
		t.Errorf("rate limit: %+v", r)
	}
	if *r.Sandbox != "false" || *r.SandboxNS != 1600*msNS || !r.CustomCA || r.OutputsKnown != 2 {
		t.Errorf("sandbox %q, custom CA %v, outputs %d", *r.Sandbox, r.CustomCA, r.OutputsKnown)
	}

	// The calls' marks: c1 has no report (its cost is unknown), c2 has one.
	d, _ := buildPeople([]rowCall{c1, c2}, [][]eventCols{evs}, nil, nil)
	_, _, marks := d.promptRows()
	unreported := map[string]bool{}
	for _, m := range marks {
		if m.Kind == "call" {
			unreported[*m.RequestID] = m.Unreported
		}
	}
	if !reflect.DeepEqual(unreported, map[string]bool{"c1": true, "c2": false}) {
		t.Errorf("unreported: %v", unreported)
	}
}

func TestEventSums(t *testing.T) {
	s := newEventSums("tengu_tool_use_success", `{"toolName":"","isMcp":"true","preToolHookDurationMs":"12.5"}`)
	if *s.tool != "" || !s.mcp || *s.hookMS != 12.5 { // json_extract_string: "" is a value, not null
		t.Errorf("tool use: %+v", s)
	}
	s = newEventSums("tengu_bash_tool_command_executed",
		`{"bash_argv0":["rm"],"destructive_category":null,"destructive_target_scope":"outside_cwd"}`)
	if *s.argv0 != `["rm"]` || s.category != nil || !s.outside {
		t.Errorf("bash: %+v", s)
	}
	s = newEventSums("tengu_api_success", `{"cache_creation_5m_input_tokens":"7.6","costUSD":"x","gzipSkipReason":"custom_ca"}`)
	if *s.cache5m != 8 || s.cache1h != nil || s.costUSD != nil || !s.gzipCA {
		t.Errorf("api success: %+v", s)
	}
	if s := newEventSums("tengu_session_linked_to_pr", `{"prNumber":42}`); *s.pr != "42" {
		t.Errorf("pr: %+v", s)
	}
	if newEventSums("tengu_input_prompt", `{}`) != nil {
		t.Error("an event the summary reads nothing of has no sums")
	}
}
