package events

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Writer is the observer's one way to object storage
// (docs/STREAMING_PIPELINE.md, phase 1). Every sink is a Stream of it; nothing
// else uploads.
//
// One goroutine (Run's loop) owns the buffers. It selects over incoming
// lines, a ticker and the byte cap: on the tick (FlushEvery, 10 s by default)
// or when the buffered bytes reach MaxBufferBytes, it seals each stream's
// buffer into a file in the on-disk spool and hands it to the uploaders. The
// loop never waits on the object store, so a slow or failing store costs disk,
// never the request path.
//
// Uploaders retry each spooled file until the store accepts it, with capped
// backoff, forever. A file leaves the spool only after a successful upload, and
// a new Writer on the same spool uploads whatever a crashed one left behind. So
// nothing sealed is ever lost; what a hard kill can lose is the unsealed
// buffer, at most FlushEvery of data.
//
// When the spool passes 95% of SpoolMaxBytes, Full reports true until it falls
// back under 80%. Callers decide what full means: observe mode drops new
// events as a counted last resort; enforce mode refuses new calls.
type Writer struct {
	cfg     WriterConfig
	targets map[string]Target

	in       chan item
	flushReq chan chan struct{}
	loopDone chan struct{}
	mu       sync.RWMutex // guards closed against Write racing close(in)
	closed   bool

	smu     sync.Mutex // guards streams
	streams []*streamBuf

	// The upload queue: spooled file paths waiting for an uploader.
	qmu      sync.Mutex
	qcond    *sync.Cond
	queue    []string
	inFlight int // queued + being uploaded
	stopping bool

	ctx       context.Context
	cancel    context.CancelFunc
	uploaders sync.WaitGroup
	seq       atomic.Uint64

	spoolBytes     atomic.Int64
	spoolFiles     atomic.Int64
	stranded       atomic.Int64 // spooled for a target this process doesn't have
	full           atomic.Bool
	uploads        atomic.Uint64
	uploadFailures atomic.Uint64
	oldest         sync.Map // path -> sealed time, for OldestSpoolAge
}

// WriterConfig configures a Writer.
type WriterConfig struct {
	// SpoolDir holds sealed files until they're uploaded. Required. Mount it on
	// a volume that outlives the container (an emptyDir), so a restarted
	// observer uploads what the last one sealed.
	SpoolDir string
	// SpoolMaxBytes is the spool's capacity: Full past 95% of it. Default 10 GiB.
	SpoolMaxBytes int64
	// FlushEvery seals every buffer this often. Default 10 s.
	FlushEvery time.Duration
	// MaxBufferBytes seals every buffer once this much is buffered. Default 8 MiB.
	MaxBufferBytes int
	// Uploaders is the number of concurrent uploads. Default 4.
	Uploaders int
	// UploadTimeout bounds one upload attempt. Default 30 s.
	UploadTimeout time.Duration
	// RetryMin and RetryMax bound the backoff between attempts of one file.
	// Defaults 1 s and 60 s.
	RetryMin, RetryMax time.Duration
	// Targets are where files go, by name (S3Target, DirTarget).
	Targets []Target
	// NewTicker overrides the ticker (tests).
	NewTicker func(time.Duration) (<-chan time.Time, func())
	// SkipFsync skips fsync on spooled files (tests only).
	SkipFsync bool
}

// StreamConfig configures one Stream: a sequence of NDJSON lines sealed into
// objects of one key layout on one target.
type StreamConfig struct {
	// Target names the Writer target the stream's files go to.
	Target string
	// Key names the object for the stream's seq'th file (from 1), whose first
	// line arrived at start.
	Key func(seq uint64, start time.Time) string
	// MaxBytes and MaxEvents seal this stream's buffer early (0: no limit).
	MaxBytes  int
	MaxEvents int
	// Gzip compresses each file (Content-Encoding: gzip).
	Gzip        bool
	ContentType string
}

