package stream

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// The distributed replay (§4.11): ingest pods upload record files, processor
// replicas own buckets through leases and cycle them, all against the
// in-memory store under one simulated clock, with chaos. It runs step by step
// in one goroutine, so a seed replays exactly; the guarantee it checks is that
// at every checkpoint (ingest drained, replicas caught up) and at the end the
// tables equal one pass over every record uploaded.

// Chaos is how often each fault happens, per step.
type Chaos struct {
	KillHard     float64 // a replica dies without releasing anything
	KillGraceful float64 // a replica releases its leases and leaves
	StealLease   float64 // a bucket's lease is taken by another replica while its owner runs on
	Pause        float64 // a replica stops for 40–90 s (GC pause, partition), then resumes with a stale view
	PutFailure   float64 // a store write fails (half land anyway: an ambiguous failure)
	DelayUpload  float64 // an ingest pod holds a file back for up to 3 minutes
	AddRemove    float64 // a replica is added, or one removed
}

// SimConfig configures a distributed replay.
type SimConfig struct {
	Seed                               int64
	IngestPods, Processors, BucketBits int
	Records, Steps, Checkpoints        int
	Chaos                              Chaos
	Input                              []records.Record // a real capture's records; else generated
	Model                              Model            // default CountModel
	// OutDir, when set, receives the committed layout at the end: every object
	// under SimPrefix + "stream/v1/", keyed as in the store (what the stateless
	// API reads, with SimPrefix as its prefix).
	OutDir string
}

// SimResult is what happened.
type SimResult struct {
	Steps, Checkpoints, Mismatches                    int
	Kills, Steals, Pauses, Adds, Removes, PutFailures int
	Files, Records, Commits, Fenced                   int
	FirstMismatch                                     string
}

type simFile struct {
	bucket  int
	base    string // <incarnation>-<seq>: the file's name, whatever its minute
	key     string // set at its first upload attempt
	firstAt time.Time
	readyAt time.Time
	body    []byte
	recs    []records.Record
}

type simReplica struct {
	name     string
	leaser   *Leaser
	buckets  map[int]*Bucket
	pausedTo time.Time
	nextTick time.Time
}

type sim struct {
	cfg      SimConfig
	rng      *rand.Rand
	clock    *FakeClock
	s        *MemStore
	l        Layout
	m        Model
	reps     []*simReplica
	nextName int
	pending  []*simFile
	seq      int64
	uploaded []records.Record
	res      SimResult
	// maxFiles caps each bucket cycle (the burst sim's per-replica budget).
	budget int
}

// SimPrefix is the simulated store's layout prefix.
const SimPrefix = "sim/"

func newSim(cfg SimConfig) *sim {
	if cfg.Model == nil {
		cfg.Model = CountModel{}
	}
	clock := NewFakeClock(time.Date(2027, 1, 5, 9, 0, 0, 0, time.UTC))
	x := &sim{cfg: cfg, rng: rand.New(rand.NewSource(cfg.Seed)), clock: clock, s: NewMemStore(clock),
		l: Layout{Prefix: SimPrefix}, m: cfg.Model}
	for i := 0; i < max(cfg.Processors, 1); i++ {
		x.addReplica()
	}
	return x
}

func (x *sim) buckets() int { return 1 << x.cfg.BucketBits }

func (x *sim) addReplica() {
	x.nextName++
	name := fmt.Sprintf("r%d", x.nextName)
	x.reps = append(x.reps, &simReplica{name: name, buckets: map[int]*Bucket{},
		leaser: &Leaser{S: x.s, L: x.l, Replica: name, Buckets: x.buckets(), TTL: DefaultTTL}})
}

func (x *sim) removeReplica(i int, graceful bool, ctx context.Context) {
	r := x.reps[i]
	if graceful {
		_ = r.leaser.ReleaseAll(ctx)
	}
	x.reps = append(x.reps[:i], x.reps[i+1:]...)
}

