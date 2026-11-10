package events

import (
	"encoding/json"
	"io"
	"sync"
)

// Sink consumes processed events. Implementations must be safe for use from a
// single draining goroutine. v0 provides StdoutSink; a durable sink (e.g. S3
// batching) implements the same interface.
//
// Emit must not block indefinitely — the processor calls it from the drain
// goroutine, and prolonged blocking there backs up the bounded buffer and
// causes drops. A real S3 sink should batch and flush asynchronously.
type Sink interface {
	Emit(Event) error
	// Flush is called on shutdown to drain any internal batching.
	Flush() error
}

// StdoutSink writes one JSON event per line to an io.Writer.
type StdoutSink struct {
	mu sync.Mutex
	w  io.Writer
}

// NewStdoutSink returns a Sink that writes newline-delimited JSON to w.
func NewStdoutSink(w io.Writer) *StdoutSink {
	return &StdoutSink{w: w}
}

// Emit writes the event as a single JSON line.
func (s *StdoutSink) Emit(e Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b = append(b, '\n')
	_, err = s.w.Write(b)
	return err
}

// Flush is a no-op for stdout.
func (s *StdoutSink) Flush() error { return nil }
