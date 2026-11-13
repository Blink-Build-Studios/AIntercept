package stream

import (
	"encoding/json"
	"sort"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// Model is a key's derived state and the rows it produces: the per-table logic
// (internal/stream/state) the pipeline drives. Apply must be order-free across
// a key's records: the same set, applied in any order, leaves the same rows.
// The pipeline applies each cycle's records per key in (ts, record id) order.
type Model interface {
	// Tables are the tables the model writes, each registered (RegisterTable)
	// with its row type: a Delta's row values are of that type.
	Tables() []string
	// New is an empty state for key.
	New(key string) any
	// Apply folds one record into st (mutating it) and returns the rows that
	// changed: upserts, and tombstones for rows that no longer exist.
	Apply(st any, r *records.Record) Delta
	// Encode is st's checkpoint for this cycle. Decode is a key's state from
	// every checkpoint it has, oldest first; Merge is one checkpoint standing
	// for several (compaction). A model whose checkpoint is its whole state
	// keeps the last; one whose checkpoint is what changed (StateModel) keeps
	// them all, so a cycle writes what changed, never the key's history.
	Encode(st any) ([]byte, error)
	Decode(key string, chunks [][]byte) (any, error)
	Merge(key string, chunks [][]byte) ([]byte, error)
	// Size estimates st's memory, for the state cache's byte cap.
	Size(st any) int
}

// Delta is the rows one Apply changed, by table.
type Delta map[string][]Row

// RecordTS is a record's own time: a call's end (else its start), an event's
// row time, an account sighting's time.
func RecordTS(r *records.Record) int64 {
	switch {
	case r.Call != nil:
		if r.Call.EndNS != 0 {
			return r.Call.EndNS
		}
		return r.Call.StartNS
	case r.Event != nil:
		return r.Event.RowNS
	case r.Account != nil:
		return r.Account.NS
	}
	return 0
}

// sortRecords orders records by (ts, record id).
func sortRecords(rs []*records.Record) {
	sort.SliceStable(rs, func(i, j int) bool {
		ti, tj := RecordTS(rs[i]), RecordTS(rs[j])
		if ti != tj {
			return ti < tj
		}
		return rs[i].RecordID < rs[j].RecordID
	})
}

// Expirer is a model whose state holds calls waiting for their ending (the
// rollup expires them, docs/PIPELINE.md §2): Expire ends those whose wait
// passed clockNS (data time) and says which rows changed; Deadline is the data
// time the first of the rest expires after (0: none waits).
type Expirer interface {
	Expire(st any, clockNS int64) Delta
	Deadline(st any) int64
}

// Reference is the tables one pass over rs produces: grouped by key, each
// key's records applied in (ts, record id) order. The pipeline must equal it.
func Reference(m Model, rs []records.Record) map[string][]Row { return ReferenceAt(m, rs, 0) }

// ReferenceAt is Reference with each key's calls still waiting at the end
// expired at clockNS (data time: the newest row of all; 0: none), as the
// rollup ends with them.
func ReferenceAt(m Model, rs []records.Record, clockNS int64) map[string][]Row {
	by := map[string][]*records.Record{}
	for i := range rs {
		by[rs[i].RoutingKey] = append(by[rs[i].RoutingKey], &rs[i])
	}
	out := map[string][]Row{}
	ex, _ := m.(Expirer)
	for key, krs := range by {
		sortRecords(krs)
		st := m.New(key)
		for _, r := range krs {
			for table, rows := range m.Apply(st, r) {
				out[table] = append(out[table], rows...)
			}
		}
		if ex != nil && clockNS != 0 {
			for table, rows := range ex.Expire(st, clockNS) {
				out[table] = append(out[table], rows...)
			}
		}
	}
	for table, rows := range out {
		out[table] = MergeRows(rows)
	}
	return out
}

// CountModel is the stand-in model: per key, how many records it has and its
// first and last record times, in one table. It's order-free, so the pipeline
// can be proven exact with it before the real tables (§4.5) arrive.
type CountModel struct{}

type countState struct {
	Key   string `json:"-"`
	N     int64  `json:"n"`
	First int64  `json:"first"`
	Last  int64  `json:"last"`
}

// KeyCount is the stand-in table's row.
type KeyCount struct {
	N       int64 `parquet:"n"`
	FirstNS int64 `parquet:"first_ns"`
	LastNS  int64 `parquet:"last_ns"`
}

const countTable = "key_counts"

var _ = RegisterTable[KeyCount](countTable)

func (CountModel) Tables() []string   { return []string{countTable} }
func (CountModel) New(key string) any { return &countState{Key: key} }

func (CountModel) Apply(st any, r *records.Record) Delta {
	s := st.(*countState)
	ts := RecordTS(r)
	if s.N == 0 || ts < s.First {
		s.First = ts
	}
	if s.N == 0 || ts > s.Last {
		s.Last = ts
	}
	s.N++
	return Delta{countTable: {{Key: s.Key, ID: s.Key, Value: KeyCount{N: s.N, FirstNS: s.First, LastNS: s.Last}}}}
}

func (CountModel) Encode(st any) ([]byte, error) { return json.Marshal(st) }

func (CountModel) Decode(key string, chunks [][]byte) (any, error) {
	s := &countState{Key: key}
	return s, json.Unmarshal(chunks[len(chunks)-1], s)
}

func (CountModel) Merge(_ string, chunks [][]byte) ([]byte, error) { return chunks[len(chunks)-1], nil }

func (CountModel) Size(any) int { return 64 }

// Layout names the stand-in model's table shapes.
func (CountModel) Layout() string { return "key-counts/1" }

// LayoutOf is m's Layout, when it names one: what a commit says its tables are.
func LayoutOf(m Model) string {
	if l, ok := m.(interface{ Layout() string }); ok {
		return l.Layout()
	}
	return ""
}
