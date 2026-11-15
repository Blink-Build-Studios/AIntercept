package refresh

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// The tests drain at once: a coalesced row is claimed as soon as it's marked
// (TestCoalescedKindsWait restores the wait).
func TestMain(m *testing.M) {
	Coalesce = 0
	os.Exit(m.Run())
}

const sec = int64(1e9)

// pcall is a Claude Code model call of session s in prompt p at `at`.
func pcall(id, s, p string, at int64) records.Record {
	r := call(id, s, at)
	r.Call.PromptID = p
	return r
}

// pev is a Claude Code event of session s in prompt p, with the tool use id
// canon reads from its metadata.
func pev(batch string, n int64, s, p string, at int64, name, md string) records.Record {
	r := ev(batch, n, s, at, name, md)
	r.Event.PromptID = p
	var m struct{ ToolUseID, ToolUseID2 string }
	if err := json.Unmarshal([]byte(strings.ReplaceAll(md, `"tool_use_id"`, `"ToolUseID2"`)), &m); err == nil {
		r.Event.ToolUseID = cmp.Or(m.ToolUseID, m.ToolUseID2)
	}
	return r
}

// tool is a tool event of request rid (tengu_tool_use_success, _error or _progress).
func tool(batch string, n int64, s, p string, at int64, name, rid, md string) records.Record {
	r := pev(batch, n, s, p, at, name, md)
	r.Event.RequestID = rid
	return r
}

// open is call r as its start alone (no end yet).
func open(r records.Record) records.Record {
	r.Kind, r.Call.EndNS, r.Call.OutputTokens = records.KindCallStart, 0, 0
	r.Call.Status = 0
	return r
}

