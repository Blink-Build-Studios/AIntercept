package events

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"
)

// UploadMinute is the placeholder an object key template carries for the
// minute the object is uploaded in (ResolveUploadKey). Records files are named
// by it, because the bucketed processor reads each bucket's files by minute,
// from its watermark minus a grace window (docs/STREAMING_PIPELINE.md §3.3).
const UploadMinute = "{upload-minute}"

// ResolveUploadKey fills a key template's UploadMinute with t's UTC minute.
func ResolveUploadKey(tmpl string, t time.Time) string {
	return strings.ReplaceAll(tmpl, UploadMinute, t.UTC().Format("2006-01-02T15:04"))
}

// Exister is a Target that can say whether an object exists (S3 HEAD). The
// writer needs it to name an object by its upload minute without ever writing
// it twice: before retrying under a new minute it checks whether an earlier
// attempt landed.
type Exister interface {
	Exists(ctx context.Context, key string) (bool, error)
}

// PutAtUpload queues one whole object whose key is a template with
// UploadMinute in it. The writer fills the minute in at each upload attempt,
// not when the object is sealed: a file that waits out a store outage in the
// spool is then named by the minute it actually lands in, which is after the
// processor's watermark, never behind it where the processor no longer looks.
func (o *ObjectStream) PutAtUpload(tmpl string, body []byte) error {
	w := o.w
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.closed {
		return errors.New("writer: write after close")
	}
	w.in <- item{obj: &bundled{stream: o.id, key: tmpl, body: body, atUpload: true}}
	return nil
}

// attemptsSuffix names a spool file's attempts log: for each object named at
// upload, the keys tried (each logged before its PUT) and whether it landed.
// It survives a crash, so the next process checks those keys before naming
// the object again, and an object is never written twice.
const attemptsSuffix = ".att"

type attempts struct {
	keys []string
	done bool
}

type attemptLine struct {
	I    int    `json:"i"`
	Key  string `json:"k,omitempty"`
	Done bool   `json:"d,omitempty"`
}

func readAttempts(path string) map[int]*attempts {
	out := map[int]*attempts{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var l attemptLine
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue // a line torn by a crash: the attempt it logged never started
		}
		a := out[l.I]
		if a == nil {
			a = &attempts{}
			out[l.I] = a
		}
		if l.Key != "" && !slices.Contains(a.keys, l.Key) {
			a.keys = append(a.keys, l.Key)
		}
		a.done = a.done || l.Done
	}
	return out
}

func (w *Writer) logAttempt(path string, l attemptLine) error {
	f, err := os.OpenFile(path+attemptsSuffix, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(l)
	_, err = f.Write(append(b, '\n'))
	if err == nil && !w.cfg.SkipFsync {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// putAtUpload uploads the i'th object of a spool file, whose key is a template,
// until it lands, at most once:
//   - each attempt names the object by the current minute, and logs the key
//     before the PUT;
//   - retries within the same minute reuse the key (the same bytes again);
//   - before naming it by a new minute, every key tried so far is checked: an
//     attempt that failed ambiguously (a timeout after the store had it) may
//     have landed, and then the object is done.
//
// It returns false only when the writer is abandoned.
func (w *Writer) putAtUpload(t Target, o Object, path string, i int, a *attempts) bool {
	ex, canCheck := t.(Exister)
	backoff := w.cfg.RetryMin
	for attempt := 1; ; attempt++ {
		key := ResolveUploadKey(o.Key, w.now())
		if n := len(a.keys); n > 0 && key != a.keys[n-1] {
			if !canCheck {
				key = a.keys[n-1] // can't tell whether it landed: keep its name rather than risk two
			} else {
				landed, err := w.anyExists(ex, a.keys)
				if err != nil {
					if !w.failed(key, attempt, err, &backoff) {
						return false
					}
					continue
				}
				if landed {
					w.markDone(path, i, a)
					return true
				}
			}
		}
		if !slices.Contains(a.keys, key) {
			if err := w.logAttempt(path, attemptLine{I: i, Key: key}); err != nil {
				slog.Error("ALARM writer: can't log an upload attempt; retrying", "path", path, "err", err)
				if !w.failed(key, attempt, err, &backoff) {
					return false
				}
				continue
			}
			a.keys = append(a.keys, key)
		}
		ctx, cancel := context.WithTimeout(w.ctx, w.cfg.UploadTimeout)
		err := t.Put(ctx, Object{Key: key, Body: o.Body, ContentType: o.ContentType, ContentEncoding: o.ContentEncoding})
		cancel()
		if err == nil {
			w.markDone(path, i, a)
			return true
		}
		if !w.failed(key, attempt, err, &backoff) {
			return false
		}
	}
}

// markDone logs that the object landed. If the log write fails, a restart
// finds the object by checking its tried keys, so nothing is written twice.
func (w *Writer) markDone(path string, i int, a *attempts) {
	a.done = true
	_ = w.logAttempt(path, attemptLine{I: i, Done: true})
}

func (w *Writer) anyExists(ex Exister, keys []string) (bool, error) {
	for _, k := range keys {
		ctx, cancel := context.WithTimeout(w.ctx, w.cfg.UploadTimeout)
		ok, err := ex.Exists(ctx, k)
		cancel()
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

func (w *Writer) now() time.Time {
	if w.cfg.Now != nil {
		return w.cfg.Now()
	}
	return time.Now()
}

// UploadTime is the placeholder for the second an object is uploaded at, on
// the store's clock (docs/PIPELINE.md §1), in UploadTimeFormat.
const UploadTime = "{upload-time}"

// UploadTimeFormat is how UploadTime is written (rollup.TimeFormat).
const UploadTimeFormat = "20060102T150405Z"

// ServerClock is a Target that knows how far the store's clock is from ours
// (the Date header of its last response).
type ServerClock interface {
	ServerOffset() (time.Duration, bool)
}

// PutAtUploadDurable is PutAtUpload, spooled before it returns. src is the
// buffer file the object was built from: it's removed once the spool holds the
// object (TODO).
func (o *ObjectStream) PutAtUploadDurable(tmpl string, body []byte, src string) error {
	return o.PutAtUpload(tmpl, body)
}
