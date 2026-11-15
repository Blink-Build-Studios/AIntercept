// Package render turns a key's state into the rows it writes to Postgres's
// rendered tables (docs/PIPELINE.md §2 step 6, §3.3-3.5, §3.11), and those
// rows into the changes a commit writes:
//
//   - a session key's session tables (state.RenderSession), with the two
//     re-encodings that keep a commit O(changed): prompt_rows.session_last_ns
//     stored as 0 and kept once as session_rows.prompt_last_ns, and
//     session_marks.seq keyed per block (block << 32 | rank in the block);
//   - its session_call_days (the directory's per-day call counts);
//   - every key's agent_calls and agent_call_hours, and its batch_parts;
//   - every key's drawer rows (internal/drawer).
//
// The render is incremental (§3.11, v8). A key's warm state (Warm) is what
// its render keeps between runs, in memory and in its snapshot checkpoint:
//   - the rules' running values (rules.go): the tables whose rows have stable
//     keys render only the rows a run's state changes reach;
//   - the drawer's memo (§3.7);
//   - for the tables build steps 3 to 7 make incremental (event_rows,
//     prompt_rows, prompt_steps, session_marks, session_rows,
//     session_request_rows), the hashes of the rows last
//     written: until then those render whole and write the rows whose hash
//     changed.
//
// The worker's run, for a key (the commit itself is the worker's):
//
//	rows, err := render.RenderNext(key, st.Rows(), st.Externals(), day, warm, changes, extChanged)
//	var d *render.Diff
//	switch {
//	case keyRow.n == 0:                         d = rows.Diff(nil)  // nothing written yet: every row
//	case warm == nil, keyRow.render_version != render.Version:
//		d = rows.Rewrite() // rows no warm state describes, or another render's
//	default:                                    d = rows.Diff(warm) // only the rows that changed
//	}
//	d.Queue(batch)                                // in the commit's transaction, after the guard
//	… COMMIT; then warm = rows.Warm()             // kept with the state, and in its snapshot
//
// With no warm state (a key's first run, a render_version change, a key with
// no snapshot) RenderNext renders every row once. A warm state is consumed by
// the render after it, which changes it in place: a run that doesn't commit
// drops it with the state. A cold load decodes it from the key's snapshot and
// never renders.
package render

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/drawer"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/snap"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream/state"
)

// Version is this build's render version, keys.render_version: bump it
// whenever the same state renders other rows, so each key's next commit
// rewrites its rendered rows whole (Rewrite) instead of diffing against rows
// an older build wrote.
const Version = 4

// The tables a render writes, in the order a diff writes them.
var tableNames = []string{
	"session_rows", "prompt_rows", "prompt_steps", "session_marks", "event_rows",
	"session_tools", "call_tool_uses", "session_request_rows",
	"session_call_days", "agent_calls", "agent_call_hours", "batch_parts",
	"drawer_requests", "drawer_step_counts", "drawer_events",
}

// Tables is the tables a render writes (pg.Tables'), in the order a diff writes them.
var Tables []*pg.Table

// tableOrder is each table's place in tableNames.
var tableOrder = map[string]int{}

