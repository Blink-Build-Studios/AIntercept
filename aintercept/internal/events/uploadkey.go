package events

import (
	"context"
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
// UploadMinute in it.
func (o *ObjectStream) PutAtUpload(tmpl string, body []byte) error {
	return o.Put(ResolveUploadKey(tmpl, o.w.now()), body) // TODO: resolve at each upload attempt
}

func (w *Writer) now() time.Time {
	if w.cfg.Now != nil {
		return w.cfg.Now()
	}
	return time.Now()
}
