// Package keyflag is ingest's recent index (docs/PIPELINE.md §1 step 4): how
// the workers learn which keys have new ingest files, without ingest ever
// talking to a database.
//
//   - Before an ingest file is handed to the writer, its key is journaled in the
//     spool (Journal: appended and fsynced), so a crash can't lose it.
//   - Once the file has landed (the writer's OnUploaded: Landed), its key goes
//     into the next index object.
//   - Every 5 s (Run), and at shutdown (Close), the pod writes one index object,
//     v1/recent/<s3-time>-<incarnation>-<seq>.json: the keys, plus the pod's
//     newest upload time for /status. It's handed to the writer durably (in the
//     spool when Put returns) and named by the S3 time it lands at, so the
//     workers' tail reads every pod's objects in one time-ordered listing.
//   - Then the journal is rewritten to hold only the files still in flight.
//   - At start (Open), whatever a crash left in the journal goes into the
//     first index object. A key named for a file that never landed is
//     harmless: its run finds nothing and clears the flag.
//
// Ingest has no Postgres connection, DSN or login, and no Redis: an outage of
// either can't touch traffic or lose a flag. The one gap is losing the node,
// and with it the spool, within 5 s of an upload: that key waits for its next
// file (the file itself is in S3).
package keyflag

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// TimeFormat is the S3 time an ingest file's or index object's name begins with.
const TimeFormat = events.UploadTimeFormat

// DefaultEvery is how often a pod writes its index object.
const DefaultEvery = 5 * time.Second

// journalName is the journal's file in Config.Dir.
const journalName = "journal"

// Object is one index object's body.
type Object struct {
	Incarnation string   `json:"incarnation"`
	Keys        []string `json:"keys"`
	// LastNS is the S3 time (Unix ns) of the newest ingest file the pod had
	// uploaded (0: unknown, as for a crash's leftovers).
	LastNS int64 `json:"last_ns"`
}

// Prefix is the recent index's folder: <prefix>/v1/recent/.
func Prefix(prefix string) string { return join(prefix, "v1/recent/") }

// Template is a pod's seq'th index object's key, with the writer's UploadTime
// placeholder for its S3 time: <Prefix>/<s3-time>-<incarnation>-<seq>.json.
func Template(prefix, incarnation string, seq int64) string {
	return fmt.Sprintf("%s%s-%s-%012d.json", Prefix(prefix), events.UploadTime, incarnation, seq)
}

// NameTime is the S3 time an index object's name (relative to Prefix) or an
// ingest file's name (relative to in/) begins with.
func NameTime(name string) (time.Time, error) {
	i := strings.IndexByte(name, '-')
	if i < 0 {
		return time.Time{}, fmt.Errorf("keyflag: name %q has no time", name)
	}
	return time.Parse(TimeFormat, name[:i])
}

// InTemplate is an ingest file's key template: <prefix>/v1/keys/<esc
// key>/in/<s3-time>-<incarnation>-<seq>.parquet, the time left for the writer.
func InTemplate(prefix, key, incarnation string, seq int64) string {
	return records.KeyTemplate(prefix, key, incarnation, seq)
}

// Config configures an Index.
type Config struct {
	// Dir holds the journal. It must outlive the container: the spool's volume.
	Dir         string
	Prefix      string // the object prefix v1/ is under
	Incarnation string
	// Put hands one index object to the writer durably (in its spool, fsynced,
	// when it returns); tmpl's UploadTime is filled in when it lands.
	Put func(tmpl string, body []byte) error
	// NewTicker is Run's ticker (tests). Default a time.Ticker of DefaultEvery.
	NewTicker func(time.Duration) (<-chan time.Time, func())
	SkipFsync bool // tests only
}

// Index is one pod's recent index.
type Index struct {
	cfg  Config
	keys string // the escaped-key prefix every ingest file's key starts with

	mu      sync.Mutex
	journal *os.File
	closed  bool
	seq     int64
	// pending: journaled files not landed yet, by their template's id.
	pending map[string]string
	// landed: keys to index next (landed, or a crash's leftovers).
	landed map[string]bool
	lastNS int64

	stop chan struct{}
	done chan struct{}
}

type journalLine struct {
	Key string `json:"k"`
}

// Open opens the journal in cfg.Dir. Whatever a previous process left in it
// goes into the first index object (Flush).
func Open(cfg Config) (*Index, error) {
	if cfg.Dir == "" || cfg.Put == nil {
		return nil, errors.New("keyflag: Dir and Put are required")
	}
	if cfg.NewTicker == nil {
		cfg.NewTicker = func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		}
	}
	if err := os.MkdirAll(cfg.Dir, 0o750); err != nil {
		return nil, fmt.Errorf("keyflag: journal dir: %w", err)
	}
	x := &Index{cfg: cfg, keys: join(cfg.Prefix, "v1/keys/"), pending: map[string]string{}, landed: map[string]bool{},
		stop: make(chan struct{}), done: make(chan struct{})}
	left, err := readJournal(filepath.Join(cfg.Dir, journalName))
	if err != nil {
		return nil, err
	}
	for _, k := range left {
		x.landed[k] = true
	}
	if len(left) > 0 {
		slog.Info("keyflag: indexing the keys a previous process journaled", "keys", len(left))
	}
	// A fresh journal holding the leftovers until an index object holds them
	// (rewritten whole: a torn last line must not swallow the next entry).
	if err := x.rewrite(left); err != nil {
		return nil, err
	}
	return x, nil
}