func TestPromptAndSteps(t *testing.T) {
	db := fresh(t)
	a := pcall("a", "s", "p1", t0+1*sec)
	a.Call.InputTokens, a.Call.CacheReadTokens = 10, 20
	a.Call.ToolUsesJSON, a.Call.StopReason, a.Call.Model = `[{"tool":"Bash","name":"Bash"}]`, "tool_use", "claude-opus-4"
	b := pcall("b", "s", "p1", t0+2*sec)
	b.Call.RequestClass, b.Call.OutputTokens, b.Call.AgentType = "subagent", 0, "Explore"
	b.Call.ToolUsesJSON = `[{"tool":"apply_patch"}]`
	b.Call.DetailJSON = `{"calls":[{"id":"x1","tool":"exec","commands":["go test","ls"],"patches":[{"file":"a.go","added":2,"removed":1},{"file":"","added":1}]}],"text":"Done.","prompt":"fix it"}`
	c := pcall("c", "s", "p1", t0+3*sec)
	c.Call.DetailJSON = `{"results":{"x1":{"exit":[0,2]}}}`
	d := open(pcall("d", "s", "p1", t0+4*sec))
	write(t, db, a, b, c, d,
		pev("B", 1, "s", "p1", t0, "tengu_input_prompt", `{"prompt_index":3,"prompt_length":"12","is_negative":"true"}`),
		rep("B", 2, "s", t0+1*sec, "req-a"),
		tool("B", 3, "s", "p1", t0+1*sec, "tengu_tool_use_success", "req-a", `{"toolUseID":"tu1","toolName":"Bash","durationMs":15}`),
		tool("B", 4, "s", "p1", t0+1*sec, "tengu_tool_use_progress", "req-a", `{"toolUseID":"tu1","toolName":"Bash"}`),
		tool("B", 5, "s", "p1", t0+1*sec, "tengu_tool_use_progress", "req-a", `{"toolUseID":"tu2","toolName":"Read"}`),
		pev("B", 6, "s", "p1", t0+1*sec, "tengu_bash_tool_command_executed",
			`{"tool_use_id":"tu1","bash_argv0":"rm","exit_code":1,"destructive_category":"rm_force","destructive_target_scope":"cwd"}`),
		pev("B", 7, "s", "p1", t0+5*sec, "tengu_turn_end", `{}`),
		pev("B", 8, "s", "p1", t0+5*sec, "tengu_cancel", `{}`),
	)
	// The report's rep() has no prompt id, and its metadata names its cost.
	drain(t, db)
	expect(t, row(t, db, "SELECT * FROM prompts WHERE prompt_id = 'p1'"), map[string]any{
		"session_id": "s", "t_ns": float64(t0), "product": "claude_code", "has_input": true, "prompt_index": 3, "text_chars": 12,
		"frustrated": true, "steps": 4, "unfinished_max_ns": float64(t0 + 4*sec), "turn_end_ns": float64(t0 + 5*sec),
		"text": "fix it", "tokens_in": 30, "tokens_out": 10, "tool_counts": map[string]any{"Bash": float64(1)},
		"interrupted": true, "destructive": 1, "turn_report": nil,
	})
	bash := map[string]any{"toolUseId": "tu1", "name": "Bash", "mcpServer": nil, "target": nil, "status": "ok", "durationMs": float64(15),
		"exitCode": float64(1), "command": "rm", "resultBytes": nil, "destructive": map[string]any{"category": "rm_force", "scope": "cwd"}}
	read := map[string]any{"toolUseId": "tu2", "name": "Read", "mcpServer": nil, "target": nil, "status": "running", "durationMs": nil,
		"exitCode": nil, "command": nil, "resultBytes": nil, "destructive": nil}
	expect(t, row(t, db, "SELECT * FROM steps WHERE prompt_id = 'p1' AND step = 1"), map[string]any{
		"call_id": "a", "kind": "model", "model": "claude-opus-4", "request_class": "main", "tokens_input": 10, "tokens_cache_read": 20,
		"tokens_cache_write": nil, "tokens_cache_write_5m": 7, "tokens_output": 5, "ttft_ms": nil, "cost_usd": 0.5, "reported": true,
		"failed": false, "end_ns": float64(t0 + 2*sec), "does": "exec", "stop_reason": "tool_use",
		"tools": []any{bash}, "tools_live": []any{bash, read},
		"commands": []any{"rm"}, "exit_codes": []any{float64(1)}, "content_live": false, "usage_missing": nil,
	})
	expect(t, row(t, db, "SELECT * FROM steps WHERE prompt_id = 'p1' AND step = 2"), map[string]any{
		"call_id": "b", "kind": "subagent", "request_class": "subagent", "agent_type": "Explore", "tokens_output": nil,
		"reported": false, "does": "patch", "tools": []any{}, "tools_live": nil,
		"commands": []any{"go test", "ls"}, "exit_codes": []any{float64(0), float64(2)},
		"patches": []any{map[string]any{"file": "a.go", "added": float64(2), "removed": float64(1)}}, "text": "Done.", "usage_missing": "streamCut",
	})
	expect(t, row(t, db, "SELECT * FROM steps WHERE prompt_id = 'p1' AND step = 4"), map[string]any{
		"call_id": "d", "end_ns": nil, "failed": true, "does": "text", "tokens_output": nil, "model": nil})
	checkScratch(t, db)
}

// A call arriving after a later step of its prompt renumbers the steps.
func TestLateCallRenumbersSteps(t *testing.T) {
	db := fresh(t)
	write(t, db, pcall("b", "s", "p", t0+2*sec), pcall("c", "s", "p", t0+3*sec))
	drain(t, db)
	steps := func() map[string]any {
		return row(t, db, "SELECT jsonb_object_agg(call_id, step) AS s, (SELECT steps FROM prompts WHERE prompt_id = 'p') AS n FROM steps WHERE prompt_id = 'p'")
	}
	expect(t, steps(), map[string]any{"s": map[string]any{"b": float64(1), "c": float64(2)}, "n": 2})
	write(t, db, pcall("a", "s", "p", t0+1*sec))
	drain(t, db)
	expect(t, steps(), map[string]any{"s": map[string]any{"a": float64(1), "b": float64(2), "c": float64(3)}, "n": 3})
	checkScratch(t, db)
}

