package stream

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/compress/zstd"
)

// Table is a derived table's file format: the columns key, id and tomb, then
// the table's row type's own columns, flattened (an embedded struct's fields
// become columns of their own). The API queries these files with SQL.
type Table struct {
	Name  string
	typ   reflect.Type // the row type
	flat  reflect.Type // key, id, tomb, then the row type's leaf fields
	paths [][]int      // for each flattened field after the first three, its index path in the row type
	cols  []string     // and its column name
	sch   *parquet.Schema
}

var (
	tablesMu sync.Mutex
	tables   = map[string]*Table{}
)

// RegisterTable declares table name with row type T (a struct). It panics on a
// type whose columns would collide with key, id or tomb, or with each other.
// Registering the same name with the same type again returns the first.
func RegisterTable[T any](name string) *Table {
	return RegisterTableType(name, reflect.TypeFor[T]())
}

// RegisterTableType is RegisterTable with the row type as a value.
func RegisterTableType(name string, typ reflect.Type) *Table {
	tablesMu.Lock()
	defer tablesMu.Unlock()
	if t, ok := tables[name]; ok {
		if t.typ != typ {
			panic(fmt.Sprintf("stream: table %s registered as %s and %s", name, t.typ, typ))
		}
		return t
	}
	if typ.Kind() != reflect.Struct {
		panic(fmt.Sprintf("stream: table %s: row type %s isn't a struct", name, typ))
	}
	fields := []reflect.StructField{
		{Name: "XKey", Type: reflect.TypeFor[string](), Tag: `parquet:"key"`},
		{Name: "XID", Type: reflect.TypeFor[string](), Tag: `parquet:"id"`},
		{Name: "XTomb", Type: reflect.TypeFor[bool](), Tag: `parquet:"tomb"`},
	}
	seen := map[string]bool{"key": true, "id": true, "tomb": true}
	var paths [][]int
	var cols []string
	var walk func(t reflect.Type, path []int)
	walk = func(t reflect.Type, path []int) {
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			p := append(append([]int(nil), path...), i)
			if f.Anonymous && f.Type.Kind() == reflect.Struct {
				walk(f.Type, p)
				continue
			}
			if !f.IsExported() {
				continue
			}
			col := columnName(f)
			if col == "-" {
				continue
			}
			if seen[col] {
				panic(fmt.Sprintf("stream: table %s: column %q collides", name, col))
			}
			seen[col] = true
			fields = append(fields, reflect.StructField{Name: fmt.Sprintf("F%d", len(fields)), Type: f.Type, Tag: f.Tag})
			paths = append(paths, p)
			cols = append(cols, col)
		}
	}
	walk(typ, nil)
	flat := reflect.StructOf(fields)
	t := &Table{Name: name, typ: typ, flat: flat, paths: paths, cols: cols, sch: parquet.SchemaOf(reflect.New(flat).Interface())}
	tables[name] = t
	return t
}

// TableFor is a registered table.
func TableFor(name string) (*Table, error) {
	tablesMu.Lock()
	defer tablesMu.Unlock()
	if t, ok := tables[name]; ok {
		return t, nil
	}
	return nil, fmt.Errorf("stream: table %s isn't registered", name)
}

func columnName(f reflect.StructField) string {
	tag := f.Tag.Get("parquet")
	if name, _, _ := strings.Cut(tag, ","); name != "" {
		return name
	}
	return f.Name
}

// Encode is rows as a Parquet file of this table (zstd).
func (t *Table) Encode(rows []Row) ([]byte, error) {
	var buf bytes.Buffer
	w := parquet.NewWriter(&buf, t.sch, parquet.Compression(&zstd.Codec{}))
	v := reflect.New(t.flat).Elem()
	for _, r := range rows {
		v.SetZero()
		v.Field(0).SetString(r.Key)
		v.Field(1).SetString(r.ID)
		v.Field(2).SetBool(r.Tomb)
		if !r.Tomb {
			if r.Value == nil {
				return nil, fmt.Errorf("stream: table %s row %s/%s has no value", t.Name, r.Key, r.ID)
			}
			rv := reflect.ValueOf(r.Value)
			if rv.Type() != t.typ {
				return nil, fmt.Errorf("stream: table %s row %s/%s is a %s, not a %s", t.Name, r.Key, r.ID, rv.Type(), t.typ)
			}
			for i, p := range t.paths {
				v.Field(3 + i).Set(rv.FieldByIndex(p))
			}
		}
		if err := w.Write(v.Addr().Interface()); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Decode reads a file of this table.
func (t *Table) Decode(b []byte) ([]Row, error) {
	r := parquet.NewReader(bytes.NewReader(b), t.sch)
	defer r.Close()
	var out []Row
	for {
		p := reflect.New(t.flat)
		if err := r.Read(p.Interface()); err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return nil, err
		}
		v := p.Elem()
		row := Row{Key: v.Field(0).String(), ID: v.Field(1).String(), Tomb: v.Field(2).Bool()}
		if !row.Tomb {
			val := reflect.New(t.typ).Elem()
			for i, path := range t.paths {
				val.FieldByIndex(path).Set(v.Field(3 + i))
			}
			row.Value = val.Interface()
		}
		out = append(out, row)
	}
}

// Columns are the table's own typed columns, in file order (after key, id, tomb).
func (t *Table) Columns() []string { return append([]string(nil), t.cols...) }

// ValueMap is a row value as {column: value}.
func (t *Table) ValueMap(v any) map[string]any {
	rv := reflect.ValueOf(v)
	out := make(map[string]any, len(t.cols))
	for i, p := range t.paths {
		out[t.cols[i]] = rv.FieldByIndex(p).Interface()
	}
	return out
}

// RowGroupRows is how many rows each row group of a table file holds (stub).
var RowGroupRows = 0
