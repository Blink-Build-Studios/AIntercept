package process

import (
	"encoding/json"
	"reflect"
	"testing"
)

// The prompt rows keep the API's rules exactly (session.py items, typed_prompts);
// these cases are the ones a port gets wrong. The API tests cover the rest end to
// end: every session endpoint reads these tables.

func buildPrompts(calls []rowCall, evs []eventCols) ([]PromptRow, []PromptStep) {
	rows, steps, _ := buildMarks(calls, evs)
	return rows, steps
}

func buildMarks(calls []rowCall, evs []eventCols) ([]PromptRow, []PromptStep, []SessionMark) {
	part := newEventAgg()
	for _, e := range evs {
		part.add(e)
	}
	part.numberUnnamed()
	ptrs := make([]*rowCall, len(calls))
	for i := range calls {
		ptrs[i] = &calls[i]
	}
	return newRowData(ptrs, []*eventAgg{part}, nil).promptRows()
}

func pev(corr, session, prompt, name string, ts int64, md string) eventCols {
	e := ccEvent(corr, session, "acct", name, ts, md)
	e.PromptID = prompt
	return e
}

func itemKeys(rows []PromptRow) []string {
	return mapf(rows, func(r PromptRow) string {
		if r.Type == "prompt" {
			return "prompt:" + *r.PromptID
		}
		return *r.BetweenKind + ":" + deref(r.RequestID)
	})
}

// Items in session.py items()'s order: prompts by start (a prompt seen only on
// calls starts at its first call), then between rows; at the same time a
// between row comes first, and ties keep prompts, compactions, recaps, calls.
func TestPromptRowsOrder(t *testing.T) {
	c1 := ccCall("c1", "s", "acct", "k", 2000*msNS)
	c1.prompt = "p-typed"
	c2 := ccCall("c2", "s", "acct", "k", 1000*msNS)
	c2.prompt = "p-untyped" // typed before capture began: no tengu_input_prompt
	aux := ccCall("aux", "s", "acct", "k", 2000*msNS)
	aux.class = "auxiliary"
	comp := ccCall("comp", "s", "acct", "k", 5000*msNS)
	comp.class = "compaction"
	evs := []eventCols{
		pev("b1", "s", "p-typed", "tengu_input_prompt", 2000*msNS, `{"prompt_index":4,"prompt_length":12}`),
		pev("b1", "s", "", "tengu_reactive_compact_attempt", 4900*msNS, `{}`),
		pev("b1", "s", "", "tengu_reactive_compact_succeeded", 5100*msNS, `{}`),
		pev("b1", "s", "", "tengu_return_to_session", 6000*msNS, `{"hadRecap":"true"}`),
		pev("b1", "s", "", "tengu_return_to_session", 7000*msNS, `{"hadRecap":false}`),
	}
	rows, _ := buildPrompts([]rowCall{c1, c2, aux, comp}, evs)
	want := []string{"prompt:p-untyped", "call:aux", "prompt:p-typed", "compaction:comp", "recap:"}
	if g := itemKeys(rows); !reflect.DeepEqual(g, want) {
		t.Fatalf("items %v, want %v", g, want)
	}
	for i, r := range rows {
		if r.Seq != int64(i+1) || r.SessionLastNS != 7000*msNS {
			t.Errorf("item %d: seq %d, session last %d", i, r.Seq, r.SessionLastNS)
		}
	}
	if rows[0].Typed || !rows[2].Typed || *rows[2].PromptIndex != 4 || rows[0].PromptIndex != nil {
		t.Errorf("typed and index: %+v / %+v", rows[0], rows[2])
	}
	if c := rows[3]; *c.Detail != "compaction" || *c.WaitMS != 200 || c.TokensFrom != nil {
		t.Errorf("compaction: %+v", c)
	}
	if a := rows[1]; *a.Detail != "auxiliary · opus · end_turn" {
		t.Errorf("between call detail %q", *a.Detail)
	}
}

