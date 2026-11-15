package process

import (
	"context"
	"reflect"
	"testing"
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
}
