package render

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
)

// Writing a diff (docs/PIPELINE.md §2 step 8.2): per table, one INSERT … SELECT
// FROM unnest(…) ON CONFLICT (primary key) DO UPDATE and one DELETE … WHERE
// (primary key) IN (SELECT * FROM unnest(…)), each with one array per column,
// queued on the commit's pgx batch. unnest can't take an array of arrays, so a
// list column goes as text[] of array literals, cast back per row.

// Queue adds the diff's statements to b, in its tables' order: a reset's
// deletes by owner first, then each table's deletes and upserts. The caller
// sends b in the commit's transaction, after the guard.
func (d *Diff) Queue(b *pgx.Batch) {
	if len(d.Templates) > 0 {
		var keys, ids, bodies []string
		for _, t := range d.Templates {
			keys, ids, bodies = append(keys, t[0].(string)), append(ids, t[1].(string)), append(bodies, t[2].(string))
		}
		b.Queue(`INSERT INTO drawer_templates (key, id, body) SELECT * FROM unnest($1::text[], $2::text[], $3::text[])
			ON CONFLICT (key, id) DO NOTHING`, keys, ids, bodies)
	}
	if d.Reset {
		for _, t := range Tables {
			e := encOf(t.Name)
			owner := d.Key
			if e.cols[e.owner].name != "key" {
				if d.Session == "" {
					continue // a key that isn't a session's owns no session rows
				}
				owner = d.Session
			}
			b.Queue(fmt.Sprintf("DELETE FROM %s WHERE %s = $1", t.Name, quote(e.cols[e.owner].name)), owner)
		}
	}
	for _, td := range d.Tables {
		e := encOf(td.Table.Name)
		s := statementsOf(e)
		if len(td.Deletes) > 0 {
			args := make([]any, len(e.pk))
			for j, c := range e.pk {
				args[j] = columnArg(e.cols[c].kind, td.Deletes, j)
			}
			b.Queue(s.delete, args...)
		}
		if len(td.Upserts) > 0 {
			args := make([]any, len(e.cols))
			for c := range e.cols {
				args[c] = columnArg(e.cols[c].kind, td.Upserts, c)
			}
			b.Queue(s.upsert, args...)
		}
	}
}

// Write sends the diff's statements in tx as one batch.
func (d *Diff) Write(ctx context.Context, tx pgx.Tx) error {
	b := &pgx.Batch{}
	d.Queue(b)
	if b.Len() == 0 {
		return nil
	}
	return tx.SendBatch(ctx, b).Close()
}

type statements struct{ upsert, delete string }

var stmtCache sync.Map // table name → *statements

func statementsOf(e *tableEnc) *statements {
	if s, ok := stmtCache.Load(e.t.Name); ok {
		return s.(*statements)
	}
	var cols, sel, arrays, set []string
	pk := map[string]bool{}
	for _, c := range e.t.PK {
		pk[c] = true
	}
	for i, c := range e.cols {
		q := quote(c.name)
		cols = append(cols, q)
		arrays = append(arrays, fmt.Sprintf("$%d::%s", i+1, arrayType(c.kind)))
		switch c.kind {
		case kTextList:
			sel = append(sel, "u."+q+"::text[]")
		case kIntList:
			sel = append(sel, "u."+q+"::bigint[]")
		default:
			sel = append(sel, "u."+q)
		}
		if !pk[c.name] {
			set = append(set, q+" = excluded."+q)
		}
	}
	conflict := "DO NOTHING"
	if len(set) > 0 {
		conflict = "DO UPDATE SET " + strings.Join(set, ", ")
	}
	var pkCols, pkArrays []string
	for j, c := range e.pk {
		pkCols = append(pkCols, quote(e.cols[c].name))
		pkArrays = append(pkArrays, fmt.Sprintf("$%d::%s", j+1, arrayType(e.cols[c].kind)))
	}
	s := &statements{
		upsert: fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM unnest(%s) AS u(%s) ON CONFLICT (%s) %s",
			e.t.Name, strings.Join(cols, ", "), strings.Join(sel, ", "), strings.Join(arrays, ", "), strings.Join(cols, ", "),
			strings.Join(pkCols, ", "), conflict),
		delete: fmt.Sprintf("DELETE FROM %s WHERE (%s) IN (SELECT * FROM unnest(%s))",
			e.t.Name, strings.Join(pkCols, ", "), strings.Join(pkArrays, ", ")),
	}
	stmtCache.Store(e.t.Name, s)
	return s
}

func quote(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

// arrayType is the parameter type of a column's values: a list column's are
// array literals.
func arrayType(k kind) string {
	switch k {
	case kInt:
		return "bigint[]"
	case kFloat:
		return "double precision[]"
	case kBool:
		return "boolean[]"
	default:
		return "text[]"
	}
}

// columnArg is column c of rows as one array parameter (a nil pointer is NULL).
func columnArg(k kind, rows [][]any, c int) any {
	switch k {
	case kText:
		out := make([]*string, len(rows))
		for i, r := range rows {
			if v, ok := r[c].(string); ok {
				out[i] = &v
			}
		}
		return out
	case kInt:
		out := make([]*int64, len(rows))
		for i, r := range rows {
			if v, ok := r[c].(int64); ok {
				out[i] = &v
			}
		}
		return out
	case kFloat:
		out := make([]*float64, len(rows))
		for i, r := range rows {
			if v, ok := r[c].(float64); ok {
				out[i] = &v
			}
		}
		return out
	case kBool:
		out := make([]*bool, len(rows))
		for i, r := range rows {
			if v, ok := r[c].(bool); ok {
				out[i] = &v
			}
		}
		return out
	default:
		out := make([]*string, len(rows))
		for i, r := range rows {
			if r[c] != nil {
				v := arrayLiteral(r[c])
				out[i] = &v
			}
		}
		return out
	}
}

// arrayLiteral is a list as a Postgres array literal: each text element quoted
// with its backslashes and quotes escaped.
func arrayLiteral(v any) string {
	var b strings.Builder
	b.WriteByte('{')
	switch x := v.(type) {
	case []string:
		for i, s := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteByte('"')
			for j := 0; j < len(s); j++ { // bytes: a quote or backslash is never part of another character
				if s[j] == '"' || s[j] == '\\' {
					b.WriteByte('\\')
				}
				b.WriteByte(s[j])
			}
			b.WriteByte('"')
		}
	case []int64:
		for i, n := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(strconv.FormatInt(n, 10))
		}
	default:
		panic(fmt.Sprintf("render: no array literal for %T", v))
	}
	b.WriteByte('}')
	return b.String()
}
