package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
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
	fs.Parse(os.Args[2:])
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