// A late input event sets the prompt's number, its time and its typed values.
func TestLateInputSetsPromptNumber(t *testing.T) {
	db := fresh(t)
	write(t, db, pcall("a", "s", "p", t0+2*sec))
	drain(t, db)
	expect(t, row(t, db, "SELECT p.prompt_index, p.has_input, p.t_ns, i.prompt_index AS number FROM prompts p JOIN prompt_items i USING (session_id, prompt_id)"),
		map[string]any{"prompt_index": nil, "has_input": false, "t_ns": float64(t0 + 2*sec), "number": nil})
	write(t, db, pev("B", 1, "s", "p", t0, "tengu_input_prompt", `{"prompt_index":7,"prompt_length":5}`))
	drain(t, db)
	expect(t, row(t, db, "SELECT p.prompt_index, p.has_input, p.t_ns, p.text_chars, i.prompt_index AS number FROM prompts p JOIN prompt_items i USING (session_id, prompt_id)"),
		map[string]any{"prompt_index": 7, "has_input": true, "t_ns": float64(t0), "text_chars": 5, "number": 7})
	checkScratch(t, db)
}

// A step's tool, report and Bash events can name another prompt (a subagent's
// call): arriving late, they still refresh the step's prompt.
func TestLateEventsOfAnotherPromptRefreshTheStep(t *testing.T) {
	db := fresh(t)
	write(t, db, pcall("a", "s", "p", t0))
	drain(t, db)
	write(t, db, tool("B", 1, "s", "q", t0, "tengu_tool_use_success", "req-a", `{"toolUseID":"tu1","toolName":"Bash"}`))
	drain(t, db)
	write(t, db, pev("C", 1, "s", "q", t0, "tengu_bash_tool_command_executed", `{"tool_use_id":"tu1","bash_argv0":"ls","exit_code":0}`))
	drain(t, db)
	r := rep("D", 1, "s", t0, "req-a")
	r.Event.PromptID = "q"
	write(t, db, r)
	drain(t, db)
	expect(t, row(t, db, "SELECT commands, exit_codes, reported FROM steps WHERE call_id = 'a'"),
		map[string]any{"commands": []any{"ls"}, "exit_codes": []any{float64(0)}, "reported": true})
	checkScratch(t, db)
}

// A tool call's exit codes come from the session's first call that carries
// its result back, which can be a later prompt's (the prompt after an
// interrupt): in either order of arrival, the tool call's step gets them, and
// a later carrier doesn't override the first.
func TestResultCarriedByAnotherPromptsCall(t *testing.T) {
	for _, order := range []string{"use first", "result first"} {
		t.Run(order, func(t *testing.T) {
			db := fresh(t)
			use := pcall("a", "s", "p1", t0)
			use.Call.DetailJSON = `{"calls":[{"id":"x1","tool":"Bash","commands":["make"]},{"id":"x2","tool":"Bash","commands":["ls"]}]}`
			carry := pcall("b", "s", "p2", t0+5*sec)
			carry.Call.DetailJSON = `{"results":{"x1":{"exit":[2]},"x2":{"error":true}}}`
			later := pcall("c", "s", "p3", t0+9*sec)
			later.Call.DetailJSON = `{"results":{"x1":{"exit":[7]}}}`
			first, second := []records.Record{use}, []records.Record{carry, later}
			if order == "result first" {
				first, second = second, first
			}
			write(t, db, first...)
			drain(t, db)
			write(t, db, second...)
			drain(t, db)
			expect(t, row(t, db, "SELECT commands, exit_codes FROM steps WHERE call_id = 'a'"),
				map[string]any{"commands": []any{"make", "ls"}, "exit_codes": []any{float64(2), float64(1)}})
			checkScratch(t, db)
		})
	}
}

