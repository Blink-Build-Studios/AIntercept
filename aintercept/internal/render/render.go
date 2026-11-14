// Package render turns a key's state into the rows it writes to Postgres's
// rendered tables (docs/PIPELINE.md v7 §2 step 6, §3.3-3.5), and those rows
// into the changes a commit writes:
//
//   - a session key's 9 session tables (state.RenderSession), with the two
//     re-encodings that keep a commit O(changed): prompt_rows.session_last_ns
//     stored as 0 and kept once as session_rows.prompt_last_ns, and
//     session_marks.seq keyed per block (block << 32 | rank in the block);
//   - its session_call_days (the directory's per-day call counts);
//   - every key's agent_calls and agent_call_hours, and its batch_parts.
//
// The worker's run, for a key (the commit itself is the worker's):
//
//	rows, err := render.Render(key, state.Rows(), day)    // day: the keys.day the commit writes
//	var d *render.Diff
//	switch {
//	case keyRow.n == 0:                         d = rows.Diff(nil)       // nothing written yet
//	case keyRow.render_version != render.Version: d = rows.Rewrite()      // another render's rows
//	default:                                    d = rows.Diff(baseline) // only the rows that changed
//	}
//	d.Queue(batch)                                // in the commit's transaction, after the guard
//	… COMMIT; then baseline = rows.Baseline()     // kept in memory with the warm state
//
// The baseline is the hashes of the rows the last commit wrote. It's kept in
// memory only: a cold load renders the replayed state with the day its key
// row holds, Render(key, decoded.Rows(), keyRow.day).Baseline(), which are
// the rows committed with that state as long as keyRow.render_version is
// Version. Postgres is never read for it. A lost guard or an unknown COMMIT
// drops it with the warm state.
package render

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/drawer"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream/state"
)

// Version is this build's render version, keys.render_version: bump it
// whenever the same state renders other rows, so each key's next commit
// rewrites its rendered rows whole (Rewrite) instead of diffing against rows
// an older build wrote.
const Version = 3

// The tables a render writes, in the order a diff writes them.
var tableNames = []string{
	"session_rows", "prompt_rows", "prompt_steps", "session_marks", "event_rows", "presence_segments",
	"session_tools", "call_tool_uses", "session_request_rows",
	"session_call_days", "agent_calls", "agent_call_hours", "batch_parts",
	"drawer_requests", "drawer_step_counts", "drawer_events",
}

// Tables is the tables a render writes (pg.Tables'), in the order a diff writes them.
var Tables []*pg.Table

func init() {
	for _, name := range tableNames {
		t := pg.TableNamed(name)
		if t == nil {
			panic("render: no table " + name + " in pg.Tables")
		}
		owner := "key"
		switch {
		case name == "session_request_rows":
			owner = "scope_session_id"
		case t.Kind == pg.Rendered || name == "session_call_days":
			owner = "session_id"
		}
		encs[name] = newEnc(t, owner)
		Tables = append(Tables, t)
	}
}

// OwnerColumn is the column holding a table's owner: the session (a session's
// tables and its call days) or the key (agent calls, their hours, batch
// parts). A rewrite deletes the key's rows by it.
func OwnerColumn(table string) string {
	e := encOf(table)
	return e.cols[e.owner].name
}

// The session_marks blocks, in the render's order: a new call renumbers no
// later block.
var markBlocks = map[string]int64{"call": 0, "reported": 1, "missed": 1, "friction": 2, "compaction": 3, "permission": 4, "network": 5}

// Rows is a key's rendered rows, hashed.
type Rows struct {
	Key     string
	Session string // the session a session key renders ("" for any other key)
	tables  []*rendered
	next    *Baseline
}

// rendered is one table's rows.
type rendered struct {
	enc   *tableEnc
	n     int
	row   func(i int) reflect.Value // a typed table's row i
	vals  [][]any                   // a fold's rows, in its columns
	extra map[int]func(i int) cell  // a typed table's extra columns
	keys  []string                  // each row's primary key (pkKey)
}

