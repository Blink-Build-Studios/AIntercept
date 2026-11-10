package membudget

import (
	"sync"
	"testing"
)

func TestBudgetCountsConcurrentChargesAndPeak(t *testing.T) {
	b := New(1000)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.Add(10)
			b.Add(-10)
		}()
	}
	wg.Wait()
	if b.Held() != 0 || b.Peak() < 10 || b.Peak() > 500 {
		t.Fatalf("held %d peak %d", b.Held(), b.Peak())
	}
	b.Add(500)
	if u := b.Utilization(); u != 0.5 {
		t.Fatalf("utilization %v", u)
	}
	var nilBudget *Budget
	nilBudget.Add(5) // no panic
	if nilBudget.Utilization() != 0 {
		t.Fatal("nil budget")
	}
}