func TestPromptItems(t *testing.T) {
	db := fresh(t)
	main := func(id, p string, at, ctx int64) records.Record {
		r := pcall(id, "s", p, at)
		r.Call.InputTokens = ctx
		return r
	}
	comp := pcall("k", "s", "", t0+101*sec)
	comp.Call.RequestClass = "compaction"
	aux := pcall("x", "s", "", t0+120*sec)
	aux.Call.RequestClass, aux.Call.Model, aux.Call.StopReason = "auxiliary", "claude-haiku-4-5", "end_turn"
	title := pcall("t", "s", "", t0+50*sec)
	title.Call.HiddenKind = "title"
	prewarm := pcall("w", "s", "", t0+50*sec)
	prewarm.Call.HiddenKind = "prewarm"
	write(t, db,
		pev("B", 1, "s", "p1", t0, "tengu_input_prompt", `{"prompt_index":1}`), main("a", "p1", t0+1*sec, 1000),
		ev("B", 2, "s", t0+30*sec, "tengu_paste_text", `{"pastedTextBytes":40}`), ev("B", 3, "s", t0+31*sec, "tengu_paste_image", `{}`),
		pev("B", 4, "s", "p2", t0+60*sec, "tengu_input_prompt", `{"prompt_index":2}`), main("b", "p2", t0+61*sec, 200000),
		title, prewarm,
		ev("B", 5, "s", t0+95*sec, "tengu_idle_compact_fired", `{}`),
		ev("B", 6, "s", t0+100*sec, "tengu_reactive_compact_attempt", `{}`), comp,
		ev("B", 7, "s", t0+102*sec, "tengu_reactive_compact_succeeded", `{}`),
		main("c", "p2", t0+103*sec, 50000),
		ev("B", 8, "s", t0+110*sec, "tengu_return_to_session", `{"hadRecap":true}`),
		aux,
	)
	drain(t, db)
	rows := row(t, db, `SELECT jsonb_agg(jsonb_build_object('seq', seq, 'type', type, 'prompt', prompt_id, 'n', prompt_index, 'tail', open_tail,
		'pasted', pasted_bytes, 'images', images, 'hidden', hidden, 'kind', between_kind, 'detail', detail, 'call', call_id,
		'from', tokens_from, 'to', tokens_to, 'attempt', attempt_ns) ORDER BY seq) AS items FROM prompt_items WHERE session_id = 's'`)["items"].([]any)
	want := []map[string]any{
		{"seq": 1, "type": "prompt", "prompt": "p1", "n": 1, "tail": false, "pasted": nil, "images": 0, "hidden": map[string]any{"title": float64(1)}},
		{"seq": 2, "type": "prompt", "prompt": "p2", "n": 2, "tail": true, "pasted": 40, "images": 1, "hidden": map[string]any{"prewarm": float64(1)}},
		{"seq": 3, "type": "between", "kind": "idle-compaction", "detail": "idle compaction · context 200k → 50k tokens", "call": "k",
			"from": 200000, "to": 50000, "attempt": float64(t0 + 100*sec)},
		{"seq": 4, "type": "between", "kind": "recap", "detail": "recap shown on return"},
		{"seq": 5, "type": "between", "kind": "call", "detail": "auxiliary · haiku · end_turn", "call": "x"},
	}
	if len(rows) != len(want) {
		t.Fatalf("%d items, want %d: %v", len(rows), len(want), rows)
	}
	for i, w := range want {
		expect(t, rows[i].(map[string]any), w)
	}
	checkScratch(t, db)
}

// codex is a Codex model call of thread x in turn p.
func codex(id, x, p string, at int64) records.Record {
	r := pcall(id, x, p, at)
	r.Call.Product, r.Call.Host, r.Call.Path = canon.ProductCodex, "chatgpt.com", "/backend-api/codex/responses"
	return r
}

func codexEvent(batch string, n int64, x, p string, at int64, md string) records.Record {
	return records.Record{Kind: records.KindClientEvent, BatchID: batch, BatchN: n, Call: &canon.Request{KeyHash: "kh1"},
		Event: &canon.Event{EventKey: fmt.Sprintf("%s/%d", batch, n), CorrelationID: batch, RowNS: at, NS: at, Seq: n,
			Source: canon.ProductCodex, Name: "codex_turn_event", SessionID: x, PromptID: p, MetadataJSON: md}}
}

