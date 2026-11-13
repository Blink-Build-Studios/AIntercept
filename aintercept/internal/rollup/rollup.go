package rollup

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream"
)

// Redis keys. Redis only speeds things up: losing all of them loses no data
// (the sweep re-flags keys, and the commit fences two holders).
const (
	dirtySet     = "dirty"       // keys with new ingest files (ingest SADDs)
	committedSet = "committed"   // keys committed since the merge last ran
	dirtySince   = "dirty_since" // when each dirty key was first noticed: the lag
	replicasZSet = "replicas"    // live replicas, scored by heartbeat time
	lockPrefix   = "lock:"
)

// Config is a rollup replica's setup. Zero durations take the defaults.
type Config struct {
	Store   stream.Store
	Redis   *redis.Client
	Prefix  string       // the bucket prefix; v1/ is under it
	Replica string       // this replica's name (lock values)
	Model   stream.Model // stream.StateModel in production
	Clock   stream.Clock

	Grace      time.Duration // late-file window on in/ names (5 min)
	LockTTL    time.Duration // lock:<key> expiry (60 s)
	Renew      time.Duration // lock renewal period (20 s)
	Idle       time.Duration // release a key after this long with no input (60 s)
	Fanout     int           // output dirs (and state chunks) of one tier merged together (8)
	GCGrace    time.Duration // a superseded file is deleted after this (10 min)
	SweepEvery time.Duration // the sweep's period (10 min)
	SweepSpan  time.Duration // how far back the sweep (and the merge, after a Redis wipe) reads recent/ (15 min)
	MergeEvery time.Duration // the merge's period, on the replica holding lock:merge (30 s)
	MaxFiles   int           // in/ files applied per run at most (500)
	Parallel   int           // keys run at once (8)

	// Hook, if set, is called at each step of a run ("listed", "read", "out",
	// "state", "commit", "committed"); an error stops the run there, as a crash
	// would. Tests only.
	Hook func(step, key string) error
}

func (c *Config) defaults() {
	set := func(d *time.Duration, v time.Duration) {
		if *d == 0 {
			*d = v
		}
	}
	set(&c.Grace, 5*time.Minute)
	set(&c.LockTTL, 60*time.Second)
	set(&c.Renew, 20*time.Second)
	set(&c.Idle, 60*time.Second)
	set(&c.GCGrace, 10*time.Minute)
	set(&c.SweepEvery, 10*time.Minute)
	set(&c.SweepSpan, 15*time.Minute)
	set(&c.MergeEvery, 30*time.Second)
	if c.Fanout < 2 {
		c.Fanout = 8
	}
	if c.MaxFiles <= 0 {
		c.MaxFiles = 500
	}
	if c.Parallel <= 0 {
		c.Parallel = 8
	}
	if c.Clock == nil {
		c.Clock = stream.RealClock{}
	}
}

// Worker is one rollup replica.
type Worker struct {
	cfg  Config
	mu   sync.Mutex
	keys map[string]*held
}

// held is a key this replica has state for: its newest commit and the state
// it stands for, kept while the replica holds the key's lock (or runs it).
type held struct {
	c         *Commit
	st        any
	locked    bool
	lastInput time.Time
	lastRenew time.Time
}

// New is a replica.
func New(cfg Config) *Worker {
	cfg.defaults()
	return &Worker{cfg: cfg, keys: map[string]*held{}}
}

func (w *Worker) hook(step, key string) error {
	if w.cfg.Hook != nil {
		return w.cfg.Hook(step, key)
	}
	return nil
}

func (w *Worker) now() time.Time { return w.cfg.Clock.Now() }

func (w *Worker) get(key string) *held {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.keys[key]
}

func (w *Worker) put(key string, h *held) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if old := w.keys[key]; old != nil {
		h.locked, h.lastRenew = old.locked, old.lastRenew
		if h.lastInput.IsZero() {
			h.lastInput = old.lastInput
		}
	}
	w.keys[key] = h
}

// forget drops key's state (after a failed run or a lost lock): the next run
// reloads it from the newest commit.
func (w *Worker) forget(key string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if h := w.keys[key]; h != nil && h.locked {
		w.keys[key] = &held{locked: true, lastInput: h.lastInput, lastRenew: h.lastRenew}
		return
	}
	delete(w.keys, key)
}

