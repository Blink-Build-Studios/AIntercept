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
)

// v1layout lays out a raw capture as the v5 pipeline would (docs/PIPELINE.md):
// the observer's code turns it into records and each call's detail object
// (calls/<id>.ndjson.gz, what the drawer's raw view reads), each key's records
// are applied in one pass, every key gets its first commit under v1/keys/, and
// one merge run writes v1/merge/. The API's parity tests and local tools read
// the result.
func v1layout() {
	fs := flag.NewFlagSet("v1layout", flag.ExitOnError)
	src := fs.String("src", "", "raw capture directory")
	out := fs.String("out", "", "output directory (the bucket's root)")
	prefix := fs.String("prefix", "", "key prefix inside the bucket")
	_ = fs.Parse(os.Args[2:])
	if *src == "" || *out == "" {
		fatalf("v1layout: -src and -out are required")
	}
	ctx := context.Background()
	s := stream.DirStore{DirStore: process.DirStore{Root: *out}}
	col := &recordCollector{}
	put := detailsTo{s: s, records: col}
	if _, err := records.CanonDirWith(ctx, *src, put, *prefix, records.CanonOptions{Details: true}); err != nil {
		fatalf("canon: %v", err)
	}
	byKey := map[string]map[string][]stream.Row{}
	for table, rows := range stream.Reference(stream.StateModel{}, col.recs) {
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
}

// detailsTo writes the calls' detail objects to the store and hands records to
// the collector.
type detailsTo struct {
	s       stream.DirStore
	records *recordCollector
}

func (d detailsTo) Put(ctx context.Context, key string, body []byte) error {
	if strings.Contains(key, "calls/") {
		return d.s.Put(ctx, key, body)
	}
	return d.records.Put(ctx, key, body)
}
