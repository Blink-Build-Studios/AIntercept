// aintercept is the one binary behind every Go service, run in a mode: one mode
// per process (and per pod), each with only its own configuration and secrets.
//
//	aintercept observer   Envoy ext_proc tee: observations to the sink
//	aintercept certsvc    Envoy SDS server: mints leaf certificates on demand
//	aintercept replay     replays an existing capture through the stream processor with chaos and checks it (a tool; takes flags)
//	aintercept canon      turns raw observer files into canonical records, through the observer's code (a tool; takes flags)
//	aintercept rollup     v5 per-key rollups: claim dirty keys (Redis locks), apply new ingest files, commit; the merge
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
)

var modes = map[string]func(){
	"observer":   observer.Run,
	"certsvc":    certsvc.Run,
	"replay":     replay,
	"canon":      canonCmd,
	"storecheck": storecheck,
	"v1layout":   v1layout,
	"rollup":     rollup.Run,
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
