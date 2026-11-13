package records

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// v5 ingest (docs/PIPELINE.md §1): records are buffered per routing key, each
// buffer a file in the spool (one JSON record per line, fsynced about once a
// second by Run). A buffer flushes when it reaches FlushBytes or when its
// timer, from its first record, runs out (FlushAge, ±10% jitter, at most 60
// s); over MaxBufferBytes across all buffers, the oldest flush first. A flush
// encodes the buffer as one Parquet ingest file, hands it to the writer
// durably (PutKeyFile) and the writer removes the buffer file once its spool
// holds it, so a crash loses nothing that reached the disk and nothing is
// uploaded twice.

const (
	defaultFlushBytes     = 1 << 20
	defaultFlushAge       = 30 * time.Second
	maxFlushAge           = 60 * time.Second
	defaultMaxBufferBytes = 64 << 20
)

// KeyTemplate is an ingest file's key, with the writer's UploadTime placeholder
// for its S3 time: <prefix>/v1/keys/<url.PathEscape(key)>/in/<time>-<inc>-<seq>.parquet.
// It's the layout's rollup.InPrefix + rollup.InName (records can't import
// rollup, which reads records); keyflag's test pins the two equal.
func KeyTemplate(prefix, key, incarnation string, seq int64) string {
	p := strings.Trim(prefix, "/")
	if p != "" {
		p += "/"
	}
	return fmt.Sprintf("%sv1/keys/%s/in/%s-%s-%012d.parquet", p, url.PathEscape(key), events.UploadTime, incarnation, seq)
}

// KeyFilePutter writes an ingest file durably: it's in the writer's spool when
// the call returns, and src (the buffer file it was built from) is removed.
// The observer's writer is one (WriterPutter).
type KeyFilePutter interface {
	PutKeyFile(ctx context.Context, tmpl string, body []byte, src string) error
}

func (p writerPutter) PutKeyFile(_ context.Context, tmpl string, body []byte, src string) error {
	return p.records.PutAtUploadDurable(tmpl, body, src)
}

type keyBuf struct {
	key   string
	path  string
	f     *os.File
	bytes int64
	born  time.Time
	due   time.Time
	dirty bool
}

func (s *Sink) initKeyed() {
	c := &s.cfg
	if c.FlushBytes <= 0 {
		c.FlushBytes = defaultFlushBytes
	}
	if c.FlushAge <= 0 {
		c.FlushAge = defaultFlushAge
	}
	if c.FlushAge > maxFlushAge {
		c.FlushAge = maxFlushAge
	}
	if c.MaxBufferBytes <= 0 {
		c.MaxBufferBytes = defaultMaxBufferBytes
	}
	if c.Jitter == nil {
		c.Jitter = func(d time.Duration) time.Duration {
			return time.Duration(float64(d) * (0.9 + 0.2*rand.Float64()))
		}
	}
	if c.KeySpoolDir == "" {
		dir, err := os.MkdirTemp("", "aintercept-keys-")
		if err != nil {
			panic(fmt.Sprintf("records: key spool: %v", err))
		}
		c.KeySpoolDir = dir
	}
	s.bufs = map[string]*keyBuf{}
	s.recoverKeyed()
}

// AddRecords buffers records by their routing key.
func (s *Sink) AddRecords(rs ...Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bufferAll(rs)
}

// bufferAll appends records to their keys' buffers, then flushes any buffer at
// its size and, over the memory cap, the oldest. Called with mu held.
func (s *Sink) bufferAll(rs []Record) {
	now := s.cfg.Now()
	full := map[string]bool{}
	for i := range rs {
		r := &rs[i]
		key := r.RoutingKey
		if key == "" {
			key = RoutingKey(r)
			r.RoutingKey = key
		}
		kb, err := s.bufFor(key, now)
		if err != nil {
			s.errs = append(s.errs, err)
			slog.Error("ALARM records: can't open a key buffer; record dropped", "key", key, "err", err)
			continue
		}
		line, err := json.Marshal(r)
		if err != nil {
			s.errs = append(s.errs, err)
			continue
		}
		line = append(line, '\n')
		if _, err := kb.f.Write(line); err != nil {
			s.errs = append(s.errs, err)
			slog.Error("ALARM records: can't write a key buffer; record dropped", "key", key, "err", err)
			continue
		}
		kb.bytes += int64(len(line))
		kb.dirty = true
		s.total += int64(len(line))
		if kb.bytes >= int64(s.cfg.FlushBytes) {
			full[key] = true
		}
	}
	for key := range full {
		if kb := s.bufs[key]; kb != nil {
			s.flushKey(kb)
		}
	}
	for s.total > s.cfg.MaxBufferBytes && len(s.bufs) > 0 {
		s.flushKey(s.oldest())
	}
}