func (r *rendered) cell(i, c int) cell {
	if r.vals != nil {
		return anyCell(r.vals[i][c], r.enc.cols[c].kind)
	}
	if f := r.extra[c]; f != nil {
		return f(i)
	}
	if r.enc.cols[c].path == nil {
		return cell{null: true}
	}
	return fieldCell(r.row(i), &r.enc.cols[c])
}

func typed[T any](name string, rows []T, extra map[string]func(i int) cell) *rendered {
	e := encOf(name)
	r := &rendered{enc: e, n: len(rows), row: func(i int) reflect.Value { return reflect.ValueOf(&rows[i]).Elem() }}
	if len(extra) > 0 {
		r.extra = map[int]func(int) cell{}
		for i, c := range e.cols {
			if f, ok := extra[c.name]; ok {
				r.extra[i] = f
			}
		}
		if len(r.extra) != len(extra) {
			panic("render: " + name + " has no column for one of its extras")
		}
	}
	return r
}

func fold(name string, vals [][]any) *rendered {
	return &rendered{enc: encOf(name), n: len(vals), vals: vals}
}

// Render is key's rendered rows from its state's rows (KeyState.Rows()), with
// day its key row's day (keys.day, which session_rows.key_day repeats). It
// errors on rows the schema can't hold: two rows with one primary key, or a
// NULL in one.
//
// Render has no external inputs: the drawer is built from the calls' request
// rows and each key's own batch parts. RenderState renders a state with its own.
func Render(key string, rows state.Rows, day string) (*Rows, error) {
	return render(key, rows, nil, day, nil, nil)
}

// RenderState is key's rendered rows from its state: its rows and its
// external inputs (docs/PIPELINE.md v7 §3.7): the calls' wires read from their
// detail objects (WireID), and what other keys hold of its batches (PartsID).
func RenderState(key string, st *state.KeyState, day string) (*Rows, error) {
	return render(key, st.Rows(), st.Externals(), day, nil, nil)
}

// RenderWith is Render with external inputs given by id (WireID, PartsID).
func RenderWith(key string, rows state.Rows, ext map[string]any, day string) (*Rows, error) {
	return render(key, rows, ext, day, nil, nil)
}

// RenderNext is RenderWith after base's render, changed the state's rows
// changed since (a run's deltas): the drawer derives again only the rows those
// can have changed, and takes the rest from base (drawer.RenderNext, §3.7).
// With no base it renders every row. base is consumed: a baseline is the next
// render's once.
func RenderNext(key string, rows state.Rows, ext map[string]any, day string, base *Baseline, changed drawer.Changed) (*Rows, error) {
	var memo *drawer.Memo
	if base != nil {
		memo = base.drawer
	}
	return render(key, rows, ext, day, memo, changed)
}

// The external inputs' ids.
const (
	wirePrefix  = "wire:"
	partsPrefix = "parts:"
)

// WireID is the external input of a call's wire: a *drawer.Wire, or
// drawer.Missing where its detail object wasn't there.
func WireID(callID string) string { return wirePrefix + callID }

// PartsID is the external input of what other keys hold of a batch: a
// state.OtherParts (none: no other key holds any of it).
func PartsID(batchID string) string { return partsPrefix + batchID }

