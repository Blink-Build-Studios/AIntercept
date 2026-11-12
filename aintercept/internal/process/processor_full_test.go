//go:build full

package process

import (
	"fmt"
	"testing"
)

func init() { full = true }

// The exhaustive version of TestIncrementalDerivingEveryRun: five seeds, the
// smallest runs, every run deriving every table. Slow (minutes under -race), so
// it runs with `make test-full` (nightly in CI), not in `make check`.
func TestIncrementalDerivingEveryRunAllSeeds(t *testing.T) {
	c := newCapture()
	want, _ := rebuildDeriving(t, c)
	for seed := int64(1); seed <= 5; seed++ {
		t.Run(fmt.Sprint("seed", seed), func(t *testing.T) {
			t.Parallel()
			out := incremental(t, c, seed, newProcDeriving, tinyRuns)
			compareTables(t, readTables(t, out, "aintercept/shaped"), want)
		})
	}
}