// load is key's newest commit and state: the cached ones when nothing newer
// was committed (one Stat), else from the store.
func (w *Worker) load(ctx context.Context, key string) (*held, error) {
	if h := w.get(key); h != nil && h.c != nil {
		_, err := w.cfg.Store.Stat(ctx, CommitKey(w.cfg.Prefix, key, h.c.N+1))
		if errors.Is(err, process.ErrNotFound) {
			return h, nil
		}
		if err != nil {
			return nil, err
		}
	}
	c, err := ReadNewest(ctx, w.cfg.Store, w.cfg.Prefix, key)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return &held{c: &Commit{Key: key}, st: w.cfg.Model.New(key)}, nil
	}
	if len(c.State) == 0 {
		return &held{c: c, st: w.cfg.Model.New(key)}, nil
	}
	chunks := make([][]byte, 0, len(c.State))
	for _, p := range c.State {
		b, err := w.cfg.Store.Get(ctx, KeyDir(w.cfg.Prefix, key)+p)
		if err != nil {
			return nil, fmt.Errorf("rollup: %s state %s: %w", key, p, err)
		}
		chunks = append(chunks, b)
	}
	st, err := w.cfg.Model.Decode(key, chunks)
	if err != nil {
		return nil, err
	}
	return &held{c: c, st: st}, nil
}

// RunKey runs key once: applies the key's new in/ files and commits. It
// returns how many files it applied and whether it committed. A run that
// loses the commit race (another holder committed first) returns false and a
// nil error; its files are never named, so readers never see them.
func (w *Worker) RunKey(ctx context.Context, key string) (applied int, committed bool, err error) {
	defer func() {
		if err != nil || !committed && applied > 0 {
			w.forget(key)
		}
	}()
	// Remove the flag before listing: a file that lands mid-run flags it again.
	_ = w.cfg.Redis.SRem(ctx, dirtySet, key).Err()
	h, err := w.load(ctx, key)
	if err != nil {
		return 0, false, err
	}
	names, err := w.newInput(ctx, key, h.c)
	if err != nil {
		return 0, false, err
	}
	if err := w.hook("listed", key); err != nil {
		return 0, false, err
	}
	if len(names) == 0 {
		w.put(key, h)
		return 0, false, nil
	}
	applied = len(names)
	var rs []records.Record
	for _, name := range names {
		b, err := w.cfg.Store.Get(ctx, InPrefix(w.cfg.Prefix, key)+name)
		if err != nil {
			return applied, false, fmt.Errorf("rollup: %s %s: %w", key, name, err)
		}
		got, err := records.Decode(b)
		if err != nil {
			return applied, false, fmt.Errorf("rollup: %s %s: %w", key, name, err)
		}
		rs = append(rs, got...)
	}
	if err := w.hook("read", key); err != nil {
		return applied, false, err
	}
	sort.SliceStable(rs, func(i, j int) bool {
		ti, tj := stream.RecordTS(&rs[i]), stream.RecordTS(&rs[j])
		if ti != tj {
			return ti < tj
		}
		return rs[i].RecordID < rs[j].RecordID
	})
	changed := map[string][]stream.Row{}
	for i := range rs {
		for table, rows := range w.cfg.Model.Apply(h.st, &rs[i]) {
			changed[table] = append(changed[table], rows...)
		}
	}

	n, nonce := h.c.N+1, newNonce()
	dir := KeyDir(w.cfg.Prefix, key)
	next := &Commit{N: n, Key: key, Out: append([]Out(nil), h.c.Out...), State: append([]string(nil), h.c.State...),
		StateLevels: padLevels(h.c.StateLevels, len(h.c.State)), UpdatedNS: w.now().UnixNano()}
	out, err := w.writeTables(ctx, dir, OutDir(n, nonce), 0, changed, false)
	if err != nil {
		return applied, false, err
	}
	if out != nil {
		next.Out = append(next.Out, *out)
	}
	if err := w.hook("out", key); err != nil {
		return applied, false, err
	}
	chunk, err := w.cfg.Model.Encode(h.st)
	if err != nil {
		return applied, false, err
	}
	sp := StatePath(n, nonce)
	if err := w.cfg.Store.Put(ctx, dir+sp, chunk); err != nil {
		return applied, false, err
	}
	next.State, next.StateLevels = append(next.State, sp), append(next.StateLevels, 0)
	if err := w.hook("state", key); err != nil {
		return applied, false, err
	}

	next.LastApplied, next.Grace = h.c.LastApplied, nil
	for _, name := range names {
		if name > next.LastApplied {
			next.LastApplied = name
		}
	}
	last, _ := InTime(next.LastApplied)
	for _, name := range append(append([]string(nil), h.c.Grace...), names...) {
		if t, err := InTime(name); err == nil && !t.Before(last.Add(-w.cfg.Grace)) {
			next.Grace = append(next.Grace, name)
		}
	}
	sort.Strings(next.Grace)

	// Garbage: what's due goes now (whatever this run's outcome, nothing in the
	// last GCGrace names it); the previous commit is superseded by this one.
	nowNS := w.now().UnixNano()
	var due []Garbage
	for _, g := range h.c.Garbage {
		if g.DeleteAfterNS <= nowNS {
			due = append(due, g)
		} else {
			next.Garbage = append(next.Garbage, g)
		}
	}
	later := nowNS + w.cfg.GCGrace.Nanoseconds()
	if h.c.N > 0 {
		next.Garbage = append(next.Garbage, Garbage{Path: fmt.Sprintf("commit/%012d.json", h.c.N), DeleteAfterNS: later})
	}
	compacted, err := w.compact(ctx, key, next, nonce, later)
	if err != nil {
		return applied, false, err
	}

	if err := w.hook("commit", key); err != nil {
		return applied, false, err
	}
	body, err := json.Marshal(next)
	if err != nil {
		return applied, false, err
	}
	ok, err := w.cfg.Store.PutIfAbsent(ctx, CommitKey(w.cfg.Prefix, key, n), body)
	if err != nil {
		// Ambiguous: it may have landed. It's ours only if it's our bytes.
		got, gerr := w.cfg.Store.Get(ctx, CommitKey(w.cfg.Prefix, key, n))
		if gerr != nil || !bytes.Equal(got, body) {
			return applied, false, err
		}
		ok = true
	}
	if !ok {
		return applied, false, nil
	}
	w.put(key, &held{c: next, st: h.st, lastInput: w.now()})
	for _, g := range due {
		w.deletePath(ctx, dir, g.Path)
	}
	if compacted {
		w.deleteOrphans(ctx, key, next)
	}
	if err := w.hook("committed", key); err != nil {
		return applied, true, err
	}
	_ = w.cfg.Redis.SAdd(ctx, committedSet, key).Err()
	return applied, true, nil
}

