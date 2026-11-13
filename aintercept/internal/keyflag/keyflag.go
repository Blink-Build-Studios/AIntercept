// Package keyflag tells the rollups which keys have new ingest files
// (docs/PIPELINE.md §1). After each ingest file lands (the writer's
// OnUploaded):
//   - SADD dirty <key> in Redis, fire-and-forget: a bounded queue drained by
//     one goroutine, so ingest never blocks or fails when Redis is slow or down
//     (a full queue drops the flag, counted; the rollups' sweep finds it);
//   - the key goes into this pod's recent index for the file's S3 minute,
//     uploaded once a minute (FlushRecent) as v1/recent/<minute>/<inc>-<seq>.json;
//   - the pod's newest ingest time goes to Redis (ingest:last:<incarnation>,
//     10 minute expiry) for /status.
//
// Redis only speeds things up: losing a flag delays a key until the sweep,
// never loses data.
package keyflag

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/rollup"
)

// DirtySet is the Redis set of keys with new ingest files.
const DirtySet = "dirty"

// IngestLastPrefix + incarnation holds a pod's newest ingest time (Unix ns).
const IngestLastPrefix = "ingest:last:"

const queueSize = 4096

// Config configures a Flagger.
type Config struct {
	Redis       string // host:port; empty: no Redis (local development)
	Prefix      string // the object prefix v1/ is under
	Incarnation string
	Put         func(key string, body []byte) error // uploads one recent-index object
	Now         func() time.Time
}

// Flagger flags keys.
type Flagger struct {
	cfg     Config
	rdb     *redis.Client
	ops     chan op
	pending sync.WaitGroup
	done    chan struct{}
	dropped atomic.Int64
	failed  atomic.Int64

	mu     sync.Mutex
	recent map[time.Time]map[string]bool
	seq    int64
}

type op struct {
	key string
	ns  int64
}

// New starts a Flagger.
func New(cfg Config) *Flagger {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	f := &Flagger{cfg: cfg, recent: map[time.Time]map[string]bool{}, done: make(chan struct{})}
	if cfg.Redis != "" {
		f.rdb = redis.NewClient(&redis.Options{Addr: cfg.Redis, DialTimeout: time.Second,
			ReadTimeout: time.Second, WriteTimeout: time.Second, MaxRetries: -1})
		f.ops = make(chan op, queueSize)
		go f.run()
	} else {
		close(f.done)
	}
	return f
}

// InTemplate is an ingest file's key template: rollup.InPrefix + rollup.InName,
// with the writer's UploadTime placeholder for the time.
func InTemplate(prefix, key, incarnation string, seq int64) string {
	return records.KeyTemplate(prefix, key, incarnation, seq)
}

// Landed is the writer's OnUploaded hook: an object landed at objKey.
func (f *Flagger) Landed(_, objKey string) {
	key, at, ok := f.ingestFile(objKey)
	if !ok {
		return
	}
	f.mu.Lock()
	m := at.Truncate(time.Minute)
	if f.recent[m] == nil {
		f.recent[m] = map[string]bool{}
	}
	f.recent[m][key] = true
	f.mu.Unlock()
	if f.ops == nil {
		return
	}
	f.pending.Add(1)
	select {
	case f.ops <- op{key: key, ns: at.UnixNano()}:
	default:
		f.pending.Done()
		if f.dropped.Add(1)%1000 == 1 {
			slog.Warn("keyflag: Redis queue full; dirty flags dropped (the rollups' sweep finds them)", "dropped", f.dropped.Load())
		}
	}
}

// ingestFile parses an ingest file's routing key and S3 time from its object key.
func (f *Flagger) ingestFile(objKey string) (string, time.Time, bool) {
	base := strings.TrimSuffix(rollup.KeyDir(f.cfg.Prefix, "x"), "x/")
	rest, ok := strings.CutPrefix(objKey, base)
	if !ok {
		return "", time.Time{}, false
	}
	esc, name, ok := strings.Cut(rest, "/in/")
	if !ok || strings.Contains(name, "/") {
		return "", time.Time{}, false
	}
	key, err := url.PathUnescape(esc)
	if err != nil {
		return "", time.Time{}, false
	}
	at, err := rollup.InTime(name)
	if err != nil {
		return "", time.Time{}, false
	}
	return key, at, true
}

func (f *Flagger) run() {
	defer close(f.done)
	var downUntil time.Time // after a failure, flags are skipped for a while rather than each waiting on a dead Redis
	for o := range f.ops {
		if time.Now().Before(downUntil) {
			f.failed.Add(1)
			f.pending.Done()
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		pipe := f.rdb.Pipeline()
		pipe.SAdd(ctx, DirtySet, o.key)
		pipe.Set(ctx, IngestLastPrefix+f.cfg.Incarnation, o.ns, 10*time.Minute)
		if _, err := pipe.Exec(ctx); err != nil {
			downUntil = time.Now().Add(5 * time.Second)
			if f.failed.Add(1)%1000 == 1 {
				slog.Warn("keyflag: Redis unavailable; dirty flags lost (the rollups' sweep finds them)", "failed", f.failed.Load(), "err", err)
			}
		}
		cancel()
		f.pending.Done()
	}
}

// FlushRecent uploads one recent-index object per pending minute: the keys
// whose files landed in it.
func (f *Flagger) FlushRecent() error {
	f.mu.Lock()
	pending := f.recent
	f.recent = map[time.Time]map[string]bool{}
	f.mu.Unlock()
	minutes := make([]time.Time, 0, len(pending))
	for m := range pending {
		minutes = append(minutes, m)
	}
	sort.Slice(minutes, func(i, j int) bool { return minutes[i].Before(minutes[j]) })
	for _, m := range minutes {
		keys := make([]string, 0, len(pending[m]))
		for k := range pending[m] {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		body, _ := json.Marshal(keys)
		f.mu.Lock()
		f.seq++
		seq := f.seq
		f.mu.Unlock()
		if err := f.cfg.Put(rollup.RecentName(f.cfg.Prefix, m, f.cfg.Incarnation, seq), body); err != nil {
			f.mu.Lock() // keep them for the next flush
			for _, k := range keys {
				if f.recent[m] == nil {
					f.recent[m] = map[string]bool{}
				}
				f.recent[m][k] = true
			}
			f.mu.Unlock()
			return err
		}
	}
	return nil
}

// Run uploads the recent index once a minute until ctx ends.
func (f *Flagger) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := f.FlushRecent(); err != nil {
				slog.Error("keyflag: recent index upload failed; retrying next minute", "err", err)
			}
		}
	}
}

// Wait waits for the queued Redis writes.
func (f *Flagger) Wait() { f.pending.Wait() }

// Stats is the flagger's drop and failure counts.
func (f *Flagger) Stats() map[string]int64 {
	return map[string]int64{"dropped": f.dropped.Load(), "failed": f.failed.Load()}
}

// Close stops the Flagger (after its queued writes).
func (f *Flagger) Close() {
	if f.ops != nil {
		close(f.ops)
		<-f.done
		_ = f.rdb.Close()
	}
}
