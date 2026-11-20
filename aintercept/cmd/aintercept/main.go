// aintercept is the one binary behind every Go service, run in a mode: one mode
// per process (and per pod), each with only its own configuration and secrets.
//
//	aintercept observer   Envoy ext_proc tee: streams raw files to the object store (docs/PIPELINE.md §2.1)
//	aintercept certsvc    Envoy SDS server: mints leaf certificates on demand
//	aintercept worker     the workers (docs/PIPELINE.md §2.2, §5): load the observers' raw files into Postgres and refresh the rollups
//	aintercept canon      writes a capture as raw files, or loads it into Postgres, through the observer's and the workers' code (a tool; takes flags)
//	aintercept capture-fixture  writes one call from the raw files as an anonymized corpus case (a tool; takes flags; docs/PROVIDERS.md)
//
// The services are configured through environment variables.
package main

import (
	"fmt"
	"os"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/certsvc"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/ingest"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/observer"
)

var modes = map[string]func(){
	"observer":        observer.Run,
	"certsvc":         certsvc.Run,
	"canon":           canonCmd,
	"capture-fixture": captureFixtureCmd,
	"worker":          ingest.Run,
}

// tools take flags; the services take none.
var tools = map[string]bool{"canon": true, "capture-fixture": true}

func main() {
	if len(os.Args) < 2 || (len(os.Args) > 2 && !tools[os.Args[1]]) {
		usage()
	}
	run, ok := modes[os.Args[1]]
	if !ok {
		usage()
	}
	run()
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: aintercept observer|certsvc|worker, or aintercept canon -h, or aintercept capture-fixture -h")
	os.Exit(2)
}

func fatalf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}
