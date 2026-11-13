package pg

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
)

// The commit's row writes (docs/PIPELINE.md §2 Run 8.2): each table gets one
// INSERT … SELECT FROM unnest(…) ON CONFLICT (primary key) DO UPDATE and one
// DELETE … WHERE (primary key) IN (SELECT FROM unnest(…)), queued into the
// commit's pgx batch.

// Changes is one table's rows to write in a commit: each upsert's values in
// the table's column order (Table.Columns, extras included), each delete's
// primary-key values in PK order. A value is nil (NULL), a string, []byte,
// int64, float64, bool, []string or []int64.
type Changes struct {
	Table   string
	Upserts [][]any
	Deletes [][]any
}

// RowValues is a row's values for the table's row columns (those read from the
// row type: every column but the leading key and id of a stored table and the
// trailing extras), as the table codec writes them: a nil pointer is NULL, an
// `optional` field's zero value is NULL, a list is never NULL.
func (t *Table) RowValues(row any) []any {
	rv := reflect.ValueOf(row)
	out := make([]any, 0, len(t.Columns))
	for _, c := range t.Columns {
		if c.Field == nil {
			continue
		}
		out = append(out, fieldValue(rv, c))
	}
	return out
}

func fieldValue(rv reflect.Value, c Column) any {
	v := rv
	for _, i := range c.Field {
		if v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return nil
			}
			v = v.Elem()
		}
		v = v.Field(i)
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	} else if c.Optional && v.IsZero() {
		return nil
	}
	switch v.Kind() {
	case reflect.String:
		return v.String()
	case reflect.Int64, reflect.Int, reflect.Int32:
		return v.Int()
	case reflect.Uint64, reflect.Uint32:
		return int64(v.Uint())
	case reflect.Float64, reflect.Float32:
		return v.Float()
	case reflect.Bool:
		return v.Bool()
	case reflect.Slice:
		switch v.Type().Elem().Kind() {
		case reflect.String:
			out := make([]string, v.Len())
			for i := range out {
				out[i] = v.Index(i).String()
			}
			return out
		case reflect.Int64, reflect.Int:
			out := make([]int64, v.Len())
			for i := range out {
				out[i] = v.Index(i).Int()
			}
			return out
		}
	}
	panic(fmt.Sprintf("pg: no value for %s (%s)", c.Name, v.Type()))
}

// QueueChanges queues each table's upsert and delete into b.
func QueueChanges(b *pgx.Batch, cs []Changes) error {
	for _, c := range cs {
		w, err := writerFor(c.Table)
		if err != nil {
			return err
		}
		if len(c.Upserts) > 0 {
			args, err := w.columns(c.Upserts, w.cols)
			if err != nil {
				return fmt.Errorf("pg: %s upserts: %w", c.Table, err)
			}
			b.Queue(w.upsert, args...)
		}
		if len(c.Deletes) > 0 {
			args, err := w.columns(c.Deletes, w.pk)
			if err != nil {
				return fmt.Errorf("pg: %s deletes: %w", c.Table, err)
			}
			b.Queue(w.delete, args...)
		}
	}
	return nil
}

// writer is a table's two statements and their parameters' shapes.
type writer struct {
	cols, pk       []Column
	upsert, delete string
}

var (
	writersMu sync.Mutex
	writers   = map[string]*writer{}
)

