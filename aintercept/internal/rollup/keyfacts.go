package rollup

import (
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream/state"
)

// KeyFacts is what v5's merge folded from a key's rows, which v7's key row
// keeps instead (docs/PIPELINE.md §3.2), computed by the key's own run.
type KeyFacts struct {
	// Day is DayOf's: the day of the key's newest last_ns/ts_ns ("" for an a: key).
	Day string
	// FirstRequestNS and EarliestNS are /status's (rawFirst), 0 for none.
	FirstRequestNS, EarliestNS int64
	// RawNewestNS is the newest raw row time among its fact rows (rawNewest).
	RawNewestNS     int64
	ContentRetained bool
}

// FactsOf is a key's facts from all its rows (the state's Rows: table → id →
// row), equal to what the merge computes from the key's tables, at the cost of
// a field read per row.
func FactsOf(key string, rows map[string]map[string]any, updatedNS int64) KeyFacts {
	facts := map[string][]stream.Row{}
	for _, rt := range rawTimes {
		if facts[rt.table] != nil {
			continue
		}
		for id, v := range rows[rt.table] {
			facts[rt.table] = append(facts[rt.table], stream.Row{Key: key, ID: id, Value: v})
		}
	}
	first, earliest := rawFirst(facts)
	out := KeyFacts{FirstRequestNS: first, EarliestNS: earliest, RawNewestNS: rawNewest(facts), ContentRetained: retainsContent(facts)}
	switch {
	case strings.HasPrefix(key, "a:"):
		return out
	case strings.HasPrefix(key, "c:"), strings.HasPrefix(key, "ip:"):
		if d := keyDay(key); d != "" {
			out.Day = d
			return out
		}
	}
	ns := newestOfRows(rows)
	if ns == 0 {
		ns = updatedNS
	}
	out.Day = time.Unix(0, ns).UTC().Format("2006-01-02")
	return out
}

// newestOfRows is newestOf over the state's rows: each table's last_ns column,
// else its ts_ns, read by its field path.
func newestOfRows(rows map[string]map[string]any) int64 {
	var best int64
	for name, byID := range rows {
		path := newestPath(name)
		if path == nil {
			continue
		}
		for _, v := range byID {
			f := reflect.ValueOf(v).FieldByIndex(path)
			if f.Kind() == reflect.Int64 && f.Int() > best {
				best = f.Int()
			}
		}
	}
	return best
}

var newestPaths sync.Map // table → []int (empty: none)

// newestPath is the field path of a table's last_ns column, else its ts_ns, as
// the table codec flattens its row type (nil: neither).
func newestPath(table string) []int {
	if p, ok := newestPaths.Load(table); ok {
		return p.([]int)
	}
	var path []int
	if typ := reflect.TypeOf(state.TableTypes[table]); typ != nil {
		for _, col := range []string{"last_ns", "ts_ns"} {
			if path = fieldPath(typ, col); path != nil {
				break
			}
		}
	}
	if path == nil {
		path = []int{}
	}
	newestPaths.Store(table, path)
	if len(path) == 0 {
		return nil
	}
	return path
}

// fieldPath finds a column among a row type's fields and its embedded
// structs' (stream.RegisterTableType's flattening).
func fieldPath(t reflect.Type, col string) []int {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous && f.Type.Kind() == reflect.Struct {
			if p := fieldPath(f.Type, col); p != nil {
				return append([]int{i}, p...)
			}
			continue
		}
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("parquet"), ",")
		if name == "" {
			name = f.Name
		}
		if name == col {
			return []int{i}
		}
	}
	return nil
}
