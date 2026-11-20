package main

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/flush"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/ingest"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/refresh"
)

// loadChunk is how many records a flush writes, as the observer flushes (§2.2).
const loadChunk = 1000

// canonLoad is `aintercept canon -load <postgres-url>`: the raw files' records,
// as canon makes them, written to the v9 fact tables by the flush
// (docs/PIPELINE.md §4), 1,000 records per flush. It applies the schema to an
// empty database. With shuffle (a seed, nonzero), the records are written in a
// random order instead (the order-independence gate): spooled to a temp
// directory in random parts, then each part shuffled and written, the parts in
// random order. With refreshers, that many rollup refreshers run alongside
// the load as the workers' do, then the open calls are swept, the rest drained
// and every rollup checked against the facts from scratch (refreshLoad); with
// timings, each kind of refresh is timed too.
func canonLoad(src, dsn string, shuffle int64, refreshers int, timings bool) {
	ctx := context.Background()
	start := time.Now()
	db, err := pg.Open(ctx, dsn, int32(4+refreshers))
	if err != nil {
		fatalf("load: %v", err)
	}
	defer db.Close()
	if err := pg.Apply(ctx, db); err != nil {
		fatalf("load: %v", err)
	}
	if refreshers > 0 {
		defer refreshLoad(ctx, db, refreshers, timings)()
	}
	if fi, err := os.Stat(filepath.Join(src, "raw")); err == nil && fi.IsDir() {
		if shuffle != 0 {
			fatalf("load: -shuffle reads observer files, not raw files")
		}
		rawLoad(ctx, db, src, start)
		return
	}
	var flushes int64
	var flushTime time.Duration
	write := func(rs []records.Record) error {
		t := time.Now()
		if err := flush.Write(ctx, db, "canon", rs); err != nil {
			return err
		}
		flushes++
		flushTime += time.Since(t)
		return nil
	}
	var st records.Stats
	if shuffle == 0 {
		var buf []records.Record
		st, err = records.CanonDirRecords(ctx, src, func(rs []records.Record) error {
			if buf = append(buf, rs...); len(buf) < loadChunk {
				return nil
			}
			err := write(buf)
			buf = buf[:0]
			return err
		})
		if err == nil && len(buf) > 0 {
			err = write(buf)
		}
	} else {
		st, err = shuffledLoad(ctx, src, shuffle, write)
	}
	if err != nil {
		fatalf("load: %v", err)
	}
	fmt.Printf("load: %d files, %d rows (%d bad), %d records, %d flushes, %.1fs (flushing %.1fs, %.1f µs a record)\n",
		st.Files, st.Rows, st.Bad, st.Records, flushes, time.Since(start).Seconds(), flushTime.Seconds(),
		float64(flushTime.Microseconds())/float64(max(st.Records, 1)))
	if err := summarize(ctx, db); err != nil {
		fatalf("load: %v", err)
	}
}

// refreshLoad starts n refreshers (and the sweep, every 5 s) and returns
// what ends them: it stops them, sweeps, drains and checks every rollup
// against the facts from scratch, printing what each took, and fails if one
// differs; with timings, it then times each kind of refresh.
func refreshLoad(ctx context.Context, db *pgxpool.Pool, n int, timings bool) func() {
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var refreshed atomic.Int64
	// run repeats f every interval until stopped; stopping never waits out
	// an interval.
	run := func(every time.Duration, f func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if err := f(); err != nil {
					fatalf("refresh: %v", err)
				}
				select {
				case <-stop:
					return
				case <-time.After(every):
				}
			}
		}()
	}
	for i := 0; i < n; i++ {
		run(100*time.Millisecond, func() error { k, err := refresh.Drain(ctx, db); refreshed.Add(int64(k)); return err })
	}
	run(5*time.Second, func() error { _, err := refresh.Sweep(ctx, db); return err })
	return func() {
		close(stop)
		wg.Wait()
		during := refreshed.Load()
		refresh.Coalesce = 0 // the load is over: drain the coalesced rows too, without waiting
		t := time.Now()
		swept, err := refresh.Sweep(ctx, db)
		if err != nil {
			fatalf("refresh: sweep: %v", err)
		}
		var drained atomic.Int64
		var dg sync.WaitGroup
		for i := 0; i < n; i++ {
			dg.Add(1)
			go func() {
				defer dg.Done()
				k, err := refresh.Drain(ctx, db)
				if err != nil {
					fatalf("refresh: %v", err)
				}
				drained.Add(int64(k))
			}()
		}
		dg.Wait()
		fmt.Printf("refresh: %d rows refreshed during the load; then %d calls swept and %d rows drained in %.1fs\n",
			during, swept, drained.Load(), time.Since(t).Seconds())
		t = time.Now()
		res, err := refresh.Check(ctx, db)
		if err != nil {
			fatalf("refresh: check: %v", err)
		}
		fmt.Printf("  %-16s %10s %10s %8s %8s %24s  (from scratch in %.1fs)\n", "rollup", "rows", "scratch", "missing", "extra", "checksum", time.Since(t).Seconds())
		var differ []string
		for _, r := range res {
			mark := "equal"
			if !r.Equal() {
				mark = "DIFFERENT"
				differ = append(differ, r.Table)
			}
			fmt.Printf("  %-16s %10d %10d %8d %8d %24s  %s\n", r.Table, r.Rows, r.Scratch, r.Missing, r.Extra, r.Checksum, mark)
		}
		if len(differ) > 0 {
			fatalf("refresh: %v differ from the facts from scratch", differ)
		}
		if timings {
			timeRefreshes(ctx, db)
		}
	}
}