// ingest seals rs into one file per bucket (an ingest pod's flush).
func (x *sim) ingest(pod int, rs []records.Record) {
	by := map[int][]records.Record{}
	for _, r := range rs {
		b := records.BucketOf(r.RoutingKey, x.cfg.BucketBits)
		r.Bucket = int32(b)
		by[b] = append(by[b], r)
	}
	bs := make([]int, 0, len(by))
	for b := range by {
		bs = append(bs, b)
	}
	sort.Ints(bs)
	now := x.clock.Now()
	for _, b := range bs {
		x.seq++
		body, err := records.Encode(by[b])
		if err != nil {
			panic(err)
		}
		f := &simFile{bucket: b, base: fmt.Sprintf("pod%d-%012d", pod, x.seq), body: body, recs: by[b], readyAt: now}
		if x.rng.Float64() < x.cfg.Chaos.DelayUpload {
			f.readyAt = now.Add(time.Duration(x.rng.Intn(180)) * time.Second)
		}
		x.pending = append(x.pending, f)
	}
}

// upload tries every ready file once, the way the ingest writer does: a file
// keeps the key it was first tried under, and is re-keyed to the current
// minute only if it has been failing for over two minutes and the first key
// verifiably never landed.
func (x *sim) upload(ctx context.Context) {
	now := x.clock.Now()
	var keep []*simFile
	for _, f := range x.pending {
		if now.Before(f.readyAt) {
			keep = append(keep, f)
			continue
		}
		if f.key == "" {
			f.key, f.firstAt = x.keyFor(f, now), now
		} else if _, err := x.s.Stat(ctx, f.key); err == nil {
			x.landed(f) // an earlier "failed" put had landed
			continue
		} else if errors.Is(err, process.ErrNotFound) && now.Sub(f.firstAt) > 2*time.Minute {
			f.key = x.keyFor(f, now)
		}
		if err := x.s.Put(ctx, f.key, f.body); err != nil {
			x.res.PutFailures++
			keep = append(keep, f)
			continue
		}
		x.landed(f)
	}
	x.pending = keep
}

func (x *sim) keyFor(f *simFile, now time.Time) string {
	return fmt.Sprintf("%sminute=%s/%s.parquet", records.ObjectKeyPrefix(x.l.Prefix, f.bucket), now.UTC().Format(minuteLayout), f.base)
}

func (x *sim) landed(f *simFile) {
	x.uploaded = append(x.uploaded, f.recs...)
	x.res.Files++
	x.res.Records += len(f.recs)
}

// stepReplicas runs each live replica once: its owned buckets' cycles, then a
// lease round when one is due.
func (x *sim) stepReplicas(ctx context.Context) {
	now := x.clock.Now()
	for _, r := range x.reps {
		if now.Before(r.pausedTo) {
			continue
		}
		bs := make([]int, 0, len(r.buckets))
		for b := range r.buckets {
			bs = append(bs, b)
		}
		sort.Ints(bs)
		for _, b := range bs {
			w := r.buckets[b]
			if x.budget > 0 {
				w.MaxFiles = max(1, x.budget/len(bs))
			}
			st, err := w.Cycle(ctx)
			if errors.Is(err, ErrFenced) {
				x.res.Fenced++
				delete(r.buckets, b)
				continue
			}
			if st.Committed {
				x.res.Commits++
			}
		}
		if !now.Before(r.nextTick) {
			r.nextTick = now.Add(10 * time.Second)
			owned, err := r.leaser.Tick(ctx)
			if err != nil {
				continue
			}
			for b := range r.buckets {
				if _, ok := owned[b]; !ok {
					delete(r.buckets, b)
				}
			}
			for b, ep := range owned {
				if _, ok := r.buckets[b]; !ok {
					r.buckets[b] = &Bucket{S: x.s, L: x.l, M: x.m, B: b, Bits: x.cfg.BucketBits, Epoch: ep,
						Replica: r.name, Clock: x.clock, CacheBytes: 1 << 16}
				}
			}
		}
	}
}

