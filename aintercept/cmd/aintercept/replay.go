package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream"
)

// replay is `aintercept replay`: streams an existing raw capture through the
// processor event by event and checks the result against a full rebuild. It
// only reads the source; everything it writes goes under -work.
func replay() {
	fs := flag.NewFlagSet("replay", flag.ExitOnError)
	src := fs.String("src", "", "raw capture: a local directory, or s3://bucket/prefix")
	region := fs.String("region", "nyc3", "S3 region")
	endpoint := fs.String("endpoint", "https://nyc3.digitaloceanspaces.com", "S3 endpoint (empty: AWS)")
	work := fs.String("work", "", "local directory for the replay's uploads and results (must be empty or a previous replay's)")
	batch := fs.Int("batch-events", 200, "events per replayed upload (1: each event its own upload)")
	pollEvery := fs.Int("poll-every", 1, "poll after this many uploads")
	restartEvery := fs.Int("restart-every", 50, "restart the processor after about this many polls (0: never)")
	seed := fs.Int64("seed", 1, "random seed (pod lag, restarts)")
	from := fs.String("from", "", "only raw files from this hour on (2006-01-02T15)")
	to := fs.String("to", "", "only raw files before this hour (2006-01-02T15)")
	checkEvery := fs.Int64("checkpoint-every", 0, "run -checkpoint-cmd after this many polls, and at the end (0: never)")
	checkCmd := fs.String("checkpoint-cmd", "", "a shell command run at each checkpoint, with SHAPED set to the replay's tables so far; failing stops the replay")
	processors := fs.Int("processors", 0, "replay through the bucketed processor instead: this many replicas, with chaos, in memory (-src must be local)")
	ingestPods := fs.Int("ingest-pods", 3, "with -processors: ingest pods uploading records")
	steps := fs.Int("steps", 2000, "with -processors: simulation steps")
	checkpoints := fs.Int("checkpoints", 10, "with -processors: drained checkpoints compared with one pass")
	out := fs.String("out", "", "with -processors: write the committed layout (stream/v1/…) here, for the stateless API")
	fs.Parse(os.Args[2:])
	if *processors > 0 {
		replayStream(*src, *seed, *ingestPods, *processors, *steps, *checkpoints, *out)
		return
	}
	if *src == "" || *work == "" {
		fs.Usage()
		os.Exit(2)
	}
	ctx := context.Background()
	cfg := process.ReplayConfig{Work: process.DirStore{Root: *work}, BatchEvents: *batch, PollEvery: *pollEvery,
		RestartEvery: *restartEvery, Seed: *seed}
	if rest, ok := strings.CutPrefix(*src, "s3://"); ok {
		bucket, prefix, _ := strings.Cut(rest, "/")
		// Credentials from the environment (AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY) or the default chain.
		s, err := process.NewS3Store(ctx, process.S3Config{Bucket: bucket, Region: *region, Endpoint: *endpoint})
		if err != nil {
			fatalf("%v", err)
		}
		cfg.Source, cfg.SourcePrefix = s, prefix
	} else {
		if err := process.SeparateDirs(*src, *work); err != nil {
			fatalf("%v", err)
		}
		cfg.Source = process.DirStore{Root: *src}
	}
	for _, x := range []struct {
		s  string
		at *time.Time
	}{{*from, &cfg.From}, {*to, &cfg.To}} {
		if x.s != "" {
			t, err := time.Parse("2006-01-02T15", x.s)
			if err != nil {
				fatalf("bad hour %q: %v", x.s, err)
			}
			*x.at = t
		}
	}
	if *checkCmd != "" {
		shaped := filepath.Join(*work, "replay")
		cfg.CheckpointEvery = *checkEvery
		cfg.Checkpoint = func(ctx context.Context, polls int64) error {
			fmt.Fprintf(os.Stderr, "replay: checkpoint after %d polls\n", polls)
			cmd := exec.CommandContext(ctx, "sh", "-c", *checkCmd)
			cmd.Env = append(os.Environ(), "SHAPED="+shaped, fmt.Sprintf("POLLS=%d", polls))
			cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
			return cmd.Run()
		}
	}
	cfg.Progress = func(p process.ReplayProgress) {
		fmt.Fprintf(os.Stderr, "replay: %d events, %d uploads, %d polls, %d restarts; data time %s\n",
			p.Events, p.Uploads, p.Polls, p.Restarts, p.DataTime.Format(time.RFC3339))
	}
	rep, err := process.Replay(ctx, cfg)
	if err != nil {
		fatalf("replay: %v", err)
	}
	fmt.Printf("source files     %d (%d unreadable, skipped by both)\n", rep.SourceFiles, rep.UnreadableFiles)
	fmt.Printf("streamed         %d events as %d uploads; %d polls, %d restarts\n", rep.Events, rep.Uploads, rep.Polls, rep.Restarts)
	fmt.Printf("tables           %d requests, %d analytics events, %d sessions, %d prompts, %d people\n",
		rep.Requests, rep.AnalyticsEvents, rep.Sessions, rep.Prompts, rep.People)
	fmt.Printf("uncaptured calls %d analytics events report a call the proxy never saw (coverage gaps)\n", rep.UncapturedCalls)
	fmt.Printf("uncarried events %d analytics events whose carrying request has no row\n", rep.UncarriedEvents)
	fmt.Printf("elapsed          %s\n", rep.Elapsed.Round(time.Second))
	ok := true
	if len(rep.Differences) == 0 {
		fmt.Println("vs rebuild       identical")
	} else {
		ok = false
		fmt.Printf("vs rebuild       %d differences:\n", len(rep.Differences))
		for _, d := range rep.Differences {
			fmt.Println("  " + d)
		}
	}
	if len(rep.KeyProblems) == 0 {
		fmt.Println("keys             every primary key unique, every reference resolves")
	} else {
		ok = false
		fmt.Printf("keys             %d problems:\n", len(rep.KeyProblems))
		for i, k := range rep.KeyProblems {
			if i == 20 {
				fmt.Printf("  … and %d more\n", len(rep.KeyProblems)-20)
				break
			}
			fmt.Println("  " + k)
		}
	}
	if !ok {
		os.Exit(1)
	}
}

