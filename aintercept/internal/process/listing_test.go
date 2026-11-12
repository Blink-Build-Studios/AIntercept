package process

import (
	"bytes"
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"
)

// countingStore counts the objects listings return.
type countingStore struct {
	*memStore
	listed int
}

func (c *countingStore) List(ctx context.Context, prefix string) ([]Object, error) {
	o, err := c.memStore.List(ctx, prefix)
	c.listed += len(o)
	return o, err
}

func (c *countingStore) ListAfter(ctx context.Context, prefix, after string) ([]Object, error) {
	o, err := c.memStore.ListAfter(ctx, prefix, after)
	c.listed += len(o)
	return o, err
}

// A poll lists only the files after each pod folder's cursor: its cost is the
// new files, not the hour's.
func TestPollListsOnlyNewFiles(t *testing.T) {
	t.Parallel() // slow under -race: let it overlap the others
	ctx := context.Background()
	raw, out := &countingStore{memStore: newMem()}, newMem()
	files := newCapture().sorted()
	now := t0
	p := newProc(raw, out, &now, 500, 64)
	if err := p.Open(ctx); err != nil {
		t.Fatal(err)
	}
	polls := 0
	for _, f := range files {
		put(t, raw.memStore, f)
		now = f.at.Add(time.Minute)
		raw.listed = 0
		n, err := p.Poll(ctx, false)
		if err != nil {
			t.Fatal(err)
		}
		if n != 1 || raw.listed != 1 {
			t.Fatalf("poll after %s: processed %d, listed %d; want 1 and 1", f.key, n, raw.listed)
		}
		polls++
	}
	// A restarted processor rebuilds its cursors from the ledger.
	p = newProc(raw, out, &now, 500, 64)
	if err := p.Open(ctx); err != nil {
		t.Fatal(err)
	}
	raw.listed = 0
	if n, _ := p.Poll(ctx, false); n != 0 || raw.listed != 0 {
		t.Fatalf("after restart: processed %d, listed %d", n, raw.listed)
	}
	if polls < 20 {
		t.Fatalf("only %d polls", polls)
	}
}

func TestDirStoreFolderListings(t *testing.T) {
	ctx := context.Background()
	d := DirStore{Root: t.TempDir()}
	for _, k := range []string{"h/instance=a/1.gz", "h/instance=a/2.gz", "h/instance=a/3.gz", "h/instance=b/1.gz", "h/x.gz"} {
		if err := d.Put(ctx, k, []byte(k)); err != nil {
			t.Fatal(err)
		}
	}
	dirs, err := d.ListDirs(ctx, "h/")
	if err != nil || !reflect.DeepEqual(dirs, []string{"h/instance=a/", "h/instance=b/"}) {
		t.Fatalf("dirs %v %v", dirs, err)
	}
	after, err := d.ListAfter(ctx, "h/instance=a/", "h/instance=a/1.gz")
	if err != nil || len(after) != 2 || after[0].Key != "h/instance=a/2.gz" || after[1].Size != int64(len("h/instance=a/3.gz")) {
		t.Fatalf("after %v %v", after, err)
	}
	if all, _ := d.ListAfter(ctx, "h/instance=a/", ""); len(all) != 3 {
		t.Fatalf("all %v", all)
	}
	if none, err := d.ListAfter(ctx, "nope/", ""); err != nil || none != nil {
		t.Fatalf("missing folder: %v %v", none, err)
	}
}

// An optional column stays null for a zero value after holding a non-zero one
// (parquet-go v0.32.0's typed batch Write gets this wrong; see encodeParquet).
func TestOptionalZeroIsNull(t *testing.T) {
	type row struct {
		S string `parquet:"s,optional"`
		N int64  `parquet:"n,optional"`
	}
	b := encodeParquet([]row{{}, {S: "x", N: 5}, {}, {}})
	f, err := parquet.OpenFile(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	for _, cc := range f.RowGroups()[0].ColumnChunks() {
		pages := cc.Pages()
		var nulls []bool
		for {
			pg, err := pages.ReadPage()
			if err != nil {
				break
			}
			vals := make([]parquet.Value, pg.NumValues())
			pg.Values().ReadValues(vals)
			for _, v := range vals {
				nulls = append(nulls, v.IsNull())
			}
		}
		if !reflect.DeepEqual(nulls, []bool{true, false, true, true}) {
			t.Fatalf("nulls %v", nulls)
		}
	}
}
