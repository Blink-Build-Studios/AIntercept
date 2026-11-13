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
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/membudget"
)

// Putter writes one records object. In the observer it's the single writer
// every output goes through (WriterPutter).
type Putter interface {
	Put(ctx context.Context, key string, body []byte) error
}

// UploadKeyPutter is a Putter that names records files by their upload minute
// (ObjectKeyTemplate) when they're uploaded, not when they're sealed. The
// observer's writer is one.
type UploadKeyPutter interface {
	PutAtUpload(ctx context.Context, tmpl string, body []byte) error
}

// DetailPutter is a Putter that writes call detail objects apart (their own
// content type and encoding). A Putter without it writes them with Put.
type DetailPutter interface {
	PutDetail(ctx context.Context, key string, body []byte) error
}

// WriterPutter is the observer's single writer as a Putter: records and call
// detail objects each go through an object stream of it, to target.
func WriterPutter(w *events.Writer, target string) Putter {
	return writerPutter{
		records: w.NewObjectStream(events.ObjectStreamConfig{Target: target, ContentType: "application/vnd.apache.parquet"}),
		details: w.NewObjectStream(events.ObjectStreamConfig{Target: target, ContentType: "application/x-ndjson", ContentEncoding: "gzip"}),
	}
}

type writerPutter struct{ records, details *events.ObjectStream }

func (p writerPutter) Put(_ context.Context, key string, body []byte) error {
	return p.records.Put(key, body)
}
func (p writerPutter) PutAtUpload(_ context.Context, tmpl string, body []byte) error {
	return p.records.PutAtUpload(tmpl, body)
}
func (p writerPutter) PutDetail(_ context.Context, key string, body []byte) error {
	return p.details.Put(key, body)
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
	// Budget is charged for the rows held for detail objects.
	Budget *membudget.Budget
	Now    func() time.Time
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
	s.asm.KeepDetail, s.asm.Budget = cfg.Details, cfg.Budget
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
		body, err := Encode(rs)
		if err != nil {
			s.errs = append(s.errs, err)
			slog.Error("ALARM records encode failed", "bucket", b, "err", err)
			continue
		}
		seq := s.seq
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			if err := PutBucketFile(context.Background(), s.cfg.Putter, s.cfg.Prefix, int(b), s.cfg.Incarnation, seq, body, now); err != nil {
				s.mu.Lock()
				s.errs = append(s.errs, err)
				s.mu.Unlock()
				slog.Error("ALARM records put failed", "bucket", b, "err", err)
			}
		}()
	}
}

// PutBucketFile writes one bucket's encoded records file. A Putter that names
// files at upload (UploadKeyPutter: the observer's writer) gets the key
// template; any other is given the key for now.
func PutBucketFile(ctx context.Context, put Putter, prefix string, bucket int, incarnation string, seq int64, body []byte, now time.Time) error {
	if up, ok := put.(UploadKeyPutter); ok {
		return up.PutAtUpload(ctx, ObjectKeyTemplate(prefix, bucket, incarnation, seq), body)
	}
	return put.Put(ctx, ObjectKey(prefix, bucket, now, incarnation, seq), body)
}

// putDetail writes one call's detail object, gzipped. Called with mu held.
func (s *Sink) putDetail(d Detail) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	for _, l := range d.Lines {
		zw.Write(l)
		zw.Write([]byte{'\n'})
	}
	// The routing line: every key the call's records went to (the drawer
	// loads only those).
	routing, _ := json.Marshal(struct {
		Phase         string   `json:"phase"`
		CorrelationID string   `json:"correlation_id"`
		RoutingKeys   []string `json:"routing_keys"`
	}{"routing", d.CorrelationID, append([]string{}, d.RoutingKeys...)})
	zw.Write(routing)
	zw.Write([]byte{'\n'})
	zw.Close()
	key := DetailKey(s.cfg.Prefix, d.CorrelationID)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		put := s.cfg.Putter.Put
		if dp, ok := s.cfg.Putter.(DetailPutter); ok {
			put = dp.PutDetail
		}
		if err := put(context.Background(), key, buf.Bytes()); err != nil {
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