// newInput is key's in/ files not yet applied: from (last applied − grace) on,
// minus those the commit records as applied in that window, oldest first.
func (w *Worker) newInput(ctx context.Context, key string, c *Commit) ([]string, error) {
	in := InPrefix(w.cfg.Prefix, key)
	after := in
	if c.LastApplied != "" {
		t, err := InTime(c.LastApplied)
		if err != nil {
			return nil, err
		}
		after = in + t.Add(-w.cfg.Grace).UTC().Format(TimeFormat)
	}
	objs, err := w.cfg.Store.ListAfter(ctx, in, after)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(c.Grace))
	for _, g := range c.Grace {
		seen[g] = true
	}
	var out []string
	for _, o := range objs {
		name := strings.TrimPrefix(o.Key, in)
		if !strings.HasSuffix(name, ".parquet") || seen[name] {
			continue
		}
		if _, err := InTime(name); err != nil {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	if len(out) > w.cfg.MaxFiles {
		out = out[:w.cfg.MaxFiles]
	}
	return out, nil
}

// writeTables writes one output dir: a <table>.parquet per table with rows.
// bottom drops tombstones (nothing older can hold the rows they remove).
func (w *Worker) writeTables(ctx context.Context, keyDir, outDir string, level int, sets map[string][]stream.Row, bottom bool) (*Out, error) {
	names := make([]string, 0, len(sets))
	for t := range sets {
		names = append(names, t)
	}
	sort.Strings(names)
	o := &Out{Dir: outDir, Level: level}
	for _, table := range names {
		rows := stream.CollapseRows(sets[table])
		if bottom {
			rows = stream.MergeRows(rows)
		}
		if len(rows) == 0 {
			continue
		}
		t, err := stream.TableFor(table)
		if err != nil {
			return nil, err
		}
		b, err := t.Encode(rows)
		if err != nil {
			return nil, err
		}
		if err := w.cfg.Store.Put(ctx, keyDir+TableFile(outDir, table), b); err != nil {
			return nil, err
		}
		o.Tables = append(o.Tables, table)
	}
	if len(o.Tables) == 0 {
		return nil, nil
	}
	return o, nil
}

// compact folds the newest Fanout output dirs (and state chunks) of one tier
// into one of the next tier, repeatedly: each row is rewritten O(log N) times
// and the commit names O(Fanout · log N) of each. The folded files become
// garbage. It reports whether it folded anything.
func (w *Worker) compact(ctx context.Context, key string, c *Commit, nonce string, later int64) (bool, error) {
	dir := KeyDir(w.cfg.Prefix, key)
	folded := false
	for i := 0; ; i++ {
		lo, ok := trailingRun(outLevels(c.Out), w.cfg.Fanout)
		if !ok {
			break
		}
		sets := map[string][]stream.Row{}
		for _, o := range c.Out[lo:] {
			for _, table := range o.Tables {
				t, err := stream.TableFor(table)
				if err != nil {
					return false, err
				}
				b, err := w.cfg.Store.Get(ctx, dir+TableFile(o.Dir, table))
				if err != nil {
					return false, err
				}
				rows, err := t.Decode(b)
				if err != nil {
					return false, err
				}
				sets[table] = append(sets[table], rows...)
			}
		}
		level := c.Out[len(c.Out)-1].Level + 1
		merged, err := w.writeTables(ctx, dir, OutDir(c.N, fmt.Sprintf("%sc%d", nonce, i)), level, sets, lo == 0)
		if err != nil {
			return false, err
		}
		for _, o := range c.Out[lo:] {
			c.Garbage = append(c.Garbage, Garbage{Path: o.Dir, DeleteAfterNS: later})
		}
		c.Out = c.Out[:lo]
		if merged != nil {
			c.Out = append(c.Out, *merged)
		}
		folded = true
	}
	for i := 0; ; i++ {
		lo, ok := trailingRun(c.StateLevels, w.cfg.Fanout)
		if !ok {
			break
		}
		chunks := make([][]byte, 0, len(c.State)-lo)
		for _, p := range c.State[lo:] {
			b, err := w.cfg.Store.Get(ctx, dir+p)
			if err != nil {
				return false, err
			}
			chunks = append(chunks, b)
		}
		b, err := w.cfg.Model.Merge(key, chunks)
		if err != nil {
			return false, err
		}
		p := StatePath(c.N, fmt.Sprintf("%sc%d", nonce, i))
		if err := w.cfg.Store.Put(ctx, dir+p, b); err != nil {
			return false, err
		}
		level := c.StateLevels[len(c.StateLevels)-1] + 1
		for _, old := range c.State[lo:] {
			c.Garbage = append(c.Garbage, Garbage{Path: old, DeleteAfterNS: later})
		}
		c.State, c.StateLevels = append(c.State[:lo], p), append(c.StateLevels[:lo], level)
		folded = true
	}
	return folded, nil
}

// trailingRun finds the newest entries sharing one level; it reports their
// start when there are at least fanout of them.
func trailingRun(levels []int, fanout int) (int, bool) {
	if len(levels) < fanout {
		return 0, false
	}
	l := levels[len(levels)-1]
	i := len(levels) - 1
	for i > 0 && levels[i-1] == l {
		i--
	}
	return i, len(levels)-i >= fanout
}

func outLevels(out []Out) []int {
	ls := make([]int, len(out))
	for i, o := range out {
		ls[i] = o.Level
	}
	return ls
}

func padLevels(ls []int, n int) []int {
	out := append([]int(nil), ls...)
	for len(out) < n {
		out = append(out, 0)
	}
	return out[:n]
}

// deleteOrphans removes out dirs and state chunks of runs that lost a commit
// race: numbered at or before c.N, named neither by c nor as garbage. They
// can never be named (their commit number is taken), so they go at once.
func (w *Worker) deleteOrphans(ctx context.Context, key string, c *Commit) {
	dir := KeyDir(w.cfg.Prefix, key)
	keep := map[string]bool{}
	for _, o := range c.Out {
		keep[o.Dir] = true
	}
	for _, p := range c.State {
		keep[p] = true
	}
	for _, g := range c.Garbage {
		keep[g.Path] = true
	}
	for _, sub := range []string{"out/", "state/"} {
		infos, err := w.cfg.Store.ListInfo(ctx, dir+sub)
		if err != nil {
			return
		}
		for _, in := range infos {
			rel := strings.TrimPrefix(in.Key, dir)
			unit := rel
			if sub == "out/" {
				rest := strings.TrimPrefix(rel, "out/")
				if i := strings.IndexByte(rest, '/'); i >= 0 {
					unit = "out/" + rest[:i+1]
				}
			}
			if keep[unit] || runNumber(unit) >= c.N {
				continue
			}
			_ = w.cfg.Store.Delete(ctx, in.Key)
		}
	}
}

// runNumber is the run an out dir or state chunk belongs to.
func runNumber(rel string) int64 {
	rel = strings.TrimPrefix(strings.TrimPrefix(rel, "out/"), "state/")
	var n int64
	if _, err := fmt.Sscanf(rel, "%012d", &n); err != nil {
		return math.MaxInt64
	}
	return n
}

// deletePath deletes a superseded file, or every file of an out dir.
func (w *Worker) deletePath(ctx context.Context, dir, rel string) {
	if !strings.HasSuffix(rel, "/") {
		_ = w.cfg.Store.Delete(ctx, dir+rel)
		return
	}
	infos, err := w.cfg.Store.ListInfo(ctx, dir+rel)
	if err != nil {
		return
	}
	for _, in := range infos {
		_ = w.cfg.Store.Delete(ctx, in.Key)
	}
}

func newNonce() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// --- scheduling -------------------------------------------------------------

var renewScript = redis.NewScript(`if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("PEXPIRE", KEYS[1], ARGV[2]) else return 0 end`)
var releaseScript = redis.NewScript(`if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("DEL", KEYS[1]) else return 0 end`)

// Tick is one scheduling pass: claim dirty keys (a lock each, shared fairly
// across live replicas), run the claimed ones that have new input, renew
// locks, and release keys idle past Idle.
func (w *Worker) Tick(ctx context.Context) error {
	now := w.now()
	rdb := w.cfg.Redis
	_ = rdb.ZAdd(ctx, replicasZSet, redis.Z{Score: float64(now.Unix()), Member: w.cfg.Replica}).Err()

	sample, err := rdb.SRandMemberN(ctx, dirtySet, 256).Result()
	if err != nil {
		return err
	}
	mine := w.lockedKeys()
	want := map[string]bool{}
	for _, k := range sample {
		want[k] = true
	}
	if len(mine) > 0 {
		is, err := rdb.SMIsMember(ctx, dirtySet, toAny(mine)...).Result()
		if err != nil {
			return err
		}
		for i, k := range mine {
			if is[i] {
				want[k] = true
			}
		}
	}
	for k := range want {
		_ = rdb.ZAddNX(ctx, dirtySince, redis.Z{Score: float64(now.UnixNano()) / 1e9, Member: k}).Err()
	}

	w.renew(ctx, false)
	limit := w.share(ctx)
	var todo []string
	for k := range want {
		if w.isLocked(k) {
			todo = append(todo, k)
			continue
		}
		if len(w.lockedKeys()) >= limit {
			continue
		}
		ok, err := rdb.SetNX(ctx, lockPrefix+k, w.cfg.Replica, w.cfg.LockTTL).Result()
		if err != nil {
			return err
		}
		if ok {
			w.markLocked(k, now)
			todo = append(todo, k)
		}
	}
	sort.Strings(todo)

	var (
		wg    sync.WaitGroup
		sem   = make(chan struct{}, w.cfg.Parallel)
		errMu sync.Mutex
		first error
	)
	for _, k := range todo {
		wg.Add(1)
		sem <- struct{}{}
		go func(k string) {
			defer wg.Done()
			defer func() { <-sem }()
			n, _, err := w.RunKey(ctx, k)
			if err != nil {
				_ = rdb.SAdd(ctx, dirtySet, k).Err() // try again
				errMu.Lock()
				if first == nil {
					first = err
				}
				errMu.Unlock()
				return
			}
			if n > 0 {
				w.touch(k, w.now())
			}
			if in, _ := rdb.SIsMember(ctx, dirtySet, k).Result(); !in {
				_ = rdb.ZRem(ctx, dirtySince, k).Err()
			}
		}(k)
	}
	wg.Wait()
	w.releaseIdle(ctx, want)
	return first
}

// share is how many keys this replica may hold: its fair share of the dirty
// keys plus a margin, at least 16.
func (w *Worker) share(ctx context.Context) int {
	rdb := w.cfg.Redis
	dirty, _ := rdb.SCard(ctx, dirtySet).Result()
	live, _ := rdb.ZCount(ctx, replicasZSet, fmt.Sprint(w.now().Add(-30*time.Second).Unix()), "+inf").Result()
	if live < 1 {
		live = 1
	}
	n := int(math.Ceil(float64(dirty)/float64(live)*1.25)) + len(w.lockedKeys())
	if n < 16 {
		n = 16
	}
	return n
}

func (w *Worker) lockedKeys() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	for k, h := range w.keys {
		if h.locked {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func (w *Worker) isLocked(k string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	h := w.keys[k]
	return h != nil && h.locked
}

func (w *Worker) markLocked(k string, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	h := w.keys[k]
	if h == nil {
		h = &held{}
		w.keys[k] = h
	}
	h.locked, h.lastRenew, h.lastInput = true, now, now
}

func (w *Worker) touch(k string, t time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if h := w.keys[k]; h != nil {
		h.lastInput = t
	}
}

// renew extends the locks this replica holds (all of them when force, else
// those not renewed for Renew). A lock it no longer holds is dropped with its
// state: another replica owns the key now.
func (w *Worker) renew(ctx context.Context, force bool) {
	now := w.now()
	for _, k := range w.lockedKeys() {
		h := w.get(k)
		if h == nil || (!force && now.Sub(h.lastRenew) < w.cfg.Renew && !h.lastRenew.IsZero()) {
			// Still verify ownership cheaply when not renewing: nothing to do.
			if h != nil && !force {
				ok, err := w.cfg.Redis.Get(ctx, lockPrefix+k).Result()
				if err == nil && ok == w.cfg.Replica {
					continue
				}
				if err != nil && !errors.Is(err, redis.Nil) {
					continue // Redis trouble: keep going; the commit fences
				}
				w.drop(k)
			}
			continue
		}
		res, err := renewScript.Run(ctx, w.cfg.Redis, []string{lockPrefix + k}, w.cfg.Replica, w.cfg.LockTTL.Milliseconds()).Int()
		if err != nil {
			continue
		}
		if res == 0 {
			w.drop(k)
			continue
		}
		w.mu.Lock()
		if h := w.keys[k]; h != nil {
			h.lastRenew = now
		}
		w.mu.Unlock()
	}
}

func (w *Worker) drop(k string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.keys, k)
}

// releaseIdle lets go of keys with no input for Idle that aren't dirty.
func (w *Worker) releaseIdle(ctx context.Context, dirty map[string]bool) {
	now := w.now()
	for _, k := range w.lockedKeys() {
		h := w.get(k)
		if h == nil || dirty[k] || now.Sub(h.lastInput) < w.cfg.Idle {
			continue
		}
		if in, _ := w.cfg.Redis.SIsMember(ctx, dirtySet, k).Result(); in {
			continue
		}
		_ = releaseScript.Run(ctx, w.cfg.Redis, []string{lockPrefix + k}, w.cfg.Replica).Err()
		w.drop(k)
	}
}

// Release drops every lock this replica holds (on SIGTERM).
func (w *Worker) Release(ctx context.Context) {
	for _, k := range w.lockedKeys() {
		_ = releaseScript.Run(ctx, w.cfg.Redis, []string{lockPrefix + k}, w.cfg.Replica).Err()
		w.drop(k)
	}
}

// Sweep re-flags every key in the recent index of the last SweepSpan: it
// covers lost flags, Redis restarts, and pods that crashed between upload and
// flag. Any replica may run it any time; doing it twice does no harm.
func (w *Worker) Sweep(ctx context.Context) error {
	now := w.now()
	from := now.Add(-w.cfg.SweepSpan).Truncate(time.Minute)
	for m := from; !m.After(now); m = m.Add(time.Minute) {
		infos, err := w.cfg.Store.ListInfo(ctx, RecentPrefix(w.cfg.Prefix, m))
		if err != nil {
			return err
		}
		for _, in := range infos {
			b, err := w.cfg.Store.Get(ctx, in.Key)
			if err != nil {
				return err
			}
			var keys []string
			if err := json.Unmarshal(b, &keys); err != nil {
				continue
			}
			if len(keys) == 0 {
				continue
			}
			if err := w.cfg.Redis.SAdd(ctx, dirtySet, toAny(keys)...).Err(); err != nil {
				return err
			}
			for _, k := range keys {
				_ = w.cfg.Redis.ZAddNX(ctx, dirtySince, redis.Z{Score: float64(now.UnixNano()) / 1e9, Member: k}).Err()
			}
		}
	}
	return nil
}

// Lag is how long the oldest flagged key has waited (0: none waiting).
func (w *Worker) Lag(ctx context.Context) (time.Duration, error) {
	zs, err := w.cfg.Redis.ZRangeWithScores(ctx, dirtySince, 0, 0).Result()
	if err != nil || len(zs) == 0 {
		return 0, err
	}
	lag := w.now().Sub(time.Unix(0, int64(zs[0].Score*1e9)))
	if lag < 0 {
		lag = 0
	}
	return lag, nil
}

// Handler serves /healthz and /lag ({"oldest_unapplied_seconds": s}).
func (w *Worker) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(rw http.ResponseWriter, _ *http.Request) { _, _ = rw.Write([]byte("ok\n")) })
	mux.HandleFunc("/lag", func(rw http.ResponseWriter, r *http.Request) {
		lag, err := w.Lag(r.Context())
		if err != nil {
			http.Error(rw, err.Error(), http.StatusServiceUnavailable)
			return
		}
		rw.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(rw).Encode(map[string]float64{"oldest_unapplied_seconds": lag.Seconds()})
	})
	return mux
}

