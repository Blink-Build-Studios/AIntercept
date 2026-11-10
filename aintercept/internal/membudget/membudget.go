// Package membudget measures the bytes the observer holds for traffic in
// flight: request/response bodies buffered per stream, WebSocket decoder
// state and turns in progress, and bodies queued for extraction. Utilization
// (held / limit) is what the observer reports to Envoy for load balancing
// (ORCA) and to the autoscaler, so pods are added and new streams steered
// away before memory runs out. It measures; it never refuses.
package membudget

import "sync/atomic"

// Budget is a byte budget shared by all streams of one observer.
type Budget struct {
	limit int64
	held  atomic.Int64
	peak  atomic.Int64
}

// New returns a budget of limit bytes (at least 1).
func New(limit int64) *Budget {
	return &Budget{limit: max(limit, 1)}
}

// Add charges n bytes, or releases them when n is negative. A nil Budget
// ignores it, so callers need no checks.
func (b *Budget) Add(n int64) {
	if b == nil || n == 0 {
		return
	}
	v := b.held.Add(n)
	for {
		p := b.peak.Load()
		if v <= p || b.peak.CompareAndSwap(p, v) {
			return
		}
	}
}

// Held is the bytes currently held.
func (b *Budget) Held() int64 {
	if b == nil {
		return 0
	}
	return b.held.Load()
}

// Limit is the budget's size in bytes.
func (b *Budget) Limit() int64 {
	if b == nil {
		return 0
	}
	return b.limit
}

// Peak is the most bytes held at once since start.
func (b *Budget) Peak() int64 {
	if b == nil {
		return 0
	}
	return b.peak.Load()
}

// Utilization is held / limit (may exceed 1).
func (b *Budget) Utilization() float64 {
	if b == nil {
		return 0
	}
	return float64(b.held.Load()) / float64(b.limit)
}