// A prompt's numbers: its steps' tokens, tool counts (most first, then by name),
// friction, and its duration to its last turn end; the last prompt with no turn
// end is the open tail.
func TestPromptRowsPrompt(t *testing.T) {
	a := ccCall("a", "s", "acct", "k", 1000*msNS)
	a.prompt, a.reqID, a.input, a.cacheRead, a.output = "p1", "req-a", 10, 5, 7
	b := ccCall("b", "s", "acct", "k", 2000*msNS)
	b.prompt, b.clientReqID, b.output, b.status = "p1", "creq-b", 0, 529
	open := ccCall("open", "s", "acct", "k", 9000*msNS)
	open.prompt, open.end = "p2", 0
	evs := []eventCols{
		pev("b1", "s", "p1", "tengu_paste_text", 900*msNS, `{"pastedTextBytes":"40"}`),
		pev("b1", "s", "p1", "tengu_paste_image", 950*msNS, `{}`),
		pev("b1", "s", "p1", "tengu_input_prompt", 1000*msNS, `{"prompt_index":1,"is_negative":"true"}`),
		pev("b1", "s", "p1", "tengu_api_success", 1500*msNS, `{"requestId":"req-a","costUSD":0.25,"ttftMs":"812.5","cache_creation_5m_input_tokens":3}`),
		pev("b1", "s", "p1", "tengu_api_success", 2500*msNS, `{"clientRequestId":"creq-b"}`),
		pev("b1", "s", "p1", "tengu_tool_use_success", 1600*msNS, `{"requestId":"req-a","toolName":"Read","toolUseID":"t1"}`),
		pev("b1", "s", "p1", "tengu_tool_use_error", 1700*msNS, `{"requestId":"req-a","toolName":"Bash","toolUseID":"t2"}`),
		pev("b1", "s", "p1", "tengu_tool_use_success", 1800*msNS, `{"requestId":"req-a","toolName":"Bash","toolUseID":"t3"}`),
		pev("b1", "s", "p1", "tengu_tool_use_progress", 1650*msNS, `{"requestId":"req-a","toolName":"Bash","toolUseID":"t4"}`),
		pev("b1", "s", "p1", "tengu_bash_tool_command_executed", 1790*msNS, `{"tool_use_id":"t3","bash_argv0":"rm","exit_code":"1","destructive_category":"delete","destructive_target_scope":"repo"}`),
		pev("b1", "s", "p1", "tengu_cancel", 2600*msNS, `{}`),
		pev("b1", "s", "p1", "tengu_turn_end", 3000*msNS, `{}`),
		pev("b1", "s", "p1", "tengu_turn_end", 3500*msNS, `{}`),
		pev("b1", "s", "p2", "tengu_input_prompt", 8000*msNS, `{"prompt_index":2}`),
	}
	rows, steps := buildPrompts([]rowCall{a, b, open}, evs)
	p1, p2 := rows[0], rows[1]
	if p1.Steps != 2 || *p1.TokensIn != 15 || *p1.TokensOut != 7 || *p1.DurationTurnMS != 2500 || p1.OpenTail ||
		!p1.Frustrated || !p1.Interrupted || p1.Destructive != 1 || *p1.PastedBytes != 40 || *p1.Images != 1 ||
		p1.UnfinishedMaxNS != nil || p1.ToolCountsJSON != `{"Bash":2,"Read":1}` || p1.HiddenJSON != `{}` {
		t.Errorf("p1: %+v", p1)
	}
	if !p2.OpenTail || *p2.UnfinishedMaxNS != 9000*msNS || p2.DurationTurnMS != nil || p2.PastedBytes != nil || *p2.Images != 0 {
		t.Errorf("p2: %+v", p2)
	}
	sa, sb, so := steps[0], steps[1], steps[2]
	if *sa.CostUSD != 0.25 || *sa.TTFTMS != 812.5 || *sa.TokensCacheWrite5m != 3 || sa.EventsPendingIfLive || sa.Failed ||
		sa.TokensAccuracy != "exact" || *sa.DurationMS != 1500 {
		t.Errorf("step a: %+v", sa)
	}
	var tools []toolJSON
	json.Unmarshal([]byte(sa.ToolsJSON), &tools)
	if len(tools) != 3 || tools[1].Status != "error" || *tools[2].Command != "rm" || *tools[2].ExitCode != 1 ||
		tools[2].Destructive.Category != "delete" || *tools[2].Destructive.Scope != "repo" {
		t.Errorf("tools: %s", sa.ToolsJSON)
	}
	var live []toolJSON
	json.Unmarshal([]byte(*sa.ToolsLiveJSON), &live)
	if len(live) != 4 || live[3].Status != "running" || *live[3].ToolUseID != "t4" {
		t.Errorf("live tools: %s", *sa.ToolsLiveJSON)
	}
	if !reflect.DeepEqual(sa.Commands, []string{"rm"}) || !reflect.DeepEqual(sa.ExitCodes, []int64{1}) || sa.ContentLive {
		t.Errorf("commands: %v %v", sa.Commands, sa.ExitCodes)
	}
	if !sb.Failed || sb.TokensAccuracy != "missing" || *sb.UsageMissing != "failed" || sb.EventsPendingIfLive {
		t.Errorf("step b (reported by client request id): %+v", sb)
	}
	if so.EndNS != nil || so.DurationMS != nil || !so.EventsPendingIfLive {
		t.Errorf("open step: %+v", so)
	}
}