// Run loops Tick (every second) and Sweep (every SweepEvery, starting now),
// renewing locks in between, until ctx ends; then it releases every lock.
func (w *Worker) Run(ctx context.Context) error {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	renew := time.NewTicker(w.cfg.Renew)
	defer renew.Stop()
	lastSweep := time.Time{}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-renew.C:
				w.renew(ctx, true)
			}
		}
	}()
	for {
		if now := w.now(); now.Sub(lastSweep) >= w.cfg.SweepEvery {
			if err := w.Sweep(ctx); err == nil {
				lastSweep = now
			}
		}
		_ = w.Tick(ctx)
		select {
		case <-ctx.Done():
			rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			w.Release(rctx)
			cancel()
			return nil
		case <-tick.C:
		}
	}
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// --- reading ---------------------------------------------------------------

// ReadNewest is key's newest commit (nil, nil: none yet). Superseded commits
// are deleted after the GC grace, so the listing stays short.
func ReadNewest(ctx context.Context, s stream.Store, prefix, key string) (*Commit, error) {
	objs, err := s.ListAfter(ctx, KeyDir(prefix, key)+"commit/", "")
	if err != nil {
		return nil, err
	}
	var best int64 = -1
	for _, o := range objs {
		if n, ok := CommitN(o.Key); ok && n > best {
			best = n
		}
	}
	if best < 0 {
		return nil, nil
	}
	b, err := s.Get(ctx, CommitKey(prefix, key, best))
	if err != nil {
		return nil, err
	}
	var c Commit
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("rollup: %s commit %d: %w", key, best, err)
	}
	return &c, nil
}

// ReadTables is key's current rows per table, as readers see them: every
// output dir of the newest commit, oldest first, newest row per (key, id).
func ReadTables(ctx context.Context, s stream.Store, prefix, key string) (map[string][]stream.Row, error) {
	c, err := ReadNewest(ctx, s, prefix, key)
	if err != nil || c == nil {
		return map[string][]stream.Row{}, err
	}
	sets := map[string][][]stream.Row{}
	for _, o := range c.Out {
		for _, table := range o.Tables {
			t, err := stream.TableFor(table)
			if err != nil {
				return nil, err
			}
			b, err := s.Get(ctx, KeyDir(prefix, key)+TableFile(o.Dir, table))
			if err != nil {
				return nil, err
			}
			rows, err := t.Decode(b)
			if err != nil {
				return nil, err
			}
			sets[table] = append(sets[table], rows)
		}
	}
	out := map[string][]stream.Row{}
	for table, ss := range sets {
		if rows := stream.MergeRows(ss...); len(rows) > 0 {
			out[table] = rows
		}
	}
	return out, nil
}
