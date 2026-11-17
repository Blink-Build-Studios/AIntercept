package refresh

import (
	"fmt"
	"os"
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

// pev is a Claude Code analytics event of session s in prompt p (not loaded).
func pev(batch string, n int64, s, p string, at int64, name, md string) records.Record {
	r := ev(batch, n, s, at, name, md)
	r.Event.PromptID = p
	return r
}

// open is call r as its start alone (no end yet).
func open(r records.Record) records.Record {
	r.Kind, r.Call.EndNS, r.Call.OutputTokens = records.KindCallStart, 0, 0
	r.Call.Status = 0
	return r
}

// stepNumbers is a prompt's steps' numbers as the API counts them at read:
// each step's place in its prompt by (t_ns, ord).
const stepNumbers = `SELECT jsonb_object_agg(call_id, n) AS s, (SELECT steps FROM prompts WHERE prompt_id = $1) AS n
	FROM (SELECT call_id, row_number() OVER (ORDER BY t_ns, ord) AS n FROM steps WHERE prompt_id = $1) x`

func TestPromptAndSteps(t *testing.T) {
	db := fresh(t)
	a := pcall("a", "s", "p1", t0+1*sec)
	a.Call.InputTokens, a.Call.CacheReadTokens = 10, 20
	a.Call.ToolUsesJSON, a.Call.StopReason, a.Call.Model = `[{"tool":"Bash","name":"Bash"}]`, "tool_use", "claude-opus-4"
	a.Call.DetailJSON = `{"said":[{"role":"system","note":"<system-reminder","text":"<system-reminder>x</system-reminder>"},{"role":"user","text":"fix it"}],` +
		`"prompt":"fix it","promptChars":6,"calls":[{"id":"tu1","tool":"Bash","commands":["rm"]},{"id":"tu2","tool":"Read","file":"src/A.TS"}]}`
	b := pcall("b", "s", "p1", t0+2*sec)
	b.Call.RequestClass, b.Call.OutputTokens, b.Call.AgentType = "subagent", 0, "Explore"
	b.Call.ToolUsesJSON = `[{"tool":"apply_patch"}]`
	b.Call.DetailJSON = `{"said":[{"role":"user","text":"look"}],"prompt":"look","promptChars":4,` +
		`"calls":[{"id":"x1","tool":"exec","commands":["go test","ls"],"patches":[{"file":"a.go","added":2,"removed":1},{"file":"","added":1}]}],` +
		`"text":"Done.","results":{"tu1":{"exit":[1]},"tu2":{}}}`
	c := pcall("c", "s", "p1", t0+3*sec)
	c.Call.DetailJSON = `{"results":{"x1":{"exit":[0,2]}}}`
	d := open(pcall("d", "s", "p1", t0+4*sec))
	write(t, db, a, b, c, d,
		pev("B", 1, "s", "p1", t0, "tengu_input_prompt", `{"prompt_index":3,"prompt_length":"12","is_negative":"true"}`),
		pev("B", 2, "s", "p1", t0+5*sec, "tengu_cancel", `{}`),
	)
	drain(t, db)
	expect(t, row(t, db, "SELECT * FROM prompts WHERE prompt_id = 'p1'"), map[string]any{
		"session_id": "s", "t_ns": float64(t0 + 1*sec), "product": "claude_code", "text_chars": 6,
		"steps": 4, "unfinished_max_ns": float64(t0 + 4*sec), "text": "fix it", "tokens_in": 30, "tokens_out": 10,
		"tool_counts": map[string]any{"Bash": float64(1), "Read": float64(1), "exec": float64(1)},
		"interrupted": false, "follows_interrupt": false, "ended_ns": float64(t0 + 4*sec),
		"parts": []any{map[string]any{"role": "system", "note": "<system-reminder", "text": "<system-reminder>x</system-reminder>"},
			map[string]any{"role": "user", "text": "fix it"}},
	})
	bash := map[string]any{"toolUseId": "tu1", "name": "Bash", "mcpServer": nil, "target": nil, "status": "error", "durationMs": nil,
		"exitCode": float64(1), "command": "rm", "program": "rm", "fileType": nil, "resultBytes": nil, "destructive": nil}
	read := map[string]any{"toolUseId": "tu2", "name": "Read", "mcpServer": nil, "target": "src/A.TS", "status": "ok", "durationMs": nil,
		"exitCode": nil, "command": nil, "program": nil, "fileType": "ts", "resultBytes": nil, "destructive": nil}
	expect(t, row(t, db, "SELECT * FROM steps WHERE call_id = 'a'"), map[string]any{
		"prompt_id": "p1", "ord": "a", "kind": "model", "model": "claude-opus-4", "request_class": "main", "tokens_input": 10,
		"tokens_cache_read": 20, "tokens_cache_write": nil, "tokens_output": 5, "failed": false, "end_ns": float64(t0 + 2*sec),
		"does": "exec", "stop_reason": "tool_use", "tools": []any{bash, read}, "commands": []any{"rm"}, "exit_codes": []any{float64(1)},
		"usage_missing": nil, "prompt_chars": 6, "work_kind": "run", "program": "rm",
		"work_pieces": []any{[]any{"w:run", "rm", float64(1), float64(1)}, []any{"w:run_failed", "rm", float64(1), nil},
			[]any{"w:read", "ts", float64(1), nil}, []any{"w:read_file", "src/A.TS", float64(1), nil}},
	})
	expect(t, row(t, db, "SELECT * FROM steps WHERE call_id = 'b'"), map[string]any{
		"kind": "subagent", "request_class": "subagent", "agent_type": "Explore", "tokens_output": nil,
		"does": "patch", "commands": []any{"go test", "ls"}, "exit_codes": []any{float64(0), float64(2)},
		"patches": []any{map[string]any{"file": "a.go", "added": float64(2), "removed": float64(1)}}, "text": "Done.", "usage_missing": "streamCut",
	})
	expect(t, row(t, db, "SELECT * FROM steps WHERE call_id = 'd'"), map[string]any{
		"end_ns": nil, "failed": true, "does": "text", "tokens_output": nil, "model": nil, "prompt_chars": nil, "work_kind": "think"})
	// The turn's work: its steps' pieces added up, each command with its last run's exit code.
	work := row(t, db, "SELECT work FROM prompts WHERE prompt_id = 'p1'")["work"].(map[string]any)
	expect(t, work, map[string]any{"reads": 1, "readFiles": 1, "readTypes": map[string]any{"ts": float64(1)}, "editedFiles": 1,
		"linesAdded": 2, "linesRemoved": 1})
	expect(t, work["commands"].([]any)[0].(map[string]any), map[string]any{"program": "go test", "runs": 1, "failed": 0, "lastExit": float64(0)})
	checkScratch(t, db)
}

// A call arriving after a later step of its prompt takes its place: the
// numbers counted at read follow.
func TestLateCallRenumbersSteps(t *testing.T) {
	db := fresh(t)
	write(t, db, pcall("b", "s", "p", t0+2*sec), pcall("c", "s", "p", t0+3*sec))
	drain(t, db)
	expect(t, row(t, db, stepNumbers, "p"), map[string]any{"s": map[string]any{"b": float64(1), "c": float64(2)}, "n": 2})
	write(t, db, pcall("a", "s", "p", t0+1*sec))
	drain(t, db)
	expect(t, row(t, db, stepNumbers, "p"), map[string]any{"s": map[string]any{"a": float64(1), "b": float64(2), "c": float64(3)}, "n": 3})
	checkScratch(t, db)
}

// A call that moves to another prompt (its end names one its start didn't)
// leaves the first: both prompts' rows follow.
func TestStepMovesBetweenPrompts(t *testing.T) {
	db := fresh(t)
	a, b := pcall("a", "s", "p", t0), pcall("b", "s", "p", t0+sec)
	b.Kind, b.Call.EndNS = records.KindCallStart, 0
	write(t, db, a, b)
	drain(t, db)
	moved := pcall("b", "s", "q", t0+sec)
	write(t, db, moved)
	drain(t, db)
	expect(t, row(t, db, "SELECT steps, tokens_out FROM prompts WHERE prompt_id = 'p'"), map[string]any{"steps": 1, "tokens_out": 5})
	expect(t, row(t, db, "SELECT steps, tokens_out FROM prompts WHERE prompt_id = 'q'"), map[string]any{"steps": 1, "tokens_out": 5})
	checkScratch(t, db)
}

// Analytics events change nothing: a prompt's input event makes no prompt,
// and a step's tool and report events add nothing to it.
func TestAnalyticsMakeNoPrompts(t *testing.T) {
	db := fresh(t)
	write(t, db, pcall("a", "s", "p", t0+sec),
		ev("B", 3, "s", t0, "tengu_input_prompt", `{"prompt_index":2,"prompt_length":9}`),
		pev("B", 4, "s", "q", t0, "tengu_input_prompt", `{"prompt_index":3}`),
		pev("B", 5, "s", "p", t0, "tengu_tool_use_success", `{"toolName":"Edit"}`))
	drain(t, db)
	expect(t, row(t, db, "SELECT count(*) AS n, jsonb_agg(prompt_id) AS ids, min(tool_counts::text) AS tools FROM prompts"),
		map[string]any{"n": 1, "ids": []any{"p"}, "tools": "{}"})
	expect(t, row(t, db, "SELECT prompts FROM sessions WHERE session_id = 's'"), map[string]any{"prompts": 1})
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
			work := row(t, db, "SELECT work FROM prompts WHERE prompt_id = 'p1'")["work"].(map[string]any)
			expect(t, work["commands"].([]any)[1].(map[string]any), map[string]any{"program": "make", "runs": 1, "failed": 1, "lastExit": float64(2)})
			checkScratch(t, db)
		})
	}
}

