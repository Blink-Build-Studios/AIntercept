package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream"
)

// replay is `aintercept replay`: canonicalizes a local raw capture and replays
// its records through the bucketed stream processor with chaos, comparing every
// checkpoint with one pass. It only reads the source.
func replay() {
	fs := flag.NewFlagSet("replay", flag.ExitOnError)
	src := fs.String("src", "", "raw capture: a local directory")
	seed := fs.Int64("seed", 1, "random seed (chaos)")
	processors := fs.Int("processors", 3, "stream processor replicas")
	ingestPods := fs.Int("ingest-pods", 3, "ingest pods uploading records")
	steps := fs.Int("steps", 2000, "simulation steps")
	checkpoints := fs.Int("checkpoints", 10, "drained checkpoints compared with one pass")
	out := fs.String("out", "", "write the committed layout (stream/v1/…) here")
	fs.Parse(os.Args[2:])
	replayStream(*src, *seed, *ingestPods, *processors, *steps, *checkpoints, *out)
}

func fatalf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}

// replayStream canonicalizes a local raw capture with the observer's code and
// replays its records through the bucketed processor (internal/stream).
func replayStream(src string, seed int64, pods, processors, steps, checkpoints int, out string) {
	if src == "" || strings.HasPrefix(src, "s3://") {
		fatalf("-src must be a local directory")
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
	mu     sync.Mutex
	recs   []records.Record
	newest int64            // the newest raw row any file carried (data time)
	byKey  map[string]int64 // each key's newest raw row its files carried (rows that complete no record too)
}

func (c *recordCollector) Put(_ context.Context, key string, body []byte) error {
	if !strings.HasSuffix(key, ".parquet") || !strings.Contains(key, "v1/keys/") || !strings.Contains(key, "/in/") {
		return nil
	}
	rs, newest, err := records.DecodeWithNewest(body)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.recs = append(c.recs, rs...)
	c.newest = max(c.newest, newest)
	if k, ok := ingestKey(key); ok {
		if c.byKey == nil {
			c.byKey = map[string]int64{}
		}
		c.byKey[k] = max(c.byKey[k], newest)
	}
	c.mu.Unlock()
	return nil
}

// ingestKey is the routing key an ingest file's name holds: <prefix>v1/keys/<esc key>/in/….
func ingestKey(name string) (string, bool) {
	_, rest, ok := strings.Cut(name, "v1/keys/")
	if !ok {
		return "", false
	}
	esc, _, ok := strings.Cut(rest, "/in/")
	if !ok {
		return "", false
	}
	k, err := url.PathUnescape(esc)
	return k, err == nil
}
