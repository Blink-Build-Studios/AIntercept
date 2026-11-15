package records

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/membudget"
)

// SinkConfig configures a records Sink.
type SinkConfig struct {
	// Raw gets every record and each completed call's rows as raw file
	// members (raw.go, docs/PIPELINE.md §2.1).
	Raw         RawWriter
	Incarnation string // NewIncarnation: record ids begin with it
	// MaxAge releases the start of a call still uploading its request after
	// this long (the assembler); default 10 s.
	MaxAge time.Duration
	// Content is true when rows carry bodies (full interception): the line an
	// unnumbered event's key hashes is then the row with its content, as the
	// full raw log writes it.
	Content bool
	// Budget is charged for the rows held for a call's member.
	Budget *membudget.Budget
	Now    func() time.Time
}

// Sink is an events.Sink that assembles the rows it's given into records and
// writes them, with each completed call's rows, to the raw files.
type Sink struct {
	cfg  SinkConfig
	mu   sync.Mutex
	asm  *Assembler
	errs []error
}

// NewSink starts a records sink.
func NewSink(cfg SinkConfig) *Sink {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MaxAge <= 0 {
		cfg.MaxAge = 10 * time.Second
	}
	s := &Sink{cfg: cfg, asm: NewAssembler(cfg.Incarnation)}
	s.asm.KeepDetail, s.asm.Budget = true, cfg.Budget
	return s
}

// Emit assembles one row.
func (s *Sink) Emit(e events.Event) error {
	row := e
	if !s.cfg.Content {
		row = e.WithoutContent()
	}
	line, err := json.Marshal(row)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.add(s.asm.Apply(&e, line, ""))
	return nil
}

// add writes records and the calls' rows completed so far. Called with mu held.
func (s *Sink) add(rs []Record) {
	ds := s.asm.TakeDetails()
	if len(rs) == 0 && len(ds) == 0 {
		return
	}
	ms, err := RawMembers(rs, ds)
	if err == nil {
		for _, m := range ms {
			if err = s.cfg.Raw.Write(m); err != nil {
				break
			}
		}
	}
	if err != nil {
		s.errs = append(s.errs, err)
		slog.Error("ALARM raw file write failed", "err", err)
	}
}

// SealIfOld is Tick (the observer's older name for it).
func (s *Sink) SealIfOld() { s.Tick() }

// Tick releases held starts of long uploads (the assembler).
func (s *Sink) Tick() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.add(s.asm.StartUploading(s.cfg.Now().Add(-s.cfg.MaxAge).UnixNano()))
}

// Run ticks every second until ctx ends.
func (s *Sink) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Tick()
		}
	}
}

// Flush releases held records and writes everything.
func (s *Sink) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.add(s.asm.Flush())
	err := errors.Join(s.errs...)
	s.errs = nil
	return err
}

// Cases is the assembler's case histogram so far.
func (s *Sink) Cases() map[string]int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.asm.Cases()
}
