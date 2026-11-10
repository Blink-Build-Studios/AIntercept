package events

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// FileSink writes one JSON event per line (NDJSON) to a file. It is the durable
// capture path used to build the extractor: point it at a mounted volume and
// the full event stream — including DEV_CAPTURE_FULL_BODIES content — survives
// container restarts and is immune to Docker log rotation.
//
// This is NOT the production sink. The forward-looking durable sink is S3-style
// batching; FileSink is the local-dev stand-in that implements the same Sink
// interface, so swapping it later touches nothing in the hot path.
type FileSink struct {
	mu sync.Mutex
	f  *os.File
	w  *bufio.Writer
}

// NewFileSink opens (creating parent dirs, appending if present) the capture
// file at path.
func NewFileSink(path string) (*FileSink, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("file sink mkdir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("file sink open: %w", err)
	}
	return &FileSink{f: f, w: bufio.NewWriterSize(f, 64*1024)}, nil
}

// Emit appends the event as a single JSON line.
func (s *FileSink) Emit(e Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.w.Write(b); err != nil {
		return err
	}
	return s.w.WriteByte('\n')
}

// Flush flushes buffered writes to the underlying file.
func (s *FileSink) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Flush()
}

// Close flushes and closes the file.
func (s *FileSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.w.Flush(); err != nil {
		return err
	}
	return s.f.Close()
}

// MultiSink fans each event out to several sinks. Used to send full-fidelity
// events to a file while sending a stdout-safe (body-stripped) copy to the
// terminal. An error from any sink is returned, but all sinks are attempted.
type MultiSink struct {
	// StdoutSafe sinks receive Event.ForStdout() (no full bodies).
	stdoutSafe []Sink
	// Durable sinks receive the complete event.
	durable []Sink
}

// NewMultiSink builds a fan-out. stdoutSafe sinks get body-stripped events;
// durable sinks get the full event.
func NewMultiSink(stdoutSafe, durable []Sink) *MultiSink {
	return &MultiSink{stdoutSafe: stdoutSafe, durable: durable}
}

// Emit sends a body-stripped copy to stdout-safe sinks and the full event to
// durable sinks.
func (m *MultiSink) Emit(e Event) error {
	var firstErr error
	safe := e.ForStdout()
	for _, s := range m.stdoutSafe {
		if err := s.Emit(safe); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	for _, s := range m.durable {
		if err := s.Emit(e); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Flush flushes every sink.
func (m *MultiSink) Flush() error {
	var firstErr error
	for _, s := range append(append([]Sink{}, m.stdoutSafe...), m.durable...) {
		if err := s.Flush(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
