package ingest

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/flush"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg/pgtest"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/refresh"
)

// Stage 2 of ingest (docs/PIPELINE.md §2.2): one worker at a time lists raw
// files into ingest_files; any worker claims an unloaded one and loads it in
// one transaction.

func ctx(t testing.TB) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	return c
}

func fresh(t testing.TB) *pgxpool.Pool {
	t.Helper()
	p, err := pg.Open(ctx(t), pgtest.Fresh(t), 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	if err := pg.Apply(ctx(t), p); err != nil {
		t.Fatal(err)
	}
	return p
}

var t0 = time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)

// rawFile is a raw file of n calls, each completed with one row, and an
// analytics event: as the observer's sink writes them.
func rawFile(t testing.TB, name string, n int) []byte {
	t.Helper()
	var rs []records.Record
	var ds []records.Detail
	var out []byte
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%s-c%d", name, i)
		at := t0.Add(time.Duration(i) * time.Second).UnixNano()
		c := &canon.Request{CallID: id, CorrelationID: id, SessionID: "s-" + name, StartNS: at, EndNS: at + 1e9, Status: 200,
			Host: "api.anthropic.com", Method: "POST", Path: "/v1/messages", Product: canon.ProductClaudeCode, KeyHash: "kh1"}
		start := *c
		start.EndNS, start.Status = 0, 0
		rs = append(rs[:0], records.Record{Kind: records.KindCallStart, RecordID: id + "-s", CallID: id, Call: &start})
		ms, err := records.RawMembers(rs, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range ms {
			out = append(out, m...)
		}
		rs = append(rs[:0], records.Record{Kind: records.KindCallEnd, RecordID: id + "-e", CallID: id, Call: c},
			records.Record{Kind: records.KindClientEvent, RecordID: id + "-ev", BatchID: id, BatchN: 1, Call: &canon.Request{KeyHash: "kh1"},
				Event: &canon.Event{EventKey: id + "/ev", CorrelationID: id, RowNS: at, NS: at, Source: canon.ProductClaudeCode,
					Name: "tengu_input_prompt", ClientEventID: id + "-ev", SessionID: "s-" + name}})
		ds = append(ds[:0], records.Detail{CorrelationID: id, Lines: [][]byte{[]byte(`{"phase":"complete","correlation_id":"` + id + `"}`)}})
		if ms, err = records.RawMembers(rs, ds); err != nil {
			t.Fatal(err)
		}
		for _, m := range ms {
			out = append(out, m...)
		}
	}
	return out
}

func put(t testing.TB, root, key string, b []byte) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(filepath.Dir(p), ".tmp-"+filepath.Base(p)) // as the observer's DirTarget: whole or not at all
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, p); err != nil {
		t.Fatal(err)
	}
}

func rawKey(at time.Time, pod string, seq int) string {
	return fmt.Sprintf("raw/%s/%s/%019d-%06d.ndjson.gz", at.UTC().Format("2006-01-02/15"), pod, at.UnixNano(), seq)
}

func loadAll(t testing.TB, db *pgxpool.Pool, s process.Store) []string {
	t.Helper()
	var keys []string
	for {
		k, err := LoadOne(ctx(t), db, s)
		if err != nil {
			t.Fatal(err)
		}
		if k == "" {
			return keys
		}
		keys = append(keys, k)
	}
}

func TestListThenLoad(t *testing.T) {
	db := fresh(t)
	root := t.TempDir()
	s := process.DirStore{Root: root}
	k := rawKey(time.Now(), "pod-a", 1)
	put(t, root, k, rawFile(t, "f1", 3))
	put(t, root, "raw/"+time.Now().UTC().Format("2006-01-02/15")+"/pod-a/.tmp-in-progress", []byte("x"))
	n, ok, err := List(ctx(t), db, s, "")
	if err != nil || !ok || n != 1 {
		t.Fatalf("List: %d %v %v", n, ok, err)
	}
	if got := loadAll(t, db, s); len(got) != 1 || got[0] != k {
		t.Fatalf("loaded %v", got)
	}
	var calls, evs, ranged int
	if err := db.QueryRow(ctx(t), `SELECT (SELECT count(*) FROM calls), (SELECT count(*) FROM events),
		(SELECT count(*) FROM calls WHERE raw_key = $1 AND raw_offset IS NOT NULL AND raw_len > 0)`, k).Scan(&calls, &evs, &ranged); err != nil {
		t.Fatal(err)
	}
	if calls != 3 || evs != 3 || ranged != 3 {
		t.Fatalf("calls %d, events %d, with a raw range %d", calls, evs, ranged)
	}
	// Each call's range is its member: the call's own row, read alone.
	rows, err := db.Query(ctx(t), "SELECT call_id, raw_key, raw_offset, raw_len FROM calls")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, key string
		var off, n int64
		if err := rows.Scan(&id, &key, &off, &n); err != nil {
			t.Fatal(err)
		}
		b, err := s.Get(ctx(t), key)
		if err != nil {
			t.Fatal(err)
		}
		zr, err := gzip.NewReader(bytes.NewReader(b[off : off+n]))
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		zr.Multistream(false)
		body, _ := io.ReadAll(zr)
		if !strings.Contains(string(body), `{"phase":"complete","correlation_id":"`+id+`"}`) {
			t.Fatalf("%s's range doesn't hold its row: %q", id, body)
		}
	}
	var pods int
	if err := db.QueryRow(ctx(t), "SELECT count(*) FROM ingest_pods WHERE incarnation = 'pod-a'").Scan(&pods); err != nil || pods != 1 {
		t.Fatalf("ingest_pods: %d %v", pods, err)
	}
}

