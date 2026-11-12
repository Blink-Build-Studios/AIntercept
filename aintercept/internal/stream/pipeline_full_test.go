//go:build full

package stream

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// The end-to-end gate on a real capture (local only, make test-pipeline): the
// capture's records (aintercept canon over its raw log: the live ingest's code)
// are uploaded by N ingest pods and processed by R replicas with chaos, and at
// every checkpoint and the end the committed tables equal one pass of the
// stream state over everything uploaded. (internal/stream/state TestRealData
// proves that one pass equals the precompute reference, table by table.)
func TestPipelineRealCapture(t *testing.T) {
	dir := os.Getenv("AINTERCEPT_RECORDS")
	if dir == "" {
		t.Skip("AINTERCEPT_RECORDS isn't set")
	}
	var recs []records.Record
	err := filepath.WalkDir(dir, func(p string, de fs.DirEntry, err error) error {
		if err != nil || de.IsDir() || !strings.HasSuffix(p, ".parquet") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		x, err := records.Decode(b)
		recs = append(recs, x...)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []int{1, 3} {
		cfg := simCfg(int64(10+r), 3, r)
		cfg.Model, cfg.Input, cfg.Steps, cfg.Checkpoints = StateModel{}, recs, 400, 4
		start := time.Now()
		res, err := Simulate(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("R=%d: %d records, %v: %+v", r, len(recs), time.Since(start).Round(time.Second), res)
		if res.Mismatches != 0 {
			t.Errorf("R=%d: %s", r, res.FirstMismatch)
		}
	}
}
