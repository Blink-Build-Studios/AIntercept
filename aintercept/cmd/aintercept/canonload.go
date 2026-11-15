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
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/flush"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/ingest"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// loadChunk is how many records a flush writes, as the observer flushes (§2.2).
const loadChunk = 1000

// canonLoad is `aintercept canon -load <postgres-url>`: the raw files' records,
// as canon makes them, written to the v9 fact tables by the flush
// (docs/PIPELINE.md §4), 1,000 records per flush. It applies the schema to an
// empty database. With shuffle (a seed, nonzero), the records are written in a
// random order instead (the order-independence gate): spooled to a temp
// directory in random parts, then each part shuffled and written, the parts in
// random order.
func canonLoad(src, dsn string, shuffle int64) {
	ctx := context.Background()
	start := time.Now()
	db, err := pg.Open(ctx, dsn, 4)
	if err != nil {
		fatalf("load: %v", err)
	}
	defer db.Close()
	if err := pg.Apply(ctx, db); err != nil {
		fatalf("load: %v", err)
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
