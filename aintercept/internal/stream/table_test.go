package stream

import (
	"bytes"
	"reflect"
	"sort"
	"testing"

	"github.com/parquet-go/parquet-go"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// Table files are queried with SQL (the API, phase 5): each has key, id and
// tomb, then the table's own typed columns, flattened. Only state files hold
// opaque blobs.

type testSeg struct {
	SessionID string `parquet:"session_id"`
	FromNS    int64  `parquet:"from_ns"`
	State     string `parquet:"state,optional"`
}

type testWithEmbed struct {
	testSeg
	Extra int32 `parquet:"extra"`
}

func columns(t *testing.T, b []byte) []string {
	t.Helper()
	f, err := parquet.OpenFile(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, c := range f.Schema().Columns() {
		out = append(out, c[len(c)-1])
	}
	sort.Strings(out)
	return out
}

func TestTableFileHasTypedColumns(t *testing.T) {
	tb := RegisterTable[testSeg]("t_segs")
	rows := []Row{
		{Key: "s:a", ID: "1", Value: testSeg{SessionID: "a", FromNS: 5, State: "focused"}},
		{Key: "s:a", ID: "2", Tomb: true},
	}
	b, err := tb.Encode(rows)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := columns(t, b), []string{"from_ns", "id", "key", "session_id", "state", "tomb"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("columns %v, want %v", got, want)
	}
	back, err := tb.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, rows) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", back, rows)
	}
}

// An embedded struct's fields are flattened into the table's columns.
func TestTableFlattensEmbeddedStructs(t *testing.T) {
	tb := RegisterTable[testWithEmbed]("t_embed")
	b, err := tb.Encode([]Row{{Key: "k", ID: "i", Value: testWithEmbed{testSeg: testSeg{SessionID: "s"}, Extra: 3}}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := columns(t, b), []string{"extra", "from_ns", "id", "key", "session_id", "state", "tomb"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("columns %v, want %v", got, want)
	}
}

// A table type whose columns collide with key, id or tomb is refused at
// registration, not discovered in a query.
func TestTableRefusesColliding(t *testing.T) {
	type bad struct {
		ID string `parquet:"id"`
	}
	defer func() {
		if recover() == nil {
			t.Fatal("a column named id was accepted")
		}
	}()
	RegisterTable[bad]("t_bad")
}

// The pipeline writes typed run files, and compaction keeps the columns.
func TestRunAndBaseFilesAreTyped(t *testing.T) {
	h, ctx := setup(t, 1)
	w := h.worker("a", 1)
	w.CompactRuns = 3
	for i := 0; i < 5; i++ {
		h.upload(0, h.gen.batch(10))
		if _, err := w.Cycle(ctx); err != nil {
			t.Fatal(err)
		}
	}
	c, err := LoadLatest(ctx, h.s, h.l, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"first_ns", "id", "key", "last_ns", "n", "tomb"}
	refs := append(append([]FileRef(nil), c.Base[countTable]...), c.Runs[countTable]...)
	if len(c.Base[countTable]) == 0 {
		t.Fatal("never compacted")
	}
	for _, f := range refs {
		b, err := h.s.Get(ctx, f.Key)
		if err != nil {
			t.Fatal(err)
		}
		if got := columns(t, b); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: columns %v, want %v", f.Key, got, want)
		}
	}
	h.checkAll(ctx)
}

// A model that removes rows: tombstones must drop them from the newest-wins
// view, in runs and after compaction.
func TestTombstonesDropRows(t *testing.T) {
	h, ctx := setup(t, 1)
	h.m = toggleModel{}
	w := h.worker("a", 1)
	w.CompactRuns = 4
	for i := 0; i < 9; i++ {
		h.upload(0, h.gen.batch(15))
		if _, err := w.Cycle(ctx); err != nil {
			t.Fatal(err)
		}
		h.checkAll(ctx) // runs only, then a base plus runs
	}
}

// toggleModel keeps a row per key only while the key has an odd number of
// records: every record adds or removes it. Order-free (parity of a count).
type toggleModel struct{ CountModel }

type toggleRow struct {
	N int64 `parquet:"n"`
}

var toggleTable = RegisterTable[toggleRow]("toggled")

func (toggleModel) Tables() []string { return []string{"toggled"} }

func (toggleModel) Apply(st any, r *records.Record) Delta {
	s := st.(*countState)
	s.N++
	if s.N%2 == 1 {
		return Delta{"toggled": {{Key: s.Key, ID: s.Key, Value: toggleRow{N: s.N}}}}
	}
	return Delta{"toggled": {{Key: s.Key, ID: s.Key, Tomb: true}}}
}