func render(key string, rows state.Rows, ext map[string]any, day string, memo *drawer.Memo, changed drawer.Changed) (*Rows, error) {
	out := &Rows{Key: key}
	others := map[string]state.OtherParts{}
	for id, v := range ext {
		if b, ok := strings.CutPrefix(id, partsPrefix); ok {
			if p, ok := v.(state.OtherParts); ok {
				others[b] = p
			}
		}
	}
	var events []process.EventRow
	if sid, ok := strings.CutPrefix(key, "s:"); ok {
		out.Session = sid
		st, ev, err := sessionTables(sid, rows, others, day)
		if err != nil {
			return nil, fmt.Errorf("render %s: %w", key, err)
		}
		out.tables, events = st, ev
	}
	calls := agentCalls(key, rows)
	out.tables = append(out.tables, fold("agent_calls", calls), fold("agent_call_hours", agentCallHours(key, calls)),
		fold("batch_parts", batchParts(key, rows)))
	reqs, evs, memo := drawer.RenderNext(drawer.Inputs{Key: key, Rows: rows, Parts: others, EventRows: events,
		Wire: func(callID string) *drawer.Wire {
			w, _ := ext[WireID(callID)].(*drawer.Wire)
			return w
		}}, memo, changed)
	rq := make([][]any, len(reqs))
	for i, r := range reqs {
		rq[i] = []any{r.ID, key, r.Data, r.Details, nil, nil}
		if r.StepSession != "" || r.StepPrompt != "" {
			rq[i][4], rq[i][5] = r.StepSession, r.StepPrompt
		}
	}
	counts := memo.StepCounts()
	sc := make([][]any, len(counts))
	for i, c := range counts {
		sc[i] = []any{c.Session, c.Prompt, key, c.N}
	}
	ev := make([][]any, len(evs))
	for i, e := range evs {
		ev[i] = []any{e.ID, key, e.Data, e.CorrelationID, e.EventKey}
	}
	out.tables = append(out.tables, fold("drawer_requests", rq), fold("drawer_step_counts", sc), fold("drawer_events", ev))
	if err := out.hash(); err != nil {
		return nil, fmt.Errorf("render %s: %w", key, err)
	}
	out.next.drawer = memo
	return out, nil
}

// DrawerDerived is how many drawer request rows the render derived; the rest
// were its base's.
func (r *Rows) DrawerDerived() int { return r.next.drawer.Derived() }

// sessionTables is session sid's 9 tables, re-encoded, and its call days.
func sessionTables(sid string, rows state.Rows, others map[string]state.OtherParts, day string) ([]*rendered, []process.EventRow, error) {
	st := state.RenderSessionWith(sid, rows, others)
	// prompt_rows.session_last_ns is kept once, on the session's row. The render
	// gives every prompt row the same one; a session with prompt rows and no
	// session row (none the state makes) keeps it on its prompt rows.
	var promptLast *int64
	if len(st.PromptRows) > 0 && len(st.SessionRows) > 0 {
		v := st.PromptRows[0].SessionLastNS
		promptLast = &v
		for i := range st.PromptRows {
			if st.PromptRows[i].SessionLastNS != v {
				return nil, nil, errors.New("prompt rows with different session_last_ns")
			}
			st.PromptRows[i].SessionLastNS = 0
		}
	}
	// session_marks.seq: block << 32 | rank in the block. The render's blocks
	// are contiguous and in markBlocks' order, so the key sorts as its seq.
	var rank [6]int64
	last := int64(-1)
	for i := range st.SessionMarks {
		m := &st.SessionMarks[i]
		b, ok := markBlocks[m.Kind]
		if !ok {
			return nil, nil, fmt.Errorf("a session mark of kind %q, in no block", m.Kind)
		}
		if b < last {
			return nil, nil, fmt.Errorf("session mark %d (%s) after a later block", m.Seq, m.Kind)
		}
		last = b
		m.Seq = b<<32 | rank[b]
		rank[b]++
	}
	var ownTitle *string
	for _, v := range rows[state.TableSessionRows] {
		if k, ok := v.(state.SessionRowK); ok && k.SessionID == sid {
			ownTitle = k.OwnTitle
		}
	}
	for i := range st.SessionRows {
		st.SessionRows[i].ChildrenJSON = nil // its children's keys': joined at read time (§4)
	}
	dayCell := cell{s: day}
	sessionExtra := map[string]func(int) cell{
		"own_title":      func(int) cell { return anyCell(ownTitle, kText) },
		"prompt_last_ns": func(int) cell { return anyCell(promptLast, kInt) },
		"key_day":        func(int) cell { return dayCell },
	}
	events := st.EventRows
	eventExtra := map[string]func(int) cell{"batch": func(i int) cell { return cell{s: BatchOf(events[i].EventID)} }}
	return []*rendered{
		typed("session_rows", st.SessionRows, sessionExtra),
		typed("prompt_rows", st.PromptRows, nil),
		typed("prompt_steps", st.PromptSteps, nil),
		typed("session_marks", st.SessionMarks, nil),
		typed("event_rows", st.EventRows, eventExtra),
		typed("presence_segments", st.PresenceSegments, nil),
		typed("session_tools", st.SessionTools, nil),
		typed("call_tool_uses", st.CallToolUses, nil),
		typed("session_request_rows", st.SessionRequestRows, nil),
		fold("session_call_days", callDays(sid, st.SessionMarks)),
	}, st.EventRows, nil
}

