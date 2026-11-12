package process

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"time"
)

// ReplayConfig sets up a replay: the raw capture in Source is taken apart
// into its events and uploaded again, a few at a time, the way the observer
// streams them, with the processor polling as they land. The same files are
// then processed in one full rebuild, and the two results are compared.
type ReplayConfig struct {
	Source       Store
	SourcePrefix string
	// Work holds the replay's own uploads and both results; it must not be
	// the source. Keys under it: raw/, replay/, rebuild/.
	Work Store
	// BatchEvents is the events per replayed upload (1: every event its own
	// file). PollEvery polls after this many uploads. RestartEvery restarts the
	// processor (a new one resuming from the last commit) after about this many
	// polls (0: never).
	BatchEvents, PollEvery, RestartEvery int
	// MaxAgeNS cuts a pod's batch once this much data time has passed since its
	// first event, like the observer's 60 s flush timer (default 60 s).
	MaxAgeNS int64
	// MaxLagNS is how far a pod's uploads may trail the others (default 3 min).
	MaxLagNS int64
	Seed     int64
	// From and To limit the replay to raw files named for that time (zero: no limit).
	From, To time.Time
	// Progress, if set, is called every so often.
	Progress func(ReplayProgress)
}

type ReplayProgress struct {
	Events, Uploads, Polls, Restarts int64
	DataTime                         time.Time
}

// ReplayReport is what a replay found.
type ReplayReport struct {
	SourceFiles, UnreadableFiles                         int
	Events, Uploads, Polls, Restarts                     int64
	Requests, AnalyticsEvents, Sessions, Prompts, People int
	// Differences between the replay's tables and the rebuild's (empty: identical).
	Differences []string
	// Problems with keys in the replay's tables (empty: every key unique, every reference resolves).
	KeyProblems                      []string
	UncapturedCalls, UncarriedEvents int
	Elapsed                          time.Duration
}

// sourceFiles lists the raw files under the source prefix inside the window.
func sourceFiles(ctx context.Context, cfg ReplayConfig) ([]Object, error) {
	base := strings.Trim(cfg.SourcePrefix, "/")
	pre := "dt="
	if base != "" {
		pre = base + "/dt="
	}
	objs, err := cfg.Source.List(ctx, pre)
	if err != nil {
		return nil, err
	}
	var out []Object
	for _, o := range objs {
		rel := strings.TrimPrefix(strings.TrimPrefix(o.Key, base), "/")
		if !rawKey.MatchString(rel) {
			continue
		}
		at, err := time.Parse("2006-01-02T15", rel[3:13]+"T"+rel[19:21])
		if err != nil {
			continue
		}
		if (!cfg.From.IsZero() && at.Before(cfg.From.Truncate(time.Hour))) || (!cfg.To.IsZero() && !at.Before(cfg.To)) {
			continue
		}
		out = append(out, o)
	}
	return out, nil
}

// onlyKeys is a read view of a store that lists only the given keys.
type onlyKeys struct {
	Store
	keys map[string]bool
}

func (o onlyKeys) ListAfter(ctx context.Context, prefix, after string) ([]Object, error) {
	objs, err := o.Store.ListAfter(ctx, prefix, after)
	var out []Object
	for _, x := range objs {
		if o.keys[x.Key] {
			out = append(out, x)
		}
	}
	return out, err
}

func (o onlyKeys) List(ctx context.Context, prefix string) ([]Object, error) {
	objs, err := o.Store.List(ctx, prefix)
	var out []Object
	for _, x := range objs {
		if o.keys[x.Key] {
			out = append(out, x)
		}
	}
	return out, err
}

// podStream yields one pod's events in the order the pod wrote them.
type podStream struct {
	pod   string
	files []Object
	lines [][]byte
	ts    []int64
	next  int
	lag   int64
	buf   [][]byte
	first int64 // data time of the buffered batch's first event
	seq   int
	last  int64 // the last upload's name time (kept increasing)
}

