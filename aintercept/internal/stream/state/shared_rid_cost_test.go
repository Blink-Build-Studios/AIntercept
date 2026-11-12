package state

import (
	"fmt"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// One more call or report sharing a request id costs the same whether 1, 100
// or 2,000 steps already carry that id: a record touches the rows it affects,
// not every row of its request id.
func TestSharedRequestIDFlatCost(t *testing.T) {
	measure := func(shared int) (float64, time.Duration) {
		s := New("s:rid")
		for i := 0; i < shared; i++ {
			s.Apply(callEnd(withReq(ccCall(fmt.Sprintf("c%d", i), "rid", int64(i)*1000), "rx")))
		}
		base := int64(shared) * 1000
		pre := make([]records.Record, 600)
		for k := range pre {
			at := base + int64(k)*10
			if k%2 == 0 {
				pre[k] = ccEvent("rid", "tengu_api_success", at, `{"requestId":"rx"}`)
			} else {
				pre[k] = callEnd(withReq(ccCall(fmt.Sprintf("cx%d", k), "rid", at), fmt.Sprintf("ry%d", k)))
			}
		}
		k := 0
		allocs := testing.AllocsPerRun(200, func() { s.Apply(pre[k]); k++ })
		start := time.Now()
		for ; k < len(pre); k++ {
			s.Apply(pre[k])
		}
		return allocs, time.Since(start) / time.Duration(len(pre)-200)
	}
	a1, t1 := measure(1)
	a100, t100 := measure(100)
	a2k, t2k := measure(2000)
	t.Logf("per record, steps sharing the id: 1 %.1f allocs %v; 100 %.1f allocs %v; 2,000 %.1f allocs %v", a1, t1, a100, t100, a2k, t2k)
	if a2k > a1*1.5+4 {
		t.Errorf("allocations grow with the steps sharing a request id: %.1f at 1, %.1f at 2,000", a1, a2k)
	}
	if t2k > 4*t1+20*time.Microsecond {
		t.Errorf("time grows with the steps sharing a request id: %v at 1, %v at 2,000", t1, t2k)
	}
}
