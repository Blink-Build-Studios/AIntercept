package process

import (
	"reflect"
	"testing"
)

// The event rows keep session.py's event_rows, state and request_rows exactly;
// these cases are the ones a port gets wrong.

func buildEvents(calls []rowCall, evs []eventCols) ([]EventRow, []SessionTool) {
	part := newEventAgg()
	for _, e := range evs {
		part.add(e)
	}
	part.numberEvents()
	part.numberUnnamed()
	ptrs := make([]*rowCall, len(calls))
	for i := range calls {
		ptrs[i] = &calls[i]
	}
	return newRowData(ptrs, []*eventAgg{part}, nil).eventRows()
}

// A row's summary is its first six scalar fields, ids aside, as Python prints them:
// booleans lowercased, floats as repr, a value cut to 40 characters; a key given
// twice keeps its first place and its last value.
func TestEventRowsSummary(t *testing.T) {
	long := `"ééééééééééééééééééééééééééééééééééééééééééé"` // 43 characters
	rows, _ := buildEvents(nil, []eventCols{
		ccEvent("b", "s", "", "tengu_a", 1, `{"requestId":"r","ok":true,"no":false,"n":null,"o":{"x":1},"l":[1],"f":1.50,`+
			`"e":1e-7,"big":12345678901234567890,"inf":1e400,"s":`+long+`,"k":1,"k2":2}`),
		ccEvent("b", "s", "", "tengu_b", 2, `{"a":1,"b":2,"a":3}`),
		ccEvent("b", "s", "", "tengu_c", 3, `[1,2]`),
	})
	want := []string{
		"ok true · no false · f 1.5 · e 1e-07 · big 12345678901234567890 · inf inf",
		"a 3 · b 2",
		"",
	}
	for i, r := range rows {
		if r.Summary != want[i] {
			t.Errorf("%s: summary %q, want %q", r.Name, r.Summary, want[i])
		}
	}
	rows, _ = buildEvents(nil, []eventCols{ccEvent("b", "s", "", "tengu_a", 1, `{"s":`+long+`}`)})
	if got := rows[0].Summary; got != "s "+string([]rune(long[1 : len(long)-1])[:40])+"…" {
		t.Errorf("a long value: %q", got)
	}
}

// What a row links to: a stall its device, then a tool use, a call (captured,
// with its step's detail, or not), a prompt (with its number), else the session.
func TestEventRowsLinks(t *testing.T) {
	call := ccCall("c1", "s", "acct", "key", 100)
	call.reqID, call.class, call.model, call.stop = "req_1", "", "claude-sonnet-4-5", ""
	evs := []eventCols{
		{CorrelationID: "b", NS: 1, Source: productClaudeCode, Name: "tengu_event_loop_stall", SessionID: "s", DeviceID: "dev", MetadataJSON: `{"toolUseID":"t"}`},
		ccEvent("b", "s", "", "tengu_tool_use_success", 2, `{"toolUseID":"","tool_use_id":"t2","requestId":"req_1"}`),
		ccEvent("b", "s", "", "tengu_tool_use_success", 3, `{"toolUseID":"t3","toolName":"Bash"}`),
		ccEvent("b", "s", "", "tengu_api_success", 4, `{"requestId":"req_1"}`),
		ccEvent("b", "s", "", "tengu_api_success", 5, `{"requestId":"req_9"}`),
		{CorrelationID: "b", NS: 6, Source: productClaudeCode, Name: "tengu_input_prompt", SessionID: "s", PromptID: "p1", MetadataJSON: `{"cc_prompt_id":"p1","prompt_index":3}`},
		{CorrelationID: "b", NS: 7, Source: productClaudeCode, Name: "tengu_x", SessionID: "s", PromptID: "p2"},
		ccEvent("b", "s", "", "tengu_y", 8, `{"requestId":true}`),
		ccEvent("b", "s", "", "tengu_z", 9, `{}`),
	}
	rows, _ := buildEvents([]rowCall{call}, evs)
	type link struct{ kind, id, label string }
	want := []link{
		{"device", "dev", "device"}, {"tool", "t2", "tool"}, {"tool", "t3", "Bash"}, {"request", "c1", "— · sonnet · —"},
		{"request", "reported:req_9", "not captured"}, {"prompt", "p1", "#3"}, {"prompt", "p2", "prompt"},
		{"request", "reported:True", "not captured"}, {"session", "s", "session"},
	}
	for i, r := range rows {
		if got := (link{r.LinkKind, r.LinkID, r.LinkLabel}); got != want[i] {
			t.Errorf("%s: link %v, want %v", r.Name, got, want[i])
		}
	}
	if rows[5].PromptIndex == nil || *rows[5].PromptIndex != 3 || rows[6].PromptIndex != nil {
		t.Errorf("prompt numbers: %v, %v", rows[5].PromptIndex, rows[6].PromptIndex)
	}
}