func init() {
	// The warm state, and the column values its rows as written hold.
	snap.Register(&Warm{}, "", int64(0), float64(0), false, []string{}, []int64{})
	for i, name := range tableNames {
		tableOrder[name] = i
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
var markBlocks = map[string]int64{"call": 0, "reported": 1, "missed": 1, "friction": 2, "compaction": 3}

// Rows is a key's render: the tables it rendered whole (hashed), the rules'
// run, and the drawer's.
type Rows struct {
	Key     string
	Session string // the session a session key renders ("" for any other key)
	tables  []*rendered
	next    *Warm
	// base is the warm state it rendered after (nil: a whole render): Diff
	// from it writes the rules' and the drawer's changes.
	base *Warm
	run  *ruleRun
	// Took is how long the render's parts took: the session's tables (whole,
	// hashed), the rules, and the drawer; and Diff's of the whole tables.
	Took struct{ Tables, Rules, Drawer, WholeDiff time.Duration }
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
	return render(key, rows, nil, day, nil, nil, nil)
}

// RenderState is key's rendered rows from its state: its rows and its
// external inputs (docs/PIPELINE.md v7 §3.7): the calls' wires read from their
// detail objects (WireID), and what other keys hold of its batches (PartsID).
func RenderState(key string, st *state.KeyState, day string) (*Rows, error) {
	return render(key, st.Rows(), st.Externals(), day, nil, nil, nil)
}

// RenderWith is Render with external inputs given by id (WireID, PartsID).
func RenderWith(key string, rows state.Rows, ext map[string]any, day string) (*Rows, error) {
	return render(key, rows, ext, day, nil, nil, nil)
}

// RenderNext is RenderWith after warm's render: changes are the state's
// changes since (a run's deltas: each (table, id)'s first Old and last Row),
// extChanged the external inputs set since. The rules and the drawer apply
// them to warm's and render only what they reach (§3.11, §3.7); Diff(warm) is
// what changed. With no warm state it renders every row. warm is consumed:
// it's the next render's once.
func RenderNext(key string, rows state.Rows, ext map[string]any, day string, warm *Warm, changes []state.Change,
	extChanged []string) (*Rows, error) {
	return render(key, rows, ext, day, warm, changes, extChanged)
}

// The external inputs' ids.
const (
	wirePrefix  = state.WirePrefix
	partsPrefix = "parts:"
)

// WireID is the external input of a call's wire: a *drawer.Wire, or
// drawer.Missing where its detail object wasn't there.
func WireID(callID string) string { return wirePrefix + callID }

// PartsID is the external input of what other keys hold of a batch: a
// state.OtherParts (none: no other key holds any of it).
func PartsID(batchID string) string { return partsPrefix + batchID }

func render(key string, rows state.Rows, ext map[string]any, day string, base *Warm, changes []state.Change,
	extChanged []string) (*Rows, error) {
	if base != nil && (base.Rules == nil || base.used) {
		panic("render: a warm state rendered from twice, or without its rules")
	}
	out := &Rows{Key: key, base: base}
	t0 := time.Now()
	var events []process.EventRow
	if sid, ok := strings.CutPrefix(key, "s:"); ok {
		out.Session = sid
		others := map[string]state.OtherParts{}
		for id, v := range ext {
			if b, ok := strings.CutPrefix(id, partsPrefix); ok {
				if p, ok := v.(state.OtherParts); ok {
					others[b] = p
				}
			}
		}
		st, ev, err := sessionTables(sid, rows, others, day)
		if err != nil {
			return nil, fmt.Errorf("render %s: %w", key, err)
		}
		out.tables, events = st, ev
	}
	if err := out.hash(); err != nil {
		return nil, fmt.Errorf("render %s: %w", key, err)
	}
	var memo *drawer.Memo
	var evChanged []process.EventRow
	if base != nil && base.Drawer != nil {
		// What the drawer reads of the session's tables: their changed event
		// rows, from the tables' own diff (their cost, not the drawer's).
		memo, evChanged = base.Drawer, changedEventRows(out, base, events)
	}
	out.Took.Tables = time.Since(t0)
	t0 = time.Now()
	// The rules: every row from nothing, or the run's changes.
	rules, ruleIn := base.rulesOf(), changes
	if base == nil {
		ruleIn = wholeChanges(rows)
	} else {
		base.used = true
	}
	out.run = &ruleRun{r: rules, key: key, session: out.Session, rows: rows}
	out.run.apply(ruleIn)
	if VerifyIncremental && base != nil {
		verifyRules(key, out.Session, rows, rules)
	}
	out.next.Rules = rules
	out.Took.Rules = time.Since(t0)
	t0 = time.Now()
	changed := drawer.Changed{}
	for _, c := range changes {
		if changed[c.Table] == nil {
			changed[c.Table] = map[string]bool{}
		}
		changed[c.Table][c.ID] = true
	}
	in := drawer.Inputs{Key: key, Rows: rows, EventRows: events,
		Wire: func(callID string) *drawer.Wire {
			w, _ := ext[WireID(callID)].(*drawer.Wire)
			return w
		},
		Part: func(batch string) (drawer.BatchParts, bool) {
			p, ok := ext[PartsID(batch)].(state.OtherParts)
			return p, ok
		}}
	if memo != nil {
		in.ExternalsChanged, in.EventRowsChanged = extChanged, evChanged
	}
	out.next.Drawer = drawer.RenderNext(in, memo, changed)
	out.Took.Drawer = time.Since(t0)
	return out, nil
}

// changedEventRows is the session's event rows whose hash isn't base's: what
// the drawer reads of them again.
func changedEventRows(r *Rows, base *Warm, events []process.EventRow) []process.EventRow {
	var out []process.EventRow
	old, cur := base.Hashes["event_rows"], r.next.Hashes["event_rows"]
	for _, t := range r.tables {
		if t.enc.t.Name != "event_rows" {
			continue
		}
		for i, k := range t.keys {
			if h, ok := old[k]; !ok || h != cur[k] {
				out = append(out, events[i])
			}
		}
	}
	return out
}

// DrawerDerived is how many drawer request rows the render derived; the rest
// were its base's.
func (r *Rows) DrawerDerived() int { return r.next.Drawer.Derived() }

// sessionTables is session sid's tables that render whole (the ones build
// steps 3 to 7 make incremental), re-encoded. Its session_tools,
// call_tool_uses and session_call_days are the rules'.
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
		typed("session_request_rows", st.SessionRequestRows, nil),
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

const anthropicHost = "api.anthropic.com"

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

// hash computes every whole table's rows' primary keys and hashes.
func (r *Rows) hash() error {
	r.next = &Warm{Hashes: map[string]map[string]uint64{}}
	h := newHasher()
	for _, t := range r.tables {
		e := t.enc
		m := make(map[string]uint64, t.n)
		r.next.Hashes[e.t.Name] = m
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
func (r *Rows) Len() int { return r.next.Len() }

// Warm is the render's warm state: once its rows are committed, the next
// run's.
func (r *Rows) Warm() *Warm { return r.next }

// Warm is what a key's render keeps between runs (docs/PIPELINE.md §2, v8):
// in memory with the warm state, and in the key's snapshot.
type Warm struct {
	// Hashes are the hashes of the whole tables' rows as last written, by
	// table and primary key (build steps 3 to 7 replace them with rules).
	Hashes map[string]map[string]uint64
	// Rules are the rules' running values and rows as last written.
	Rules *Rules
	// Drawer is the drawer's rows and what they read (§3.7).
	Drawer *drawer.Memo
	used   bool // rendered from: consumed
}

// rulesOf is the warm state's rules, or new ones with none.
func (b *Warm) rulesOf() *Rules {
	if b == nil {
		return newRules()
	}
	return b.Rules
}

// Equal is whether two warm states hold the same rows and running values.
func (b *Warm) Equal(o *Warm) bool {
	if b == nil || o == nil {
		return b == o
	}
	if !drawerEqual(b.Drawer, o.Drawer) || !reflect.DeepEqual(b.Hashes, o.Hashes) {
		return false
	}
	if (b.Rules == nil) != (o.Rules == nil) {
		return false
	}
	return b.Rules == nil || reflect.DeepEqual(b.Rules.Written, o.Rules.Written) && reflect.DeepEqual(b.Rules.ToolUses, o.Rules.ToolUses) &&
		reflect.DeepEqual(b.Rules.Days, o.Rules.Days) && reflect.DeepEqual(b.Rules.Hours, o.Rules.Hours) &&
		reflect.DeepEqual(b.Rules.Members, o.Rules.Members)
}

// Len is how many rows the warm state holds.
func (b *Warm) Len() int {
	n := 0
	if b == nil {
		return 0
	}
	for _, m := range b.Hashes {
		n += len(m)
	}
	if b.Rules != nil {
		for _, m := range b.Rules.Written {
			n += len(m)
		}
	}
	if m := b.Drawer; m != nil {
		reqs, evs, counts := m.All()
		n += len(reqs) + len(evs) + len(counts)
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
	// Templates are the drawer templates (key, id, body) its drawer rows
	// reference that the key hasn't written yet: inserted first (unless
	// there), as the rows' foreign key needs.
	Templates [][]any
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

// Diff is the changes from base, the warm state of the rows already written
// (nil: none written). The whole tables' rows whose hash changed or that are
// new are upserts, the rows base has and the render doesn't are deletes. The
// rules' and the drawer's tables are their changes from the warm state they
// rendered after (all their rows from nil, none from the render's own).
func (r *Rows) Diff(base *Warm) *Diff {
	d := &Diff{Key: r.Key, Session: r.Session}
	t0 := time.Now()
	for _, t := range r.tables {
		name := t.enc.t.Name
		var old map[string]uint64
		if base != nil {
			old = base.Hashes[name]
		}
		cur := r.next.Hashes[name]
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
	r.Took.WholeDiff = time.Since(t0)
	d.Tables = append(d.Tables, r.ruleDiff(base)...)
	d.Tables = append(d.Tables, r.drawerDiff(base)...)
	sort.SliceStable(d.Tables, func(i, j int) bool { return tableOrder[d.Tables[i].Table.Name] < tableOrder[d.Tables[j].Table.Name] })
	// The templates its drawer rows reference that the key hasn't written.
	for _, td := range d.Tables {
		if td.Table.Name != "drawer_requests" || r.next.Drawer == nil {
			continue
		}
		var ids []string
		for _, row := range td.Upserts {
			for _, c := range []int{2, 4} {
				if id, ok := row[c].(string); ok {
					ids = append(ids, id)
				}
			}
		}
		for _, t := range r.next.Drawer.Unwritten(ids) {
			d.Templates = append(d.Templates, []any{r.Key, t[0], t[1]})
		}
	}
	return d
}

// ruleDiff is the rule tables' changes from base.
func (r *Rows) ruleDiff(base *Warm) []TableDiff {
	switch {
	case base == r.base:
		return r.run.diffs()
	case base != nil && base.Rules == r.next.Rules:
		return nil
	}
	var from map[string]map[string][]any
	if base != nil && base.Rules != nil {
		from = base.Rules.Written
	}
	return writtenDiff(from, r.next.Rules.Written)
}

// drawerDiff is the drawer's tables' changes from base.
func (r *Rows) drawerDiff(base *Warm) []TableDiff {
	m := r.next.Drawer
	var delta drawer.Delta
	switch {
	case m == nil || base == r.next:
		return nil
	case base == nil:
		delta.Requests, delta.Events, delta.Counts = m.All()
	case base == r.base && m.Delta() != nil:
		delta = *m.Delta()
	case base.Drawer != nil:
		delta = drawer.Between(base.Drawer, m)
	default:
		delta.Requests, delta.Events, delta.Counts = m.All()
	}
	key := r.Key
	rq := TableDiff{Table: pg.TableNamed("drawer_requests")}
	for _, q := range delta.Requests {
		row := []any{q.ID, key, q.DataTpl, q.DataVals, q.DetailsTpl, q.DetailsVals, nil, nil}
		if q.StepSession != "" || q.StepPrompt != "" {
			row[6], row[7] = q.StepSession, q.StepPrompt
		}
		rq.Upserts = append(rq.Upserts, row)
	}
	for _, id := range delta.GoneRequests {
		rq.Deletes = append(rq.Deletes, []any{id, key})
	}
	sc := TableDiff{Table: pg.TableNamed("drawer_step_counts")}
	for _, c := range delta.Counts {
		sc.Upserts = append(sc.Upserts, []any{c.Session, c.Prompt, key, c.N})
	}
	for _, c := range delta.GoneCounts {
		sc.Deletes = append(sc.Deletes, []any{c.Session, c.Prompt, key})
	}
	ev := TableDiff{Table: pg.TableNamed("drawer_events")}
	for _, e := range delta.Events {
		ev.Upserts = append(ev.Upserts, []any{e.ID, key, e.Data, e.CorrelationID, e.EventKey})
	}
	for _, id := range delta.GoneEvents {
		ev.Deletes = append(ev.Deletes, []any{id, key})
	}
	var out []TableDiff
	for _, td := range []TableDiff{rq, sc, ev} {
		if len(td.Upserts) > 0 || len(td.Deletes) > 0 {
			out = append(out, td)
		}
	}
	return out
}

// drawerEqual is whether two drawer memos hold the same rows.
func drawerEqual(a, b *drawer.Memo) bool {
	if a == nil || b == nil {
		return a == b
	}
	ar, ae, ac := a.All()
	br, be, bc := b.All()
	if len(ar) != len(br) || len(ae) != len(be) || len(ac) != len(bc) {
		return false
	}
	for i := range ar {
		x, y := ar[i], br[i]
		if x.ID != y.ID || x.DataVals != y.DataVals || x.DetailsVals != y.DetailsVals || a.Template(x.DataTpl) != b.Template(y.DataTpl) ||
			a.Template(x.DetailsTpl) != b.Template(y.DetailsTpl) || x.StepSession != y.StepSession || x.StepPrompt != y.StepPrompt {
			return false
		}
	}
	for i := range ae {
		if ae[i] != be[i] {
			return false
		}
	}
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
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