// Stream writes lines to one key layout through the Writer.
type Stream struct {
	w  *Writer
	id int
}

type streamBuf struct {
	cfg   StreamConfig
	buf   bytes.Buffer
	count int
	start time.Time
	seq   uint64
}

type item struct {
	stream int
	line   []byte
}

// spoolHeader is the first line of a spooled file.
type spoolHeader struct {
	Target          string `json:"target"`
	Key             string `json:"key"`
	ContentType     string `json:"ct,omitempty"`
	ContentEncoding string `json:"ce,omitempty"`
}

// WriterStats is a snapshot of the writer's spool and uploads.
type WriterStats struct {
	SpoolFiles     int64   `json:"spool_files"`
	SpoolBytes     int64   `json:"spool_bytes"`
	OldestSpoolAge float64 `json:"oldest_spool_age_seconds"`
	Stranded       int64   `json:"stranded_files"`
	Full           bool    `json:"full"`
	Uploads        uint64  `json:"uploads"`
	UploadFailures uint64  `json:"upload_failures"`
}

const (
	defaultSpoolMaxBytes  = 10 << 30
	defaultFlushEvery     = 10 * time.Second
	defaultMaxBufferBytes = 8 << 20
	defaultUploaders      = 4
	defaultUploadTimeout  = 30 * time.Second
	spoolHigh, spoolLow   = 0.95, 0.80
)

// NewWriter starts the writer: it re-queues every file a previous process left
// in the spool, then starts the loop and the uploaders.
func NewWriter(cfg WriterConfig) (*Writer, error) {
	if cfg.SpoolDir == "" {
		return nil, errors.New("writer: SpoolDir is required")
	}
	if cfg.SpoolMaxBytes <= 0 {
		cfg.SpoolMaxBytes = defaultSpoolMaxBytes
	}
	if cfg.FlushEvery <= 0 {
		cfg.FlushEvery = defaultFlushEvery
	}
	if cfg.MaxBufferBytes <= 0 {
		cfg.MaxBufferBytes = defaultMaxBufferBytes
	}
	if cfg.Uploaders <= 0 {
		cfg.Uploaders = defaultUploaders
	}
	if cfg.UploadTimeout <= 0 {
		cfg.UploadTimeout = defaultUploadTimeout
	}
	if cfg.RetryMin <= 0 {
		cfg.RetryMin = time.Second
	}
	if cfg.RetryMax < cfg.RetryMin {
		cfg.RetryMax = 60 * time.Second
	}
	if cfg.NewTicker == nil {
		cfg.NewTicker = func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		}
	}
	for _, d := range []string{"ready", "tmp"} {
		if err := os.MkdirAll(filepath.Join(cfg.SpoolDir, d), 0o750); err != nil {
			return nil, fmt.Errorf("writer: spool dir: %w", err)
		}
	}
	w := &Writer{
		cfg:      cfg,
		targets:  map[string]Target{},
		in:       make(chan item, 1024),
		flushReq: make(chan chan struct{}),
		loopDone: make(chan struct{}),
	}
	w.qcond = sync.NewCond(&w.qmu)
	w.ctx, w.cancel = context.WithCancel(context.Background())
	for _, t := range cfg.Targets {
		w.targets[t.Name()] = t
	}
	if err := w.recover(); err != nil {
		return nil, err
	}
	go w.loop()
	for i := 0; i < cfg.Uploaders; i++ {
		w.uploaders.Add(1)
		go w.uploader()
	}
	return w, nil
}

// NewStream adds a stream. Call before writing to it, from one goroutine at
// setup (streams are registered before traffic starts).
func (w *Writer) NewStream(cfg StreamConfig) *Stream {
	w.smu.Lock()
	defer w.smu.Unlock()
	w.streams = append(w.streams, &streamBuf{cfg: cfg})
	return &Stream{w: w, id: len(w.streams) - 1}
}

