package render

import (
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream/state"
)

// The incremental render's rules (docs/PIPELINE.md §3.11): each table whose
// rows have stable keys has a dependency rule, which names the rows a run's
// state changes reach, and renders just those from the state. A rule keeps
// the running values it needs between runs, in the key's warm state (and its
// snapshot), so no run reads every row:
//
//	table              a change of                    reaches           running values
//	session_tools      a session tool                 its row           —
//	call_tool_uses     a call's tool use              its row           each row's tool uses
//	session_call_days  a call mark                    its day's row     each day's call times
//	agent_calls        a call's ending or start       its call's row    —
//	agent_call_hours   (agent_calls' rows)            their hours' rows each hour's count
//	batch_parts        a batch member                 its batch's row   each batch's members
//
// A rule's whole render is its incremental one from nothing, every row a
// change, so the two can't disagree. Each row it renders is compared with the
// row as last written (Written), and only a row that changed is written.
//
// Adding a table (build steps 3 to 7) is a rule: a func from a run's changes
// to the rows they change, with a test that changes each of its inputs alone
// (rules_test.go).

// VerifyIncremental makes every incremental render check its rules' rows and
// running values against a whole render's (the tests, and a gate's drain
// with AINTERCEPT_RENDER_VERIFY=1): a mismatch panics.
var VerifyIncremental = os.Getenv("AINTERCEPT_RENDER_VERIFY") == "1"

// verifyRules panics unless r, an incremental run's rules, are a whole
// render's of rows.
func verifyRules(key, session string, rows state.Rows, r *Rules) {
	w := &ruleRun{r: newRules(), key: key, session: session, rows: rows}
	w.apply(wholeChanges(rows))
	for _, c := range []struct {
		name      string
		inc, want any
	}{{"rows", r.Written, w.r.Written}, {"tool uses", r.ToolUses, w.r.ToolUses}, {"days", r.Days, w.r.Days},
		{"hours", r.Hours, w.r.Hours}, {"members", r.Members, w.r.Members}} {
		if !equalLoose(c.inc, c.want) {
			panic(fmt.Sprintf("render: key %s: the incremental rules' %s aren't a whole render's", key, c.name))
		}
	}
}