// A prompt sent with no prompt id is "input:<batch>:<its number in the batch>",
// numbered over all the batch's Claude Code events by seq.
func TestPromptRowsUnnamedInput(t *testing.T) {
	first := pev("batch", "other-session", "", "tengu_startup", 5*msNS, `{}`)
	first.Seq = 1
	input := pev("batch", "s", "", "tengu_input_prompt", 1*msNS, `{"prompt_index":"3.9"}`)
	input.Seq = 2
	rows, _ := buildPrompts(nil, []eventCols{input, first})
	if len(rows) != 1 || *rows[0].PromptID != "input:batch:2" || *rows[0].PromptIndex != 3 {
		t.Errorf("rows: %+v", rows)
	}
}

// Hidden calls: a prewarm warms the next prompt to start, a title names the one
// before it; kinds in the order they're first seen.
func TestPromptRowsHidden(t *testing.T) {
	p1 := ccCall("p1", "s", "acct", "k", 1000)
	p1.prompt = "a"
	p2 := ccCall("p2", "s", "acct", "k", 5000)
	p2.prompt = "b"
	title := ccCall("t", "s", "acct", "k", 3000)
	title.hidden = "title"
	prewarm := ccCall("w", "s", "acct", "k", 3000)
	prewarm.hidden = "prewarm"
	late := ccCall("w2", "s", "acct", "k", 9000)
	late.hidden = "prewarm" // no prompt after it: the last one before
	rows, _ := buildPrompts([]rowCall{p1, p2, title, prewarm, late}, nil)
	if rows[0].HiddenJSON != `{"title":1}` || rows[1].HiddenJSON != `{"prewarm":2}` {
		t.Errorf("hidden: %s, %s", rows[0].HiddenJSON, rows[1].HiddenJSON)
	}
}

// A Codex turn is typed, numbered by its place in the thread, and carries its
// report (the newest of a turn's), whose status closes it.
func TestPromptRowsCodex(t *testing.T) {
	call := func(id, prompt string, ts int64) rowCall {
		return rowCall{id: id, corrID: id, ts: ts, end: ts + msNS, host: "chatgpt.com", method: "POST",
			path: "/backend-api/codex/responses", status: 200, session: "th", prompt: prompt, product: productCodex}
	}
	report := func(prompt string, ts int64, md string) eventCols {
		return eventCols{CorrelationID: "r", NS: ts, Source: productCodex, Name: "codex_turn_event", SessionID: "th", PromptID: prompt, MetadataJSON: md}
	}
	rows, _ := buildPrompts([]rowCall{call("c1", "t1", 1000), call("c2", "t2", 2000)}, []eventCols{
		report("t1", 1500, `{"status":"failed"}`),
		report("t1", 1600, `{"status":"completed","duration_ms":1234.5,"sampling_request_count":2.7,"shell_command_count":3,"mcp_tool_call_count":0,"compaction_ms":0,"turn_trigger":"user","steer_count":"1"}`),
		report("t2", 2500, `{"status":""}`),
	})
	t1, t2 := rows[0], rows[1]
	if !t1.Typed || *t1.PromptIndex != 1 || *t1.DurationReportMS != 1234.5 || t1.OpenTail ||
		*t1.TurnReportJSON != `{"status":"completed","durationMs":1234.5,"samplingMs":null,"requests":2,"retries":null,"toolCalls":{"shell":3},"steers":null,"compactionMs":null,"trigger":"user"}` {
		t.Errorf("t1: %+v %s", t1, deref(t1.TurnReportJSON))
	}
	if *t2.PromptIndex != 2 || !t2.OpenTail || t2.Images != nil {
		t.Errorf("t2: %+v", t2)
	}
}