func (x *sim) chaos(ctx context.Context) {
	c := x.cfg.Chaos
	now := x.clock.Now()
	roll := func(p float64) bool { return p > 0 && x.rng.Float64() < p }
	if len(x.reps) > 1 && roll(c.KillHard) {
		x.removeReplica(x.rng.Intn(len(x.reps)), false, ctx)
		x.res.Kills++
	}
	if len(x.reps) > 1 && roll(c.KillGraceful) {
		x.removeReplica(x.rng.Intn(len(x.reps)), true, ctx)
		x.res.Kills++
	}
	if roll(c.StealLease) {
		if len(x.reps) < 2 {
			x.addReplica()
			x.res.Adds++
		}
		// Another replica takes a leased bucket while its owner runs on.
		b := x.rng.Intn(x.buckets())
		if st, err := x.reps[0].leaser.latest(ctx, b); err == nil && st.epoch > 0 {
			var thieves []*simReplica
			for _, r := range x.reps {
				if r.name != st.body.Replica {
					thieves = append(thieves, r)
				}
			}
			if len(thieves) > 0 {
				thief := thieves[x.rng.Intn(len(thieves))]
				if ok, _ := thief.leaser.write(ctx, b, st.epoch+1, leaseBody{Replica: thief.name}); ok {
					x.res.Steals++
				}
			}
		}
	}
	if roll(c.Pause) && len(x.reps) > 0 {
		r := x.reps[x.rng.Intn(len(x.reps))]
		r.pausedTo = now.Add(time.Duration(40+x.rng.Intn(50)) * time.Second)
		x.res.Pauses++
	}
	if roll(c.AddRemove) {
		if x.rng.Intn(2) == 0 || len(x.reps) <= 1 {
			x.addReplica()
			x.res.Adds++
		} else {
			x.removeReplica(x.rng.Intn(len(x.reps)), x.rng.Intn(2) == 0, ctx)
			x.res.Removes++
		}
	}
	if len(x.reps) == 0 {
		x.addReplica()
		x.res.Adds++
	}
}

// drain stops faults, uploads everything pending, and runs the replicas until
// no bucket has unread files, then compares the tables with one pass.
func (x *sim) drain(ctx context.Context) error {
	x.s.FailRate(0, x.rng)
	defer x.s.FailRate(x.cfg.Chaos.PutFailure, x.rng)
	for i := 0; ; i++ {
		x.clock.Advance(5 * time.Second)
		x.upload(ctx)
		x.stepReplicas(ctx)
		if len(x.pending) == 0 {
			lag, err := ComputeLag(ctx, x.s, x.l, x.buckets(), x.clock.Now())
			if err != nil {
				return err
			}
			if lag.BacklogFiles == 0 {
				break
			}
		}
		if i > 2000 {
			return fmt.Errorf("never drained: %d pending files", len(x.pending))
		}
	}
	x.res.Checkpoints++
	got, err := ReadAll(ctx, x.s, x.l, x.m, x.buckets())
	if err != nil {
		return err
	}
	if want := Reference(x.m, x.uploaded); !tablesEqual(got, want) {
		x.res.Mismatches++
		if x.res.FirstMismatch == "" {
			x.res.FirstMismatch = fmt.Sprintf("checkpoint %d at step %d: %s", x.res.Checkpoints, x.res.Steps, diffTables(got, want))
		}
	}
	return nil
}

// Simulate runs a distributed replay.
func Simulate(ctx context.Context, cfg SimConfig) (SimResult, error) {
	x := newSim(cfg)
	recs := cfg.Input
	if recs == nil {
		recs = newGen(cfg.Seed, 40).batch(cfg.Records)
	}
	steps := max(cfg.Steps, 1)
	per := (len(recs) + steps - 1) / steps
	every := steps / max(cfg.Checkpoints, 1)
	x.s.FailRate(cfg.Chaos.PutFailure, x.rng)
	for i := 0; i < steps; i++ {
		x.res.Steps = i + 1
		x.clock.Advance(time.Duration(1+x.rng.Intn(5)) * time.Second)
		lo, hi := min(i*per, len(recs)), min((i+1)*per, len(recs))
		if lo < hi {
			x.ingest(x.rng.Intn(max(cfg.IngestPods, 1)), recs[lo:hi])
		}
		x.upload(ctx)
		x.chaos(ctx)
		x.stepReplicas(ctx)
		if every > 0 && (i+1)%every == 0 && i+1 < steps {
			if err := x.drain(ctx); err != nil {
				return x.res, err
			}
		}
	}
	if err := x.drain(ctx); err != nil {
		return x.res, err
	}
	if cfg.OutDir != "" {
		return x.res, x.dump(cfg.OutDir)
	}
	return x.res, nil
}