func writerFor(name string) (*writer, error) {
	writersMu.Lock()
	defer writersMu.Unlock()
	if w, ok := writers[name]; ok {
		return w, nil
	}
	t := TableNamed(name)
	if t == nil || t.Kind == Coordination {
		return nil, fmt.Errorf("pg: no data table %s", name)
	}
	w := &writer{cols: t.Columns}
	isPK := map[string]bool{}
	for _, k := range t.PK {
		c, _ := t.Column(k)
		w.pk = append(w.pk, c)
		isPK[k] = true
	}
	id := func(s string) string { return pgx.Identifier{s}.Sanitize() }
	// unnest's parameters, one array per column; a list travels as its JSON text
	// (unnest would flatten an array of arrays), turned back into an array here.
	params := func(cols []Column) (from, sel []string) {
		for i, c := range cols {
			base := baseType(c.Type)
			u := fmt.Sprintf("u.%s", id(c.Name))
			switch base {
			case "text[]", "bigint[]":
				from = append(from, fmt.Sprintf("$%d::text[]", i+1))
				elem := "x"
				if base == "bigint[]" {
					elem = "x::bigint"
				}
				sel = append(sel, fmt.Sprintf("CASE WHEN %s IS NULL THEN NULL ELSE ARRAY(SELECT %s FROM "+
					"jsonb_array_elements_text(%s::jsonb) WITH ORDINALITY AS e(x, i) ORDER BY i)::%s END", u, elem, u, base))
			default:
				from = append(from, fmt.Sprintf("$%d::%s[]", i+1, base))
				sel = append(sel, u)
			}
		}
		return from, sel
	}
	names := func(cols []Column) []string {
		out := make([]string, len(cols))
		for i, c := range cols {
			out[i] = id(c.Name)
		}
		return out
	}
	from, sel := params(w.cols)
	var set []string
	for _, c := range w.cols {
		if !isPK[c.Name] {
			set = append(set, fmt.Sprintf("%s = excluded.%s", id(c.Name), id(c.Name)))
		}
	}
	conflict := "DO NOTHING"
	if len(set) > 0 {
		conflict = "DO UPDATE SET " + strings.Join(set, ", ")
	}
	w.upsert = fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM unnest(%s) AS u(%s) ON CONFLICT (%s) %s",
		id(name), strings.Join(names(w.cols), ", "), strings.Join(sel, ", "), strings.Join(from, ", "),
		strings.Join(names(w.cols), ", "), strings.Join(names(w.pk), ", "), conflict)
	pfrom, _ := params(w.pk)
	w.delete = fmt.Sprintf("DELETE FROM %s WHERE (%s) IN (SELECT * FROM unnest(%s))",
		id(name), strings.Join(names(w.pk), ", "), strings.Join(pfrom, ", "))
	writers[name] = w
	return w, nil
}

// baseType is a column's SQL type without its constraints.
func baseType(typ string) string {
	t, _, _ := strings.Cut(typ, " NOT NULL")
	return t
}

// columns turns rows into one array per column, typed for its parameter.
func (w *writer) columns(rows [][]any, cols []Column) ([]any, error) {
	out := make([]any, len(cols))
	for j, c := range cols {
		base := baseType(c.Type)
		var arr any
		switch base {
		case "text":
			arr = make([]*string, len(rows))
		case "bytea":
			arr = make([][]byte, len(rows))
		case "bigint", "integer":
			arr = make([]*int64, len(rows))
		case "double precision":
			arr = make([]*float64, len(rows))
		case "boolean":
			arr = make([]*bool, len(rows))
		case "text[]", "bigint[]":
			arr = make([]*string, len(rows))
		default:
			return nil, fmt.Errorf("column %s: type %s isn't written by rows", c.Name, c.Type)
		}
		out[j] = arr
	}
	for i, r := range rows {
		if len(r) != len(cols) {
			return nil, fmt.Errorf("a row of %d values for %d columns", len(r), len(cols))
		}
		for j, v := range r {
			if v == nil {
				continue // the arrays start NULL (bytea: a nil slice)
			}
			c := cols[j]
			bad := func() error { return fmt.Errorf("column %s (%s): a %T", c.Name, c.Type, v) }
			switch a := out[j].(type) {
			case []*string:
				switch x := v.(type) {
				case string:
					a[i] = &x
				case []string, []int64:
					b, err := json.Marshal(x)
					if err != nil {
						return nil, err
					}
					s := string(b)
					if s == "null" {
						s = "[]"
					}
					a[i] = &s
				default:
					return nil, bad()
				}
			case [][]byte:
				switch x := v.(type) {
				case []byte:
					a[i] = x
				case string:
					a[i] = []byte(x)
				default:
					return nil, bad()
				}
			case []*int64:
				x, ok := v.(int64)
				if !ok {
					return nil, bad()
				}
				a[i] = &x
			case []*float64:
				x, ok := v.(float64)
				if !ok {
					return nil, bad()
				}
				a[i] = &x
			case []*bool:
				x, ok := v.(bool)
				if !ok {
					return nil, bad()
				}
				a[i] = &x
			}
		}
	}
	return out, nil
}