func fatalf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}

// replayStream canonicalizes a local raw capture with the observer's code and
// replays its records through the bucketed processor (internal/stream) with
// chaos, comparing every checkpoint with one pass.
func replayStream(src string, seed int64, pods, processors, steps, checkpoints int, out string) {
	if src == "" || strings.HasPrefix(src, "s3://") {
		fatalf("-processors needs a local -src")
	}
	ctx := context.Background()
	col := &recordCollector{}
	st, err := records.CanonDir(ctx, src, col, "")
	if err != nil {
		fatalf("canon: %v", err)
	}
	fmt.Fprintf(os.Stderr, "replay: %d raw rows -> %d records\n", st.Rows, len(col.recs))
	res, err := stream.Simulate(ctx, stream.SimConfig{Seed: seed, IngestPods: pods, Processors: processors,
		BucketBits: records.DefaultBucketBits, Steps: steps, Checkpoints: checkpoints, Input: col.recs,
		Model: stream.StateModel{}, OutDir: out,
		Chaos: stream.Chaos{KillHard: 0.005, KillGraceful: 0.005, StealLease: 0.01, Pause: 0.01,
			PutFailure: 0.02, DelayUpload: 0.02, AddRemove: 0.005}})
	fmt.Printf("records          %d in %d files\n", res.Records, res.Files)
	fmt.Printf("chaos            %d kills, %d steals, %d pauses, %d adds, %d removes, %d failed puts; %d fenced cycles\n",
		res.Kills, res.Steals, res.Pauses, res.Adds, res.Removes, res.PutFailures, res.Fenced)
	fmt.Printf("checkpoints      %d, %d differing from one pass\n", res.Checkpoints, res.Mismatches)
	if err != nil {
		fatalf("replay: %v", err)
	}
	if res.Mismatches > 0 {
		fatalf("first difference: %s", res.FirstMismatch)
	}
}

// recordCollector keeps every record file's records. The records sink puts on
// its own goroutines, so Put locks.
type recordCollector struct {
	mu   sync.Mutex
	recs []records.Record
}

func (c *recordCollector) Put(_ context.Context, key string, body []byte) error {
	if !strings.HasSuffix(key, ".parquet") || !strings.Contains(key, "v1/keys/") || !strings.Contains(key, "/in/") {
		return nil
	}
	rs, err := records.Decode(body)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.recs = append(c.recs, rs...)
	c.mu.Unlock()
	return nil
}