// A Codex turn's number is its place in the thread; its report gives its
// duration and status.
func TestCodexPrompts(t *testing.T) {
	db := fresh(t)
	b := codex("b", "x", "t2", t0+10*sec)
	b.Call.ExtJSON = `{"workspaces":{"/home/u/repo/":{"latest_git_commit_hash":"0123456789"}},"sandbox_mode":"workspace-write","history_initialization":"cold_resume"}`
	b.Call.ProviderJSON = `{"x-codex-plan-type":"pro","x-codex-primary-used-percent":"12"}`
	write(t, db, codex("a", "x", "t1", t0), b,
		codexEvent("E", 1, "x", "t1", t0+5*sec, `{"status":"completed","duration_ms":1500,"shell_command_count":2,"mcp_tool_call_count":0,"turn_trigger":"user","approval_policy":"never"}`))
	drain(t, db)
	expect(t, row(t, db, "SELECT turn_report, product, has_input FROM prompts WHERE prompt_id = 't1'"), map[string]any{
		"product": "codex", "has_input": false,
		"turn_report": map[string]any{"status": "completed", "durationMs": float64(1500), "samplingMs": nil, "requests": nil, "retries": nil,
			"toolCalls": map[string]any{"shell": float64(2)}, "steers": nil, "compactionMs": nil, "trigger": "user"}})
	expect(t, row(t, db, "SELECT jsonb_object_agg(prompt_id, jsonb_build_array(prompt_index, open_tail)) AS n FROM prompt_items WHERE session_id = 'x'"),
		map[string]any{"n": map[string]any{"t1": []any{float64(1), false}, "t2": []any{float64(2), true}}})
	expect(t, row(t, db, "SELECT * FROM sessions WHERE session_id = 'x'"), map[string]any{
		"product": "codex", "repo": "repo", "commit": "0123456", "has_facts": true, "facts_cwd": "/home/u/repo/",
		"facts_approval": "never", "facts_sandbox": "workspace-write", "limits_ns": float64(t0 + 10*sec),
		"limits":      map[string]any{"x-codex-plan-type": "pro", "x-codex-primary-used-percent": "12"},
		"origin_kind": "resume", "origin": map[string]any{"kind": "resume", "sessionId": nil, "turn": nil, "init": "cold_resume", "agentName": nil},
	})
	checkScratch(t, db)
}

