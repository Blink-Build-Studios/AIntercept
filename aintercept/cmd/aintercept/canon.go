package main

import (
	"flag"
	"os"
)

// canonCmd is `aintercept canon`: a directory of raw observer files, through
// the same code the live observer and the workers run. With -raw it writes
// them as v9 raw files (docs/PIPELINE.md §2.1); with -load it writes their
// records to Postgres (canonload.go), and a directory of v9 raw files (raw/…)
// is loaded as the workers load them.
func canonCmd() {
	fs := flag.NewFlagSet("canon", flag.ExitOnError)
	src := fs.String("src", "", "a directory of raw observer files (…/instance=<pod>/<file>.ndjson.gz), or with -load of v9 raw files (raw/…)")
	load := fs.String("load", "", "a Postgres URL: write the records to the v9 fact tables (docs/PIPELINE.md §4)")
	shuffle := fs.Int64("shuffle", 0, "with -load: write the records in a random order from this seed (the order-independence gate)")
	refreshers := fs.Int("refresh", 0, "with -load: run this many rollup refreshers alongside the load, then sweep, drain and check every rollup against the facts from scratch (docs/PIPELINE.md §5)")
	raw := fs.String("raw", "", "a directory: write the capture there as v9 raw files (raw/…, docs/PIPELINE.md §2.1), through the observer's writer")
	fs.Parse(os.Args[2:])
	if *src == "" || (*raw == "") == (*load == "") {
		fs.Usage()
		os.Exit(2)
	}
	if *raw != "" {
		canonRaw(*src, *raw)
		return
	}
	canonLoad(*src, *load, *shuffle, *refreshers)
}