// Write appends one line (without its newline) to the stream. It blocks only
// while the loop is busy appending, never on the object store. It fails once
// the writer is closed.
func (s *Stream) Write(line []byte) error {
	w := s.w
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.closed {
		return errors.New("writer: write after close")
	}
	w.in <- item{stream: s.id, line: append([]byte(nil), line...)}
	return nil
}

// Flush seals every buffer and waits until everything sealed so far has been
// uploaded.
func (w *Writer) Flush() error {
	w.mu.RLock()
	closed := w.closed
	if !closed {
		done := make(chan struct{})
		w.flushReq <- done
		<-done
	}
	w.mu.RUnlock()
	w.qmu.Lock()
	for w.inFlight > 0 {
		w.qcond.Wait()
	}
	w.qmu.Unlock()
	return nil
}

// Close seals what's buffered, waits for every upload, and stops. Idempotent.
func (w *Writer) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	close(w.in)
	w.mu.Unlock()
	<-w.loopDone
	if err := w.Flush(); err != nil {
		return err
	}
	w.qmu.Lock()
	w.stopping = true
	w.qcond.Broadcast()
	w.qmu.Unlock()
	w.uploaders.Wait()
	w.cancel()
	return nil
}

// Abandon stops the writer as a crash would: no sealing, no waiting. What was
// spooled stays for the next writer on the same spool. Tests only.
func (w *Writer) Abandon() {
	w.cancel()
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.in)
	}
	w.mu.Unlock()
	<-w.loopDone
	w.qmu.Lock()
	w.stopping = true
	w.queue = nil
	w.qcond.Broadcast()
	w.qmu.Unlock()
	w.uploaders.Wait()
}

// Full reports whether the spool is past its high watermark (and not yet back
// under its low one).
func (w *Writer) Full() bool { return w.full.Load() }

// Stats returns a snapshot.
func (w *Writer) Stats() WriterStats {
	s := WriterStats{
		SpoolFiles:     w.spoolFiles.Load(),
		SpoolBytes:     w.spoolBytes.Load(),
		Stranded:       w.stranded.Load(),
		Full:           w.full.Load(),
		Uploads:        w.uploads.Load(),
		UploadFailures: w.uploadFailures.Load(),
	}
	var oldest time.Time
	w.oldest.Range(func(_, v any) bool {
		if t := v.(time.Time); oldest.IsZero() || t.Before(oldest) {
			oldest = t
		}
		return true
	})
	if !oldest.IsZero() {
		s.OldestSpoolAge = time.Since(oldest).Seconds()
	}
	return s
}

// ── the loop ──────────────────────────────────────────────────────────────────

func (w *Writer) loop() {
	defer close(w.loopDone)
	tick, stop := w.cfg.NewTicker(w.cfg.FlushEvery)
	defer stop()
	buffered := 0
	add := func(it item) {
		w.smu.Lock()
		sb := w.streams[it.stream]
		w.smu.Unlock()
		if sb.count == 0 {
			sb.start = nowFn()
		}
		sb.buf.Write(it.line)
		sb.buf.WriteByte('\n')
		sb.count++
		buffered += len(it.line) + 1
		if (sb.cfg.MaxBytes > 0 && sb.buf.Len() >= sb.cfg.MaxBytes) || (sb.cfg.MaxEvents > 0 && sb.count >= sb.cfg.MaxEvents) {
			buffered -= sb.buf.Len()
			w.seal(sb)
		}
		if buffered >= w.cfg.MaxBufferBytes {
			w.sealAll()
			buffered = 0
		}
	}
	for {
		select {
		case it, ok := <-w.in:
			if !ok {
				if w.ctx.Err() == nil { // closed, not abandoned
					w.sealAll()
				}
				return
			}
			add(it)
		case <-tick:
			w.sealAll()
			buffered = 0
		case done := <-w.flushReq:
			// Everything written before Flush is already in the channel: take
			// it all before sealing.
			for drained := false; !drained; {
				select {
				case it := <-w.in:
					add(it)
				default:
					drained = true
				}
			}
			w.sealAll()
			buffered = 0
			close(done)
		}
	}
}

