package process

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	"github.com/parquet-go/parquet-go"
)

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
// (parquet-go v0.32.0's typed batch Write gets this wrong; see EncodeParquet).
func TestOptionalZeroIsNull(t *testing.T) {
	type row struct {
		S string `parquet:"s,optional"`
		N int64  `parquet:"n,optional"`
	}
	b := EncodeParquet([]row{{}, {S: "x", N: 5}, {}, {}})
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

// readBack is a Parquet file's rows.
func readBack[T any](b []byte) ([]T, error) {
	return parquet.Read[T](bytes.NewReader(b), int64(len(b)))
}
