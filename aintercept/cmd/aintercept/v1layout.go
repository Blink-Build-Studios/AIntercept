package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/rollup"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/worker"
)

// v1layout lays out a raw capture as the v5 pipeline would (docs/PIPELINE.md):
// the observer's code turns it into records and each call's detail object
// (calls/<id>.ndjson.gz, what the drawer's raw view reads), each key's records
// are applied in one pass (its calls still waiting expired by the newest data),
// every key gets its first commit under v1/keys/, and one merge run writes
// v1/merge/. With -pg it also writes the ingest files and runs the v7 worker
// over them into Postgres (runWorker): every key's stored inputs and rendered
// rows, through the worker's own commits. The API's parity tests, the oracle's table-level harness
// (api/tests/oracle/) and local tools read the result.
func v1layout() {
	fs := flag.NewFlagSet("v1layout", flag.ExitOnError)
	src := fs.String("src", "", "raw capture directory")
	out := fs.String("out", "", "output directory (the bucket's root)")
	prefix := fs.String("prefix", "", "key prefix inside the bucket")
	pgDSN := fs.String("pg", "", "also run the v7 worker over the ingest files into this Postgres (docs/PIPELINE.md v7 §2-3)")
	_ = fs.Parse(os.Args[2:])
	if *src == "" || *out == "" {
		fatalf("v1layout: -src and -out are required")
	}
	ctx := context.Background()
	s := stream.DirStore{DirStore: process.DirStore{Root: *out}}
	col := &recordCollector{}
	put := detailsTo{s: s, records: col, ingest: *pgDSN != ""}
	if _, err := records.CanonDirWith(ctx, *src, put, *prefix, records.CanonOptions{Details: true}); err != nil {
		fatalf("canon: %v", err)
	}
	// The calls still waiting at the end expire by the newest data, as the
	// rollup's would once the merge has seen it all.
	clock := col.newest
	for i := range col.recs {
		clock = max(clock, stream.RecordTS(&col.recs[i]))
	}
	byKey := map[string]map[string][]stream.Row{}
	for table, rows := range stream.ReferenceAt(stream.StateModel{}, col.recs, clock) {
		for _, r := range rows {
			if byKey[r.Key] == nil {
				byKey[r.Key] = map[string][]stream.Row{}
			}
			byKey[r.Key][table] = append(byKey[r.Key][table], r)
		}
	}
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	now := time.Now().UnixNano()
	for _, k := range keys {
		if err := rollup.WriteOnePass(ctx, s, *prefix, k, byKey[k], now); err != nil {
			fatalf("v1layout: %v", err)
		}
	}
	m := &rollup.Merger{Store: s, Prefix: *prefix, Queue: &rollup.KeysQueue{Keys: keys}, Batch: len(keys) + 1}
	res, err := m.RunOnce(ctx)
	if err != nil {
		fatalf("v1layout: merge: %v", err)
	}
	fmt.Printf("v1layout: %d records, %d keys, merge %d (%d partitions)\n", len(col.recs), len(keys), res.N, len(res.Rewritten))
	if *pgDSN != "" {
		seen := map[string]bool{}
		var inKeys []string
		for i := range col.recs {
			if k := col.recs[i].RoutingKey; !seen[k] {
				seen[k] = true
				inKeys = append(inKeys, k)
			}
		}
		sort.Strings(inKeys)
		ticks, err := runWorker(ctx, s, *prefix, *pgDSN, inKeys)
		if err != nil {
			fatalf("v1layout: pg: %v", err)
		}
		fmt.Printf("v1layout: the worker drained %d keys into Postgres in %d ticks\n", len(inKeys), ticks)
	}
}

// runWorker is v7's path over the layout's ingest files (docs/PIPELINE.md §2):
// every key with files is flagged, as the tail would, and one worker ticks
// until nothing is flagged, with the due calls run between drains so calls
// that never end expire by data time. The schema is applied if the database
// is empty. It returns the ticks it took. The side-by-side harnesses
// (api/tests/test_v6_session_rows.py, test_v7_stored_inputs.py) read the rows.
func runWorker(ctx context.Context, s stream.Store, prefix, dsn string, keys []string) (int, error) {
	w, err := worker.New(ctx, worker.Config{Store: s, Prefix: prefix, Name: "v1layout", DSN: dsn})
	if err != nil {
		return 0, err
	}
	defer w.Pool().Close()
	defer w.Release(context.WithoutCancel(ctx))
	if _, err := w.Pool().Exec(ctx, `INSERT INTO dirty_keys (key, seq, since) SELECT k, 1, now() FROM unnest($1::text[]) k
		ON CONFLICT (key) DO UPDATE SET seq = dirty_keys.seq + 1`, keys); err != nil {
		return 0, err
	}
	flags := func() (int, error) {
		var n int
		err := w.Pool().QueryRow(ctx, "SELECT count(*) FROM dirty_keys").Scan(&n)
		return n, err
	}
	for ticks := 1; ticks <= 1000; ticks++ {
		if err := w.Tick(ctx); err != nil {
			return ticks, err
		}
		n, err := flags()
		if err != nil {
			return ticks, err
		}
		if n > 0 {
			continue
		}
		if err := w.FlagDueCalls(ctx); err != nil {
			return ticks, err
		}
		if n, err = flags(); err != nil || n == 0 {
			return ticks, err
		}
	}
	return 0, fmt.Errorf("keys stayed flagged")
}

// detailsTo writes the calls' detail objects to the store and hands records to
// the collector; with ingest, it also writes the ingest files, as ingest does.
type detailsTo struct {
	s       stream.DirStore
	records *recordCollector
	ingest  bool
}

func (d detailsTo) Put(ctx context.Context, key string, body []byte) error {
	if strings.Contains(key, "calls/") || strings.HasSuffix(key, records.ContentMark("")) {
		return d.s.Put(ctx, key, body)
	}
	if d.ingest && strings.Contains(key, "v1/keys/") && strings.Contains(key, "/in/") {
		if err := d.s.Put(ctx, key, body); err != nil {
			return err
		}
	}
	return d.records.Put(ctx, key, body)
}