func (w *Writer) sealAll() {
	w.smu.Lock()
	streams := append([]*streamBuf(nil), w.streams...)
	w.smu.Unlock()
	for _, sb := range streams {
		w.seal(sb)
	}
}

// seal writes a stream's buffer to the spool and queues it for upload. A spool
// write failure (a full or broken disk) is retried: the data is still in
// memory, and dropping it is not an option.
func (w *Writer) seal(sb *streamBuf) {
	if sb.count == 0 {
		return
	}
	sb.seq++
	key := sb.cfg.Key(sb.seq, sb.start)
	body := sb.buf.Bytes()
	ce := ""
	if sb.cfg.Gzip {
		var gz bytes.Buffer
		zw := gzip.NewWriter(&gz)
		_, _ = zw.Write(body)
		_ = zw.Close()
		body = gz.Bytes()
		ce = "gzip"
	}
	hdr := spoolHeader{Target: sb.cfg.Target, Key: key, ContentType: sb.cfg.ContentType, ContentEncoding: ce}
	for attempt := 0; ; attempt++ {
		path, size, err := w.spool(hdr, body)
		if err == nil {
			w.enqueue(path, size)
			break
		}
		if attempt == 0 || attempt%60 == 0 {
			slog.Error("ALARM writer: can't write to the spool; holding the batch in memory and retrying", "key", key, "err", err)
		}
		time.Sleep(time.Second)
	}
	sb.buf.Reset()
	sb.count = 0
}

// spool writes one file: a JSON header line, then the body. It's written to
// tmp/, fsynced, and renamed into ready/, so a crash leaves a whole file or none.
func (w *Writer) spool(hdr spoolHeader, body []byte) (string, int64, error) {
	name := fmt.Sprintf("%020d-%08d.spool", time.Now().UnixNano(), w.seq.Add(1))
	tmp := filepath.Join(w.cfg.SpoolDir, "tmp", name)
	dst := filepath.Join(w.cfg.SpoolDir, "ready", name)
	h, _ := json.Marshal(hdr)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return "", 0, err
	}
	bw := bufio.NewWriter(f)
	_, _ = bw.Write(h)
	_ = bw.WriteByte('\n')
	_, _ = bw.Write(body)
	err = bw.Flush()
	if err == nil && !w.cfg.SkipFsync {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return "", 0, err
	}
	if !w.cfg.SkipFsync {
		if d, err := os.Open(filepath.Dir(dst)); err == nil {
			_ = d.Sync()
			_ = d.Close()
		}
	}
	return dst, int64(len(h) + 1 + len(body)), nil
}

func (w *Writer) enqueue(path string, size int64) {
	w.oldest.Store(path, time.Now())
	w.spoolFiles.Add(1)
	w.addSpoolBytes(size)
	w.qmu.Lock()
	w.queue = append(w.queue, path)
	w.inFlight++
	w.qcond.Signal()
	w.qmu.Unlock()
}

func (w *Writer) addSpoolBytes(n int64) {
	b := w.spoolBytes.Add(n)
	max := float64(w.cfg.SpoolMaxBytes)
	switch {
	case !w.full.Load() && float64(b) >= spoolHigh*max:
		w.full.Store(true)
		slog.Error("ALARM writer: spool full; observed events are dropped (counted) and enforced calls refused until it drains",
			"spool_bytes", b, "spool_max_bytes", w.cfg.SpoolMaxBytes)
	case w.full.Load() && float64(b) <= spoolLow*max:
		w.full.Store(false)
		slog.Info("writer: spool drained below its low watermark", "spool_bytes", b)
	}
}

