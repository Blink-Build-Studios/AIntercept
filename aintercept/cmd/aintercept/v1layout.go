package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/render"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/rollup"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream/state"
)

// v1layout lays out a raw capture as the v5 pipeline would (docs/PIPELINE.md):
// the observer's code turns it into records and each call's detail object
// (calls/<id>.ndjson.gz, what the drawer's raw view reads), each key's records
// are applied in one pass (its calls still waiting expired by the newest data),
// every key gets its first commit under v1/keys/, and one merge run writes
// v1/merge/. With -pg it also writes every key's rendered rows into Postgres
// (writePG). The API's parity tests, the oracle's table-level harness
// (api/tests/oracle/) and local tools read the result.
func v1layout() {
	fs := flag.NewFlagSet("v1layout", flag.ExitOnError)
	src := fs.String("src", "", "raw capture directory")
	out := fs.String("out", "", "output directory (the bucket's root)")
	prefix := fs.String("prefix", "", "key prefix inside the bucket")
	pgDSN := fs.String("pg", "", "also write every key's rendered rows into this Postgres (docs/PIPELINE.md v7 §3.3-3.5)")
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
	if *pgDSN != "" {
		if err := writePG(ctx, *pgDSN, keys, byKey, now); err != nil {
			fatalf("v1layout: pg: %v", err)
		}
	}
	fmt.Printf("v1layout: %d records, %d keys, merge %d (%d partitions)\n", len(col.recs), len(keys), res.N, len(res.Rewritten))
}

// writePG writes every key's rendered rows (internal/render: a session's 9
// tables, its call days, every key's agent calls and batch parts) into the
// Postgres at dsn, each key written whole in its own transaction, under the
// day its rows give it (rollup.DayOf). The schema is applied if the database
// is empty. The side-by-side harness (api/tests/test_v6_session_rows.py)
// reads them back.
func writePG(ctx context.Context, dsn string, keys []string, byKey map[string]map[string][]stream.Row, now int64) error {
	pool, err := pg.Open(ctx, dsn, 2)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := pg.Apply(ctx, pool); err != nil {
		return err
	}
	for _, k := range keys {
		rows := state.Rows{}
		for table, rs := range byKey[k] {
			t := make(map[string]any, len(rs))
			for _, r := range rs {
				t[r.ID] = r.Value
			}
			rows[table] = t
		}
		r, err := render.Render(k, rows, rollup.DayOf(k, byKey[k], now))
		if err != nil {
			return err
		}
		if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error { return r.Diff(nil).Write(ctx, tx) }); err != nil {
			return fmt.Errorf("key %s: %w", k, err)
		}
	}
	return nil
}

// detailsTo writes the calls' detail objects to the store and hands records to
// the collector.
type detailsTo struct {
	s       stream.DirStore
	records *recordCollector
}

func (d detailsTo) Put(ctx context.Context, key string, body []byte) error {
	if strings.Contains(key, "calls/") || strings.HasSuffix(key, records.ContentMark("")) {
		return d.s.Put(ctx, key, body)
	}
	return d.records.Put(ctx, key, body)
}