// equalLoose is reflect.DeepEqual, with an empty map or slice equal to a nil one.
func equalLoose(a, b any) bool {
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	if va.Kind() == reflect.Map {
		if va.Len() != vb.Len() {
			return false
		}
		for _, k := range va.MapKeys() {
			x, y := va.MapIndex(k), vb.MapIndex(k)
			if !y.IsValid() || !equalLoose(x.Interface(), y.Interface()) {
				return false
			}
		}
		return true
	}
	if va.Kind() == reflect.Slice && va.Len() == 0 && vb.Len() == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

// ruleTables are the tables the rules render, in Tables' order.
var ruleTables = []string{"session_tools", "call_tool_uses", "session_call_days", "agent_calls", "agent_call_hours", "batch_parts"}

func isRuleTable(name string) bool {
	for _, t := range ruleTables {
		if t == name {
			return true
		}
	}
	return false
}

// Rules is the rules' running values: part of a key's warm state.
type Rules struct {
	// Written is each rule table's rows as last written: table → primary key
	// (pkKey) → its columns' values.
	Written map[string]map[string][]any
	// ToolUses is how many of the state's tool uses render each call_tool_uses row.
	ToolUses map[process.CallToolUse]int
	// Days is each day's call mark times, sorted (session_call_days).
	Days map[int64][]int64
	// Hours is each UTC hour's agent call count (agent_call_hours).
	Hours map[int64]int64
	// Members is each batch's batch_members ids (batch_parts).
	Members map[string]map[string]bool
}

func newRules() *Rules {
	r := &Rules{Written: map[string]map[string][]any{}, ToolUses: map[process.CallToolUse]int{}, Days: map[int64][]int64{},
		Hours: map[int64]int64{}, Members: map[string]map[string]bool{}}
	for _, t := range ruleTables {
		r.Written[t] = map[string][]any{}
	}
	return r
}

// ruleRun is one run of the rules over a warm state's Rules, which it
// changes in place (a warm state is consumed by its next render, as the
// drawer's memo is: a run that doesn't commit drops it). It keeps each row it
// touched as it was before, for the diff.
type ruleRun struct {
	r       *Rules
	key     string
	session string // "" for a key that isn't a session's
	rows    state.Rows
	before  map[string]map[string]was // table → primary key → the row before the run
}

type was struct {
	row []any
	had bool
}

// put renders a row again: written when it isn't the row as last written.
func (x *ruleRun) put(table string, row []any) {
	e := encOf(table)
	row = normalize(e, row)
	k := rowKey(e, row)
	x.touch(table, k)
	x.r.Written[table][k] = row
}

// gone removes a row (by a row holding its primary key).
func (x *ruleRun) gone(table string, row []any) {
	e := encOf(table)
	k := rowKey(e, normalize(e, row))
	x.touch(table, k)
	delete(x.r.Written[table], k)
}

func (x *ruleRun) touch(table, k string) {
	if x.before == nil {
		x.before = map[string]map[string]was{}
	}
	if x.before[table] == nil {
		x.before[table] = map[string]was{}
	}
	if _, ok := x.before[table][k]; !ok {
		row, had := x.r.Written[table][k]
		x.before[table][k] = was{row, had}
	}
}

// diffs is the rows the run changed, per table in ruleTables' order.
func (x *ruleRun) diffs() []TableDiff {
	var out []TableDiff
	for _, name := range ruleTables {
		b := x.before[name]
		if len(b) == 0 {
			continue
		}
		td := TableDiff{Table: encOf(name).t}
		keys := make([]string, 0, len(b))
		for k := range b {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			now, has := x.r.Written[name][k]
			switch {
			case has && (!b[k].had || !reflect.DeepEqual(b[k].row, now)):
				td.Upserts = append(td.Upserts, now)
			case !has && b[k].had:
				td.Deletes = append(td.Deletes, keyValues(k))
			}
		}
		if len(td.Upserts) > 0 || len(td.Deletes) > 0 {
			out = append(out, td)
		}
	}
	return out
}

// writtenDiff is the rule tables' changes from one set of rows as written
// (nil: none) to another.
func writtenDiff(from, to map[string]map[string][]any) []TableDiff {
	var out []TableDiff
	for _, name := range ruleTables {
		td := TableDiff{Table: encOf(name).t}
		keys := make([]string, 0, len(to[name]))
		for k := range to[name] {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if old, ok := from[name][k]; !ok || !reflect.DeepEqual(old, to[name][k]) {
				td.Upserts = append(td.Upserts, to[name][k])
			}
		}
		var gone []string
		for k := range from[name] {
			if _, ok := to[name][k]; !ok {
				gone = append(gone, k)
			}
		}
		sort.Strings(gone)
		for _, k := range gone {
			td.Deletes = append(td.Deletes, keyValues(k))
		}
		if len(td.Upserts) > 0 || len(td.Deletes) > 0 {
			out = append(out, td)
		}
	}
	return out
}

// normalize is a row's values as Diff writes them (render's cells).
func normalize(e *tableEnc, row []any) []any {
	out := make([]any, len(e.cols))
	for c := range e.cols {
		out[c] = anyCell(row[c], e.cols[c].kind).value(e.cols[c].kind)
	}
	return out
}

// rowKey is a row's primary key, as pkKey.
func rowKey(e *tableEnc, row []any) string {
	cells := make([]cell, len(e.pk))
	kinds := make([]kind, len(e.pk))
	for j, c := range e.pk {
		cells[j], kinds[j] = anyCell(row[c], e.cols[c].kind), e.cols[c].kind
	}
	return pkKey(cells, kinds)
}

// typedRow is a typed table row's columns, as the whole render encodes them.
func typedRow[T any](table string, v T) []any { return typed(table, []T{v}, nil).values(0) }

// apply runs every rule over a run's changes.
func (x *ruleRun) apply(changes []state.Change) {
	var calls, batches map[string]bool
	for _, c := range changes {
		switch c.Table {
		case state.TableSessionTools:
			x.sessionTool(c)
		case state.TableCallToolUses:
			x.callToolUse(c)
		case state.TableMarks:
			x.callMark(c)
		case state.TableFactRequests, state.TableFactPending:
			if calls == nil {
				calls = map[string]bool{}
			}
			calls[c.ID] = true
		case state.TableBatchMembers:
			if batches == nil {
				batches = map[string]bool{}
			}
			x.member(c, batches)
		}
	}
	for _, id := range sortedKeys(calls) {
		x.agentCall(id)
	}
	for _, b := range sortedKeys(batches) {
		x.batchPart(b)
	}
}

// --- session_tools: a session tool's row ---------------------------------------

func (x *ruleRun) sessionTool(c state.Change) {
	if x.session == "" {
		return
	}
	if old, ok := c.Old.(process.SessionTool); ok && old.SessionID == x.session {
		x.gone("session_tools", typedRow("session_tools", old))
	}
	if cur, ok := c.Row.(process.SessionTool); ok && cur.SessionID == x.session {
		x.put("session_tools", typedRow("session_tools", cur))
	}
}

// --- call_tool_uses: the distinct rows of the calls' tool uses -----------------

func (x *ruleRun) callToolUse(c state.Change) {
	if x.session == "" {
		return
	}
	if old, ok := c.Old.(state.CallToolUse); ok && old.SessionID == x.session {
		u := old.CallToolUse
		if x.r.ToolUses[u]--; x.r.ToolUses[u] <= 0 {
			delete(x.r.ToolUses, u)
			x.gone("call_tool_uses", typedRow("call_tool_uses", u))
		}
	}
	if cur, ok := c.Row.(state.CallToolUse); ok && cur.SessionID == x.session {
		u := cur.CallToolUse
		if x.r.ToolUses[u]++; x.r.ToolUses[u] == 1 {
			x.put("call_tool_uses", typedRow("call_tool_uses", u))
		}
	}
}

// --- session_call_days: per day, the session's call marks ---------------------

// callMarkTime is a session_marks row's time when it's one of the session's
// call marks.
func (x *ruleRun) callMarkTime(v any) (int64, bool) {
	m, ok := v.(state.MarkRow)
	if !ok || m.Group != 0 || m.Mark.Kind != "call" || m.Mark.SessionID != x.session {
		return 0, false
	}
	return m.Mark.TNS, true
}

func (x *ruleRun) callMark(c state.Change) {
	if x.session == "" {
		return
	}
	days := map[int64]bool{}
	if t, ok := x.callMarkTime(c.Old); ok {
		d := DayNumber(t)
		x.r.Days[d] = removeSorted(x.r.Days[d], t)
		days[d] = true
	}
	if t, ok := x.callMarkTime(c.Row); ok {
		d := DayNumber(t)
		x.r.Days[d] = insertSorted(x.r.Days[d], t)
		days[d] = true
	}
	for d := range days {
		ts := x.r.Days[d]
		if len(ts) == 0 {
			delete(x.r.Days, d)
			x.gone("session_call_days", []any{x.session, d, nil, nil, nil})
			continue
		}
		x.put("session_call_days", []any{x.session, d, int64(len(ts)), ts[0], ts[len(ts)-1]})
	}
}

// insertSorted and removeSorted keep a sorted multiset.
func insertSorted(s []int64, v int64) []int64 {
	i := sort.Search(len(s), func(i int) bool { return s[i] >= v })
	s = append(s, 0)
	copy(s[i+1:], s[i:])
	s[i] = v
	return s
}

func removeSorted(s []int64, v int64) []int64 {
	i := sort.Search(len(s), func(i int) bool { return s[i] >= v })
	if i == len(s) || s[i] != v {
		return s
	}
	return append(s[:i], s[i+1:]...)
}

// --- agent_calls and agent_call_hours: a call's row, its hour's count ---------

// agentCall renders a call's agent_calls row again (agentCalls' rule for one
// call) and moves its hour's count.
func (x *ruleRun) agentCall(id string) {
	var ts any
	has := false
	if c, ok := x.rows[state.TableFactRequests][id].(canon.Request); ok && !c.Synthetic {
		product := c.Product
		if product == "" && c.Host == anthropicHost {
			product = canon.ProductClaudeCode
		}
		if product == canon.ProductClaudeCode || product == canon.ProductCodex {
			has = true
			if ns := c.StartNS; ns != 0 {
				ts = ns
			} else if c.EndNS != 0 {
				ts = c.EndNS
			}
		}
	}
	if !has {
		if p, ok := x.rows[state.TableFactPending][id].(canon.Pending); ok && !p.Done && !p.Synthetic && p.StartNS != 0 &&
			(p.Host == anthropicHost || strings.HasPrefix(originator(p.ClientJSON), "codex")) {
			has, ts = true, p.StartNS
		}
	}
	e := encOf("agent_calls")
	k := rowKey(e, normalize(e, []any{x.key, id, nil}))
	if old, ok := x.r.Written["agent_calls"][k]; ok {
		if has && reflect.DeepEqual(old[2], ts) {
			return
		}
		if ns, ok := old[2].(int64); ok {
			x.hour(HourNS(ns), -1)
		}
		if !has {
			x.gone("agent_calls", old)
			return
		}
	} else if !has {
		return
	}
	x.put("agent_calls", []any{x.key, id, ts})
	if ns, ok := ts.(int64); ok {
		x.hour(HourNS(ns), +1)
	}
}

func (x *ruleRun) hour(h, n int64) {
	x.r.Hours[h] += n
	if x.r.Hours[h] == 0 {
		delete(x.r.Hours, h)
		x.gone("agent_call_hours", []any{h, x.key, nil})
		return
	}
	x.put("agent_call_hours", []any{h, x.key, x.r.Hours[h]})
}

// --- batch_parts: a batch's part, from its members -----------------------------

func (x *ruleRun) member(c state.Change, batches map[string]bool) {
	if old, ok := c.Old.(state.BatchMember); ok {
		if m := x.r.Members[old.BatchID]; m != nil {
			delete(m, c.ID)
			if len(m) == 0 {
				delete(x.r.Members, old.BatchID)
			}
		}
		batches[old.BatchID] = true
	}
	if cur, ok := c.Row.(state.BatchMember); ok {
		m := x.r.Members[cur.BatchID]
		if m == nil {
			m = map[string]bool{}
			x.r.Members[cur.BatchID] = m
		}
		m[c.ID] = true
		batches[cur.BatchID] = true
	}
}

// batchPart renders a batch's part again from its members (batchParts' rule
// for one batch).
func (x *ruleRun) batchPart(batch string) {
	members := x.r.Members[batch]
	if len(members) == 0 {
		x.gone("batch_parts", []any{batch, x.key, nil, nil, nil, nil})
		return
	}
	var n, ns int64
	var name, session string
	var hasName, hasSession bool
	for id := range members {
		m, ok := x.rows[state.TableBatchMembers][id].(state.BatchMember)
		if !ok {
			continue
		}
		n++
		if m.Name != "" && (!hasName || m.Name < name) {
			name, hasName = m.Name, true
		}
		if m.SessionID != "" && (!hasSession || m.NS < ns || m.NS == ns && m.SessionID < session) {
			ns, session, hasSession = m.NS, m.SessionID, true
		}
	}
	row := []any{batch, x.key, n, nil, nil, nil}
	if hasName {
		row[3] = name
	}
	if hasSession {
		row[4], row[5] = ns, session
	}
	x.put("batch_parts", row)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// wholeChanges is every row of the state as a change from nothing: the whole
// render's input to the rules.
func wholeChanges(rows state.Rows) []state.Change {
	var out []state.Change
	for _, t := range []string{state.TableSessionTools, state.TableCallToolUses, state.TableMarks, state.TableFactRequests,
		state.TableFactPending, state.TableBatchMembers} {
		ids := make([]string, 0, len(rows[t]))
		for id := range rows[t] {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			out = append(out, state.Change{Table: t, ID: id, Row: rows[t][id]})
		}
	}
	return out
}
