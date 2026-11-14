package main

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// The records sink uploads on its own goroutines: the replay's collector must
// keep every record whatever the interleaving (it lost ~0.1% of a real capture's).
func TestRecordCollectorKeepsEveryRecordUnderConcurrentPuts(t *testing.T) {
	c := &recordCollector{}
	const files, per = 64, 50
	var wg sync.WaitGroup
	for f := 0; f < files; f++ {
		rs := make([]records.Record, per)
		for i := range rs {
			id := fmt.Sprintf("f%d-%d", f, i)
			rs[i] = records.Record{Version: records.Version, RecordID: id, Kind: records.KindCallEnd, CallID: id,
				Call: &canon.Request{CallID: id, SessionID: "s1"}, RoutingKey: "s:s1"}
		}
		body, err := records.Encode(rs)
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func(f int) {
			defer wg.Done()
			if err := c.Put(context.Background(), fmt.Sprintf("v1/keys/s%%3Ax/in/20270301T100000Z-pod-%012d.parquet", f), body); err != nil {
				t.Error(err)
			}
		}(f)
	}
	wg.Wait()
	if len(c.recs) != files*per {
		t.Fatalf("%d records, want %d", len(c.recs), files*per)
	}
}

// Each key's commit records the newest raw row its files carried, rows that
// complete no record (a call still in flight) included, as the rollup loop's
// commits do (Commit.NewestRowNS): v5's merge, and so the oracle's /status,
// takes it over the newest record.
func TestRecordCollectorKeepsEachKeysNewestRawRow(t *testing.T) {
	c := &recordCollector{}
	put := func(name, key string, newest int64) {
		t.Helper()
		rs := []records.Record{{Version: records.Version, RecordID: name, Kind: records.KindCallEnd, CallID: name,
			Call: &canon.Request{CallID: name}, RoutingKey: key}}
		body, err := records.EncodeWithNewest(rs, newest)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Put(context.Background(), "sim/v1/keys/"+name+"/in/20270301T100000Z-pod-000000000001.parquet", body); err != nil {
			t.Fatal(err)
		}
	}
	put("s%3Aa", "s:a", 7)
	put("s%3Aa", "s:a", 5)
	put("c%3Akh%2F1%3Ad", "c:kh/1:d", 9)
	if c.byKey["s:a"] != 7 || c.byKey["c:kh/1:d"] != 9 || c.newest != 9 {
		t.Fatalf("byKey %v, newest %d", c.byKey, c.newest)
	}
	if _, ok := ingestKey("sim/v1/keys/s%3Aa/state/1-x"); ok {
		t.Fatal("a state chunk isn't an ingest file")
	}
}
