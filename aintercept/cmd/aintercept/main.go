// aintercept is the one binary behind every Go service, run in a mode: one mode
// per process (and per pod), each with only its own configuration and secrets.
//
//	aintercept observer   Envoy ext_proc tee: observations to the sink
//	aintercept certsvc    Envoy SDS server: mints leaf certificates on demand
//	aintercept process    stage 2: raw partitions to shaped Parquet tables
//	aintercept mirror     the query API's sidecar: mirrors the tables, serves raw files
//	aintercept replay     streams an existing capture through the processor and checks it (a tool; takes flags)
//	aintercept derive     builds the derived tables from an existing shaped output (a tool; takes flags)
//
// The services are configured through environment variables.
package main

import (
	"fmt"
	"os"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/certsvc"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/observer"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
)

var modes = map[string]func(){
	"observer": observer.Run,
	"certsvc":  certsvc.Run,
	"process":  process.Run,
	"mirror":   process.RunMirror,
	"replay":   replay,
	"derive":   derive,
}

func main() {
	if len(os.Args) < 2 || (len(os.Args) > 2 && os.Args[1] != "replay" && os.Args[1] != "derive") {
		usage()
	}
	run, ok := modes[os.Args[1]]
	if !ok {
		usage()
	}
	run()
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: aintercept observer|certsvc|process|mirror, or aintercept replay|derive -h")
	os.Exit(2)
}