// The harness's interrupt note in a turn's request marks it as following an
// interrupted turn; a step that stopped on an interrupt marks its own.
func TestInterrupts(t *testing.T) {
	db := fresh(t)
	a := pcall("a", "s", "p1", t0)
	b := pcall("b", "s", "p2", t0+10*sec)
	b.Call.DetailJSON = `{"said":[{"role":"system","note":"[Request interrupted by user","text":"[Request interrupted by user]"},{"role":"user","text":"stop"}],"prompt":"stop","promptChars":4,"interrupted":true}`
	c := codex("c", "x", "t1", t0)
	c.Call.StopReason = "incomplete:interrupted"
	write(t, db, a, b, c)
	drain(t, db)
	expect(t, row(t, db, "SELECT jsonb_object_agg(prompt_id, jsonb_build_array(interrupted, follows_interrupt)) AS p FROM prompts"),
		map[string]any{"p": map[string]any{"p1": []any{false, false}, "p2": []any{false, true}, "t1": []any{true, false}}})
	expect(t, row(t, db, "SELECT turns, turns_interrupted FROM sessions WHERE session_id = 's'"), map[string]any{"turns": 2, "turns_interrupted": 1})
	expect(t, row(t, db, "SELECT turns, turns_interrupted FROM sessions WHERE session_id = 'x'"), map[string]any{"turns": 1, "turns_interrupted": 1})
	checkScratch(t, db)
}

