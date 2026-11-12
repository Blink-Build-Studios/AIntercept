package state

import (
	"fmt"
	"testing"
)

// A retry rebuilds only the rows of the failed calls it becomes the nearest
// retry of, however many failed calls its window holds: the rows it rebuilds
// are exactly the rows it changes.
func TestRetryRewritesOnlyCallsItIsNearestFor(t *testing.T) {
	s := New("s:w")
	const failedCalls = 300
	for i := 0; i < failedCalls; i++ {
		s.Apply(callEnd(failed(rqCall(fmt.Sprintf("f%d", i), "w", int64(i), "p", fmt.Sprintf("r%d", i)), 529)))
	}
	s.Apply(retryEv("w", failedCalls, "p", 1)) // nearest for every failed call
	before := s.rq.rebuilt
	d := s.Apply(retryEv("w", failedCalls+5000, "p", 2))
	n := 0
	for _, c := range d.Changes {
		if c.Table == TableRequestTxs {
			n++
		}
	}
	if got := s.rq.rebuilt - before; got != n {
		t.Errorf("a retry rebuilt %d request rows and changed %d (of %d failed calls in its window)", got, n, failedCalls)
	}
}

// Allocations per request record on the retry-dense mix: the rows a record
// changes are rebuilt from facts the state already parsed, not re-parsed.
func TestRequestsAllocsPerRecord(t *testing.T) {
	s := New("s:big")
	for i := 0; i < 1000; i++ {
		at := int64(i) * 1000
		pid, rid := fmt.Sprintf("p%d", i/50), fmt.Sprintf("r%d", i)
		switch {
		case i%100 == 99:
			s.Apply(retryEv("big", at, pid, 1))
		case i%50 == 49:
			s.Apply(callEnd(failed(rqCall(fmt.Sprintf("c%d", i), "big", at, pid, rid), 529)))
		case i%3 == 0:
			s.Apply(callEnd(rqCall(fmt.Sprintf("c%d", i), "big", at, pid, rid)))
		default:
			s.Apply(reportEv("big", at, fmt.Sprintf("r%d", i-1)))
		}
	}
	pre := requestsDenseMix(1000)
	k := 0
	allocs := testing.AllocsPerRun(1000, func() { s.Apply(pre[k]); k++ })
	t.Logf("%.1f allocs per record", allocs)
	if allocs > 200 {
		t.Errorf("%.1f allocations per record on the retry-dense mix, want at most 200", allocs)
	}
}
