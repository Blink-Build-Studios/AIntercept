//go:build full

package stream

import (
	"context"
	"fmt"
	"testing"
)

// The exhaustive distributed replay (make test-full): every N and R in
// {1, 3, 8}, several seeds, with chaos.
func TestSimulateMatrix(t *testing.T) {
	for _, n := range []int{1, 3, 8} {
		for _, r := range []int{1, 3, 8} {
			for seed := int64(1); seed <= 3; seed++ {
				t.Run(fmt.Sprintf("N%d_R%d_seed%d", n, r, seed), func(t *testing.T) {
					t.Parallel()
					cfg := simCfg(seed, n, r)
					cfg.Records, cfg.Steps, cfg.Checkpoints = 20000, 2000, 10
					res, err := Simulate(context.Background(), cfg)
					if err != nil || res.Mismatches != 0 {
						t.Fatalf("%+v %v", res, err)
					}
					t.Logf("%+v", res)
				})
			}
		}
	}
}
