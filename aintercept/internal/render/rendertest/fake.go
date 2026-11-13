// Package rendertest is an in-memory stand-in for Postgres's rendered tables
// (docs/PIPELINE.md, Testing: the Go layers on a fake): it applies a
// render.Diff as Postgres would (a reset's deletes by owner, deletes and
// upserts by primary key), and reads a session's rows back as the API loads
// them (prompt_rows.session_last_ns and session_marks.seq filled back).
package rendertest

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/render"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream/state"
)

// Fake is the rendered tables' rows: table → primary key → the row's values.
type Fake struct {
	tables map[string]map[string][]any
}

// New is an empty fake.
func New() *Fake { return &Fake{tables: map[string]map[string][]any{}} }

func colIndex(t *pg.Table, name string) int {
	for i, c := range t.Columns {
		if c.Name == name {
			return i
		}
	}
	panic("rendertest: " + t.Name + " has no column " + name)
}

func keyOf(vals []any) string { return fmt.Sprintf("%#v", vals) }

func pkOf(t *pg.Table, row []any) []any {
	out := make([]any, len(t.PK))
	for j, k := range t.PK {
		out[j] = row[colIndex(t, k)]
	}
	return out
}

// Apply applies a diff. Strict, it also refuses a write that changes nothing:
// an upsert of a row as stored, or a delete of a row that isn't there (a
// reset's writes aside), so a test sees that a diff writes only what changed.
func (f *Fake) Apply(d *render.Diff, strict bool) error {
	if d.Reset {
		for _, t := range render.Tables {
			owner := d.Key
			if render.OwnerColumn(t.Name) != "key" {
				owner = d.Session
			}
			i := colIndex(t, render.OwnerColumn(t.Name))
			for k, row := range f.tables[t.Name] {
				if owner != "" && row[i] == owner {
					delete(f.tables[t.Name], k)
				}
			}
		}
		strict = false
	}
	for _, td := range d.Tables {
		m := f.tables[td.Table.Name]
		if m == nil {
			m = map[string][]any{}
			f.tables[td.Table.Name] = m
		}
		for _, pk := range td.Deletes {
			k := keyOf(pk)
			if _, ok := m[k]; !ok && strict {
				return fmt.Errorf("%s: deleted %v, which isn't there", td.Table.Name, pk)
			}
			delete(m, k)
		}
		for _, row := range td.Upserts {
			if len(row) != len(td.Table.Columns) {
				return fmt.Errorf("%s: a row of %d values, %d columns", td.Table.Name, len(row), len(td.Table.Columns))
			}
			k := keyOf(pkOf(td.Table, row))
			if old, ok := m[k]; ok && strict && reflect.DeepEqual(old, row) {
				return fmt.Errorf("%s: upserted %v unchanged", td.Table.Name, pkOf(td.Table, row))
			}
			m[k] = row
		}
	}
	return nil
}

// Plant puts a row in as is (a row an older render wrote).
func (f *Fake) Plant(table string, row []any) {
	t := pg.TableNamed(table)
	if f.tables[table] == nil {
		f.tables[table] = map[string][]any{}
	}
	f.tables[table][keyOf(pkOf(t, row))] = row
}

// Rows is a table's rows, by primary key.
func (f *Fake) Rows(table string) map[string][]any { return f.tables[table] }

// Len is how many rows the fake holds.
func (f *Fake) Len() int {
	n := 0
	for _, m := range f.tables {
		n += len(m)
	}
	return n
}

// Diff names the first difference from want ("" when they hold the same rows).
func (f *Fake) Diff(want *Fake) string {
	names := map[string]bool{}
	for n := range f.tables {
		names[n] = true
	}
	for n := range want.tables {
		names[n] = true
	}
	var sorted []string
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	for _, n := range sorted {
		a, b := f.tables[n], want.tables[n]
		for k, row := range b {
			got, ok := a[k]
			if !ok {
				return fmt.Sprintf("%s: missing %s", n, k)
			}
			if !reflect.DeepEqual(got, row) {
				return fmt.Sprintf("%s %s:\n got %v\nwant %v", n, k, got, row)
			}
		}
		for k := range a {
			if _, ok := b[k]; !ok {
				return fmt.Sprintf("%s: extra %s", n, k)
			}
		}
	}
	return ""
}

// Written is the fake holding renders written whole.
func Written(rs ...*render.Rows) *Fake {
	f := New()
	for _, r := range rs {
		if err := f.Apply(r.Diff(nil), false); err != nil {
			panic(err)
		}
	}
	return f
}

