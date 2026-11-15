package render

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream/state"
)

// A test per dependency rule (docs/PIPELINE.md §3.11, v8's tests): each case
// changes one kind of input alone, run by run. After every run the rules'
// rows and running values are a whole render's of the same rows, and the run
// wrote exactly the rows it names (each "table key" its rows' first primary
// key column and the rest, "-" for a delete). A rule that misses an input its
// rows read fails the first check; one that writes a row the change can't
// reach fails the second.

const testKey, testSession = "s:s1", "s1"

type put struct {
	table, id string
	row       any // nil: removed
}

// ruleCase runs its steps (each a run's state changes), checking each run.
func ruleCase(t *testing.T, name string, runs [][]put, wrote [][]string) {
	t.Helper()
	rows := state.Rows{}
	r := newRules()
	for i, run := range runs {
		var changes []state.Change
		for _, p := range run {
			if rows[p.table] == nil {
				rows[p.table] = map[string]any{}
			}
			old := rows[p.table][p.id]
			if p.row == nil {
				delete(rows[p.table], p.id)
			} else {
				rows[p.table][p.id] = p.row
			}
			changes = append(changes, state.Change{Table: p.table, ID: p.id, Row: p.row, Old: old})
		}
		x := &ruleRun{r: r, key: testKey, session: testSession, rows: rows}
		x.apply(changes)
		w := &ruleRun{r: newRules(), key: testKey, session: testSession, rows: rows}
		w.apply(wholeChanges(rows))
		for _, c := range []struct {
			what      string
			inc, want any
		}{{"rows", r.Written, w.r.Written}, {"tool uses", r.ToolUses, w.r.ToolUses}, {"days", r.Days, w.r.Days},
			{"hours", r.Hours, w.r.Hours}, {"members", r.Members, w.r.Members}} {
			if !equalLoose(c.inc, c.want) {
				t.Fatalf("%s, run %d: the rules' %s aren't a whole render's:\n%v\n%v", name, i, c.what, c.inc, c.want)
			}
		}
		var got []string
		for _, td := range x.diffs() {
			for _, u := range td.Upserts {
				got = append(got, td.Table.Name+" "+fmtRow(u[:len(encOf(td.Table.Name).pk)]))
			}
			for _, d := range td.Deletes {
				got = append(got, td.Table.Name+" -"+fmtRow(d))
			}
		}
		sort.Strings(got)
		want := append([]string(nil), wrote[i]...)
		sort.Strings(want)
		if !reflect.DeepEqual(got, want) && len(got)+len(want) > 0 {
			t.Fatalf("%s, run %d: wrote %q, want %q", name, i, got, want)
		}
	}
}

func fmtRow(vs []any) string {
	parts := make([]string, len(vs))
	for i, v := range vs {
		parts[i] = fmt.Sprint(v)
	}
	return strings.Join(parts, " ")
}

func tool(sid, kind, server, name string, first int64) process.SessionTool {
	return process.SessionTool{SessionID: sid, Kind: kind, Server: server, Tool: name, FirstNS: first}
}

func TestRuleSessionTools(t *testing.T) {
	ruleCase(t, "session tools", [][]put{
		{{state.TableSessionTools, "t:a", tool(testSession, "builtin", "", "Read", 50)}},
		{{state.TableSessionTools, "t:a", tool(testSession, "builtin", "", "Read", 40)}}, // its first time moves
		{{state.TableSessionTools, "t:o", tool("other", "builtin", "", "Bash", 1)}},      // another session's: none
		{{state.TableSessionTools, "t:a", nil}},
	}, [][]string{
		{"session_tools s1 builtin  Read"},
		{"session_tools s1 builtin  Read"},
		nil,
		{"session_tools -s1 builtin  Read"},
	})
}

func use(callID string, ts int64, name string) state.CallToolUse {
	return state.CallToolUse{CallToolUse: process.CallToolUse{SessionID: testSession, TsNS: ts, Kind: "builtin", Tool: name}, CallID: callID}
}

func TestRuleCallToolUses(t *testing.T) {
	ruleCase(t, "call tool uses", [][]put{
		{{state.TableCallToolUses, "tu:c1 Read", use("c1", 10, "Read")}},
		{{state.TableCallToolUses, "tu:c2 Read", use("c2", 10, "Read")}}, // the same row: nothing new
		{{state.TableCallToolUses, "tu:c1 Read", nil}},                   // one of two left: it stays
		{{state.TableCallToolUses, "tu:c2 Read", use("c2", 20, "Read")}}, // its time moves: a row each
		{{state.TableCallToolUses, "tu:c2 Read", nil}},
	}, [][]string{
		{"call_tool_uses s1 10 builtin  Read"},
		nil,
		nil,
		{"call_tool_uses -s1 10 builtin  Read", "call_tool_uses s1 20 builtin  Read"},
		{"call_tool_uses -s1 20 builtin  Read"},
	})
}

