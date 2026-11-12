package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
)

// derive is `aintercept derive`: builds the derived tables (request_rows,
// person_last, prompt_rows, prompt_steps, session_rows,
// session_marks, presence_segments, event_rows, session_tools,
// session_request_rows) from an existing shaped output (what
// its manifest names) without processing anything, and writes each to
// <out>/<table>.parquet. The parity test's quick loop; the output is only read.
func derive() {
	fs := flag.NewFlagSet("derive", flag.ExitOnError)
	shaped := fs.String("shaped", "", "the processor's output directory (holding manifest.json)")
	out := fs.String("out", "", "the directory to write the tables to")
	fs.Parse(os.Args[2:])
	if *shaped == "" || *out == "" {
		fs.Usage()
		os.Exit(2)
	}
	start := time.Now()
	d, err := process.Derive(context.Background(), process.DirStore{Root: *shaped}, "")
	if err != nil {
		fatalf("%v", err)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fatalf("%v", err)
	}
	for name, b := range map[string][]byte{
		"request_rows":         process.EncodeParquet(d.RequestRows),
		"person_last":          process.EncodeParquet(d.PersonLast),
		"prompt_rows":          process.EncodeParquet(d.PromptRows),
		"prompt_steps":         process.EncodeParquet(d.PromptSteps),
		"session_rows":         process.EncodeParquet(d.SessionRows),
		"session_marks":        process.EncodeParquet(d.SessionMarks),
		"presence_segments":    process.EncodeParquet(d.Presence),
		"event_rows":           process.EncodeParquet(d.EventRows),
		"session_tools":        process.EncodeParquet(d.SessionTools),
		"session_request_rows": process.EncodeParquet(d.SessionReqs),
		"person_map":           process.EncodeParquet(d.PersonMap),
		"person_sessions":      process.EncodeParquet(d.PersonSess),
		"account_facts":        process.EncodeParquet(d.AcctFacts),
		"account_devices":      process.EncodeParquet(d.AcctDevices),
		"call_tool_uses":       process.EncodeParquet(d.CallTools),
	} {
		if err := os.WriteFile(filepath.Join(*out, name+".parquet"), b, 0o644); err != nil {
			fatalf("%v", err)
		}
	}
	fmt.Fprintf(os.Stderr, "%d request rows, %d prompt rows, %d prompt steps, %d session rows, %d session marks, %d presence segments, %d event rows, %d session tools, %d session request rows, %d person map rows, %d person sessions, %d account facts, %d account devices, %d call tool uses in %s\n",
		len(d.RequestRows), len(d.PromptRows), len(d.PromptSteps), len(d.SessionRows), len(d.SessionMarks), len(d.Presence),
		len(d.EventRows), len(d.SessionTools), len(d.SessionReqs), len(d.PersonMap), len(d.PersonSess), len(d.AcctFacts),
		len(d.AcctDevices), len(d.CallTools), time.Since(start).Round(time.Millisecond))
}
