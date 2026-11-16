package refresh

import (
	"encoding/json"
	"fmt"
	"sort"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// tcall is call id of prompt p in session s at `at`, asking for tools (in order).
func tcall(id, s, p string, at int64, tools ...string) records.Record {
	r := pcall(id, s, p, at)
	if len(tools) > 0 {
		var us []map[string]any
		for _, t := range tools {
			us = append(us, map[string]any{"name": t, "tool": t, "kind": "builtin", "count": 1})
		}
		b, _ := json.Marshal(us)
		r.Call.ToolUsesJSON = string(b)
	}
	return r
}

// workTurn is one turn of session s (prompt p1): it reads a file, edits it
// (+31 −9), runs `npm test` twice (fails, then passes, the second as `cd ui &&
// npm test`), commits, links PR #7 and replies; Claude Code's analytics for
// each, and its reports' costs.
func workTurn(s string, base int64) []records.Record {
	at := func(n int64) int64 { return base + n*sec }
	b := "W" + s
	return []records.Record{
		pev(b, 1, s, "p1", at(0), "tengu_input_prompt", `{"prompt_index":1,"prompt_length":212}`),
		tcall("r"+s, s, "p1", at(1), "Read"),
		tool(b, 2, s, "p1", at(1), "tengu_tool_use_success", "req-r"+s, `{"toolName":"Read","toolUseID":"tu-r","fileExtension":"TS"}`),
		pev(b, 3, s, "p1", at(1), "tengu_file_operation", `{"operation":"read","filePathHash":"h1"}`),
		tcall("e"+s, s, "p1", at(2), "Edit"),
		tool(b, 4, s, "p1", at(2), "tengu_tool_use_success", "req-e"+s, `{"toolName":"Edit","toolUseID":"tu-e","fileExtension":"ts"}`),
		pev(b, 5, s, "p1", at(2), "tengu_file_operation", `{"operation":"edit","filePathHash":"h1"}`),
		pev(b, 6, s, "p1", at(2), "tengu_file_changed", `{"lines_added":31,"lines_removed":9}`),
		tcall("x"+s, s, "p1", at(3), "Bash"),
		tool(b, 7, s, "p1", at(3), "tengu_tool_use_success", "req-x"+s, `{"toolName":"Bash","toolUseID":"tu-x`+s+`","bash_argv0":"npm","bash_subcommand":"test"}`),
		pev(b, 8, s, "p1", at(3), "tengu_bash_tool_command_executed", `{"tool_use_id":"tu-x`+s+`","bash_argv0":"npm","bash_subcommand":"test","exit_code":1}`),
		tcall("y"+s, s, "p1", at(4), "Bash"),
		tool(b, 9, s, "p1", at(4), "tengu_tool_use_success", "req-y"+s, `{"toolName":"Bash","toolUseID":"tu-y`+s+`","bash_argv0":"cd"}`),
		pev(b, 10, s, "p1", at(4), "tengu_bash_tool_command_executed", `{"tool_use_id":"tu-y`+s+`","bash_argv0":"cd","bash_last_argv0":"npm","exit_code":0}`),
		pev(b, 11, s, "p1", at(5), "tengu_git_operation", `{"operation":"commit"}`),
		pev(b, 12, s, "p1", at(6), "tengu_session_linked_to_pr", `{"prNumber":7}`),
		tcall("z"+s, s, "p1", at(6)),
		rep(b, 13, s, at(6), "req-z"+s),
		rep(b, 14, s, at(6), "req-x"+s),
	}
}

func TestTurnWork(t *testing.T) {
	db := fresh(t)
	rs := workTurn("s", t0)
	rs[len(rs)-5].Event.HeadSHA = "a41f"
	rs[len(rs)-4].Event.HeadSHA = "9c02"
	write(t, db, rs...)
	drain(t, db)
	turn := map[string]any{
		"reads": 1, "readFiles": 1, "readTypes": map[string]any{"ts": 1.0}, "edits": 1, "editedFiles": 1, "editTypes": map[string]any{"ts": 1.0},
		"linesAdded": 31, "linesRemoved": 9,
		"commands": []any{
			map[string]any{"program": "npm", "runs": 1.0, "failed": 0.0, "lastExit": 0.0},
			map[string]any{"program": "npm test", "runs": 1.0, "failed": 1.0, "lastExit": 1.0},
		},
		"subagents": []any{}, "web": map[string]any{"searches": 0.0, "fetches": 0.0}, "mcp": []any{},
		"otherTools": 0, "commits": 1, "pushes": 0, "prs": []any{7.0},
	}
	p := row(t, db, "SELECT ended_ns, cost_usd, work FROM prompts WHERE prompt_id = 'p1'")
	expect(t, p, map[string]any{"ended_ns": float64(t0 + 7*sec), "cost_usd": 1.0})
	expect(t, p["work"].(map[string]any), turn)

	// Each step's kind, program and work (its tool events by request id).
	for _, want := range []struct {
		step          int
		kind, program any
		reads, edits  int
	}{{1, "read", nil, 1, 0}, {2, "edit", nil, 0, 1}, {3, "run", "npm test", 0, 0}, {4, "run", "npm", 0, 0}, {5, "think", nil, 0, 0}} {
		got := row(t, db, "SELECT work_kind, program, work FROM steps WHERE prompt_id = 'p1' AND step = $1", want.step)
		expect(t, got, map[string]any{"work_kind": want.kind, "program": want.program})
		expect(t, got["work"].(map[string]any), map[string]any{"reads": want.reads, "edits": want.edits})
	}
	got := row(t, db, "SELECT work FROM steps WHERE prompt_id = 'p1' AND step = 3")["work"].(map[string]any)
	expect(t, got, map[string]any{"commands": []any{map[string]any{"program": "npm test", "runs": 1.0, "failed": 1.0, "lastExit": 1.0}}})

	// The session: its turns, their time, its cost and its work from its keys (no last exits: a
	// session keeps totals), its head range and its last test.
	s := row(t, db, "SELECT turns, turns_interrupted, active_ns, cost_usd, head_from, head_to, last_test, work FROM sessions WHERE session_id = 's'")
	expect(t, s, map[string]any{"turns": 1, "turns_interrupted": 0, "active_ns": float64(7 * sec), "cost_usd": 1.0,
		"head_from": "a41f", "head_to": "9c02",
		"last_test": map[string]any{"program": "npm test", "exit": 1.0, "t": float64(t0 + 3*sec)}})
	sw := s["work"].(map[string]any)
	expect(t, sw, map[string]any{"reads": 1, "readFiles": 1, "editedFiles": 1, "linesAdded": 31, "commits": 1, "prs": []any{7.0}})
	expect(t, sw, map[string]any{"commands": []any{
		map[string]any{"program": "npm", "runs": 1.0, "failed": 0.0, "lastExit": nil},
		map[string]any{"program": "npm test", "runs": 1.0, "failed": 1.0, "lastExit": nil},
	}})

	// The minute keys the Work lane reads.
	var keys []string
	rows, err := db.Query(ctx(t), `SELECT kind || '=' || key || ':' || n FROM session_minute_keys
		WHERE session_id = 's' AND kind LIKE 'w:%'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k)
	}
	want := []string{"w:commit=:1", "w:edit=ts:1", "w:edit_file=h1:1", "w:lines_added=:31", "w:lines_removed=:9",
		"w:pr=7:1", "w:read=ts:1", "w:read_file=h1:1", "w:run=npm test:1", "w:run=npm:1", "w:run_failed=npm test:1"}
	sort.Strings(want)
	sort.Strings(keys)
	if fmt.Sprint(keys) != fmt.Sprint(want) {
		t.Errorf("work keys = %v, want %v", keys, want)
	}
	checkScratch(t, db)
}

// A subagent's steps carry its id; a Codex turn's work is its own events'
// (commands with exit codes, file changes, accepted lines) and its steps' kinds
// are its calls' tools.
func TestSubagentAndCodexWork(t *testing.T) {
	db := fresh(t)
	sub := tcall("sa", "s", "p1", t0+sec, "Write")
	sub.Call.RequestClass, sub.Call.AgentID, sub.Call.AgentType = "subagent", "agent-1", "test-writer"
	cx := func(id string, at int64, tools string) records.Record {
		r := tcall(id, "cx", "turn-1", at)
		r.Call.Product, r.Call.Host, r.Call.Path, r.Call.Method, r.Call.ToolUsesJSON = canon.ProductCodex, "chatgpt.com", "/backend-api/codex/responses", "POST", tools
		return r
	}
	cev := func(n int64, at int64, name, md string) records.Record {
		r := pev("C", n, "cx", "turn-1", at, name, md)
		r.Event.Source = canon.ProductCodex
		return r
	}
	write(t, db, sub,
		cx("c1", t0, `[{"name":"exec","tool":"exec","kind":"builtin","count":1},{"name":"exec_command","tool":"exec_command","kind":"builtin","count":2}]`),
		cx("c2", t0+10*sec, `[{"name":"apply_patch","tool":"apply_patch","kind":"builtin","count":1}]`),
		cev(1, t0+1*sec, "codex_command_execution_event", `{"exit_code":1}`),
		cev(2, t0+2*sec, "codex_command_execution_event", `{"exit_code":0}`),
		cev(3, t0+11*sec, "codex_file_change_event", `{"tool_name":"apply_patch"}`),
		cev(4, t0+12*sec, "codex_accepted_line_fingerprints", `{"accepted_added_lines":12,"accepted_deleted_lines":3}`),
	)
	drain(t, db)
	expect(t, row(t, db, "SELECT agent_id, work_kind FROM steps WHERE prompt_id = 'p1'"), map[string]any{"agent_id": "agent-1", "work_kind": "edit"})
	expect(t, row(t, db, "SELECT work_kind FROM steps WHERE prompt_id = 'turn-1' AND step = 1"), map[string]any{"work_kind": "run"})
	expect(t, row(t, db, "SELECT work_kind FROM steps WHERE prompt_id = 'turn-1' AND step = 2"), map[string]any{"work_kind": "edit"})
	w := row(t, db, "SELECT work FROM prompts WHERE prompt_id = 'turn-1'")["work"].(map[string]any)
	expect(t, w, map[string]any{"edits": 1, "editedFiles": nil, "linesAdded": 12, "linesRemoved": 3,
		"commands": []any{map[string]any{"program": nil, "runs": 2.0, "failed": 1.0, "lastExit": 0.0}}})
	checkScratch(t, db)
}

// The People page's Working on reads the repos of an account's days.
func TestAccountDayRepos(t *testing.T) {
	db := fresh(t)
	a := tcall("a", "cx", "turn-1", t0)
	a.Call.Product, a.Call.Host, a.Call.Path, a.Call.Method, a.Call.PersonID = canon.ProductCodex, "chatgpt.com", "/backend-api/codex/responses", "POST", "acct"
	a.Call.ExtJSON = `{"workspaces":{"/work/pomodoro":{"latest_git_commit_hash":"1aa2f42879503b17"}}}`
	write(t, db, a)
	drain(t, db)
	expect(t, row(t, db, "SELECT repos FROM account_days WHERE account_id = 'acct'"), map[string]any{"repos": []any{"pomodoro"}})
}
