// Package stream is the bucketed processor (docs/STREAMING_PIPELINE.md §4):
// replicas own key buckets through leases in the object store, read only the
// canonical record files written since each bucket's watermark, update each
// key's state record by record, and write row upserts per table plus a
// create-only commit per bucket. Nothing is at a fixed count of one: any
// replica can own any bucket, and the commit log is the only point of
// agreement.
package stream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
)

// Info is an object's size and the store's time it was written.
type Info = process.ObjectInfo

// Store is process.Store plus the primitives the processor rests on.
type Store interface {
	process.Store
	// PutIfAbsent creates key only if no object has it (false: one did). It's
	// the compare-and-swap behind commits and leases.
	PutIfAbsent(ctx context.Context, key string, b []byte) (bool, error)
	// Stat is an object's size and the store's own time it was written.
	Stat(ctx context.Context, key string) (Info, error)
	// ListInfo lists every object under prefix, recursively, in key order.
	ListInfo(ctx context.Context, prefix string) ([]Info, error)
	// ListDirsAfter lists the folders directly under prefix sorting after `after`.
	ListDirsAfter(ctx context.Context, prefix, after string) ([]string, error)
	// ETag is key's current ETag, unquoted (a HEAD); process.ErrNotFound if
	// there's no object.
	ETag(ctx context.Context, key string) (string, error)
	// PutIf writes key only if its ETag is still etag (If-Match, sent
	// unquoted), or, with etag "", only if there's no object (If-None-Match: *).
	// It returns the new ETag; a refused write is ErrPrecondition.
	PutIf(ctx context.Context, key string, b []byte, etag string) (string, error)
}

// ErrPrecondition is a conditional write the store refused (a 412).
var ErrPrecondition = process.ErrPrecondition

// DirStore is a local directory as a Store.
type DirStore struct{ process.DirStore }

// S3Store is an S3-compatible bucket as a Store.
type S3Store struct{ *process.S3Store }

// Clock is where time comes from: real, or a test's.
type Clock interface{ Now() time.Time }

// RealClock is the wall clock.
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

// FakeClock is a clock a test moves by hand.
type FakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func NewFakeClock(t time.Time) *FakeClock { return &FakeClock{t: t} }

func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Advance moves the clock (backwards too, for a skewed writer).
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// MemStore is an in-memory object store with S3's semantics (create-only
// puts, strong read-after-write, listing in key order, the store's own write
// times) and injectable faults, for tests and the distributed simulation.
type MemStore struct {
	mu      sync.Mutex
	clock   Clock
	objs    map[string]memObj
	sorted  []string // the keys, in order: listings are a range of it
	failN   int
	failP   float64
	rng     *rand.Rand
	reqs    int
	read    int64
	written int64
}

type memObj struct {
	b    []byte
	mod  time.Time
	etag string
}

// ErrInjected is a fault the test asked for.
var ErrInjected = errors.New("injected store fault")

func NewMemStore(clock Clock) *MemStore {
	return &MemStore{clock: clock, objs: map[string]memObj{}}
}

// FailNext makes the next n writes fail.
func (m *MemStore) FailNext(n int) {
	m.mu.Lock()
	m.failN = n
	m.mu.Unlock()
}

// FailRate makes each write fail with probability p. Half the failures are
// ambiguous: the write lands, but the caller sees an error (a timeout after
// success), as S3 can do.
func (m *MemStore) FailRate(p float64, rng *rand.Rand) {
	m.mu.Lock()
	m.failP, m.rng = p, rng
	m.mu.Unlock()
}

// fault reports whether this write fails, and whether it lands anyway.
func (m *MemStore) fault() (fail, lands bool) {
	if m.failN > 0 {
		m.failN--
		return true, false
	}
	if m.failP > 0 && m.rng.Float64() < m.failP {
		return true, m.rng.Intn(2) == 0
	}
	return false, false
}

// CycleCost is the store work done: requests made and bytes read.
type CycleCost struct {
	Requests  int
	BytesRead int64
}

func (m *MemStore) ResetCounters() {
	m.mu.Lock()
	m.reqs, m.read, m.written = 0, 0, 0
	m.mu.Unlock()
}

func (m *MemStore) Cost() CycleCost {
	m.mu.Lock()
	defer m.mu.Unlock()
	return CycleCost{Requests: m.reqs, BytesRead: m.read}
}

func (m *MemStore) keys(prefix string) []string {
	i := sort.SearchStrings(m.sorted, prefix)
	j := i
	for j < len(m.sorted) && strings.HasPrefix(m.sorted[j], prefix) {
		j++
	}
	return m.sorted[i:j]
}