// What a step did (the API's rows.step_kind): a patch or a spawn wins over exec.
func TestStepKind(t *testing.T) {
	uses := func(names ...string) string {
		var u []map[string]any
		for _, n := range names {
			kind := "builtin"
			if len(n) > 5 && n[:5] == "mcp__" {
				kind = "mcp"
			}
			u = append(u, map[string]any{"name": n, "kind": kind, "count": 1})
		}
		b, _ := json.Marshal(u)
		return string(b)
	}
	for _, c := range []struct{ uses, stop, want string }{
		{uses("exec"), "completed", "exec"},
		{uses("exec", "apply_patch"), "completed", "patch"},
		{uses("Bash", "Task"), "tool_use", "spawn"},
		{uses("mcp__fs__read"), "", "mcp"},
		{uses("Read"), "", "tool"},
		{"", "end_turn", "text"},
		{uses("exec"), "incomplete:interrupted", "interrupt"},
	} {
		if g := stepKind(c.uses, c.stop); g != c.want {
			t.Errorf("stepKind(%s, %q) = %s, want %s", c.uses, c.stop, g, c.want)
		}
	}
}

// Metadata values as Python reads them: _int truncates float(v), _str is str(v).
func TestPromptMetadataValues(t *testing.T) {
	for in, want := range map[string]*int64{
		`12`: ptr(int64(12)), `-12.9`: ptr(int64(-12)), `"7.5"`: ptr(int64(7)), `" 1_000 "`: ptr(int64(1000)),
		`"1__0"`: nil, `true`: nil, `null`: nil, `"nan"`: nil, `[1]`: nil, `"0x10"`: nil,
	} {
		if g := pyIntOf(json.RawMessage(in)); !reflect.DeepEqual(g, want) {
			t.Errorf("_int(%s) = %v, want %v", in, deref(g), deref(want))
		}
	}
	for in, want := range map[string]*string{
		`"a"`: ptr("a"), `1.0`: ptr("1.0"), `1e16`: ptr("1e+16"), `0.00001`: ptr("1e-05"), `123456789012345678901`: ptr("123456789012345678901"),
		`true`: ptr("True"), `{}`: nil, `null`: nil,
	} {
		if g := pyStr(json.RawMessage(in)); !reflect.DeepEqual(g, want) {
			t.Errorf("_str(%s) = %v, want %v", in, deref(g), deref(want))
		}
	}
	if o := pyObject(`{"a":1,"a":2}`); string(o["a"]) != "2" { // json.loads: the last of a repeated key
		t.Errorf("repeated key: %s", o["a"])
	}
}

func TestPromptRowsParquet(t *testing.T) {
	rows := []PromptRow{{SessionID: "s", Seq: 1, Type: "prompt", PromptID: ptr("p"), ToolCountsJSON: "{}", HiddenJSON: "{}"}}
	steps := []PromptStep{{SessionID: "s", Seq: 1, Step: 1, Commands: []string{"rm"}, ExitCodes: []int64{0},
		CommandsLive: []string{}, ExitCodesLive: []int64{}, CostUSD: ptr(0.0)}}
	s := newMem()
	s.objs["r"], s.objs["s"] = encodeParquet(rows), encodeParquet(steps)
	gr, err := readParquet[PromptRow](t.Context(), s, "r")
	if err != nil {
		t.Fatal(err)
	}
	gs, err := readParquet[PromptStep](t.Context(), s, "s")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range [][2]any{{gr, rows}, {gs, steps}} {
		g, _ := json.Marshal(c[0])
		w, _ := json.Marshal(c[1])
		if string(g) != string(w) {
			t.Errorf("round trip:\n got %s\nwant %s", g, w)
		}
	}
}

// Events at the same time in one batch keep the API's order (its event ids number a
// batch by seq, row time, time, name and metadata), not the table's: a tool still
// running is the first progress tick of its name in that order.
func TestPromptRowsSameTimeOrder(t *testing.T) {
	c := ccCall("c", "s", "acct", "k", 1000*msNS)
	c.prompt, c.reqID = "p", "r"
	tick := func(server string) eventCols {
		return pev("b", "s", "", tickEvent, 1500*msNS, `{"requestId":"r","toolName":"mcp_tool","mcpServerName":"`+server+`"}`)
	}
	_, steps := buildPrompts([]rowCall{c}, []eventCols{tick("zeta"), tick("alpha")})
	var tools []toolJSON
	if err := json.Unmarshal([]byte(deref(steps[0].ToolsLiveJSON)), &tools); err != nil || len(tools) != 1 {
		t.Fatalf("tools %s: %v", deref(steps[0].ToolsLiveJSON), err)
	}
	if s := tools[0].MCPServer; s == nil || *s != "alpha" {
		t.Errorf("running tool's server %v, want alpha", deref(s))
	}
}