func TestSessionRow(t *testing.T) {
	db := fresh(t)
	a := pcall("a", "s", "p1", t0+10*sec)
	a.Call.PersonID, a.Call.DeviceID, a.Call.Effort, a.Call.Client, a.Call.ClientVersion = "acct", "dev-call", "high", "cli", "2.0.1"
	a.Call.UserAgent, a.Call.ProviderJSON = "claude-cli/2.0.1 (external, cli)", `{"anthropic-ratelimit-unified-status":"allowed","anthropic-ratelimit-unified-7d-utilization":"0.25"}`
	a.Call.InputTokens = 100
	b := pcall("b", "s", "p2", t0+20*sec)
	b.Call.OutputTokens, b.Call.IP = 0, "10.0.0.2"
	title := pcall("t", "s", "", t0+30*sec)
	title.Call.HiddenKind, title.Call.DetailJSON = "title", `{"text":"  \"Fix the build\" "}`
	env := ev("B", 2, "s", t0+5*sec, "tengu_api_query", `{"permissionMode":"bypassPermissions"}`)
	env.Event.DeviceID, env.Event.PersonID, env.Event.EnvJSON = "dev-ev", "acct", `{"platform":"darwin","terminal":"iTerm"}`
	ca := ev("B", 9, "s", t0+51*sec, "tengu_api_success", `{"gzipSkipReason":"custom_ca"}`)
	ca.Event.ClientRequestID = "creq-b"
	b.Call.ClientRequestID = "creq-b"
	write(t, db, a, b, title, env, ca,
		pev("B", 1, "s", "p1", t0, "tengu_input_prompt", `{"prompt_index":4,"is_negative":"true"}`),
		ev("B", 3, "s", t0+6*sec, "tengu_api_query", `{"permissionMode":"default"}`),
		ev("B", 4, "s", t0+7*sec, "tengu_bash_tool_command_executed", `{"sandbox_enabled":true,"destructive_category":"git_reset"}`),
		rep("B", 5, "s", t0+40*sec, "req-a"),
		rep("B", 6, "s", t0+41*sec, "req-gone"),
		rep("B", 7, "s", t0+41*sec+int64(20*time.Minute), "req-gone-2"),
		rep("B", 8, "s", t0+50*sec, "req-b"),
	)
	drain(t, db)
	expect(t, row(t, db, "SELECT * FROM sessions WHERE session_id = 's'"), map[string]any{
		"start_ns": float64(t0), "last_ns": float64(t0 + 41*sec + int64(20*time.Minute)), "product": "claude_code",
		"client": "cli", "client_version": "2.0.1", "client_version_ns": float64(t0 + 10*sec), "hidden_calls": 1,
		"own_title": "Fix the build", "prompts": 2, "captured_from_prompt": 4, "model_calls": 2, "events": 9,
		"frustrated": 1, "interrupted": 0, "destructive": 1, "queries": 2, "bypassed": 1,
		"cov_reported": 5, "cov_captured": 3, "cov_not_captured": 2,
		"cov_window_from":  []any{float64(t0 + 41*sec), float64(t0 + 41*sec + int64(20*time.Minute))},
		"cov_window_count": []any{float64(1), float64(1)},
		"fields_missing":   false, "person_id": "acct", "device_id": "dev-ev", "device_platform": "darwin", "device_terminal": "iTerm",
		"device_shell": nil, "networks": 2, "effort": "high", "tokens_input": 100, "tokens_output": 5, "outputs_known": 1,
		"tokens_accuracy": "estimated", "has_facts": false, "cli_version": "2.0.1", "rl_status": "allowed", "rl_util": 0.25,
		"rl_ns": float64(t0 + 10*sec), "sandbox": "true", "custom_ca": true, "unattributed": false,
		"prompt_event_last_ns": float64(t0 + 41*sec + int64(20*time.Minute)),
	})
	// A session no account appears on is unattributed.
	write(t, db, pcall("z", "u", "pz", t0))
	drain(t, db)
	expect(t, row(t, db, "SELECT unattributed, fields_missing, events, cov_reported, cov_captured FROM sessions WHERE session_id = 'u'"),
		map[string]any{"unattributed": true, "fields_missing": true, "events": nil, "cov_reported": nil, "cov_captured": 1})
	checkScratch(t, db)
}

