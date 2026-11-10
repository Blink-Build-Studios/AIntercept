package events

import (
	"sync"
	"time"
)

// sampleLimiter is a simple fixed-window rate limiter for schema sampling: it
// allows up to perMinute events in each rolling one-minute window. It bounds how
// many unrecognized-shape fingerprints we emit so a flood of a brand-new shape
// cannot fill the schema sink. It is called only from the single drain
// goroutine today, but is mutex-guarded so it stays correct if that changes.
type sampleLimiter struct {
	mu          sync.Mutex
	perMinute   int
	windowStart time.Time
	count       int
}

// newSampleLimiter returns a limiter allowing perMinute events per minute. A
// non-positive perMinute disables limiting (allow always returns true).
func newSampleLimiter(perMinute int) *sampleLimiter {
	return &sampleLimiter{perMinute: perMinute, windowStart: nowFn()}
}

// allow reports whether another sample may be emitted now, consuming one slot if
// so. The window resets every minute.
func (l *sampleLimiter) allow() bool {
	if l == nil || l.perMinute <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := nowFn()
	if now.Sub(l.windowStart) >= time.Minute {
		l.windowStart = now
		l.count = 0
	}
	if l.count >= l.perMinute {
		return false
	}
	l.count++
	return true
}