func TestLoadingAFileTwiceChangesNothing(t *testing.T) {
	db := fresh(t)
	root := t.TempDir()
	s := process.DirStore{Root: root}
	k := rawKey(time.Now(), "pod-a", 1)
	put(t, root, k, rawFile(t, "f1", 5))
	if _, _, err := List(ctx(t), db, s, ""); err != nil {
		t.Fatal(err)
	}
	loadAll(t, db, s)
	before := snapshot(t, db)
	if _, err := db.Exec(ctx(t), "UPDATE ingest_files SET loaded_at = NULL"); err != nil {
		t.Fatal(err)
	}
	if got := loadAll(t, db, s); len(got) != 1 {
		t.Fatalf("reloaded %v", got)
	}
	if after := snapshot(t, db); after != before {
		t.Fatalf("a second load changed the tables:\n%s\n%s", before, after)
	}
}

// Two workers listing and loading while files keep arriving: every file is
// loaded exactly once, none skipped.
func TestTwoWorkersLoadEveryFileOnce(t *testing.T) {
	db := fresh(t)
	root := t.TempDir()
	s := process.DirStore{Root: root}
	const files = 60
	var mu sync.Mutex
	var want []string
	done := make(chan struct{})
	go func() { // the observers: two pods, a file every few ms
		defer close(done)
		for i := 0; i < files; i++ {
			k := rawKey(time.Now(), fmt.Sprintf("pod-%d", i%2), i)
			put(t, root, k, rawFile(t, fmt.Sprintf("f%d", i), 2))
			mu.Lock()
			want = append(want, k)
			mu.Unlock()
			time.Sleep(3 * time.Millisecond)
		}
	}()
	loaded := map[string]int{}
	var lmu sync.Mutex
	var wg sync.WaitGroup
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stop := false
			for {
				if _, _, err := List(ctx(t), db, s, ""); err != nil {
					t.Error(err)
					return
				}
				for {
					k, err := LoadOne(ctx(t), db, s)
					if err != nil {
						t.Error(err)
						return
					}
					if k == "" {
						break
					}
					lmu.Lock()
					loaded[k]++
					lmu.Unlock()
				}
				if stop {
					return
				}
				select {
				case <-done:
					stop = true // one more pass after the last file
				default:
				}
			}
		}()
	}
	wg.Wait()
	sort.Strings(want)
	if len(loaded) != len(want) {
		t.Fatalf("loaded %d files of %d", len(loaded), len(want))
	}
	for _, k := range want {
		if loaded[k] != 1 {
			t.Fatalf("%s loaded %d times", k, loaded[k])
		}
	}
	var calls int
	if err := db.QueryRow(ctx(t), "SELECT count(*) FROM calls").Scan(&calls); err != nil || calls != 2*files {
		t.Fatalf("calls %d (%v)", calls, err)
	}
}

// Listing goes back five minutes before its cursor (the newest listing), in
// whole hour folders: an hour folder before that isn't listed again.
func TestListGoesBackFiveMinutesBeforeItsCursor(t *testing.T) {
	db := fresh(t)
	root := t.TempDir()
	s := process.DirStore{Root: root}
	cursor := time.Date(2026, 11, 14, 10, 3, 0, 0, time.UTC)
	if _, err := db.Exec(ctx(t), "INSERT INTO ingest_files (key, listed_at, loaded_at) VALUES ('raw/old', $1, $1)", cursor); err != nil {
		t.Fatal(err)
	}
	late := rawKey(time.Date(2026, 11, 14, 9, 59, 0, 0, time.UTC), "pod-a", 1)   // in 09/, within five minutes
	tooOld := rawKey(time.Date(2026, 11, 14, 8, 59, 0, 0, time.UTC), "pod-a", 2) // in 08/
	put(t, root, late, rawFile(t, "late", 1))
	put(t, root, tooOld, rawFile(t, "old", 1))
	if _, _, err := List(ctx(t), db, s, ""); err != nil {
		t.Fatal(err)
	}
	var keys []string
	rows, _ := db.Query(ctx(t), "SELECT key FROM ingest_files WHERE key <> 'raw/old' ORDER BY key")
	for rows.Next() {
		var k string
		_ = rows.Scan(&k)
		keys = append(keys, k)
	}
	if len(keys) != 1 || keys[0] != late {
		t.Fatalf("listed %v, want only %s", keys, late)
	}
}

