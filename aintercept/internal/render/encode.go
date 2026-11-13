package render

import (
	"encoding/binary"
	"fmt"
	"math"
	"reflect"
	"strings"

	"github.com/cespare/xxhash/v2"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg"
)

// How a row becomes its stored columns: a table's columns are pg.Tables'
// (schema.sql's), each read from its Go row type's field or, for an extra
// column, given by the render. The values are today's: a NULL where the
// Parquet tables today's API reads hold one, so the API loads the same rows:
//   - a nil pointer is NULL;
//   - a non-pointer field tagged optional is NULL at its zero value
//     (parquet-go writes an optional zero as null);
//   - a list is never NULL: a nil list is an empty one (a required list column).

// kind is a column's value type.
type kind uint8

const (
	kText kind = iota
	kInt
	kFloat
	kBool
	kTextList
	kIntList
)

func kindOf(sqlType string) kind {
	switch strings.TrimSuffix(sqlType, " NOT NULL") {
	case "text":
		return kText
	case "bigint", "integer":
		return kInt
	case "double precision":
		return kFloat
	case "boolean":
		return kBool
	case "text[]":
		return kTextList
	case "bigint[]":
		return kIntList
	}
	panic("render: no value kind for column type " + sqlType)
}

// column is one stored column.
type column struct {
	name     string
	kind     kind
	path     []int // the field in the row type (nil: an extra the render gives)
	optional bool  // a non-pointer field tagged optional: its zero value is NULL
}

// cell is one row's value of a column. A list is the field's reflect.Value, so
// hashing a row allocates nothing.
type cell struct {
	null bool
	s    string
	i    int64
	f    float64
	b    bool
	list reflect.Value
}

// tableEnc is how a table's rows are stored.
type tableEnc struct {
	t     *pg.Table
	cols  []column
	pk    []int // the primary key's columns (indexes into cols), in its order
	owner int   // the column a rewrite deletes the key's rows by
}

// encs is each table a render writes, by name.
var encs = map[string]*tableEnc{}

func encOf(name string) *tableEnc {
	e := encs[name]
	if e == nil {
		panic("render: no table " + name)
	}
	return e
}

func newEnc(t *pg.Table, owner string) *tableEnc {
	e := &tableEnc{t: t, owner: -1}
	for _, c := range t.Columns {
		col := column{name: c.Name, kind: kindOf(c.Type), path: c.Field}
		if c.Field != nil {
			col.optional = optionalField(t.Row, c.Field)
		}
		if c.Name == owner {
			e.owner = len(e.cols)
		}
		e.cols = append(e.cols, col)
	}
	for _, k := range t.PK {
		for i, c := range e.cols {
			if c.name == k {
				e.pk = append(e.pk, i)
			}
		}
	}
	if e.owner < 0 || len(e.pk) != len(t.PK) {
		panic(fmt.Sprintf("render: %s has no %s column or a primary key column missing", t.Name, owner))
	}
	for _, i := range e.pk {
		if k := e.cols[i].kind; k != kText && k != kInt {
			panic("render: a primary key column that's neither text nor bigint: " + t.Name + "." + e.cols[i].name)
		}
	}
	return e
}

// optionalField is whether the field at path is a non-pointer tagged optional.
func optionalField(t reflect.Type, path []int) bool {
	var f reflect.StructField
	for _, i := range path {
		if t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		f = t.Field(i)
		t = f.Type
	}
	if f.Type.Kind() == reflect.Pointer {
		return false
	}
	_, opts, _ := strings.Cut(f.Tag.Get("parquet"), ",")
	for _, o := range strings.Split(opts, ",") {
		if o == "optional" {
			return true
		}
	}
	return false
}

// fieldCell is row's value of column c (a field column).
func fieldCell(row reflect.Value, c *column) cell {
	v := row
	for _, i := range c.path {
		if v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return cell{null: true}
			}
			v = v.Elem()
		}
		v = v.Field(i)
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return cell{null: true}
		}
		v = v.Elem()
	} else if c.optional && v.IsZero() {
		return cell{null: true}
	}
	switch c.kind {
	case kText:
		return cell{s: v.String()}
	case kInt:
		if v.CanInt() {
			return cell{i: v.Int()}
		}
		return cell{i: int64(v.Uint())}
	case kFloat:
		return cell{f: v.Float()}
	case kBool:
		return cell{b: v.Bool()}
	default:
		return cell{list: v}
	}
}