// Sessions is every session with a row in a session table.
func (f *Fake) Sessions() []string {
	seen := map[string]bool{}
	for _, t := range render.Tables {
		if t.Kind != pg.Rendered {
			continue
		}
		i := colIndex(t, render.OwnerColumn(t.Name))
		for _, row := range f.tables[t.Name] {
			seen[row[i].(string)] = true
		}
	}
	var out []string
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Session is session sid's 9 tables as the API loads them: the rows in their
// primary key's order, the extra columns dropped, prompt_rows.session_last_ns
// from session_rows.prompt_last_ns and session_marks.seq by order.
func (f *Fake) Session(sid string) *state.SessionTables {
	st := &state.SessionTables{}
	var promptLast *int64
	for _, t := range render.Tables {
		if t.Kind != pg.Rendered {
			continue
		}
		owner := colIndex(t, render.OwnerColumn(t.Name))
		var rows [][]any
		for _, row := range f.tables[t.Name] {
			if row[owner] == sid {
				rows = append(rows, row)
			}
		}
		sort.Slice(rows, func(i, j int) bool { return lessPK(t, rows[i], rows[j]) })
		out := reflect.New(reflect.SliceOf(t.Row)).Elem()
		for _, row := range rows {
			v := reflect.New(t.Row).Elem()
			for c, col := range t.Columns {
				if col.Field == nil {
					if col.Name == "prompt_last_ns" && row[c] != nil {
						n := row[c].(int64)
						promptLast = &n
					}
					continue
				}
				set(v, col.Field, row[c])
			}
			out = reflect.Append(out, v)
		}
		reflect.ValueOf(st).Elem().FieldByName(fieldOf[t.Name]).Set(out)
	}
	for i := range st.PromptRows {
		if promptLast != nil {
			st.PromptRows[i].SessionLastNS = *promptLast
		}
	}
	for i := range st.SessionMarks {
		st.SessionMarks[i].Seq = int64(i)
	}
	return st
}

// fieldOf is each session table's SessionTables field.
var fieldOf = map[string]string{
	"session_rows": "SessionRows", "presence_segments": "PresenceSegments", "session_tools": "SessionTools",
	"event_rows": "EventRows", "session_marks": "SessionMarks", "prompt_rows": "PromptRows", "prompt_steps": "PromptSteps",
	"session_request_rows": "SessionRequestRows", "call_tool_uses": "CallToolUses",
}

func lessPK(t *pg.Table, a, b []any) bool {
	for _, k := range t.PK {
		i := colIndex(t, k)
		switch x := a[i].(type) {
		case string:
			if y := b[i].(string); x != y {
				return x < y
			}
		case int64:
			if y := b[i].(int64); x != y {
				return x < y
			}
		}
	}
	return false
}

// set stores a column's value in the row's field at path: NULL leaves the zero
// value (a nil pointer, or the zero an optional field stores as NULL).
func set(v reflect.Value, path []int, val any) {
	if val == nil {
		return
	}
	for _, i := range path {
		if v.Kind() == reflect.Pointer {
			if v.IsNil() {
				v.Set(reflect.New(v.Type().Elem()))
			}
			v = v.Elem()
		}
		v = v.Field(i)
	}
	if v.Kind() == reflect.Pointer {
		v.Set(reflect.New(v.Type().Elem()))
		v = v.Elem()
	}
	x := reflect.ValueOf(val)
	if v.Kind() == reflect.Slice && x.Len() == 0 {
		return // an empty list: the render's nil
	}
	if !x.Type().ConvertibleTo(v.Type()) {
		panic(fmt.Sprintf("rendertest: %T into %s", val, v.Type()))
	}
	v.Set(x.Convert(v.Type()))
}

// Folds is the fold tables' rows (session_call_days, agent_calls, agent_call_hours, batch_parts), each sorted.
func (f *Fake) Folds() map[string][]string {
	out := map[string][]string{}
	for _, t := range render.Tables {
		if t.Kind == pg.Rendered {
			continue
		}
		var rows []string
		for _, row := range f.tables[t.Name] {
			parts := make([]string, len(row))
			for i, v := range row {
				parts[i] = fmt.Sprint(v)
				if v == nil {
					parts[i] = "NULL"
				}
			}
			rows = append(rows, strings.Join(parts, " "))
		}
		sort.Strings(rows)
		out[t.Name] = rows
	}
	return out
}

// FromPostgres is a row as pgx's Values reads it, in the fake's values.
func FromPostgres(t *pg.Table, vals []any) []any {
	out := make([]any, len(vals))
	for i, v := range vals {
		switch x := v.(type) {
		case int32:
			out[i] = int64(x)
		case []any:
			if strings.HasPrefix(t.Columns[i].Type, "text[]") {
				l := make([]string, len(x))
				for j, e := range x {
					l[j] = e.(string)
				}
				out[i] = l
			} else {
				l := make([]int64, len(x))
				for j, e := range x {
					l[j] = e.(int64)
				}
				out[i] = l
			}
		default:
			out[i] = v
		}
	}
	return out
}