// Rows are in time, batch and place-in-batch order; a batch numbers its events by
// seq, row time, time, name and metadata, across sessions, identical ones apart.
func TestEventRowsOrderAndIDs(t *testing.T) {
	ev := func(corr, session, name string, ns, seq int64, md string) eventCols {
		return eventCols{CorrelationID: corr, NS: ns, Seq: seq, Source: productClaudeCode, Name: name, SessionID: session, MetadataJSON: md}
	}
	rows, _ := buildEvents(nil, []eventCols{
		ev("b2", "s", "tengu_a", 5, 0, `{}`),
		ev("b1", "s", "tengu_b", 5, 1, `{}`),
		ev("b1", "other", "tengu_a", 5, 0, `{}`),
		ev("b1", "s", "tengu_dup", 9, 2, `{}`),
		ev("b1", "s", "tengu_dup", 9, 2, `{}`),
		ev("b1", "s", "", 1, 3, `{}`),
	})
	var got []string
	for _, r := range rows {
		if r.SessionID == "s" {
			got = append(got, r.EventID+" "+r.Name)
		}
	}
	want := []string{"b1:5 (unnamed)", "b1:2 tengu_b", "b2:1 tengu_a", "b1:3 tengu_dup", "b1:4 tengu_dup"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rows %v, want %v", got, want)
	}
}