func (s *Sink) oldest() *keyBuf {
	var o *keyBuf
	for _, kb := range s.bufs {
		if o == nil || kb.born.Before(o.born) || (kb.born.Equal(o.born) && kb.key < o.key) {
			o = kb
		}
	}
	return o
}

func (s *Sink) bufFor(key string, now time.Time) (*keyBuf, error) {
	if kb := s.bufs[key]; kb != nil {
		return kb, nil
	}
	dir := filepath.Join(s.cfg.KeySpoolDir, url.PathEscape(key))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	s.kseq++
	path := filepath.Join(dir, fmt.Sprintf("%s-%012d.jsonl", s.cfg.Incarnation, s.kseq))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return nil, err
	}
	kb := &keyBuf{key: key, path: path, f: f, born: now, due: now.Add(s.cfg.Jitter(s.cfg.FlushAge))}
	s.bufs[key] = kb
	return kb, nil
}

// flushKey writes one key's buffer as an ingest file. Called with mu held.
func (s *Sink) flushKey(kb *keyBuf) {
	delete(s.bufs, kb.key)
	s.total -= kb.bytes
	_ = kb.f.Sync()
	_ = kb.f.Close()
	if err := s.putBuffer(kb.key, kb.path); err != nil {
		s.errs = append(s.errs, err)
		slog.Error("ALARM records: key buffer not handed to the writer; it stays on disk for the next start", "key", kb.key, "path", kb.path, "err", err)
	}
}

// putBuffer encodes a buffer file and hands it to the writer.
func (s *Sink) putBuffer(key, path string) error {
	rs, err := readBuffer(path)
	if err != nil {
		return err
	}
	if len(rs) == 0 {
		return os.Remove(path)
	}
	body, err := EncodeWithNewest(rs, s.asm.newestNS)
	if err != nil {
		return err
	}
	s.kseq++
	tmpl := KeyTemplate(s.cfg.Prefix, key, s.cfg.Incarnation, s.kseq)
	if s.cfg.KeyObject != nil {
		tmpl = s.cfg.KeyObject(key, s.kseq)
	}
	if kp, ok := s.cfg.Putter.(KeyFilePutter); ok {
		return kp.PutKeyFile(context.Background(), tmpl, body, path)
	}
	if err := s.cfg.Putter.Put(context.Background(), events.ResolveUploadKey(tmpl, s.cfg.Now()), body); err != nil {
		return err
	}
	return os.Remove(path)
}

// readBuffer reads a buffer file's records. A last line torn by a crash (never
// fsynced whole) is skipped.
func readBuffer(path string) ([]Record, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []Record
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var r Record
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

// recoverKeyed hands the writer every buffer a previous process left on disk.
func (s *Sink) recoverKeyed() {
	dirs, _ := os.ReadDir(s.cfg.KeySpoolDir)
	n := 0
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		key, err := url.PathUnescape(d.Name())
		if err != nil {
			continue
		}
		files, _ := os.ReadDir(filepath.Join(s.cfg.KeySpoolDir, d.Name()))
		var names []string
		for _, f := range files {
			if strings.HasSuffix(f.Name(), ".jsonl") {
				names = append(names, f.Name())
			}
		}
		sort.Strings(names)
		for _, name := range names {
			if err := s.putBuffer(key, filepath.Join(s.cfg.KeySpoolDir, d.Name(), name)); err != nil {
				slog.Error("ALARM records: can't recover a key buffer; left on disk", "key", key, "file", name, "err", err)
				continue
			}
			n++
		}
	}
	if n > 0 {
		slog.Info("records: uploading key buffers a previous process left on disk", "buffers", n)
	}
}

// Tick flushes the buffers whose timer has run out, and releases held starts
// of long uploads (the assembler).
func (s *Sink) Tick() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.cfg.Now()
	s.add(s.asm.StartUploading(now.Add(-s.cfg.MaxAge).UnixNano()))
	var due []*keyBuf
	for _, kb := range s.bufs {
		if !now.Before(kb.due) {
			due = append(due, kb)
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].key < due[j].key })
	for _, kb := range due {
		s.flushKey(kb)
	}
}

// Sync fsyncs every buffer written since the last sync.
func (s *Sink) Sync() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, kb := range s.bufs {
		if kb.dirty {
			_ = kb.f.Sync()
			kb.dirty = false
		}
	}
}

// flushAllKeys writes every buffer. Called with mu held.
func (s *Sink) flushAllKeys() {
	var all []*keyBuf
	for _, kb := range s.bufs {
		all = append(all, kb)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].born.Before(all[j].born) })
	for _, kb := range all {
		s.flushKey(kb)
	}
}