// BatchOf is an event id's batch, as the person summary's
// regexp_extract(event_id, '^(.*):[0-9]+$', 1) is: what's before its last
// ':', when digits follow it; else "". RE2's '.' doesn't match a newline.
func BatchOf(eventID string) string {
	i := strings.LastIndexByte(eventID, ':')
	if i < 0 || i == len(eventID)-1 || strings.ContainsRune(eventID[:i], '\n') {
		return ""
	}
	for _, c := range eventID[i+1:] {
		if c < '0' || c > '9' {
			return ""
		}
	}
	return eventID[:i]
}

// DayNumber is a time's day as _ACTIVITY computes it: floor(t_ns / 1e9 / 86400)
// in float64.
func DayNumber(tNS int64) int64 { return int64(math.Floor(float64(tNS) / 1e9 / 86400)) }

// callDays is session_call_days: per day, the session's call marks' count,
// first and last t_ns.
func callDays(sid string, marks []process.SessionMark) [][]any {
	type agg struct{ n, first, last int64 }
	days := map[int64]*agg{}
	for _, m := range marks {
		if m.Kind != "call" {
			continue
		}
		d := DayNumber(m.TNS)
		a := days[d]
		if a == nil {
			days[d] = &agg{1, m.TNS, m.TNS}
			continue
		}
		a.n++
		a.first, a.last = min(a.first, m.TNS), max(a.last, m.TNS)
	}
	out := make([][]any, 0, len(days))
	for d, a := range days {
		out = append(out, []any{sid, d, a.n, a.first, a.last})
	}
	sort.Slice(out, func(i, j int) bool { return out[i][1].(int64) < out[j][1].(int64) })
	return out
}

const anthropicHost = "api.anthropic.com"

// agentCalls is the key's agent_calls rows (key, call_id, ts_ns), as the
// merge's merge_agent_calls (rollup/merge.go agentCalls) and /overview count
// requests: non-synthetic ended calls whose product is claude_code or codex (a
// call to api.anthropic.com is Claude Code's when it has none), at their
// start, else their end (NULL: neither); calls in flight with a start, at it,
// by host (Claude Code's) or originator (Codex's).
func agentCalls(key string, rows state.Rows) [][]any {
	ts := map[string]any{}
	for id, v := range rows[state.TableFactRequests] {
		c, ok := v.(canon.Request)
		if !ok || c.Synthetic {
			continue
		}
		product := c.Product
		if product == "" && c.Host == anthropicHost {
			product = canon.ProductClaudeCode
		}
		if product != canon.ProductClaudeCode && product != canon.ProductCodex {
			continue
		}
		ns := c.StartNS
		if ns == 0 {
			ns = c.EndNS
		}
		if ns == 0 {
			ts[id] = nil
		} else {
			ts[id] = ns
		}
	}
	for id, v := range rows[state.TableFactPending] {
		p, ok := v.(canon.Pending)
		if !ok || p.Done || p.Synthetic || p.StartNS == 0 {
			continue
		}
		if p.Host != anthropicHost && !strings.HasPrefix(originator(p.ClientJSON), "codex") {
			continue
		}
		if _, ended := ts[id]; !ended {
			ts[id] = p.StartNS
		}
	}
	out := make([][]any, 0, len(ts))
	for id, ns := range ts {
		out = append(out, []any{key, id, ns})
	}
	sort.Slice(out, func(i, j int) bool { return out[i][1].(string) < out[j][1].(string) })
	return out
}

// originator is a call's client headers' originator ("" for none).
func originator(clientJSON string) string {
	if clientJSON == "" {
		return ""
	}
	var h struct {
		Originator string `json:"originator"`
	}
	if json.Unmarshal([]byte(clientJSON), &h) != nil {
		return ""
	}
	return h.Originator
}

