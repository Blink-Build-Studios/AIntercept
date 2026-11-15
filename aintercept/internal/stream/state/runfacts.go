package state

import (
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
)

// Facts is what the key row keeps of a key's rows (docs/PIPELINE.md §3.2:
// its day's newest time, /status's first and earliest times, its newest raw
// row, whether it retained content), kept as running values: every row change
// takes the old row's part out and puts the new one's in (docs/PIPELINE.md §2,
// v8). A run reads them instead of walking every row. rollup.FactsOf is the
// same from the rows, and rollup.FactsFrom the key row's from these.
type Facts struct {
	// Newest is the newest last_ns, else ts_ns, of any row (0: none).
	Newest int64
	// RawNewest is the newest raw row time of the fact rows (0: none).
	RawNewest int64
	// First is the first non-synthetic fact time, Earliest the first of any (0: none).
	First, Earliest int64
	// Content is whether a call's bodies were read (a detail).
	Content bool
}

// runFacts is Facts' running values: each a multiset, so a row that leaves
// takes its time with it. And the run's other running values (§2, v8): the
// batches the key holds, and its ended calls with no wire yet.
type runFacts struct {
	newest, raw, first, earliest *omap[int64, int]
	content                      int
	// batches is each batch the key holds: how many of its rows name it (a
	// batch member, an analytics call's request_txs row): drawer.Batches'.
	batches map[string]int
	// unwired is each ended call whose drawer reads its detail object
	// (drawer.EndedCalls') with no wire external yet: when it ended.
	unwired map[string]int64
}

func newRunFacts() *runFacts {
	return &runFacts{newest: newOmap[int64, int](lessInt64), raw: newOmap[int64, int](lessInt64),
		first: newOmap[int64, int](lessInt64), earliest: newOmap[int64, int](lessInt64),
		batches: map[string]int{}, unwired: map[string]int64{}}
}

// WirePrefix starts the id of a call's wire external (render.WireID).
const WirePrefix = "wire:"

// HoldsBatch is whether the key holds any of a batch (a member, or its call).
func (s *KeyState) HoldsBatch(b string) bool { return s.facts.batches[b] > 0 }

// HeldBatches is every batch the key holds, sorted.
func (s *KeyState) HeldBatches() []string {
	out := make([]string, 0, len(s.facts.batches))
	for b := range s.facts.batches {
		out = append(out, b)
	}
	sort.Strings(out)
	return out
}

// Unwired is the key's ended calls with no wire yet (their detail object not
// read, or waited for), by call id, with when each ended.
func (s *KeyState) Unwired() map[string]int64 { return s.facts.unwired }

// BatchOfChange is the batches a change names: a batch member's, an analytics
// call's (old and new).
func BatchOfChange(c Change) []string {
	var out []string
	for _, v := range []any{c.Old, c.Row} {
		if b, ok := batchOfRow(c.Table, v); ok {
			out = append(out, b)
		}
	}
	return out
}

func batchOfRow(table string, v any) (string, bool) {
	switch table {
	case TableBatchMembers:
		if m, ok := v.(BatchMember); ok {
			return m.BatchID, true
		}
	case TableRequestTxs:
		if t, ok := v.(ReqTx); ok && t.Kind == "analytics" {
			return t.CallID, true
		}
	}
	return "", false
}

// endedAt is an ended call's end (its start when it has none), when its
// drawer reads its detail object (drawer.EndedCalls').
func endedAt(id string, v any) (int64, bool) {
	c, ok := v.(canon.Request)
	if !ok || strings.Contains(id, "#") {
		return 0, false
	}
	if c.EndNS != 0 {
		return c.EndNS, true
	}
	return c.StartNS, true
}

