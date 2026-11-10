package events

import (
	"sync"
	"testing"
	"time"
)

// blockingSink blocks in Emit until released, so we can force the buffer to
// fill and exercise the drop path deterministically.
type blockingSink struct {
	release chan struct{}
	mu      sync.Mutex
	got     []Event
}

func (b *blockingSink) Emit(e Event) error {
	<-b.release
	b.mu.Lock()
	b.got = append(b.got, e)
	b.mu.Unlock()
	return nil
}
func (b *blockingSink) Flush() error { return nil }

func TestProcessorDropsWhenBufferFull(t *testing.T) {
	sink := &blockingSink{release: make(chan struct{})}
	p := NewProcessor(sink, ProcessorConfig{BufferSize: 4, AlarmEveryDrops: 1})

	// The drain goroutine will pull one event and block in Emit. Fill the
	// buffer (cap 4) plus extras that must be dropped.
	accepted, dropped := 0, 0
	for i := 0; i < 20; i++ {
		if p.Offer(Event{Seq: uint64(i)}) {
			accepted++
		} else {
			dropped++
		}
	}

	if dropped == 0 {
		t.Fatalf("expected some drops when buffer is full, got 0 (accepted=%d)", accepted)
	}
	// We should accept no more than buffer + the one in-flight at the sink.
	if accepted > 4+1 {
		t.Fatalf("accepted=%d exceeds buffer(4)+inflight(1)", accepted)
	}
	s := p.Stats()
	if s.Dropped == 0 {
		t.Fatalf("stats dropped counter not incremented")
	}

	// Release the sink and shut down cleanly.
	close(sink.release)
	p.Close()
}

func TestProcessorEmitsAllWhenSinkFast(t *testing.T) {
	sink := &countingSink{}
	p := NewProcessor(sink, ProcessorConfig{BufferSize: 1024})
	const n = 500
	for i := 0; i < n; i++ {
		p.Offer(Event{Seq: uint64(i)})
	}
	p.Close()
	if got := sink.count(); got != n {
		t.Fatalf("expected %d emitted, got %d (dropped=%d)", n, got, p.Stats().Dropped)
	}
}

type countingSink struct {
	mu sync.Mutex
	n  int
}

func (c *countingSink) Emit(Event) error { c.mu.Lock(); c.n++; c.mu.Unlock(); return nil }
func (c *countingSink) Flush() error     { return nil }
func (c *countingSink) count() int       { c.mu.Lock(); defer c.mu.Unlock(); return c.n }

func TestRedactHeaders(t *testing.T) {
	in := map[string]string{
		"authorization": "Bearer sk-secret",
		"x-api-key":     "key-123",
		"content-type":  "application/json",
		":path":         "/v1/messages",
	}
	out := RedactHeaders(in)
	if out["authorization"] != redactedMarker {
		t.Errorf("authorization not redacted: %q", out["authorization"])
	}
	if out["x-api-key"] != redactedMarker {
		t.Errorf("x-api-key not redacted: %q", out["x-api-key"])
	}
	if out["content-type"] != "application/json" {
		t.Errorf("content-type should be preserved, got %q", out["content-type"])
	}
	// original must be untouched
	if in["authorization"] != "Bearer sk-secret" {
		t.Errorf("RedactHeaders mutated input")
	}
}

func TestStatsLoggerStops(t *testing.T) {
	p := NewProcessor(NewStdoutSink(discard{}), ProcessorConfig{})
	stop := p.StartStatsLogger(10 * time.Millisecond)
	time.Sleep(25 * time.Millisecond)
	stop()
	p.Close()
}

type discard struct{}

func (discard) Write(b []byte) (int, error) { return len(b), nil }