// HourNS is the start of a time's UTC hour, Unix ns.
func HourNS(tNS int64) int64 {
	const hour = int64(3600e9)
	h := tNS / hour * hour
	if tNS < 0 && h != tNS {
		h -= hour
	}
	return h
}

// agentCallHours is agent_call_hours: the key's agent calls per UTC hour (a
// call with no time is in none).
func agentCallHours(key string, calls [][]any) [][]any {
	n := map[int64]int64{}
	for _, c := range calls {
		if ns, ok := c[2].(int64); ok {
			n[HourNS(ns)]++
		}
	}
	out := make([][]any, 0, len(n))
	for h, k := range n {
		out = append(out, []any{h, key, k})
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0].(int64) < out[j][0].(int64) })
	return out
}

// batchParts is batch_parts: per batch, the key's events of it (batch_members):
// their count, their least non-empty name, and the earliest (ns, session) that
// has a session. Read at query time as the sum, the least and the earliest
// over a batch's parts.
func batchParts(key string, rows state.Rows) [][]any {
	type part struct {
		n          int64
		name       string
		hasName    bool
		ns         int64
		session    string
		hasSession bool
	}
	parts := map[string]*part{}
	for _, v := range rows[state.TableBatchMembers] {
		m, ok := v.(state.BatchMember)
		if !ok {
			continue
		}
		p := parts[m.BatchID]
		if p == nil {
			p = &part{}
			parts[m.BatchID] = p
		}
		p.n++
		if m.Name != "" && (!p.hasName || m.Name < p.name) {
			p.name, p.hasName = m.Name, true
		}
		if m.SessionID != "" && (!p.hasSession || m.NS < p.ns || m.NS == p.ns && m.SessionID < p.session) {
			p.ns, p.session, p.hasSession = m.NS, m.SessionID, true
		}
	}
	out := make([][]any, 0, len(parts))
	for id, p := range parts {
		row := []any{id, key, p.n, nil, nil, nil}
		if p.hasName {
			row[3] = p.name
		}
		if p.hasSession {
			row[4], row[5] = p.ns, p.session
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0].(string) < out[j][0].(string) })
	return out
}

// hash computes every row's primary key and hash: the render's baseline.
func (r *Rows) hash() error {
	r.next = &Baseline{hashes: map[string]map[string]uint64{}}
	h := newHasher()
	for _, t := range r.tables {
		e := t.enc
		m := make(map[string]uint64, t.n)
		r.next.hashes[e.t.Name] = m
		t.keys = make([]string, t.n)
		pk := make([]cell, len(e.pk))
		kinds := make([]kind, len(e.pk))
		for j, c := range e.pk {
			kinds[j] = e.cols[c].kind
		}
		for i := 0; i < t.n; i++ {
			h.d.Reset()
			for c := range e.cols {
				h.cell(t.cell(i, c), e.cols[c].kind)
			}
			for j, c := range e.pk {
				pk[j] = t.cell(i, c)
				if pk[j].null {
					return fmt.Errorf("%s: a NULL %s", e.t.Name, e.cols[c].name)
				}
			}
			k := pkKey(pk, kinds)
			if _, dup := m[k]; dup {
				return fmt.Errorf("%s: two rows with the primary key %v", e.t.Name, keyValues(k))
			}
			m[k] = h.d.Sum64()
			t.keys[i] = k
		}
	}
	return nil
}

// Len is how many rows the render holds.
func (r *Rows) Len() int {
	n := 0
	for _, t := range r.tables {
		n += t.n
	}
	return n
}

// Baseline is the render's hashes: once its rows are committed, the next
// run's baseline.
func (r *Rows) Baseline() *Baseline { return r.next }

// Baseline is a render's rows' hashes, by table and primary key: what a commit
// wrote, kept in memory with the key's warm state.
type Baseline struct {
	hashes map[string]map[string]uint64
	drawer *drawer.Memo // the drawer's rows and what they read, for RenderNext
}

