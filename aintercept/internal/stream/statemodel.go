package stream

import (
	"sort"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream/state"
)

// StateModel is the derived tables (internal/stream/state, docs/STREAMING_PIPELINE.md
// §4.5) as the processor's model: each key's state applies its records one at
// a time, and every row it changes becomes an upsert (or, gone, a tombstone)
// in its table's run file.
type StateModel struct{}

func init() { RegisterStateTables() }

// Tables is every table the state writes.
func (StateModel) Tables() []string {
	out := make([]string, 0, len(state.TableTypes))
	for name := range state.TableTypes {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (StateModel) New(key string) any { return state.New(key) }

func (StateModel) Apply(st any, r *records.Record) Delta {
	s := st.(*state.KeyState)
	d := s.Apply(*r)
	if len(d.Changes) == 0 {
		return nil
	}
	out := Delta{}
	for _, c := range d.Changes {
		out[c.Table] = append(out[c.Table], Row{Key: s.Key, ID: c.ID, Tomb: c.Row == nil, Value: c.Row})
	}
	return out
}

func (StateModel) Encode(st any) ([]byte, error) { return state.Encode(st.(*state.KeyState)) }

func (StateModel) Decode(key string, chunks [][]byte) (any, error) {
	s, err := state.Decode(chunks...)
	if err != nil {
		return nil, err
	}
	s.Key = key
	return s, nil
}

func (StateModel) Merge(_ string, chunks [][]byte) ([]byte, error) { return state.Merge(chunks...) }

// Size estimates a key's state in memory from its rows (the indexes it keeps
// are of the same order).
func (StateModel) Size(st any) int {
	n := 0
	for _, rows := range st.(*state.KeyState).Rows() {
		n += len(rows)
	}
	return 512 + 1024*n + 1024*st.(*state.KeyState).Held()
}

// Layout names the state's table shapes: the API reads them as the stream
// state's (api/src/aintercept_api/state_sql.py).
func (StateModel) Layout() string { return "stream-state/1" }