// dump writes what readers read of the committed layout (the commit logs and the
// table files; never state/, leases or members) to dir, keyed exactly as in the
// store: commits name their files by those keys.
func (x *sim) dump(dir string) error {
	x.s.mu.Lock()
	defer x.s.mu.Unlock()
	root := x.l.root()
	for _, k := range x.s.keys(root) {
		if rel := strings.TrimPrefix(k, root); !strings.HasPrefix(rel, "buckets/") && !strings.HasPrefix(rel, "tables/") {
			continue
		}
		p := filepath.Join(dir, filepath.FromSlash(k))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, x.s.objs[k].b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// BurstConfig: a steady load, a burst of Burst times it, and a scaler that
// doubles replicas while lag is over Target (KEDA's behavior on /lag).
type BurstConfig struct {
	Seed            int64
	BucketBits      int
	BaseRate, Burst int // records per upload; uploads per step during the burst
	FilesPerCycle   int // a replica reads 4× this many files per step, across its buckets
	Target          time.Duration
	MaxReplicas     int
	// Steps (5 s each), and the burst's first and last step. Defaults: 400, 20–80.
	Steps, BurstFrom, BurstTo int
}

// BurstResult is how lag and replicas moved.
type BurstResult struct {
	Target, PeakLag, FinalLag time.Duration
	MaxReplicas, Mismatches   int
}

// SimulateBurst runs a load burst against lag-based scaling.
func SimulateBurst(ctx context.Context, cfg BurstConfig) (BurstResult, error) {
	x := newSim(SimConfig{Seed: cfg.Seed, IngestPods: 3, Processors: 1, BucketBits: cfg.BucketBits})
	x.budget = 4 * cfg.FilesPerCycle
	g := newGen(cfg.Seed, 60)
	res := BurstResult{Target: cfg.Target, MaxReplicas: 1}
	if cfg.Steps == 0 {
		cfg.Steps, cfg.BurstFrom, cfg.BurstTo = 400, 20, 80
	}
	var lastScale, calmSince time.Time
	for i := 0; i < cfg.Steps; i++ {
		x.clock.Advance(5 * time.Second)
		uploads := 1
		if i >= cfg.BurstFrom && i < cfg.BurstTo {
			uploads = cfg.Burst
		}
		if i >= cfg.Steps*3/4 {
			uploads = 0
		}
		for u := 0; u < uploads; u++ {
			x.ingest(u%3, g.batch(cfg.BaseRate))
		}
		x.upload(ctx)
		x.stepReplicas(ctx)
		now := x.clock.Now()
		if now.Sub(lastScale) >= 15*time.Second {
			lastScale = now
			lag, err := ComputeLag(ctx, x.s, x.l, x.buckets(), now)
			if err != nil {
				return res, err
			}
			d := time.Duration(lag.Seconds * float64(time.Second))
			if d > res.PeakLag {
				res.PeakLag = d
			}
			res.FinalLag = d
			switch {
			case d > cfg.Target && len(x.reps) < cfg.MaxReplicas:
				for n := len(x.reps); n > 0 && len(x.reps) < cfg.MaxReplicas; n-- {
					x.addReplica()
				}
				calmSince = time.Time{}
			case d < cfg.Target/6:
				if calmSince.IsZero() {
					calmSince = now
				} else if now.Sub(calmSince) >= 10*time.Minute && len(x.reps) > 1 {
					x.removeReplica(len(x.reps)-1, true, ctx)
					calmSince = now
				}
			default:
				calmSince = time.Time{}
			}
			res.MaxReplicas = max(res.MaxReplicas, len(x.reps))
		}
	}
	x.budget = 0
	if err := x.drain(ctx); err != nil {
		return res, err
	}
	res.Mismatches = x.res.Mismatches
	return res, nil
}

func tablesEqual(a, b map[string][]Row) bool {
	return reflect.DeepEqual(nonEmpty(a), nonEmpty(b))
}

func nonEmpty(t map[string][]Row) map[string][]Row {
	out := map[string][]Row{}
	for k, v := range t {
		if len(v) > 0 {
			out[k] = MergeRows(v)
		}
	}
	return out
}

func diffTables(got, want map[string][]Row) string {
	g, w := nonEmpty(got), nonEmpty(want)
	for t, wr := range w {
		gr := g[t]
		if len(gr) != len(wr) {
			return fmt.Sprintf("%s: %d rows, want %d", t, len(gr), len(wr))
		}
		for i := range wr {
			if !reflect.DeepEqual(gr[i], wr[i]) {
				return fmt.Sprintf("%s row %s/%s: %+v, want %+v", t, wr[i].Key, wr[i].ID, gr[i].Value, wr[i].Value)
			}
		}
	}
	return "tables differ"
}
