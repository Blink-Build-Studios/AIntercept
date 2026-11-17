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

// rcall is call id of prompt p in session s at `at` with its tool calls
// recorded (detail's calls, JSON) and the results of earlier ones it carries
// back (detail's results, JSON).
func rcall(id, s, p string, at int64, calls, results string) records.Record {
	r := pcall(id, s, p, at)
	d := `{"calls":` + calls
	if results != "" {
		d += `,"results":` + results
	}
	r.Call.DetailJSON = d + "}"
	return r
}

// workTurn is one turn of session s (prompt p1), recorded: it reads a file,
// edits it (+31 −9), runs `npm test` twice (fails, then passes), commits and
// replies, each tool call's result carried back by the next call.
func workTurn(s string, base int64) []records.Record {
	at := func(n int64) int64 { return base + n*sec }
	return []records.Record{
		rcall("r"+s, s, "p1", at(1), `[{"id":"tr","tool":"Read","file":"ui/a.TS"}]`, ""),
		rcall("e"+s, s, "p1", at(2), `[{"id":"te","tool":"Edit","file":"ui/a.TS","patches":[{"file":"ui/a.TS","added":31,"removed":9}]}]`, `{"tr":{}}`),
		rcall("x"+s, s, "p1", at(3), `[{"id":"tx","tool":"Bash","commands":["npm test"]}]`, `{"te":{}}`),
		rcall("y"+s, s, "p1", at(4), `[{"id":"ty","tool":"Bash","commands":["npm test"]}]`, `{"tx":{"exit":[1]}}`),
		rcall("c"+s, s, "p1", at(5), `[{"id":"tc","tool":"Bash","commands":["git commit"]}]`, `{"ty":{"exit":[0]}}`),
		rcall("z"+s, s, "p1", at(6), `[]`, `{"tc":{"exit":[0]}}`),
		pev("W"+s, 1, s, "p1", at(6), "tengu_session_linked_to_pr", `{"prNumber":7}`), // analytics: not loaded
	}
}

