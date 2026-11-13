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
			if err := c.Put(context.Background(), fmt.Sprintf("records/v1/bucket=00/minute=x/%d.parquet", f), body); err != nil {
				t.Error(err)
			}
		}(f)
	}
	wg.Wait()
	if len(c.recs) != files*per {
		t.Fatalf("%d records, want %d", len(c.recs), files*per)
	}
}