// timeRefreshes refreshes every rollup row once more, one at a time, and
// prints what a refresh of each kind costs on this data (§5.1: one over 50 ms
// goes back to the owner).
func timeRefreshes(ctx context.Context, db *pgxpool.Pool) {
	fmt.Printf("  %-12s %8s %9s %9s %9s %9s  %s\n", "refresh", "rows", "mean ms", "p99 ms", "max ms", "over 50", "slowest")
	for _, k := range []struct{ kind, sql string }{
		{"minute", "SELECT session_id || '/' || minute_ns FROM session_minutes"},
		{"hour", "SELECT session_id || '/' || hour_ns FROM session_hours"},
		{"cred_minute", "SELECT DISTINCT key_hash || '/' || minute_ns FROM cred_minutes"},
		{"cred_hour", "SELECT DISTINCT key_hash || '/' || hour_ns FROM cred_hours"},
		{"step", "SELECT call_id FROM steps"},
		{"prompt", "SELECT DISTINCT prompt_id FROM prompts"},
		{"session", "SELECT session_id FROM sessions"},
		{"account_day", "SELECT DISTINCT account_id || '/' || day_ns FROM account_days"},
	} {
		rows, err := db.Query(ctx, k.sql)
		if err != nil {
			fatalf("refresh: time: %v", err)
		}
		keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			fatalf("refresh: time: %v", err)
		}
		ds := make([]time.Duration, 0, len(keys))
		var total time.Duration
		slowest := ""
		for _, key := range keys {
			t := time.Now()
			if err := refresh.Refresh(ctx, db, refresh.Claim{Kind: k.kind, Key: key}); err != nil {
				fatalf("refresh: time: %v", err)
			}
			d := time.Since(t)
			if len(ds) == 0 || d > slices.Max(ds) {
				slowest = key
			}
			ds = append(ds, d)
			total += d
		}
		if len(ds) == 0 {
			continue
		}
		slices.Sort(ds)
		over := len(ds) - sort.Search(len(ds), func(i int) bool { return ds[i] > 50*time.Millisecond })
		ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
		fmt.Printf("  %-12s %8d %9.2f %9.2f %9.2f %9d  %s\n", k.kind, len(ds), ms(total/time.Duration(len(ds))),
			ms(ds[len(ds)*99/100]), ms(ds[len(ds)-1]), over, slowestShape(ctx, db, k.kind, slowest))
	}
	if _, err := refresh.Drain(ctx, db); err != nil { // what the timing marked
		fatalf("refresh: %v", err)
	}
}

// slowestShape is the size of the slowest refresh's input, not its key (keys
// are real identifiers).
func slowestShape(ctx context.Context, db *pgxpool.Pool, kind, key string) string {
	switch kind {
	case "prompt":
		var steps int64
		_ = db.QueryRow(ctx, `SELECT count(*) FROM steps WHERE prompt_id = $1`, key).Scan(&steps)
		return fmt.Sprintf("(a prompt of %d steps)", steps)
	case "step":
		var steps int64
		_ = db.QueryRow(ctx, `SELECT count(*) FROM steps WHERE prompt_id = (SELECT prompt_id FROM calls WHERE call_id = $1)`, key).Scan(&steps)
		return fmt.Sprintf("(a step of a prompt of %d steps)", steps)
	case "session":
		var calls int64
		_ = db.QueryRow(ctx, `SELECT count(*) FROM calls WHERE session_id = $1`, key).Scan(&calls)
		return fmt.Sprintf("(a session of %d calls)", calls)
	}
	i := strings.LastIndexByte(key, '/')
	if i < 0 || kind != "hour" {
		return ""
	}
	var calls int64
	_ = db.QueryRow(ctx, `SELECT count(*) FROM calls WHERE session_id = $1 AND start_ns >= $2::bigint AND start_ns < $2::bigint + 3600000000000`,
		key[:i], key[i+1:]).Scan(&calls)
	return fmt.Sprintf("(an hour of %d calls)", calls)
}

func summarize(ctx context.Context, db *pgxpool.Pool) error {
	ss, err := flush.Summarize(ctx, db)
	if err != nil {
		return err
	}
	fmt.Printf("  %-20s %12s %20s %14s\n", "table", "rows", "checksum", "bytes")
	for _, s := range ss {
		fmt.Printf("  %-20s %12d %20s %14d\n", s.Table, s.Rows, s.Checksum, s.Bytes)
	}
	return nil
}

// shuffledParts is how many parts a shuffled load spools records into.
const shuffledParts = 64