func TestTurnWork(t *testing.T) {
	db := fresh(t)
	write(t, db, workTurn("s", t0)...)
	drain(t, db)
	turn := map[string]any{
		"reads": 1, "readFiles": 1, "readTypes": map[string]any{"ts": 1.0}, "edits": 1, "editedFiles": 1, "editTypes": map[string]any{"ts": 1.0},
		"linesAdded": 31, "linesRemoved": 9,
		"commands": []any{
			map[string]any{"program": "npm test", "runs": 2.0, "failed": 1.0, "lastExit": 0.0},
			map[string]any{"program": "git commit", "runs": 1.0, "failed": 0.0, "lastExit": 0.0},
		},
		"subagents": []any{}, "web": map[string]any{"searches": 0.0, "fetches": 0.0}, "mcp": []any{},
		"otherTools": 0, "commits": 1, "pushes": 0, "prs": []any{},
	}
	p := row(t, db, "SELECT ended_ns, work FROM prompts WHERE prompt_id = 'p1'")
	expect(t, p, map[string]any{"ended_ns": float64(t0 + 7*sec)})
	expect(t, p["work"].(map[string]any), turn)

	// Each step's kind, program and pieces of work (its tool calls, with the exit codes their results carried back).
	for _, want := range []struct {
		call          string
		kind, program any
		pieces        []any
	}{
		{"rs", "read", nil, []any{[]any{"w:read", "ts", 1.0, nil}, []any{"w:read_file", "ui/a.TS", 1.0, nil}}},
		{"es", "edit", nil, []any{[]any{"w:edit", "ts", 1.0, nil}, []any{"w:edit_file", "ui/a.TS", 1.0, nil},
			[]any{"w:lines_added", "", 31.0, nil}, []any{"w:lines_removed", "", 9.0, nil}}},
		{"xs", "run", "npm test", []any{[]any{"w:run", "npm test", 1.0, 1.0}, []any{"w:run_failed", "npm test", 1.0, nil}}},
		{"ys", "run", "npm test", []any{[]any{"w:run", "npm test", 1.0, 0.0}}},
		{"cs", "run", "git commit", []any{[]any{"w:commit", "", 1.0, nil}, []any{"w:run", "git commit", 1.0, 0.0}}},
		{"zs", "think", nil, []any{}},
	} {
		got := row(t, db, "SELECT work_kind, program, work_pieces FROM steps WHERE call_id = $1", want.call)
		expect(t, got, map[string]any{"work_kind": want.kind, "program": want.program, "work_pieces": want.pieces})
	}

	// The session: its turns, their time, and its work from its keys (no last exits: a session keeps totals).
	s := row(t, db, "SELECT turns, turns_interrupted, active_ns, work FROM sessions WHERE session_id = 's'")
	expect(t, s, map[string]any{"turns": 1, "turns_interrupted": 0, "active_ns": float64(6 * sec)})
	sw := s["work"].(map[string]any)
	expect(t, sw, map[string]any{"reads": 1, "readFiles": 1, "editedFiles": 1, "linesAdded": 31, "commits": 1, "prs": []any{}})
	expect(t, sw, map[string]any{"commands": []any{
		map[string]any{"program": "npm test", "runs": 2.0, "failed": 1.0, "lastExit": nil},
		map[string]any{"program": "git commit", "runs": 1.0, "failed": 0.0, "lastExit": nil},
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
	want := []string{"w:commit=:1", "w:edit=ts:1", "w:edit_file=ui/a.TS:1", "w:lines_added=:31", "w:lines_removed=:9",
		"w:read=ts:1", "w:read_file=ui/a.TS:1", "w:run=git commit:1", "w:run=npm test:2", "w:run_failed=npm test:1"}
	sort.Strings(want)
	sort.Strings(keys)
	if fmt.Sprint(keys) != fmt.Sprint(want) {
		t.Errorf("work keys = %v, want %v", keys, want)
	}
	checkScratch(t, db)
}

// A content-free call's work is its tools' names and counts; a subagent's
// steps carry its id; a Codex exec script's commands are run pieces with the
// exit codes its result carries back.
func TestSubagentAndCodexWork(t *testing.T) {
	db := fresh(t)
	sub := tcall("sa", "s", "p1", t0+sec, "Write")
	sub.Call.RequestClass, sub.Call.AgentID, sub.Call.AgentType = "subagent", "agent-1", "test-writer"
	cx := func(r records.Record) records.Record {
		r.Call.Product, r.Call.Host, r.Call.Path, r.Call.Method = canon.ProductCodex, "chatgpt.com", "/backend-api/codex/responses", "POST"
		r.Call.RequestClass = ""
		return r
	}
	c1 := cx(rcall("c1", "cx", "turn-1", t0, `[{"id":"e1","tool":"exec","commands":["go test","ls"]}]`, ""))
	c2 := cx(rcall("c2", "cx", "turn-1", t0+10*sec, `[{"id":"p1","tool":"apply_patch","patches":[{"file":"a.go","added":12,"removed":3}]}]`,
		`{"e1":{"exit":[1,0]}}`))
	write(t, db, sub, c1, c2)
	drain(t, db)
	expect(t, row(t, db, "SELECT agent_id, work_kind FROM steps WHERE prompt_id = 'p1'"), map[string]any{"agent_id": "agent-1", "work_kind": "edit"})
	expect(t, row(t, db, "SELECT work_kind FROM steps WHERE call_id = 'c1'"), map[string]any{"work_kind": "run"})
	expect(t, row(t, db, "SELECT work_kind FROM steps WHERE call_id = 'c2'"), map[string]any{"work_kind": "edit"})
	w := row(t, db, "SELECT work FROM prompts WHERE prompt_id = 'turn-1'")["work"].(map[string]any)
	expect(t, w, map[string]any{"edits": 1, "editedFiles": 1, "linesAdded": 12, "linesRemoved": 3,
		"commands": []any{map[string]any{"program": "go test", "runs": 1.0, "failed": 1.0, "lastExit": 1.0},
			map[string]any{"program": "ls", "runs": 1.0, "failed": 0.0, "lastExit": 0.0}}})
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
