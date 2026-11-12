package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// canonCmd is `aintercept canon`: turns a directory of raw observer files into
// canonical records (docs/STREAMING_PIPELINE.md §3.5), through the same code
// the live observer runs. With -verify it also checks, on the same rows, that
// the records equal today's processor's request and event rows, and that the
// live sink's records equal canon's.
func canonCmd() {
	fs := flag.NewFlagSet("canon", flag.ExitOnError)
	src := fs.String("src", "", "a directory of raw observer files (…/instance=<pod>/<file>.ndjson.gz)")
	out := fs.String("out", "", "the directory to write records to (records/v1/…); empty: don't write")
	verify := fs.Bool("verify", false, "compare with today's processor and with the live sink, on the same rows")
	content := fs.Bool("content", false, "the rows carry bodies (a full-interception capture/ prefix)")
	fs.Parse(os.Args[2:])
	if *src == "" {
		fs.Usage()
		os.Exit(2)
	}
	start := time.Now()
	var put records.Putter = &collect{}
	if *out != "" {
		put = dirPut{root: *out}
	}
	var shaper *process.Shaper
	live := map[string]*records.Sink{}
	livePut := &collect{hashOnly: true}
	var observe func(string, *events.Event, []byte)
	if *verify {
		shaper = process.NewShaper()
		canonPut := &collect{}
		if *out != "" {
			put = tee{dirPut{root: *out}, canonPut}
		} else {
			put = canonPut
		}
		observe = func(pod string, e *events.Event, line []byte) {
			shaper.Apply(e, line, "")
			s := live[pod]
			if s == nil {
				s = records.NewSink(records.SinkConfig{Putter: livePut, Incarnation: "live-" + pod, Content: *content,
					MaxRecords: 50000, MaxAge: 365 * 24 * time.Hour})
				live[pod] = s
			}
			s.Emit(*e)
		}
	}
	st, err := records.CanonDirObserved(context.Background(), *src, put, "", observe)
	if err != nil {
		fatalf("%v", err)
	}
	fmt.Printf("canon: %d files, %d rows (%d bad), %d records, %.1fs\n", st.Files, st.Rows, st.Bad, st.Records, time.Since(start).Seconds())
	var names []string
	for c := range st.Cases {
		names = append(names, c)
	}
	sort.Strings(names)
	for _, c := range names {
		fmt.Printf("  %-32s %d\n", c, st.Cases[c])
	}
	failed := false
	if err := st.CheckCoverage(); err != nil {
		fmt.Println("coverage:", err)
		failed = true
	}
	if *verify {
		for _, s := range live {
			if err := s.Flush(); err != nil {
				fatalf("live sink: %v", err)
			}
		}
		var canonPut *collect
		switch p := put.(type) {
		case *collect:
			canonPut = p
		case tee:
			canonPut = p.b
		}
		failed = verifyCanon(shaper, canonPut, livePut) || failed
	}
	if failed {
		os.Exit(1)
	}
}

// verifyCanon compares canon's records with the processor's rows and with the
// live sink's records, and reports where a call's session first appeared.
func verifyCanon(shaper *process.Shaper, got, live *collect) bool {
	wantReq, wantEv := shaper.Rows()
	reqs, evs := map[string]canon.Request{}, map[string]canon.Event{}
	starts, ends := map[string]string{}, map[string]map[string]bool{}
	for _, r := range got.recs {
		switch r.Kind {
		case records.KindCallStart:
			starts[r.CallID] = r.RoutingKey
		case records.KindCallEnd, records.KindWSConnection:
			c := *r.Call
			c.SourceFile, c.FirstFile = "", ""
			reqs[r.CallID] = c
			if ends[r.CallID] == nil {
				ends[r.CallID] = map[string]bool{}
			}
			ends[r.CallID][r.RoutingKey] = true
		case records.KindClientEvent:
			e := *r.Event
			e.SourceFile = ""
			evs[e.EventKey] = e
		}
	}
	bad := false
	diff := func(what string, n, missing, extra, differ int) {
		fmt.Printf("%s: %d; missing %d, extra %d, differing %d\n", what, n, missing, extra, differ)
		bad = bad || missing+extra+differ > 0
	}
	missing, differ := 0, 0
	seen := map[string]bool{}
	for _, w := range wantReq {
		w.SourceFile, w.FirstFile = "", ""
		seen[w.CallID] = true
		g, ok := reqs[w.CallID]
		switch {
		case !ok:
			missing++
		case !reflect.DeepEqual(g, w):
			if differ++; differ <= 3 {
				fmt.Printf("  request %s differs:\n   got  %+v\n   want %+v\n", w.CallID, g, w)
			}
		}
	}
	diff("requests vs processor", len(wantReq), missing, len(reqs)-len(seen)+missing, differ)
	missing, differ = 0, 0
	seenEv := map[string]bool{}
	for _, w := range wantEv {
		w.SourceFile = ""
		seenEv[w.EventKey] = true
		g, ok := evs[w.EventKey]
		switch {
		case !ok:
			missing++
		case !reflect.DeepEqual(g, w):
			if differ++; differ <= 3 {
				fmt.Printf("  event %s differs:\n   got  %+v\n   want %+v\n", w.EventKey, g, w)
			}
		}
	}
	diff("events vs processor", len(wantEv), missing, len(evs)-len(seenEv)+missing, differ)

	// The live sink's records, as a multiset of contents (ids aside).
	g, l := got.hashes(), live.hashes()
	lm, em := 0, 0
	for h, n := range g {
		if l[h] < n {
			lm += n - l[h]
		}
	}
	for h, n := range l {
		if g[h] < n {
			em += n - g[h]
		}
	}
	diff("records: live sink vs canon", len(got.recs), lm, em, 0)

	// Where each call's session first appears: at its start (one key), or only at its end.
	sameKey, split, noStart := 0, 0, 0
	for id, keys := range ends {
		k, ok := starts[id]
		switch {
		case !ok:
			noStart++
		case len(keys) == 1 && keys[k]:
			sameKey++
		default:
			split++
		}
	}
	fmt.Printf("session at the start: %d calls end under their start's key, %d end under another too (split), %d have no start\n",
		sameKey, split, noStart)
	return bad
}

// collect keeps written records in memory (or only their content hashes).
type collect struct {
	mu       sync.Mutex
	hashOnly bool
	recs     []records.Record
	sums     map[[32]byte]int
}

func (c *collect) Put(_ context.Context, _ string, b []byte) error {
	rs, err := records.Decode(b)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.hashOnly {
		c.recs = append(c.recs, rs...)
		return nil
	}
	if c.sums == nil {
		c.sums = map[[32]byte]int{}
	}
	for _, r := range rs {
		c.sums[contentHash(r)]++
	}
	return nil
}

func (c *collect) hashes() map[[32]byte]int {
	if c.hashOnly {
		return c.sums
	}
	m := map[[32]byte]int{}
	for _, r := range c.recs {
		m[contentHash(r)]++
	}
	return m
}

// contentHash is a record's content without what names its writer.
func contentHash(r records.Record) [32]byte {
	r.RecordID, r.EventKey, r.Instance = "", "", ""
	b, _ := json.Marshal(r)
	return sha256.Sum256(b)
}

type dirPut struct{ root string }

func (d dirPut) Put(_ context.Context, key string, b []byte) error {
	p := filepath.Join(d.root, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644)
}

type tee struct {
	a records.Putter
	b *collect
}

func (t tee) Put(ctx context.Context, key string, b []byte) error {
	if err := t.a.Put(ctx, key, b); err != nil {
		return err
	}
	return t.b.Put(ctx, key, b)
}
