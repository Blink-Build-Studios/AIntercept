package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/rollup"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream/state"
)

// v1layout lays out a raw capture as the v5 pipeline would (docs/PIPELINE.md):
// the observer's code turns it into records and each call's detail object
// (calls/<id>.ndjson.gz, what the drawer's raw view reads), each key's records
// are applied in one pass (its calls still waiting expired by the newest data),
// every key gets its first commit under v1/keys/, and one merge run writes
// v1/merge/. With -v2 (the default) it also writes the v6 views beside v1
// under v2/ (docs/PIPELINE.md §3, writeV2). The API's parity tests, the v6
// oracle's table-level harness (api/tests/oracle/) and local tools read the
// result.
func v1layout() {
	fs := flag.NewFlagSet("v1layout", flag.ExitOnError)
	src := fs.String("src", "", "raw capture directory")
	out := fs.String("out", "", "output directory (the bucket's root)")
	prefix := fs.String("prefix", "", "key prefix inside the bucket")
	v2 := fs.Bool("v2", true, "also write the v6 views under <prefix>v2/ (docs/PIPELINE.md §3)")
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
	if *v2 {
		if err := writeV2(ctx, s, *prefix, keys, byKey); err != nil {
			fatalf("v1layout: v2: %v", err)
		}
	}
	fmt.Printf("v1layout: %d records, %d keys, merge %d (%d partitions)\n", len(col.recs), len(keys), res.N, len(res.Rewritten))
}

// writeV2 writes the v6 side of a layout beside its v1 output
// (docs/PIPELINE.md, Testing), for the oracle's side-by-side harness
// (api/tests/oracle/). The v1 keys and merge partitions stay: the oracle reads
// them until build step 11. So far, TEST-ONLY and not the v6 storage format
// (still under review): each session key's finished tables (state.RenderSession),
// one Parquet file per session per table, v2/test-sessions/<sid>/<table>.parquet
// (an empty table has no file).
func writeV2(ctx context.Context, s stream.DirStore, prefix string, keys []string, byKey map[string]map[string][]stream.Row) error {
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	for _, k := range keys {
		sid, ok := strings.CutPrefix(k, "s:")
		if !ok {
			continue
		}
		rows := state.Rows{}
		for table, rs := range byKey[k] {
			t := make(map[string]any, len(rs))
			for _, r := range rs {
				t[r.ID] = r.Value
			}
			rows[table] = t
		}
		dir := prefix + "v2/test-sessions/" + url.PathEscape(sid) + "/"
		for name, body := range sessionFiles(state.RenderSession(sid, rows)) {
			if err := s.Put(ctx, dir+name+".parquet", body); err != nil {
				return err
			}
		}
	}
	return nil
}

// sessionFiles is each non-empty table of a session as a Parquet file.
func sessionFiles(t *state.SessionTables) map[string][]byte {
	out := map[string][]byte{}
	add := func(name string, n int, enc func() []byte) {
		if n > 0 {
			out[name] = enc()
		}
	}
	add("session_rows", len(t.SessionRows), func() []byte { return process.EncodeParquet(t.SessionRows) })
	add("presence_segments", len(t.PresenceSegments), func() []byte { return process.EncodeParquet(t.PresenceSegments) })
	add("session_tools", len(t.SessionTools), func() []byte { return process.EncodeParquet(t.SessionTools) })
	add("event_rows", len(t.EventRows), func() []byte { return process.EncodeParquet(t.EventRows) })
	add("session_marks", len(t.SessionMarks), func() []byte { return process.EncodeParquet(t.SessionMarks) })
	add("prompt_rows", len(t.PromptRows), func() []byte { return process.EncodeParquet(t.PromptRows) })
	add("prompt_steps", len(t.PromptSteps), func() []byte { return process.EncodeParquet(t.PromptSteps) })
	add("session_request_rows", len(t.SessionRequestRows), func() []byte { return process.EncodeParquet(t.SessionRequestRows) })
	add("call_tool_uses", len(t.CallToolUses), func() []byte { return process.EncodeParquet(t.CallToolUses) })
	return out
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
