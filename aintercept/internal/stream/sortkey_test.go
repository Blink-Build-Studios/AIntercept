package stream

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"testing"

	"github.com/parquet-go/parquet-go"
)

// A table file is sorted by (key, id) and written in row groups of
// RowGroupRows, with min/max statistics and a page index on key: a reader
// filtering on key (WHERE key IN …) fetches only the row groups that can hold
// it, and a commit records each file's key range so a reader skips whole files.

func shuffledCounts(n int, rng *rand.Rand) []Row {
	out := make([]Row, n)
	for i := range out {
		k := fmt.Sprintf("s:%06d", rng.Intn(n*4))
		out[i] = Row{Key: k, ID: fmt.Sprintf("r%06d", i), Value: KeyCount{N: int64(i), FirstNS: 1, LastNS: 2}}
	}
	rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// Atomic: an encoded file's rows are in (key, id) order, whatever order they came in.
func TestTableFileIsSortedByKey(t *testing.T) {
	tb, _ := TableFor(countTable)
	b, err := tb.Encode(shuffledCounts(500, rand.New(rand.NewSource(1))))
	if err != nil {
		t.Fatal(err)
	}
	got, err := tb.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if !sort.SliceIsSorted(got, func(i, j int) bool {
		return got[i].Key < got[j].Key || got[i].Key == got[j].Key && got[i].ID < got[j].ID
	}) {
		t.Fatal("rows aren't in (key, id) order")
	}
}

// Atomic: files are written in row groups of RowGroupRows, each with min/max
// statistics on key in increasing, non-overlapping ranges, and a page index.
func TestRowGroupsCarryKeyStatisticsAndPageIndex(t *testing.T) {
	old := RowGroupRows
	RowGroupRows = 1000
	defer func() { RowGroupRows = old }()
	tb, _ := TableFor(countTable)
	b, err := tb.Encode(shuffledCounts(5000, rand.New(rand.NewSource(2))))
	if err != nil {
		t.Fatal(err)
	}
	f, err := parquet.OpenFile(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	md := f.Metadata()
	if len(md.RowGroups) != 5 {
		t.Fatalf("%d row groups, want 5", len(md.RowGroups))
	}
	col := -1
	for i, c := range md.RowGroups[0].Columns {
		if len(c.MetaData.PathInSchema) == 1 && c.MetaData.PathInSchema[0] == "key" {
			col = i
		}
	}
	if col < 0 {
		t.Fatal("no key column")
	}
	prevMax := ""
	for i, rg := range md.RowGroups {
		st := rg.Columns[col].MetaData.Statistics
		if st.MinValue == nil || st.MaxValue == nil {
			t.Fatalf("row group %d has no key statistics", i)
		}
		if string(st.MinValue) < prevMax {
			t.Fatalf("row group %d's key range starts at %s, before the last ended (%s)", i, st.MinValue, prevMax)
		}
		prevMax = string(st.MaxValue)
		if rg.Columns[col].ColumnIndexOffset == 0 {
			t.Fatalf("row group %d has no page index on key", i)
		}
	}
}

// The default row group is small enough that a key lookup in a large base
// part skips most of it.
func TestDefaultRowGroupIsSmall(t *testing.T) {
	if RowGroupRows <= 0 || RowGroupRows > 8192 {
		t.Fatalf("RowGroupRows = %d, want 1..8192", RowGroupRows)
	}
}

// Sequence: cycles then compaction keep every file in (key, id) order, and
// each file's commit ref records its exact key range.
func TestCompactionKeepsKeyOrderAndRanges(t *testing.T) {
	h, ctx := setup(t, 1)
	w := h.worker("a", 1)
	w.CompactRuns = 2
	for i := 0; i < 6; i++ {
		h.upload(0, h.gen.batch(40))
		if _, err := w.Cycle(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Compact(ctx); err != nil {
		t.Fatal(err)
	}
	c, err := LoadLatest(ctx, h.s, h.l, 0)
	if err != nil || c == nil {
		t.Fatal(err)
	}
	tb, _ := TableFor(countTable)
	refs := append(append([]FileRef(nil), c.Base[countTable]...), c.Runs[countTable]...)
	if len(refs) == 0 {
		t.Fatal("no files")
	}
	for _, f := range refs {
		b, err := h.s.Get(ctx, f.Key)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := tb.Decode(b)
		if err != nil {
			t.Fatal(err)
		}
		if !sort.SliceIsSorted(rows, func(i, j int) bool {
			return rows[i].Key < rows[j].Key || rows[i].Key == rows[j].Key && rows[i].ID < rows[j].ID
		}) {
			t.Fatalf("%s isn't in (key, id) order", f.Key)
		}
		if len(rows) > 0 && (f.MinKey != rows[0].Key || f.MaxKey != rows[len(rows)-1].Key) {
			t.Fatalf("%s records [%s,%s], rows span [%s,%s]", f.Key, f.MinKey, f.MaxKey, rows[0].Key, rows[len(rows)-1].Key)
		}
	}
}

// Every commit names its layout version, so a reader knows the tables' shape
// without opening a table file.
func TestCommitNamesItsLayout(t *testing.T) {
	h, ctx := setup(t, 1)
	h.upload(0, h.gen.batch(5))
	w := h.worker("a", 1)
	if _, err := w.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	c, err := LoadLatest(ctx, h.s, h.l, 0)
	if err != nil || c == nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(c)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if want := (CountModel{}).Layout(); m["layout"] != want || want == "" {
		t.Fatalf("commit layout %v, want %q", m["layout"], want)
	}
	if (StateModel{}).Layout() != "stream-state/1" {
		t.Fatalf("the state's layout is %q, want stream-state/1", (StateModel{}).Layout())
	}
}