func (p *podStream) peek(ctx context.Context, src Store, bad *int) (int64, bool, error) {
	for p.next >= len(p.lines) {
		if len(p.files) == 0 {
			return 0, false, nil
		}
		f := p.files[0]
		p.files = p.files[1:]
		b, err := src.Get(ctx, f.Key)
		if err != nil {
			return 0, false, fmt.Errorf("read %s: %w", f.Key, err)
		}
		lines, err := gunzipLines(b)
		if err != nil {
			*bad++ // the rebuild skips it too
			continue
		}
		p.lines, p.ts, p.next = lines, make([]int64, len(lines)), 0
		var row struct {
			TS int64 `json:"timestamp_ns"`
		}
		var prev int64
		for i, l := range lines {
			if json.Unmarshal(l, &row) == nil && row.TS != 0 {
				prev = row.TS
			}
			p.ts[i] = prev // a bad line rides with the row before it
		}
	}
	return p.ts[p.next], true, nil
}

// Replay runs a replay and reports how the streamed result compares.
func Replay(ctx context.Context, cfg ReplayConfig) (ReplayReport, error) {
	start := time.Now()
	var rep ReplayReport
	if cfg.BatchEvents <= 0 {
		cfg.BatchEvents = 200
	}
	if cfg.PollEvery <= 0 {
		cfg.PollEvery = 1
	}
	if cfg.MaxAgeNS == 0 {
		cfg.MaxAgeNS = int64(60 * time.Second)
	}
	if cfg.MaxLagNS == 0 {
		cfg.MaxLagNS = int64(3 * time.Minute)
	}
	rng := rand.New(rand.NewSource(cfg.Seed))

	files, err := sourceFiles(ctx, cfg)
	if err != nil {
		return rep, fmt.Errorf("list source: %w", err)
	}
	rep.SourceFiles = len(files)
	if len(files) == 0 {
		return rep, fmt.Errorf("no raw files under %q in the window", cfg.SourcePrefix)
	}

	// The rebuild: the same files, one full pass.
	keys := map[string]bool{}
	for _, f := range files {
		keys[f.Key] = true
	}
	rebuild := New(Config{Raw: onlyKeys{cfg.Source, keys}, RawPrefix: cfg.SourcePrefix, Out: cfg.Work, OutPrefix: "rebuild",
		Grace: time.Nanosecond})
	if err := rebuild.Open(ctx); err != nil {
		return rep, err
	}
	if _, err := rebuild.Poll(ctx, true); err != nil {
		return rep, fmt.Errorf("rebuild: %w", err)
	}

	// The replay: each pod's events, re-uploaded a batch at a time.
	byPod := map[string]*podStream{}
	for _, f := range files {
		k := f.Key
		pod := k[strings.Index(k, "instance=")+len("instance=") : strings.LastIndex(k, "/")]
		if byPod[pod] == nil {
			byPod[pod] = &podStream{pod: pod}
		}
		byPod[pod].files = append(byPod[pod].files, f)
	}
	var pods []*podStream
	for _, p := range byPod {
		sort.Slice(p.files, func(i, j int) bool { return rawOrder(p.files[i].Key) < rawOrder(p.files[j].Key) })
		p.lag = rng.Int63n(cfg.MaxLagNS)
		pods = append(pods, p)
	}
	sort.Slice(pods, func(i, j int) bool { return pods[i].pod < pods[j].pod })

	var newest int64
	now := func() time.Time { return time.Unix(0, newest).Add(time.Minute) }
	newProc := func() (*Processor, error) {
		// Nothing reads a replay while it runs: replaced files go at once.
		p := New(Config{Raw: cfg.Work, RawPrefix: "raw", Out: cfg.Work, OutPrefix: "replay", BatchFiles: 500, Grace: time.Nanosecond, Now: now})
		return p, p.Open(ctx)
	}
	proc, err := newProc()
	if err != nil {
		return rep, err
	}
	report := func() {
		if cfg.Progress != nil {
			cfg.Progress(ReplayProgress{rep.Events, rep.Uploads, rep.Polls, rep.Restarts, time.Unix(0, newest).UTC()})
		}
	}
	flush := func(p *podStream) error {
		if len(p.buf) == 0 {
			return nil
		}
		name := max(p.first, p.last+1)
		p.last, p.seq = name, p.seq+1
		at := time.Unix(0, name).UTC()
		key := fmt.Sprintf("raw/dt=%s/hour=%02d/instance=%s/%d-%06d.ndjson.gz", at.Format("2006-01-02"), at.Hour(), p.pod, name, p.seq)
		var gzbuf bytes.Buffer
		zw := gzip.NewWriter(&gzbuf)
		for _, l := range p.buf {
			zw.Write(l)
			zw.Write([]byte{'\n'})
		}
		zw.Close()
		if err := cfg.Work.Put(ctx, key, gzbuf.Bytes()); err != nil {
			return err
		}
		p.buf = p.buf[:0]
		p.lag = rng.Int63n(cfg.MaxLagNS) // the next upload may land sooner or later
		rep.Uploads++
		if rep.Uploads%int64(cfg.PollEvery) != 0 {
			return nil
		}
		if _, err := proc.Poll(ctx, false); err != nil {
			return fmt.Errorf("poll: %w", err)
		}
		rep.Polls++
		if rep.Polls%1000 == 0 {
			report()
		}
		if cfg.RestartEvery > 0 && rng.Intn(cfg.RestartEvery) == 0 {
			if proc, err = newProc(); err != nil {
				return fmt.Errorf("restart: %w", err)
			}
			rep.Restarts++
		}
		return nil
	}
	for {
		// The next event: from the pod whose next event, plus its upload lag, is earliest.
		var pick *podStream
		var pickAt int64
		for _, p := range pods {
			ts, ok, err := p.peek(ctx, cfg.Source, &rep.UnreadableFiles)
			if err != nil {
				return rep, err
			}
			if ok && (pick == nil || ts+p.lag < pickAt) {
				pick, pickAt = p, ts+p.lag
			}
		}
		if pick == nil {
			break
		}
		ts := pick.ts[pick.next]
		// The observer cuts a batch on a timer, not on its next event: a pod that
		// went quiet (or away) uploads what it holds once it's MaxAge old.
		for _, p := range pods {
			if len(p.buf) > 0 && ts-p.first > cfg.MaxAgeNS {
				if err := flush(p); err != nil {
					return rep, err
				}
			}
		}
		if len(pick.buf) == 0 {
			pick.first = ts
		}
		pick.buf = append(pick.buf, pick.lines[pick.next])
		pick.next++
		newest = max(newest, ts)
		rep.Events++
		if len(pick.buf) >= cfg.BatchEvents {
			if err := flush(pick); err != nil {
				return rep, err
			}
		}
	}
	for _, p := range pods {
		if err := flush(p); err != nil {
			return rep, err
		}
	}
	if _, err := proc.Poll(ctx, true); err != nil {
		return rep, fmt.Errorf("final poll: %w", err)
	}
	report()

	got, err := ReadTables(ctx, cfg.Work, "replay")
	if err != nil {
		return rep, err
	}
	want, err := ReadTables(ctx, cfg.Work, "rebuild")
	if err != nil {
		return rep, err
	}
	rep.Requests, rep.AnalyticsEvents, rep.Sessions, rep.Prompts, rep.People =
		len(got.Requests), len(got.Events), len(got.Sessions), len(got.Prompts), len(got.People)
	rep.Differences = Diff(got.WithoutLineage(), want.WithoutLineage(), 20)
	k := CheckKeys(got)
	rep.KeyProblems, rep.UncapturedCalls, rep.UncarriedEvents = k.Problems, k.UncapturedCalls, k.UncarriedEvents
	rep.Elapsed = time.Since(start)
	return rep, nil
}