// Facts is the key's facts.
func (s *KeyState) Facts() Facts {
	f := s.facts
	var out Facts
	if k, _, ok := f.newest.Last(); ok {
		out.Newest = k
	}
	if k, _, ok := f.raw.Last(); ok {
		out.RawNewest = k
	}
	if k, _, ok := f.first.First(); ok {
		out.First = k
	}
	if k, _, ok := f.earliest.First(); ok {
		out.Earliest = k
	}
	out.Content = f.content > 0
	return out
}

// noteFacts folds a record's changes into the running values.
func (s *KeyState) noteFacts(cs []Change) {
	f := s.facts
	for _, c := range cs {
		if c.Old != nil {
			f.row(c.Table, c.Old, -1)
			if b, ok := batchOfRow(c.Table, c.Old); ok {
				if f.batches[b]--; f.batches[b] <= 0 {
					delete(f.batches, b)
				}
			}
		}
		if c.Row != nil {
			f.row(c.Table, c.Row, +1)
			if b, ok := batchOfRow(c.Table, c.Row); ok {
				f.batches[b]++
			}
		}
		if c.Table == TableFactRequests {
			end, ok := endedAt(c.ID, c.Row)
			_, wired := s.ext[WirePrefix+c.ID]
			switch {
			case ok && !wired:
				f.unwired[c.ID] = end
			default:
				delete(f.unwired, c.ID)
			}
		}
	}
}

// noteExt keeps the unwired calls as their wires are set and removed.
func (s *KeyState) noteExt(id string, v any) {
	call, ok := strings.CutPrefix(id, WirePrefix)
	if !ok {
		return
	}
	if v != nil {
		delete(s.facts.unwired, call)
		return
	}
	if end, ok := endedAt(call, s.rows[TableFactRequests][call]); ok {
		s.facts.unwired[call] = end
	}
}

func count(m *omap[int64, int], v int64, sign int) {
	cur, _ := m.Get(v)
	if cur+sign == 0 {
		m.Delete(v)
		return
	}
	m.Set(v, cur+sign)
}

// row adds (sign +1) or removes (-1) one row's part.
func (f *runFacts) row(table string, row any, sign int) {
	if path := newestPath(table); path != nil {
		if v := reflect.ValueOf(row).FieldByIndex(path); v.Kind() == reflect.Int64 && v.Int() > 0 {
			count(f.newest, v.Int(), sign)
		}
	}
	// rawNewest, rawFirst and retainsContent (rollup/merge.go), per row.
	raw := func(ns int64) {
		if ns > 0 {
			count(f.raw, ns, sign)
		}
	}
	see := func(ns int64, synthetic bool) {
		if ns == 0 {
			return
		}
		count(f.earliest, ns, sign)
		if !synthetic {
			count(f.first, ns, sign)
		}
	}
	switch table {
	case TableFactRequests:
		c, ok := row.(canon.Request)
		if !ok {
			return
		}
		raw(c.StartNS)
		raw(c.EndNS)
		ns := c.StartNS
		if ns == 0 {
			ns = c.EndNS
		}
		see(ns, c.Synthetic)
		if c.DetailJSON != "" {
			f.content += sign
		}
	case TableFactPending:
		p, ok := row.(canon.Pending)
		if !ok {
			return
		}
		raw(p.SeenNS)
		if !p.Done {
			see(p.StartNS, p.Synthetic)
		}
	case TableFactEvents:
		e, ok := row.(canon.Event)
		if !ok {
			return
		}
		raw(e.RowNS)
		see(e.RowNS, false)
	case TableFactProbes:
		p, ok := row.(Probes)
		if !ok {
			return
		}
		raw(p.NewestNS)
		see(p.FirstNS, true)
	}
}

var newestPaths sync.Map // table → []int (empty: none)

// newestPath is the field path of a table's last_ns column, else its ts_ns,
// as the table codec flattens its row type (nil: neither): rollup.FactsOf's.
func newestPath(table string) []int {
	if p, ok := newestPaths.Load(table); ok {
		return p.([]int)
	}
	var path []int
	if typ := reflect.TypeOf(TableTypes[table]); typ != nil {
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