// An account's days sum its sessions' activity inside each day (owner
// decision 2), and an account seen on a session late still gets the
// session's earlier days.
func TestAccountDays(t *testing.T) {
	db := fresh(t)
	day := t0 - t0%dayNS
	late := day + dayNS - 30*sec // just before midnight
	write(t, db, pcall("a", "s", "p1", late), pcall("b", "s", "p2", day+dayNS+30*sec),
		pev("B", 1, "s", "p1", late, "tengu_input_prompt", `{}`))
	drain(t, db)
	var n int
	db.QueryRow(ctx(t), "SELECT count(*) FROM account_days").Scan(&n)
	if n != 0 {
		t.Fatalf("%d day rows before any account appears", n)
	}
	seen := pev("C", 1, "s", "p2", day+dayNS+40*sec, "tengu_api_query", `{"permissionMode":"bypassPermissions"}`)
	seen.Event.PersonID = "acct"
	quiet := pev("D", 1, "s", "p2", day+2*dayNS+10*sec, "tengu_feature_ok", `{}`) // a day with events and no model call
	write(t, db, seen, quiet)
	drain(t, db)
	days := row(t, db, `SELECT jsonb_object_agg(day_ns::text, jsonb_build_object('agent', agent, 'sessions', sessions, 'ids', session_ids, 'call_ids', call_session_ids, 'with_events', sessions_with_events,
		'prompts', prompts, 'calls', model_calls, 'out', tokens_output, 'queries', queries, 'bypassed', bypassed, 'first', first_ns, 'last', last_ns)) AS d
		FROM account_days WHERE account_id = 'acct'`)["d"].(map[string]any)
	expect(t, days[fmt.Sprint(day)].(map[string]any), map[string]any{"agent": "claude-code", "sessions": 1, "ids": []any{"s"}, "call_ids": []any{"s"}, "with_events": 1, "prompts": 1,
		"calls": 1, "out": 5, "queries": 0, "first": float64(late), "last": float64(late)})
	// The session is in both days' ids: a window over both counts it once (the directory's distinct sessions).
	expect(t, days[fmt.Sprint(day+dayNS)].(map[string]any), map[string]any{"sessions": 1, "ids": []any{"s"}, "with_events": 1, "prompts": 1, "calls": 1,
		"queries": 1, "bypassed": 1, "first": float64(day + dayNS + 30*sec), "last": float64(day + dayNS + 40*sec)})
	// The overview's agent cards count sessions with a model call in the window: not this day's.
	expect(t, days[fmt.Sprint(day+2*dayNS)].(map[string]any), map[string]any{"sessions": 1, "ids": []any{"s"}, "call_ids": []any{}, "calls": 0})
	checkScratch(t, db)
}

// Retention marks the prompt, session and day rollups that held facts before
// the cut, and they're recomputed from what's left.
func TestRetainRecomputesPromptsAndSessions(t *testing.T) {
	db := fresh(t)
	now := time.Unix(0, t0+20*24*hourNS)
	recent := now.UnixNano() - hourNS
	old, kept := pcall("old", "s", "p", t0), pcall("new", "s", "p", recent)
	old.Call.PersonID, kept.Call.PersonID = "acct", "acct"
	write(t, db, old, kept)
	drain(t, db)
	if _, err := Retain(ctx(t), db, 15, now); err != nil {
		t.Fatal(err)
	}
	drain(t, db)
	expect(t, row(t, db, "SELECT steps, t_ns FROM prompts WHERE prompt_id = 'p'"), map[string]any{"steps": 1, "t_ns": float64(recent)})
	expect(t, row(t, db, "SELECT model_calls, start_ns FROM sessions WHERE session_id = 's'"), map[string]any{"model_calls": 1, "start_ns": float64(recent)})
	expect(t, row(t, db, "SELECT count(*) AS n, min(day_ns) AS d FROM account_days"), map[string]any{"n": 1, "d": float64(recent - recent%dayNS)})
	checkScratch(t, db)
}

// A typed prompt with no prompt id is a prompt of its own, keyed by its place
// in its batch (promptrows.go: "input:" + its event id).
func TestUnnamedInputIsItsOwnPrompt(t *testing.T) {
	db := fresh(t)
	write(t, db, ev("B", 3, "s", t0, "tengu_input_prompt", `{"prompt_index":2,"prompt_length":9}`), pcall("a", "s", "p", t0+sec))
	drain(t, db)
	expect(t, row(t, db, "SELECT * FROM prompts WHERE prompt_id = 'input:B:3'"), map[string]any{
		"session_id": "s", "t_ns": float64(t0), "has_input": true, "prompt_index": 2, "text_chars": 9, "steps": 0})
	expect(t, row(t, db, "SELECT jsonb_agg(prompt_id ORDER BY seq) AS ids FROM prompt_items WHERE session_id = 's'"),
		map[string]any{"ids": []any{"input:B:3", "p"}})
	expect(t, row(t, db, "SELECT prompts FROM sessions WHERE session_id = 's'"), map[string]any{"prompts": 2})
	checkScratch(t, db)
}