// anyCell is a value as a cell of a column of kind k: nil is NULL.
func anyCell(v any, k kind) cell {
	switch x := v.(type) {
	case nil:
		return cell{null: true}
	case string:
		return cell{s: x}
	case int64:
		return cell{i: x}
	case float64:
		return cell{f: x}
	case bool:
		return cell{b: x}
	case *string:
		if x == nil {
			return cell{null: true}
		}
		return cell{s: *x}
	case *int64:
		if x == nil {
			return cell{null: true}
		}
		return cell{i: *x}
	case []string, []int64:
		return cell{list: reflect.ValueOf(x)}
	}
	panic(fmt.Sprintf("render: no cell for %T (kind %d)", v, k))
}

// value is the cell as a Go value: nil for NULL, a list never nil.
func (c cell) value(k kind) any {
	if c.null {
		return nil
	}
	switch k {
	case kText:
		return c.s
	case kInt:
		return c.i
	case kFloat:
		return c.f
	case kBool:
		return c.b
	case kTextList:
		out := make([]string, 0, listLen(c.list))
		for j := 0; j < listLen(c.list); j++ {
			out = append(out, c.list.Index(j).String())
		}
		return out
	default:
		out := make([]int64, 0, listLen(c.list))
		for j := 0; j < listLen(c.list); j++ {
			out = append(out, c.list.Index(j).Int())
		}
		return out
	}
}

func listLen(v reflect.Value) int {
	if !v.IsValid() {
		return 0
	}
	return v.Len()
}

// hasher hashes a row's cells: xxhash64 of their encoding.
type hasher struct {
	d   *xxhash.Digest
	buf [10]byte
}

func newHasher() *hasher { return &hasher{d: xxhash.New()} }

func (h *hasher) int(n int64) {
	binary.LittleEndian.PutUint64(h.buf[:8], uint64(n))
	h.d.Write(h.buf[:8])
}

func (h *hasher) str(s string) {
	n := binary.PutUvarint(h.buf[:], uint64(len(s)))
	h.d.Write(h.buf[:n])
	h.d.WriteString(s)
}

func (h *hasher) cell(c cell, k kind) {
	if c.null {
		h.d.Write([]byte{0})
		return
	}
	h.d.Write([]byte{1})
	switch k {
	case kText:
		h.str(c.s)
	case kInt:
		h.int(c.i)
	case kFloat:
		h.int(int64(math.Float64bits(c.f)))
	case kBool:
		if c.b {
			h.d.Write([]byte{1})
		} else {
			h.d.Write([]byte{0})
		}
	case kTextList:
		h.int(int64(listLen(c.list)))
		for j := 0; j < listLen(c.list); j++ {
			h.str(c.list.Index(j).String())
		}
	case kIntList:
		h.int(int64(listLen(c.list)))
		for j := 0; j < listLen(c.list); j++ {
			h.int(c.list.Index(j).Int())
		}
	}
}

// pkKey is a primary key's values as a map key; keyValues reads them back.
func pkKey(cells []cell, kinds []kind) string {
	var b []byte
	for i, c := range cells {
		if kinds[i] == kText {
			b = append(b, 's')
			b = binary.AppendUvarint(b, uint64(len(c.s)))
			b = append(b, c.s...)
		} else {
			b = append(b, 'i')
			b = binary.BigEndian.AppendUint64(b, uint64(c.i))
		}
	}
	return string(b)
}

func keyValues(key string) []any {
	var out []any
	b := []byte(key)
	for len(b) > 0 {
		tag := b[0]
		b = b[1:]
		if tag == 's' {
			n, w := binary.Uvarint(b)
			out = append(out, string(b[w:w+int(n)]))
			b = b[w+int(n):]
		} else {
			out = append(out, int64(binary.BigEndian.Uint64(b)))
			b = b[8:]
		}
	}
	return out
}