// One lister at a time: while another holds the lock, List does nothing.
func TestOneListerAtATime(t *testing.T) {
	db := fresh(t)
	s := process.DirStore{Root: t.TempDir()}
	conn, err := db.Acquire(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx(t), "SELECT pg_advisory_lock($1)", ListLock); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := List(ctx(t), db, s, ""); err != nil || ok {
		t.Fatalf("listed while another lister held the lock (%v)", err)
	}
	if _, err := conn.Exec(ctx(t), "SELECT pg_advisory_unlock($1)", ListLock); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := List(ctx(t), db, s, ""); err != nil || !ok {
		t.Fatalf("didn't list once the lock was free (%v)", err)
	}
}

func TestLagIsTheOldestUnloadedFile(t *testing.T) {
	db := fresh(t)
	if lag, err := Lag(ctx(t), db); err != nil || lag != 0 {
		t.Fatalf("empty: %v %v", lag, err)
	}
	if _, err := db.Exec(ctx(t), `INSERT INTO ingest_files (key, listed_at, loaded_at) VALUES
		('a', now() - interval '40 seconds', NULL), ('b', now() - interval '10 seconds', NULL), ('c', now() - interval '1 hour', now())`); err != nil {
		t.Fatal(err)
	}
	lag, err := Lag(ctx(t), db)
	if err != nil || lag < 39*time.Second || lag > 50*time.Second {
		t.Fatalf("lag %v (%v), want about 40 s", lag, err)
	}
}

// snapshot is every fact table's checksum and the calls' raw ranges.
func snapshot(t testing.TB, db *pgxpool.Pool) string {
	t.Helper()
	ss, err := flush.Summarize(ctx(t), db)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, s := range ss {
		fmt.Fprintf(&b, "%s %d %s\n", s.Table, s.Rows, s.Checksum)
	}
	var raw string
	if err := db.QueryRow(ctx(t), `SELECT coalesce(string_agg(call_id || ' ' || raw_key || ' ' || raw_offset || ' ' || raw_len, ',' ORDER BY call_id), '') FROM calls`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return b.String() + raw
}

// A file that can't be loaded doesn't hold up the others: it's passed over
// for the rest of the pass, stays unloaded and holds /lag up.
func TestABadFileDoesNotHoldUpTheOthers(t *testing.T) {
	db := fresh(t)
	root := t.TempDir()
	s := process.DirStore{Root: root}
	bad := rawKey(time.Now(), "pod-a", 1) // sorts first
	good := rawKey(time.Now().Add(time.Second), "pod-a", 2)
	put(t, root, bad, []byte("not gzip"))
	put(t, root, good, rawFile(t, "good", 1))
	if _, _, err := List(ctx(t), db, s, ""); err != nil {
		t.Fatal(err)
	}
	k, err := Load(ctx(t), db, s, nil)
	if k != bad || err == nil {
		t.Fatalf("first: %s %v, want the bad file's error", k, err)
	}
	if k, err = Load(ctx(t), db, s, []string{bad}); k != good || err != nil {
		t.Fatalf("second: %s %v, want the good file", k, err)
	}
	var unloaded int
	if err := db.QueryRow(ctx(t), "SELECT count(*) FROM ingest_files WHERE loaded_at IS NULL").Scan(&unloaded); err != nil || unloaded != 1 {
		t.Fatalf("unloaded %d (%v), want the bad file", unloaded, err)
	}
}

// A refresh that never finishes (a huge prompt, a lock it waits on) must not
// hold up loading: new raw files keep landing in Postgres while refreshers
// are stuck.
func TestASlowRefreshDoesNotHoldUpLoading(t *testing.T) {
	db := fresh(t)
	root := t.TempDir()
	s := process.DirStore{Root: root}
	stuck, entered := make(chan struct{}), make(chan struct{}, 8)
	defer close(stuck)
	drain = func(c context.Context, _ *pgxpool.Pool) (int, error) {
		entered <- struct{}{}
		select {
		case <-stuck:
		case <-c.Done():
		}
		return 0, c.Err()
	}
	defer func() { drain = refresh.Drain }()
	c, cancel := context.WithCancel(ctx(t))
	done := make(chan struct{})
	go func() { Loop(c, db, s, "", 1, 2, nil, 10*time.Millisecond); close(done) }()
	defer func() { cancel(); <-done }()
	<-entered // the refreshers are stuck before the file exists

	k := rawKey(time.Now(), "pod-a", 1)
	put(t, root, k, rawFile(t, "f1", 3))
	deadline := time.Now().Add(10 * time.Second)
	for {
		var loaded bool
		if err := db.QueryRow(ctx(t), "SELECT coalesce(bool_or(loaded_at IS NOT NULL), false) FROM ingest_files WHERE key = $1", k).Scan(&loaded); err != nil {
			t.Fatal(err)
		}
		if loaded {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a file listed while the refreshers were stuck never loaded")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