// recover queues every file a previous process left in the spool, oldest
// first, and clears half-written temp files.
func (w *Writer) recover() error {
	tmps, _ := os.ReadDir(filepath.Join(w.cfg.SpoolDir, "tmp"))
	for _, e := range tmps {
		_ = os.Remove(filepath.Join(w.cfg.SpoolDir, "tmp", e.Name()))
	}
	entries, err := os.ReadDir(filepath.Join(w.cfg.SpoolDir, "ready"))
	if err != nil {
		return fmt.Errorf("writer: read spool: %w", err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".spool") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, n := range names {
		p := filepath.Join(w.cfg.SpoolDir, "ready", n)
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		w.enqueue(p, info.Size())
	}
	if len(names) > 0 {
		slog.Info("writer: uploading files a previous process left in the spool", "files", len(names))
	}
	return nil
}

// ── uploads ───────────────────────────────────────────────────────────────────

func (w *Writer) uploader() {
	defer w.uploaders.Done()
	for {
		w.qmu.Lock()
		for len(w.queue) == 0 && !w.stopping {
			w.qcond.Wait()
		}
		if len(w.queue) == 0 {
			w.qmu.Unlock()
			return
		}
		path := w.queue[0]
		w.queue = w.queue[1:]
		w.qmu.Unlock()

		if !w.upload(path) { // abandoned mid-upload: leave the file for the next writer
			return
		}
		w.qmu.Lock()
		w.inFlight--
		w.qcond.Broadcast()
		w.qmu.Unlock()
	}
}

// upload sends one spooled file until the store accepts it, then removes it.
// It returns false only when the writer is abandoned.
func (w *Writer) upload(path string) bool {
	hdr, body, size, err := readSpooled(path)
	if err != nil {
		slog.Error("ALARM writer: unreadable spool file left in place", "path", path, "err", err)
		w.stranded.Add(1)
		return true
	}
	t, ok := w.targets[hdr.Target]
	if !ok {
		slog.Error("ALARM writer: spool file for a target this observer doesn't have; left in place", "path", path, "target", hdr.Target)
		w.stranded.Add(1)
		return true
	}
	backoff := w.cfg.RetryMin
	for attempt := 1; ; attempt++ {
		ctx, cancel := context.WithTimeout(w.ctx, w.cfg.UploadTimeout)
		err := t.Put(ctx, Object{Key: hdr.Key, Body: body, ContentType: hdr.ContentType, ContentEncoding: hdr.ContentEncoding})
		cancel()
		if err == nil {
			break
		}
		if w.ctx.Err() != nil {
			return false
		}
		w.uploadFailures.Add(1)
		if attempt == 1 || attempt%10 == 0 {
			slog.Warn("ALARM writer: upload failed; retrying, the file stays in the spool", "key", hdr.Key, "attempt", attempt, "err", err)
		}
		sleep := backoff/2 + time.Duration(rand.Int63n(int64(backoff/2)+1))
		select {
		case <-time.After(sleep):
		case <-w.ctx.Done():
			return false
		}
		if backoff *= 2; backoff > w.cfg.RetryMax {
			backoff = w.cfg.RetryMax
		}
	}
	w.uploads.Add(1)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Error("writer: uploaded but couldn't remove the spool file", "path", path, "err", err)
	}
	w.oldest.Delete(path)
	w.spoolFiles.Add(-1)
	w.addSpoolBytes(-size)
	return true
}

func readSpooled(path string) (spoolHeader, []byte, int64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return spoolHeader{}, nil, 0, err
	}
	i := bytes.IndexByte(raw, '\n')
	if i < 0 {
		return spoolHeader{}, nil, 0, errors.New("no header line")
	}
	var h spoolHeader
	if err := json.Unmarshal(raw[:i], &h); err != nil {
		return spoolHeader{}, nil, 0, err
	}
	return h, raw[i+1:], int64(len(raw)), nil
}