// readJournal is the journal's keys, each once. A last line torn by a crash
// is skipped: its file was never handed to the writer.
func readJournal(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("keyflag: journal: %w", err)
	}
	seen := map[string]bool{}
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var l journalLine
		if json.Unmarshal(sc.Bytes(), &l) != nil || l.Key == "" || seen[l.Key] {
			continue
		}
		seen[l.Key] = true
		out = append(out, l.Key)
	}
	return out, nil
}

// Journal records that the ingest file tmpl (InTemplate) of key is about to
// be handed to the writer. It returns once the entry is on disk; the caller
// must not hand the file over if it fails.
func (x *Index) Journal(key, tmpl string) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.closed {
		return errors.New("keyflag: journal after close")
	}
	if err := x.append(key); err != nil {
		return err
	}
	x.pending[fileID(tmpl)] = key
	return nil
}

func (x *Index) append(keys ...string) error {
	var buf bytes.Buffer
	for _, k := range keys {
		b, _ := json.Marshal(journalLine{Key: k})
		buf.Write(b)
		buf.WriteByte('\n')
	}
	if _, err := x.journal.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("keyflag: journal: %w", err)
	}
	if !x.cfg.SkipFsync {
		if err := x.journal.Sync(); err != nil {
			return fmt.Errorf("keyflag: journal: %w", err)
		}
	}
	return nil
}

// fileID is an ingest file's object key with its S3 time replaced by the
// writer's placeholder: the template it was journaled under.
func fileID(objKey string) string {
	dir, name := objKey[:strings.LastIndexByte(objKey, '/')+1], objKey[strings.LastIndexByte(objKey, '/')+1:]
	if i := strings.IndexByte(name, '-'); i >= 0 && !strings.HasPrefix(name, events.UploadTime) {
		name = events.UploadTime + name[i:]
	}
	return dir + name
}

// Landed is the writer's OnUploaded hook: an object landed at objKey. An
// ingest file's key goes into the next index object.
func (x *Index) Landed(_, objKey string) {
	rest, ok := strings.CutPrefix(objKey, x.keys)
	if !ok {
		return
	}
	esc, name, ok := strings.Cut(rest, "/in/")
	if !ok || strings.Contains(name, "/") {
		return
	}
	key, err := url.PathUnescape(esc)
	if err != nil {
		return
	}
	at, err := NameTime(name)
	if err != nil {
		return
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	delete(x.pending, fileID(objKey))
	x.landed[key] = true
	if ns := at.UnixNano(); ns > x.lastNS {
		x.lastNS = ns
	}
}

// Flush writes one index object naming every key landed since the last one
// (nothing, if none did), then rewrites the journal to hold only the files
// still in flight. A failed write keeps the keys for the next Flush.
func (x *Index) Flush() error {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.flush()
}

func (x *Index) flush() error {
	if len(x.landed) == 0 {
		return nil
	}
	o := Object{Incarnation: x.cfg.Incarnation, LastNS: x.lastNS}
	for k := range x.landed {
		o.Keys = append(o.Keys, k)
	}
	sort.Strings(o.Keys)
	body, _ := json.Marshal(o)
	if err := x.cfg.Put(Template(x.cfg.Prefix, x.cfg.Incarnation, x.seq+1), body); err != nil {
		return fmt.Errorf("keyflag: index object: %w", err)
	}
	x.seq++
	x.landed = map[string]bool{}
	if x.closed {
		return nil // the journal's closed; the next start re-indexes what it holds (harmless)
	}
	// Only the files still in flight stay journaled, so a crash from here on
	// re-indexes only those.
	seen := map[string]bool{}
	var keys []string
	for _, k := range x.pending {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return x.rewrite(keys)
}

// rewrite replaces the journal with keys (tmp, fsync, rename) and appends to
// the new one from then on.
func (x *Index) rewrite(keys []string) error {
	path := filepath.Join(x.cfg.Dir, journalName)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return fmt.Errorf("keyflag: journal: %w", err)
	}
	old := x.journal
	x.journal = f
	if err := x.append(keys...); err != nil {
		x.journal = old
		f.Close()
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		x.journal = old
		f.Close()
		return fmt.Errorf("keyflag: journal: %w", err)
	}
	if !x.cfg.SkipFsync {
		if d, err := os.Open(x.cfg.Dir); err == nil {
			_ = d.Sync()
			_ = d.Close()
		}
	}
	if old != nil {
		old.Close()
	}
	return nil
}

// Run writes an index object every DefaultEvery until Close.
func (x *Index) Run() {
	defer close(x.done)
	tick, stop := x.cfg.NewTicker(DefaultEvery)
	defer stop()
	for {
		select {
		case <-x.stop:
			return
		case <-tick:
			if err := x.Flush(); err != nil {
				slog.Error("keyflag: recent index upload failed; the keys wait for the next one", "err", err)
			}
		}
	}
}

// Close stops Run and writes the last index object: at shutdown, after the
// writer has uploaded every ingest file and before it closes.
func (x *Index) Close() error {
	x.mu.Lock()
	if x.closed {
		x.mu.Unlock()
		return nil
	}
	x.mu.Unlock()
	select {
	case <-x.stop:
	default:
		close(x.stop)
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	err := x.flush()
	x.closed = true
	if cerr := x.journal.Close(); err == nil {
		err = cerr
	}
	return err
}

func join(prefix, rest string) string {
	if p := strings.Trim(prefix, "/"); p != "" {
		return p + "/" + rest
	}
	return rest
}