func mark(kind string, group int32, ts int64) state.MarkRow {
	return state.MarkRow{Mark: process.SessionMark{SessionID: testSession, Kind: kind, TNS: ts}, Group: group}
}

func TestRuleSessionCallDays(t *testing.T) {
	const day = int64(86_400e9)
	ruleCase(t, "call days", [][]put{
		{{state.TableMarks, "c:1", mark("call", 0, 3*day+10)}},
		{{state.TableMarks, "c:2", mark("call", 0, 3*day+5)}},     // a new first
		{{state.TableMarks, "f:1", mark("friction", 2, 3*day+7)}}, // not a call mark
		{{state.TableMarks, "c:2", mark("call", 0, 4*day+1)}},     // moves a day: both days' rows
		{{state.TableMarks, "c:2", nil}, {state.TableMarks, "c:1", nil}},
	}, [][]string{
		{"session_call_days s1 3"},
		{"session_call_days s1 3"},
		nil,
		{"session_call_days s1 3", "session_call_days s1 4"},
		{"session_call_days -s1 3", "session_call_days -s1 4"},
	})
}

func req(id string, start, end int64, set func(*canon.Request)) canon.Request {
	c := canon.Request{CallID: id, StartNS: start, EndNS: end, Host: anthropicHost, Product: canon.ProductClaudeCode}
	if set != nil {
		set(&c)
	}
	return c
}

func TestRuleAgentCallsAndHours(t *testing.T) {
	const hour = int64(3600e9)
	ruleCase(t, "agent calls", [][]put{
		// In flight, then ended: its row's time, its hour.
		{{state.TableFactPending, "c1", canon.Pending{CorrelationID: "c1", Host: anthropicHost, StartNS: 2*hour + 5}}},
		{{state.TableFactPending, "c1", nil}, {state.TableFactRequests, "c1", req("c1", 2*hour+5, 2*hour+9, nil)}},
		// A second call in the hour; a synthetic one (none); one with no time (a row, no hour).
		{{state.TableFactRequests, "c2", req("c2", 2*hour+7, 0, nil)},
			{state.TableFactRequests, "p1", req("p1", 2*hour+8, 0, func(c *canon.Request) { c.Synthetic = true })},
			{state.TableFactRequests, "c3", req("c3", 0, 0, nil)}},
		// A call moves an hour; a Codex call, by its product.
		{{state.TableFactRequests, "c2", req("c2", 3*hour+1, 0, nil)},
			{state.TableFactRequests, "x1", req("x1", 3*hour+2, 0, func(c *canon.Request) { c.Host, c.Product = "chatgpt.com", canon.ProductCodex })}},
		// Gone: its row, and its hour's count.
		{{state.TableFactRequests, "c1", nil}},
	}, [][]string{
		{"agent_calls s:s1 c1", "agent_call_hours 7200000000000 s:s1"},
		nil, // the same time: nothing
		{"agent_calls s:s1 c2", "agent_calls s:s1 c3", "agent_call_hours 7200000000000 s:s1"},
		{"agent_calls s:s1 c2", "agent_calls s:s1 x1", "agent_call_hours 7200000000000 s:s1", "agent_call_hours 10800000000000 s:s1"},
		{"agent_calls -s:s1 c1", "agent_call_hours -7200000000000 s:s1"},
	})
}

func member(batch, key string, ns int64, sid, name string) state.BatchMember {
	return state.BatchMember{BatchID: batch, EventKey: key, NS: ns, SessionID: sid, Name: name}
}

func TestRuleBatchParts(t *testing.T) {
	ruleCase(t, "batch parts", [][]put{
		{{state.TableBatchMembers, "bm:e1", member("b1", "e1", 50, "s1", "tengu_b")}},
		{{state.TableBatchMembers, "bm:e2", member("b1", "e2", 40, "", "tengu_a")}}, // a new least name; no session
		{{state.TableBatchMembers, "bm:e3", member("b2", "e3", 10, "s1", "")}},      // another batch: its row alone
		{{state.TableBatchMembers, "bm:e2", member("b2", "e2", 40, "", "tengu_a")}}, // moves batches: both rows
		{{state.TableBatchMembers, "bm:e1", nil}},                                   // b1's last: gone
	}, [][]string{
		{"batch_parts b1 s:s1"},
		{"batch_parts b1 s:s1"},
		{"batch_parts b2 s:s1"},
		{"batch_parts b1 s:s1", "batch_parts b2 s:s1"},
		{"batch_parts -b1 s:s1"},
	})
}