// A turn's text is its first main-class step's: a subagent's request that
// came first doesn't give it.
func TestTurnTextFromItsMainCall(t *testing.T) {
	db := fresh(t)
	sub := pcall("a", "s", "p", t0)
	sub.Call.RequestClass = "subagent"
	sub.Call.DetailJSON = `{"said":[{"role":"user","text":"explore the repo"}],"prompt":"explore the repo","promptChars":16}`
	main := pcall("b", "s", "p", t0+sec)
	main.Call.DetailJSON = `{"said":[{"role":"system","note":"system","text":"background task done"}],"promptChars":0}`
	write(t, db, sub, main)
	drain(t, db)
	expect(t, row(t, db, "SELECT text, text_chars, parts FROM prompts WHERE prompt_id = 'p'"), map[string]any{
		"text": nil, "text_chars": 0, "parts": []any{map[string]any{"role": "system", "note": "system", "text": "background task done"}}})
	checkScratch(t, db)
}

func TestPromptItems(t *testing.T) {
	db := fresh(t)
	aux := pcall("x", "s", "", t0+120*sec)
	aux.Call.RequestClass, aux.Call.Model, aux.Call.StopReason = "auxiliary", "claude-haiku-4-5", "end_turn"
	title := pcall("t", "s", "", t0+50*sec)
	title.Call.HiddenKind = "title"
	prewarm := pcall("w", "s", "", t0+50*sec)
	prewarm.Call.HiddenKind = "prewarm"
	suggestion := pcall("g", "s", "p2", t0+130*sec)
	suggestion.Call.HiddenKind = "suggestion"
	last := open(pcall("c", "s", "p2", t0+103*sec))
	write(t, db, pcall("a", "s", "p1", t0+1*sec), pcall("b", "s", "p2", t0+61*sec), title, prewarm, last, aux, suggestion,
		ev("B", 8, "s", t0+110*sec, "tengu_return_to_session", `{"hadRecap":true}`))
	drain(t, db)
	rows := row(t, db, `SELECT jsonb_agg(jsonb_build_object('seq', seq, 'type', type, 'prompt', prompt_id, 'n', prompt_index, 'tail', open_tail,
		'hidden', hidden, 'kind', between_kind, 'detail', detail, 'call', call_id) ORDER BY seq) AS items FROM prompt_items WHERE session_id = 's'`)["items"].([]any)
	want := []map[string]any{
		{"seq": 1, "type": "prompt", "prompt": "p1", "n": 1, "tail": false, "hidden": map[string]any{"title": float64(1)}},
		{"seq": 2, "type": "prompt", "prompt": "p2", "n": 2, "tail": true, "hidden": map[string]any{"prewarm": float64(1), "suggestion": float64(1)}},
		{"seq": 3, "type": "between", "kind": "call", "detail": "auxiliary · haiku · end_turn", "call": "x"},
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
	r.Call.RequestClass = ""
	return r
}

// A Codex turn's number is its place in the thread; its facts are its calls'.
func TestCodexPrompts(t *testing.T) {
	db := fresh(t)
	b := codex("b", "x", "t2", t0+10*sec)
	b.Call.ExtJSON = `{"workspaces":{"/home/u/repo/":{"latest_git_commit_hash":"0123456789"}},"sandbox_mode":"workspace-write","history_initialization":"cold_resume"}`
	b.Call.ProviderJSON = `{"x-codex-plan-type":"pro","x-codex-primary-used-percent":"12"}`
	write(t, db, codex("a", "x", "t1", t0), b)
	drain(t, db)
	expect(t, row(t, db, "SELECT product FROM prompts WHERE prompt_id = 't1'"), map[string]any{"product": "codex"})
	expect(t, row(t, db, "SELECT jsonb_object_agg(prompt_id, jsonb_build_array(prompt_index, open_tail)) AS n FROM prompt_items WHERE session_id = 'x'"),
		map[string]any{"n": map[string]any{"t1": []any{float64(1), false}, "t2": []any{float64(2), false}}})
	expect(t, row(t, db, "SELECT * FROM sessions WHERE session_id = 'x'"), map[string]any{
		"product": "codex", "repo": "repo", "commit": "0123456", "has_facts": true, "facts_cwd": "/home/u/repo/",
		"facts_approval": nil, "facts_sandbox": "workspace-write", "limits_ns": float64(t0 + 10*sec),
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
	env.Event.DeviceID, env.Event.PersonID = "dev-ev", "acct"
	write(t, db, a, b, title, env, pev("B", 1, "s", "p1", t0, "tengu_input_prompt", `{"prompt_index":4}`))
	drain(t, db)
	expect(t, row(t, db, "SELECT * FROM sessions WHERE session_id = 's'"), map[string]any{
		"start_ns": float64(t0 + 10*sec), "last_ns": float64(t0 + 20*sec), "product": "claude_code",
		"client": "cli", "client_version": "2.0.1", "client_version_ns": float64(t0 + 10*sec), "hidden_calls": 1,
		"own_title": "Fix the build", "prompts": 2, "model_calls": 2,
		"fields_missing": false, "person_id": "acct", "device_id": "dev-call", "networks": 2, "effort": "high",
		"tokens_input": 100, "tokens_output": 5, "outputs_known": 1,
		"tokens_accuracy": "estimated", "has_facts": false, "cli_version": "2.0.1", "rl_status": "allowed", "rl_util": 0.25,
		"rl_ns": float64(t0 + 10*sec), "unattributed": false, "turns": 2, "turns_interrupted": 0,
	})
	// A session no account appears on is unattributed; one with only analytics has no row.
	write(t, db, pcall("z", "u", "pz", t0), ev("C", 1, "only-events", t0, "tengu_api_query", `{}`))
	drain(t, db)
	expect(t, row(t, db, "SELECT unattributed, fields_missing FROM sessions WHERE session_id = 'u'"),
		map[string]any{"unattributed": true, "fields_missing": true})
	expect(t, row(t, db, "SELECT count(*) AS n FROM sessions WHERE session_id = 'only-events'"), map[string]any{"n": 0})
	checkScratch(t, db)
}

// An account's days sum its sessions' activity inside each day (owner
// decision 2), and an account seen on a session late still gets the
// session's earlier days.
func TestAccountDays(t *testing.T) {
	db := fresh(t)
	day := t0 - t0%dayNS
	late := day + dayNS - 30*sec // just before midnight
	write(t, db, pcall("a", "s", "p1", late), pcall("b", "s", "p2", day+dayNS+30*sec))
	drain(t, db)
	var n int
	db.QueryRow(ctx(t), "SELECT count(*) FROM account_days").Scan(&n)
	if n != 0 {
		t.Fatalf("%d day rows before any account appears", n)
	}
	seen := pcall("c", "s", "p2", day+dayNS+40*sec)
	seen.Call.PersonID = "acct"
	write(t, db, seen)
	drain(t, db)
	days := row(t, db, `SELECT jsonb_object_agg(day_ns::text, jsonb_build_object('agent', agent, 'sessions', sessions, 'ids', session_ids, 'call_ids', call_session_ids,
		'prompts', prompts, 'calls', model_calls, 'out', tokens_output, 'first', first_ns, 'last', last_ns)) AS d
		FROM account_days WHERE account_id = 'acct'`)["d"].(map[string]any)
	expect(t, days[fmt.Sprint(day)].(map[string]any), map[string]any{"agent": "claude-code", "sessions": 1, "ids": []any{"s"}, "call_ids": []any{"s"}, "prompts": 1,
		"calls": 1, "out": 5, "first": float64(late), "last": float64(late)})
	// The session is in both days' ids: a window over both counts it once (the directory's distinct sessions).
	expect(t, days[fmt.Sprint(day+dayNS)].(map[string]any), map[string]any{"sessions": 1, "ids": []any{"s"}, "prompts": 1, "calls": 2,
		"first": float64(day + dayNS + 30*sec), "last": float64(day + dayNS + 40*sec)})
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
	expect(t, row(t, db, "SELECT count(*) AS n FROM steps"), map[string]any{"n": 1})
	expect(t, row(t, db, "SELECT model_calls, start_ns FROM sessions WHERE session_id = 's'"), map[string]any{"model_calls": 1, "start_ns": float64(recent)})
	expect(t, row(t, db, "SELECT count(*) AS n, min(day_ns) AS d FROM account_days"), map[string]any{"n": 1, "d": float64(recent - recent%dayNS)})
	checkScratch(t, db)
}
