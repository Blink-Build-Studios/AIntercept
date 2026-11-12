package events

import (
	"strings"
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

// A full buffer makes Offer wait for room instead of dropping: nothing is lost
// while the spool has room.
func TestProcessorBlocksWhenBufferFull(t *testing.T) {
	sink := &blockingSink{release: make(chan struct{})}
	p := NewProcessor(sink, ProcessorConfig{BufferSize: 4})
	offered := make(chan int, 1)
	go func() {
		n := 0
		for i := 0; i < 20; i++ {
			if p.Offer(Event{Seq: uint64(i)}) {
				n++
			}
		}
		offered <- n
	}()
	select {
	case <-offered:
		t.Fatal("Offer returned for all 20 events while the sink was stuck and the buffer held 4: it dropped instead of waiting")
	case <-time.After(100 * time.Millisecond):
	}
	close(sink.release)
	if n := <-offered; n != 20 {
		t.Fatalf("accepted %d of 20", n)
	}
	p.Close()
	if got := len(sink.got); got != 20 {
		t.Fatalf("sink got %d of 20 events", got)
	}
	if s := p.Stats(); s.LastResortDrops != 0 {
		t.Fatalf("LastResortDrops = %d with the spool not full", s.LastResortDrops)
	}
}

// Observe mode's last resort: only once the spool is full does Offer drop, and
// every drop is counted.
func TestProcessorLastResortDropOnlyWhenSpoolFull(t *testing.T) {
	sink := &countingSink{}
	full := false
	var mu sync.Mutex
	p := NewProcessor(sink, ProcessorConfig{BufferSize: 1024, Full: func() bool { mu.Lock(); defer mu.Unlock(); return full }})
	for i := 0; i < 10; i++ {
		if !p.Offer(Event{Seq: uint64(i)}) {
			t.Fatalf("dropped event %d with the spool not full", i)
		}
	}
	mu.Lock()
	full = true
	mu.Unlock()
	for i := 10; i < 15; i++ {
		if p.Offer(Event{Seq: uint64(i)}) {
			t.Fatalf("accepted event %d with the spool full", i)
		}
	}
	// Enforce-mode streams never drop: OfferMust waits even when full.
	p.OfferMust(Event{Seq: 99})
	p.Close()
	if got := sink.count(); got != 11 {
		t.Fatalf("sink got %d events, want 11 (10 before full + the enforce one)", got)
	}
	if s := p.Stats(); s.LastResortDrops != 5 {
		t.Fatalf("LastResortDrops = %d, want 5", s.LastResortDrops)
	}
}

// A panic while processing an event still records the event as it arrived.
func TestProcessorPanicEmitsRawEvent(t *testing.T) {
	sink := &orderSink{}
	p := NewProcessor(sink, ProcessorConfig{OnComplete: func(*Event) { panic("poisoned transaction") }})
	p.Offer(Event{CorrelationID: "boom", Phase: PhaseComplete, Host: "api.anthropic.com"})
	p.Offer(Event{CorrelationID: "fine", Phase: PhaseRequestHeaders})
	p.Close()
	got := sink.ids()
	if strings.Join(got, ",") != "boom,fine" {
		t.Fatalf("emitted %v; want the panicking event recorded raw, then the next one", got)
	}
	if s := p.Stats(); s.Panics != 1 {
		t.Fatalf("Panics = %d, want 1", s.Panics)
	}
}

type orderSink struct {
	mu  sync.Mutex
	got []Event
}

func (r *orderSink) Emit(e Event) error {
	r.mu.Lock()
	r.got = append(r.got, e)
	r.mu.Unlock()
	return nil
}
func (r *orderSink) Flush() error { return nil }
func (r *orderSink) ids() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.got {
		out = append(out, e.CorrelationID)
	}
	return out
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
		t.Fatalf("expected %d emitted, got %d", n, got)
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
