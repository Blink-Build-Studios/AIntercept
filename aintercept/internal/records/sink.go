package records

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// Putter writes one object. In the observer it's the single writer every
// output goes through.
type Putter interface {
	Put(ctx context.Context, key string, body []byte) error
}

// SinkConfig configures a records Sink.
type SinkConfig struct {
	Putter      Putter
	Prefix      string // the object prefix records/ goes under
	Incarnation string // NewIncarnation: record ids and object names begin with it
	MaxAge      time.Duration
	MaxRecords  int
	// Content is true when rows carry bodies (full interception): the line an
	// unnumbered event's key hashes is then the row with its content, as the
	// full raw log writes it.
	Content bool
	// Details writes each call's detail object (DetailKey) when it ends.
	Details bool
	Now     func() time.Time
}

// Sink is an events.Sink that assembles the rows it's given into records and
// writes each bucket's as one object, when the oldest is MaxAge old or
// MaxRecords are buffered, and at Flush.
type Sink struct {
	cfg  SinkConfig
	mu   sync.Mutex
	asm  *Assembler
	buf  []Record
	born time.Time // when the oldest buffered record arrived
	seq  int64
	wg   sync.WaitGroup
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
	if cfg.MaxRecords <= 0 {
		cfg.MaxRecords = 20000
	}
	s := &Sink{cfg: cfg, asm: NewAssembler(cfg.Incarnation)}
	s.asm.KeepDetail = cfg.Details
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
	rs := s.asm.Apply(&e, line, "")
	s.add(rs)
	return nil
}

func (s *Sink) add(rs []Record) {
	for _, d := range s.asm.TakeDetails() {
		s.putDetail(d)
	}
	if len(rs) == 0 {
		return
	}
	if len(s.buf) == 0 {
		s.born = s.cfg.Now()
	}
	s.buf = append(s.buf, rs...)
	if len(s.buf) >= s.cfg.MaxRecords {
		s.seal()
	}
}

// SealIfOld seals once the oldest buffered record is MaxAge old (the observer
// calls it on a ticker; Run does).
func (s *Sink) SealIfOld() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.buf) > 0 && s.cfg.Now().Sub(s.born) >= s.cfg.MaxAge {
		s.seal()
	}
}

// Run seals on a ticker until ctx ends.
func (s *Sink) Run(ctx context.Context) {
	t := time.NewTicker(s.cfg.MaxAge / 2)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.SealIfOld()
		}
	}
}

// seal writes the buffer, one object per bucket, named by the upload minute.
// Called with mu held; the puts run on their own goroutines.
func (s *Sink) seal() {
	by := map[int32][]Record{}
	for _, r := range s.buf {
		by[r.Bucket] = append(by[r.Bucket], r)
	}
	s.buf = nil
	now := s.cfg.Now()
	for b, rs := range by {
		s.seq++
		key := ObjectKey(s.cfg.Prefix, int(b), now, s.cfg.Incarnation, s.seq)
		body, err := Encode(rs)
		if err != nil {
			s.errs = append(s.errs, err)
			slog.Error("ALARM records encode failed", "key", key, "err", err)
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			if err := s.cfg.Putter.Put(context.Background(), key, body); err != nil {
				s.mu.Lock()
				s.errs = append(s.errs, err)
				s.mu.Unlock()
				slog.Error("ALARM records put failed", "key", key, "err", err)
			}
		}()
	}
}

// putDetail writes one call's detail object, gzipped. Called with mu held.
func (s *Sink) putDetail(d Detail) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	for _, l := range d.Lines {
		zw.Write(l)
		zw.Write([]byte{'\n'})
	}
	zw.Close()
	key := DetailKey(s.cfg.Prefix, d.CorrelationID)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := s.cfg.Putter.Put(context.Background(), key, buf.Bytes()); err != nil {
			s.mu.Lock()
			s.errs = append(s.errs, err)
			s.mu.Unlock()
			slog.Error("ALARM call detail put failed", "key", key, "err", err)
		}
	}()
}

// Wait waits for the puts started so far.
func (s *Sink) Wait() { s.wg.Wait() }

// Flush releases held records, seals everything and waits for it to be written.
func (s *Sink) Flush() error {
	s.mu.Lock()
	s.add(s.asm.Flush())
	if len(s.buf) > 0 {
		s.seal()
	}
	s.mu.Unlock()
	s.wg.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
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
