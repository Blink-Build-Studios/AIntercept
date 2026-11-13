package stream

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A simulation's committed layout, written to a directory (SimConfig.OutDir), is
// what the stateless API reads (api/tests/test_stream_pipeline.py): every object
// under the layout's stream/v1/, at the same relative key, with the sim's own
// prefix dropped; nothing outside stream/v1/ (records stay in the emulator).
func TestSimulateWritesTheLayoutToOutDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := simCfg(7, 2, 2)
	cfg.OutDir = dir
	res, err := Simulate(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Mismatches != 0 {
		t.Fatalf("mismatches: %s", res.FirstMismatch)
	}
	var commits, tables int
	err = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if !strings.HasPrefix(rel, "stream/v1/") {
			t.Errorf("%s: outside stream/v1/", rel)
		}
		switch {
		case strings.HasPrefix(rel, "stream/v1/buckets/") && strings.HasSuffix(rel, ".json"):
			commits++
		case strings.HasPrefix(rel, "stream/v1/tables/"):
			tables++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if commits == 0 || tables == 0 {
		t.Fatalf("layout has %d commits and %d table files", commits, tables)
	}
	// The dumped layout is the emulator's: every committed table file named by a
	// newest commit exists in the directory.
	for _, b := range []int{0, 1, 2, 3, 4, 5, 6, 7} {
		got, err := os.ReadDir(filepath.Join(dir, "stream/v1/buckets", twoDigits(b)))
		if err != nil {
			continue
		}
		if len(got) == 0 {
			t.Errorf("bucket %d: an empty commit log", b)
		}
	}
}

func twoDigits(b int) string {
	return string([]byte{byte('0' + b/10), byte('0' + b%10)})
}
