package stream

import (
	"context"
	"errors"
	"fmt"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
)

// Fence is one run's fencing (docs/PIPELINE.md §3.1): the ETags of every
// object the run will write, captured before a newer holder can exist, and
// the conditional writes against them. Any refused write drops the run: the
// fence writes nothing more, and the caller drops its cached state and flags
// the entity dirty again. No version metadata, no manifests.
type Fence struct {
	s    Store
	tags map[string]string // key -> ETag it must still have; "" = absent
	err  error             // the first failure: the run is dropped
}

// NewFence starts a run's fence. remembered is the ETags of the run's own
// last writes while it's warm (ETags of the last run's fence); nil when cold.
func NewFence(s Store, remembered map[string]string) *Fence {
	tags := make(map[string]string, len(remembered))
	for k, v := range remembered {
		tags[k] = v
	}
	return &Fence{s: s, tags: tags}
}

// Capture takes the ETag of each key the run will write: remembered from its
// own last write, else a HEAD (absent: it'll be written with If-None-Match: *).
func (f *Fence) Capture(ctx context.Context, keys ...string) error {
	for _, k := range keys {
		if _, ok := f.tags[k]; ok {
			continue
		}
		e, err := f.s.ETag(ctx, k)
		if errors.Is(err, process.ErrNotFound) {
			e, err = "", nil
		}
		if err != nil {
			return fmt.Errorf("capture %s: %w", k, err)
		}
		f.tags[k] = e
	}
	return nil
}

// Put writes a captured key conditionally on its captured ETag. After any
// failure (ErrPrecondition: a newer holder wrote) every Put returns that
// failure without writing.
func (f *Fence) Put(ctx context.Context, key string, b []byte) error {
	if f.err != nil {
		return f.err
	}
	etag, ok := f.tags[key]
	if !ok {
		return fmt.Errorf("fence: %s written without a capture", key)
	}
	e, err := f.s.PutIf(ctx, key, b, etag)
	if err != nil {
		f.err = err
		return err
	}
	f.tags[key] = e
	return nil
}

// Err is the failure that dropped the run, if any.
func (f *Fence) Err() error { return f.err }

// ETags is what a warm run remembers for its next capture: each key's ETag
// after this run's writes.
func (f *Fence) ETags() map[string]string {
	out := make(map[string]string, len(f.tags))
	for k, v := range f.tags {
		out[k] = v
	}
	return out
}