// Equal is whether two baselines hold the same rows.
func (b *Baseline) Equal(o *Baseline) bool {
	if b == nil || o == nil {
		return b == o
	}
	if len(b.hashes) != len(o.hashes) {
		return false
	}
	for t, m := range b.hashes {
		n := o.hashes[t]
		if len(m) != len(n) {
			return false
		}
		for k, h := range m {
			if g, ok := n[k]; !ok || g != h {
				return false
			}
		}
	}
	return true
}

// Len is how many rows the baseline holds.
func (b *Baseline) Len() int {
	n := 0
	if b != nil {
		for _, m := range b.hashes {
			n += len(m)
		}
	}
	return n
}

// Diff is what a commit writes to the rendered tables for one key: per table,
// upserts and deletes by primary key (schema.sql's). With Reset, every row
// the key owns is deleted first (a session's by its session id, the rest by
// the key), and Upserts hold the whole render.
type Diff struct {
	Key     string
	Session string
	Reset   bool
	Tables  []TableDiff // the tables with a change, in Tables' order
}

// TableDiff is one table's changes.
type TableDiff struct {
	Table *pg.Table
	// Upserts are the rows to insert or replace, each its columns' values in
	// Table.Columns' order: nil is NULL; text is string, bigint int64, double
	// precision float64, boolean bool, text[] []string, bigint[] []int64.
	Upserts [][]any
	// Deletes are the primary keys of the rows to delete, each in Table.PK's
	// order.
	Deletes [][]any
}

// Diff is the changes from base, the baseline of the rows already written (nil:
// none written): the rows whose hash changed or that are new are upserts, the
// rows base has and the render doesn't are deletes.
func (r *Rows) Diff(base *Baseline) *Diff {
	d := &Diff{Key: r.Key, Session: r.Session}
	for _, t := range r.tables {
		name := t.enc.t.Name
		var old map[string]uint64
		if base != nil {
			old = base.hashes[name]
		}
		cur := r.next.hashes[name]
		td := TableDiff{Table: t.enc.t}
		for i := 0; i < t.n; i++ {
			if h, ok := old[t.keys[i]]; !ok || h != cur[t.keys[i]] {
				td.Upserts = append(td.Upserts, t.values(i))
			}
		}
		var gone []string
		for k := range old {
			if _, ok := cur[k]; !ok {
				gone = append(gone, k)
			}
		}
		sort.Strings(gone)
		for _, k := range gone {
			td.Deletes = append(td.Deletes, keyValues(k))
		}
		if len(td.Upserts) > 0 || len(td.Deletes) > 0 {
			d.Tables = append(d.Tables, td)
		}
	}
	return d
}

// Rewrite is the whole render replacing every row the key owns: for a key row
// whose render_version isn't Version.
func (r *Rows) Rewrite() *Diff {
	d := r.Diff(nil)
	d.Reset = true
	return d
}

func (t *rendered) values(i int) []any {
	out := make([]any, len(t.enc.cols))
	for c := range t.enc.cols {
		out[c] = t.cell(i, c).value(t.enc.cols[c].kind)
	}
	return out
}

// Empty is whether the diff writes nothing.
func (d *Diff) Empty() bool { return !d.Reset && len(d.Tables) == 0 }

// Counts is how many rows the diff upserts and deletes (a reset's own deletes
// aside).
func (d *Diff) Counts() (upserts, deletes int) {
	for _, t := range d.Tables {
		upserts += len(t.Upserts)
		deletes += len(t.Deletes)
	}
	return upserts, deletes
}

// Changed is the first primary-key column's values of the rows the diff
// writes or deletes in table (a batch_parts diff's batches).
func (d *Diff) Changed(table string) []string {
	set := map[string]bool{}
	for _, t := range d.Tables {
		if t.Table.Name != table {
			continue
		}
		e := encOf(table)
		for _, r := range t.Upserts {
			if v, ok := r[e.pk[0]].(string); ok {
				set[v] = true
			}
		}
		for _, r := range t.Deletes {
			if v, ok := r[0].(string); ok {
				set[v] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