func shuffledLoad(ctx context.Context, src string, seed int64, write func([]records.Record) error) (records.Stats, error) {
	rng := rand.New(rand.NewSource(seed))
	dir, err := os.MkdirTemp("", "aintercept-load-")
	if err != nil {
		return records.Stats{}, err
	}
	defer os.RemoveAll(dir)
	type part struct {
		f  *os.File
		zw *gzip.Writer
		je *json.Encoder
	}
	parts := make([]part, shuffledParts)
	for i := range parts {
		f, err := os.Create(filepath.Join(dir, fmt.Sprintf("%02d.json.gz", i)))
		if err != nil {
			return records.Stats{}, err
		}
		zw := gzip.NewWriter(f)
		parts[i] = part{f, zw, json.NewEncoder(zw)}
	}
	st, err := records.CanonDirRecords(ctx, src, func(rs []records.Record) error {
		for i := range rs {
			if err := parts[rng.Intn(shuffledParts)].je.Encode(&rs[i]); err != nil {
				return err
			}
		}
		return nil
	})
	for _, p := range parts {
		p.zw.Close()
		p.f.Close()
	}
	if err != nil {
		return st, err
	}
	for _, i := range rng.Perm(shuffledParts) {
		rs, err := readPart(filepath.Join(dir, fmt.Sprintf("%02d.json.gz", i)))
		if err != nil {
			return st, err
		}
		rng.Shuffle(len(rs), func(a, b int) { rs[a], rs[b] = rs[b], rs[a] })
		for len(rs) > 0 {
			n := min(len(rs), loadChunk)
			if err := write(rs[:n]); err != nil {
				return st, err
			}
			rs = rs[n:]
		}
	}
	return st, nil
}

func readPart(p string) ([]records.Record, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bufio.NewReader(zr))
	var out []records.Record
	for dec.More() {
		var r records.Record
		if err := dec.Decode(&r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// rawLoad loads a directory of v9 raw files (<src>/raw/…) as the workers do
// (docs/PIPELINE.md §2.2): listed into ingest_files, then each file loaded in
// one transaction, four at a time.
func rawLoad(ctx context.Context, db *pgxpool.Pool, src string, start time.Time) {
	store := process.DirStore{Root: src}
	listed, _, err := ingest.List(ctx, db, store, "")
	if err != nil {
		fatalf("load: list: %v", err)
	}
	var (
		mu     sync.Mutex
		loaded int
		first  error
		wg     sync.WaitGroup
	)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				key, err := ingest.LoadOne(ctx, db, store)
				mu.Lock()
				if err != nil && first == nil {
					first = err
				}
				if key != "" {
					loaded++
				}
				stop := err != nil || key == "" || first != nil
				mu.Unlock()
				if stop {
					return
				}
			}
		}()
	}
	wg.Wait()
	if first != nil {
		fatalf("load: %v", first)
	}
	fmt.Printf("load: %d raw files listed, %d loaded, %.1fs\n", listed, loaded, time.Since(start).Seconds())
	if err := summarize(ctx, db); err != nil {
		fatalf("load: %v", err)
	}
}

// canonRaw is `aintercept canon -raw <dir>`: the capture's rows through the
// observer's assembler and its writer (a members stream per pod, sealed at
// 8 MiB), as v9 raw files under dir/raw/ (docs/PIPELINE.md §2.1).
func canonRaw(src, out string) {
	start := time.Now()
	spool, err := os.MkdirTemp("", "aintercept-raw-spool-")
	if err != nil {
		fatalf("raw: %v", err)
	}
	defer os.RemoveAll(spool)
	target, err := events.NewDirTarget("local", out)
	if err != nil {
		fatalf("raw: %v", err)
	}
	w, err := events.NewWriter(events.WriterConfig{SpoolDir: spool, SkipFsync: true, Targets: []events.Target{target},
		NewTicker: func(time.Duration) (<-chan time.Time, func()) { return nil, func() {} }}) // sealed by size and at the end
	if err != nil {
		fatalf("raw: %v", err)
	}
	streams := map[string]*events.Stream{}
	last := ""
	st, err := records.CanonDirRaw(context.Background(), src, func(pod string) records.RawWriter {
		s := streams[pod]
		if s == nil {
			if last != "" { // pods come one after another: seal the last one's file
				if err := w.Flush(); err != nil {
					fatalf("raw: %v", err)
				}
			}
			last = pod
			s = w.NewStream(events.StreamConfig{Target: "local", Key: records.RawKey("", pod), Members: true, AtUpload: true,
				ContentType: "application/gzip", MaxBytes: 8 << 20, MaxAge: 30 * time.Second})
			streams[pod] = s
		}
		return s
	})
	if err != nil {
		fatalf("raw: %v", err)
	}
	if err := w.Close(); err != nil {
		fatalf("raw: %v", err)
	}
	ws := w.Stats()
	fmt.Printf("raw: %d files, %d rows (%d bad), %d records, %d pods, %d raw files, %.1fs\n",
		st.Files, st.Rows, st.Bad, st.Records, len(streams), ws.Uploads, time.Since(start).Seconds())
}
