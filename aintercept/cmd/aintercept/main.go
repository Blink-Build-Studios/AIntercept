// aintercept is the one binary behind every Go service, run in a mode: one mode
// per process (and per pod), each with only its own configuration and secrets.
//
//	aintercept observer   Envoy ext_proc tee: observations to the sink
//	aintercept certsvc    Envoy SDS server: mints leaf certificates on demand
//	aintercept replay     replays an existing capture through the stream processor with chaos and checks it (a tool; takes flags)
//	aintercept canon      turns raw observer files into canonical records, through the observer's code (a tool; takes flags)
//	aintercept rollup     the workers: with ROLLUP_POSTGRES_URL, v7's (claim flagged keys, apply new ingest files, commit to Postgres; the tail); else v5's (Redis locks, the merge)
//	aintercept v1layout   lays out a raw capture as the v5 pipeline would (keys and merge; a tool; takes flags)
//	aintercept storecheck whether an object store has what the stream processor needs (a tool; takes flags)
//
// The services are configured through environment variables.
package main

import (
	"fmt"
	"os"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/certsvc"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/observer"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/rollup"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/worker"
)

var modes = map[string]func(){
	"observer":   observer.Run,
	"certsvc":    certsvc.Run,
	"replay":     replay,
	"canon":      canonCmd,
	"storecheck": storecheck,
	"v1layout":   v1layout,
	"rollup":     rollupMode,
}

// rollupMode is the v7 worker when ROLLUP_POSTGRES_URL names its Postgres,
// else v5's rollup (Redis) until build step 8 switches Compose and the chart.
func rollupMode() {
	if os.Getenv("ROLLUP_POSTGRES_URL") != "" {
		worker.Run()
		return
	}
	rollup.Run()
}

func main() {
	if len(os.Args) < 2 || (len(os.Args) > 2 && os.Args[1] != "replay" && os.Args[1] != "canon" && os.Args[1] != "storecheck" && os.Args[1] != "v1layout") {
		usage()
	}
	run, ok := modes[os.Args[1]]
	if !ok {
		usage()
	}
	run()
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: aintercept observer|certsvc|rollup, or aintercept replay|canon|storecheck|v1layout -h")
	os.Exit(2)
}
