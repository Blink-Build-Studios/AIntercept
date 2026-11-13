package stream

import (
	"bytes"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/zcodec"
	"sort"

	"github.com/parquet-go/parquet-go"
)

// Row is one row of a derived table: the key whose state produced it, its id
// within the table, and its value, the table's own row type (nil for a
// tombstone, which says the row no longer exists).
type Row struct {
	Key   string
	ID    string
	Tomb  bool
	Value any
}

// MergeRows folds row sets oldest first: a later row with the same (key, id)
// replaces an earlier one, tombstones remove it. The result is sorted by
// (key, id) and has no tombstones.
func MergeRows(sets ...[]Row) []Row {
	return collapse(sets, false)
}

// collapse is MergeRows, optionally keeping tombstones (a run file must, so it
// can remove a row an older file holds).
func collapse(sets [][]Row, keepTombs bool) []Row {
	type rk struct{ k, id string }
	latest := map[rk]Row{}
	for _, set := range sets {
		for _, r := range set {
			latest[rk{r.Key, r.ID}] = r
		}
	}
	out := make([]Row, 0, len(latest))
	for _, r := range latest {
		if r.Tomb && !keepTombs {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// StateRow is one key's encoded state in a state file. State files are only
// read back by the processor, so the state is an opaque blob.
type StateRow struct {
	Key  string `parquet:"key"`
	Data []byte `parquet:"data"`
}

func EncodeState(rows []StateRow) ([]byte, error) {
	var buf bytes.Buffer
	w := parquet.NewGenericWriter[StateRow](&buf, parquet.Compression(zcodec.Shared))
	if _, err := w.Write(rows); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func DecodeState(b []byte) ([]StateRow, error) {
	return parquet.Read[StateRow](bytes.NewReader(b), int64(len(b)))
}

// CollapseRows is MergeRows keeping tombstones: a newer file's tombstone must
// still remove a row an older file holds.
func CollapseRows(sets ...[]Row) []Row { return collapse(sets, true) }