func (m *MemStore) set(key string, b []byte) {
	if _, ok := m.objs[key]; !ok {
		i := sort.SearchStrings(m.sorted, key)
		m.sorted = append(m.sorted, "")
		copy(m.sorted[i+1:], m.sorted[i:])
		m.sorted[i] = key
	}
	m.objs[key] = memObj{b: bytes.Clone(b), mod: m.clock.Now(), etag: process.ContentETag(b)}
	m.written += int64(len(b))
}

func (m *MemStore) List(_ context.Context, prefix string) ([]process.Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reqs++
	var out []process.Object
	for _, k := range m.keys(prefix) {
		out = append(out, process.Object{Key: k, Size: int64(len(m.objs[k].b))})
	}
	return out, nil
}

func (m *MemStore) ListInfo(_ context.Context, prefix string) ([]Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reqs++
	var out []Info
	for _, k := range m.keys(prefix) {
		o := m.objs[k]
		out = append(out, Info{Key: k, Size: int64(len(o.b)), Modified: o.mod})
	}
	return out, nil
}

func (m *MemStore) dirs(prefix string) []string {
	var out []string
	ks := m.keys(prefix)
	for i := 0; i < len(ks); {
		rest := ks[i][len(prefix):]
		j := strings.Index(rest, "/")
		if j < 0 {
			i++
			continue
		}
		d := prefix + rest[:j+1]
		out = append(out, d)
		// Skip the rest of this folder.
		i += sort.Search(len(ks)-i, func(n int) bool { return !strings.HasPrefix(ks[i+n], d) })
	}
	return out
}

func (m *MemStore) ListDirs(_ context.Context, prefix string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reqs++
	return m.dirs(prefix), nil
}

func (m *MemStore) ListDirsAfter(_ context.Context, prefix, after string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reqs++
	var out []string
	for _, d := range m.dirs(prefix) {
		if d > after {
			out = append(out, d)
		}
	}
	return out, nil
}

func (m *MemStore) ListAfter(_ context.Context, prefix, after string) ([]process.Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reqs++
	var out []process.Object
	for _, k := range m.keys(prefix) {
		if k <= after || strings.Contains(k[len(prefix):], "/") {
			continue
		}
		out = append(out, process.Object{Key: k, Size: int64(len(m.objs[k].b))})
	}
	return out, nil
}

func (m *MemStore) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reqs++
	o, ok := m.objs[key]
	if !ok {
		return nil, process.ErrNotFound
	}
	m.read += int64(len(o.b))
	return bytes.Clone(o.b), nil
}

func (m *MemStore) Stat(_ context.Context, key string) (Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reqs++
	o, ok := m.objs[key]
	if !ok {
		return Info{}, process.ErrNotFound
	}
	return Info{Key: key, Size: int64(len(o.b)), Modified: o.mod}, nil
}

func (m *MemStore) Put(_ context.Context, key string, b []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reqs++
	fail, lands := m.fault()
	if !fail || lands {
		m.set(key, b)
	}
	if fail {
		return ErrInjected
	}
	return nil
}

func (m *MemStore) PutIfAbsent(_ context.Context, key string, b []byte) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reqs++
	fail, lands := m.fault()
	if fail && !lands {
		return false, ErrInjected
	}
	if _, ok := m.objs[key]; ok {
		if fail {
			return false, ErrInjected
		}
		return false, nil
	}
	m.set(key, b)
	if fail {
		return false, ErrInjected
	}
	return true, nil
}

func (m *MemStore) ETag(_ context.Context, key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reqs++
	o, ok := m.objs[key]
	if !ok {
		return "", process.ErrNotFound
	}
	return o.etag, nil
}

func (m *MemStore) PutIf(_ context.Context, key string, b []byte, etag string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reqs++
	fail, lands := m.fault()
	if fail && !lands {
		return "", ErrInjected
	}
	o, ok := m.objs[key]
	if etag = process.NormalizeETag(etag); (etag == "" && ok) || (etag != "" && (!ok || o.etag != etag)) {
		if fail {
			return "", ErrInjected
		}
		return "", fmt.Errorf("put %s: %w", key, ErrPrecondition)
	}
	m.set(key, b)
	if fail {
		return "", ErrInjected
	}
	return m.objs[key].etag, nil
}

func (m *MemStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reqs++
	if _, ok := m.objs[key]; ok {
		delete(m.objs, key)
		i := sort.SearchStrings(m.sorted, key)
		m.sorted = append(m.sorted[:i], m.sorted[i+1:]...)
	}
	return nil
}

// Len is how many objects the store holds.
func (m *MemStore) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.objs)
}