// A batch two parts hold is numbered over both (a part alone would number its
// own share from 1).
func TestEventRowsBatchAcrossParts(t *testing.T) {
	s := newMem()
	ev := func(name string, ns int64) eventCols {
		return eventCols{CorrelationID: "b", NS: ns, Source: productClaudeCode, Name: name, SessionID: "s", MetadataJSON: `{}`}
	}
	s.objs["e1"] = encodeParquet([]eventCols{ev("tengu_c", 3), ev("tengu_a", 1)})
	s.objs["e2"] = encodeParquet([]eventCols{ev("tengu_b", 2), ev("tengu_d", 4)})
	m := &Manifest{Facts: map[string][]string{"events": {"e1", "e2"}}}
	d, err := newRowParts(s).derive(t.Context(), m, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range d.EventRows {
		got = append(got, r.EventID+" "+r.Name)
	}
	if want := []string{"b:1 tengu_a", "b:2 tengu_b", "b:3 tengu_c", "b:4 tengu_d"}; !reflect.DeepEqual(got, want) {
		t.Errorf("rows %v, want %v", got, want)
	}
}

// Internals, markers, the state readout's values, and the tools the calls and
// events used (an MCP tool by its server, from the name when no server is given).
func TestEventRowsState(t *testing.T) {
	call := ccCall("c1", "s", "acct", "key", 50)
	call.ended, call.toolUses = true, []toolUse{{"github", "create_pr", "mcp"}, {"", "Read", "builtin"}, {"", "", "mcp"}}
	pending := ccCall("c2", "s", "acct", "key", 60)
	pending.toolUses = []toolUse{{"", "Write", "builtin"}} // in flight: not in the requests table
	rows, tools := buildEvents([]rowCall{call, pending}, []eventCols{
		ccEvent("b", "s", "", "tengu_tether_ping", 1, `{}`),
		ccEvent("b", "s", "", "tengu_bash_tool_command_executed", 2, `{"destructive_category":"rm_rf"}`),
		ccEvent("b", "s", "", "tengu_bash_tool_command_executed", 3, `{"destructive_category":""}`),
		ccEvent("b", "s", "", "tengu_input_prompt", 4, `{"is_negative":"true"}`),
		ccEvent("b", "s", "", "tengu_tool_use_success", 5, `{"toolName":"mcp__linear__get_issue"}`),
		ccEvent("b", "s", "", "tengu_tool_use_success", 6, `{"toolName":"mcp_tool","isMcp":true}`),
		ccEvent("b", "s", "", "tengu_tool_use_success", 7, `{"toolName":"Read"}`),
		ccEvent("b", "s", "", "tengu_tool_use_success", 8, `{"toolName":""}`),
		ccEvent("b", "s", "", "tengu_run_hook", 9, `{"hookName":"pre"}`),
		ccEvent("b", "s", "", "tengu_git_operation", 10, `{"operation":"commit"}`),
		ccEvent("b", "s", "", "tengu_session_linked_to_pr", 11, `{"prNumber":"42"}`),
		ccEvent("b", "s", "", "tengu_return_to_session", 12, `{"hadRecap":null}`),
		ccEvent("b", "s", "", "tengu_tool_search_mode_decision", 13, `{"mcpToolCount":"7.5"}`),
	})
	if !rows[0].Internal || rows[1].Internal {
		t.Errorf("internal: %v, %v", rows[0].Internal, rows[1].Internal)
	}
	if got := []string{rows[1].Marker, rows[2].Marker, rows[3].Marker}; !reflect.DeepEqual(got, []string{"destructive", "", "frustrated"}) {
		t.Errorf("markers %q", got)
	}
	if r := rows[8]; r.Hook != "pre" {
		t.Errorf("hook %q", r.Hook)
	}
	if r := rows[9]; r.GitOp != "commit" {
		t.Errorf("git %q", r.GitOp)
	}
	if r := rows[10]; r.PR == nil || *r.PR != 42 {
		t.Errorf("pr %v", r.PR)
	}
	if r := rows[11]; !r.RecapKey || r.Recap {
		t.Errorf("recap %v %v", r.RecapKey, r.Recap)
	}
	if r := rows[12]; r.Count == nil || *r.Count != 8 {
		t.Errorf("count %v", r.Count)
	}
	want := []SessionTool{
		{"s", "builtin", "", "Read", 7}, {"s", "mcp", "github", "create_pr", 50}, {"s", "mcp", "linear", "mcp__linear__get_issue", 5},
	}
	if !reflect.DeepEqual(tools, want) {
		t.Errorf("tools %+v, want %+v", tools, want)
	}
}

// Python lowers İ to i̇ and a final Σ to ς; search compares the lowered text.
func TestPyLower(t *testing.T) {
	for in, want := range map[string]string{"ToolName": "toolname", "İSTANBUL": "i̇stanbul", "ΟΔΟΣ": "οδος", "ΟΔΟΣ ΤΟΥ": "οδος του", "Σ": "σ"} {
		if got := pyLower(in); got != want {
			t.Errorf("pyLower(%q) = %q, want %q", in, got, want)
		}
	}
}

// A session's Requests tab: its own transactions over the session alone; a Codex
// thread also lists its client's calls no thread names, from its start to a
// minute after its last activity.
func TestSessionRequestRows(t *testing.T) {
	a := ccCall("a1", "s", "acct", "key", 1000*msNS)
	other := ccCall("o1", "s2", "acct", "key", 2000*msNS) // the same credential, another session
	cx := rowCall{id: "x1", corrID: "x1", ts: 1000 * msNS, end: 1100 * msNS, host: "chatgpt.com", method: "POST",
		path: "/backend-api/codex/responses", status: 200, keyHash: "kc", session: "t", product: productCodex, client: "cli"}
	usage := rowCall{id: "u1", corrID: "u1", ts: 1050 * msNS, end: 1060 * msNS, host: "chatgpt.com", method: "GET",
		path: "/backend-api/wham/usage", status: 200, keyHash: "kc", product: productCodex, client: "cli"}
	late := usage
	late.id, late.corrID, late.ts = "u2", "u2", (1000+61_000)*msNS
	otherClient := usage
	otherClient.id, otherClient.corrID, otherClient.client = "u3", "u3", "app"
	calls := []rowCall{a, other, cx, usage, late, otherClient}
	ptrs := make([]*rowCall, len(calls))
	for i := range calls {
		ptrs[i] = &calls[i]
	}
	d := newRowData(ptrs, nil, nil)
	got := map[string][]string{}
	for _, r := range d.sessionRequestRows([]SessionRow{
		{SessionID: "s", StartNS: 1000 * msNS, LastNS: 1000 * msNS, Product: productClaudeCode},
		{SessionID: "t", StartNS: 1000 * msNS, LastNS: 1000 * msNS, Product: productCodex, Client: ptr("cli")},
	}) {
		got[r.Scope] = append(got[r.Scope], r.Row.RequestID)
	}
	if want := map[string][]string{"s": {"a1"}, "t": {"x1", "u1"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("rows %v, want %v", got, want)
	}
}
