package tail_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/keyflag"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/pg/pgtest"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/tail"
)

// The tail (docs/PIPELINE.md §2, Singletons): it LISTs v1/recent/ after its
// cursor − 5 min grace, skipping the grace names its row records, GETs each
// new index object, and in one transaction flags every key named in
// dirty_keys, upserts each pod's newest upload time into ingest_pods, and
// advances its cursor. The cursor moves only together with its flags, so an
// outage or a crash makes it read again, never skip.

const prefix = "p"

type env struct {
	t     *testing.T
	ctx   context.Context
	dsn   string
	pool  *pgxpool.Pool
	store *stream.MemStore
	seq   int64
}

func newEnv(t *testing.T) *env {
	t.Helper()
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	dsn := pgtest.Fresh(t)
	pool, err := pg.Open(ctx, dsn, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := pg.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return &env{t: t, ctx: ctx, dsn: dsn, pool: pool, store: stream.NewMemStore(stream.RealClock{})}
}

func (e *env) tail(mod ...func(*tail.Config)) *tail.Tail {
	cfg := tail.Config{DB: e.pool, Store: e.store, Prefix: prefix}
	for _, m := range mod {
		m(&cfg)
	}
	return tail.New(cfg)
}

// index puts one pod's index object, named by S3 time at, naming keys.
func (e *env) index(at time.Time, inc string, lastNS int64, keys ...string) string {
	e.t.Helper()
	e.seq++
	name := events.ResolveUploadKey(keyflag.Template(prefix, inc, e.seq), at)
	body, _ := json.Marshal(keyflag.Object{Incarnation: inc, Keys: keys, LastNS: lastNS})
	if err := e.store.Put(e.ctx, name, body); err != nil {
		e.t.Fatal(err)
	}
	return name
}

func (e *env) pass(tl *tail.Tail) tail.Result {
	e.t.Helper()
	r, err := tl.Pass(e.ctx)
	if err != nil {
		e.t.Fatalf("pass: %v", err)
	}
	return r
}

type flag struct {
	seq   int64
	since time.Time
}

func (e *env) flags() map[string]flag {
	e.t.Helper()
	rows, err := e.pool.Query(e.ctx, `SELECT key, seq, since FROM dirty_keys`)
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]flag{}
	for rows.Next() {
		var k string
		var f flag
		if err := rows.Scan(&k, &f.seq, &f.since); err != nil {
			e.t.Fatal(err)
		}
		out[k] = f
	}
	if rows.Err() != nil {
		e.t.Fatal(rows.Err())
	}
	return out
}

func (e *env) keys() []string {
	var out []string
	for k := range e.flags() {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type pod struct {
	lastNS int64
	at     time.Time
}

func (e *env) pods() map[string]pod {
	e.t.Helper()
	rows, err := e.pool.Query(e.ctx, `SELECT incarnation, last_ns, at FROM ingest_pods`)
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]pod{}
	for rows.Next() {
		var inc string
		var p pod
		if err := rows.Scan(&inc, &p.lastNS, &p.at); err != nil {
			e.t.Fatal(err)
		}
		out[inc] = p
	}
	return out
}

func (e *env) cursor() (string, []string, *time.Time) {
	e.t.Helper()
	var last string
	var grace []string
	var checked *time.Time
	if err := e.pool.QueryRow(e.ctx, `SELECT last_name, grace, checked_at FROM tail`).Scan(&last, &grace, &checked); err != nil {
		e.t.Fatal(err)
	}
	return last, grace, checked
}

func base(name string) string { return name[strings.LastIndexByte(name, '/')+1:] }

func same(a []string, b ...string) bool {
	a, b = append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(a)
	sort.Strings(b)
	return fmt.Sprint(a) == fmt.Sprint(b)
}

var now = time.Now().UTC().Truncate(time.Second)

// Normal flow: every key named is flagged once, with since the index object's
// S3 time; each pod's newest upload time lands in ingest_pods; the cursor is
// the newest name, with the names inside the grace window recorded.
func TestTailFlagsEveryKeyNamed(t *testing.T) {
	e := newEnv(t)
	tl := e.tail()
	if _, _, checked := e.cursor(); checked != nil {
		t.Fatal("checked_at set before any pass")
	}
	t1, t2 := now.Add(-20*time.Second), now.Add(-10*time.Second)
	n1 := e.index(t1, "pod-a", t1.UnixNano(), "s:1", "s:2")
	n2 := e.index(t2, "pod-b", t2.UnixNano(), "s:2", "c:k:2027-03-01")
	r := e.pass(tl)
	if r.Objects != 2 || r.Keys != 3 {
		t.Fatalf("pass %+v", r)
	}
	f := e.flags()
	if !same(e.keys(), "s:1", "s:2", "c:k:2027-03-01") {
		t.Fatalf("flags %v", f)
	}
	for k, want := range map[string]time.Time{"s:1": t1, "s:2": t1, "c:k:2027-03-01": t2} {
		if f[k].seq != 1 || !f[k].since.Equal(want) {
			t.Errorf("%s: %+v, want seq 1 since %v", k, f[k], want)
		}
	}
	p := e.pods()
	if len(p) != 2 || p["pod-a"].lastNS != t1.UnixNano() || !p["pod-a"].at.Equal(t1) || p["pod-b"].lastNS != t2.UnixNano() {
		t.Fatalf("ingest_pods %+v", p)
	}
	last, grace, checked := e.cursor()
	if last != base(n2) || !same(grace, base(n1), base(n2)) || checked == nil {
		t.Fatalf("cursor %q %v %v", last, grace, checked)
	}
	// Nothing new: no flag moves, nothing is read again, and checked_at advances.
	e.store.ResetCounters()
	if r := e.pass(tl); r.Objects != 0 {
		t.Fatalf("second pass %+v", r)
	}
	if c := e.store.Cost(); c.Requests != 1 {
		t.Fatalf("an idle pass made %d store requests, want 1 LIST", c.Requests)
	}
	if f2 := e.flags(); f2["s:1"].seq != 1 {
		t.Fatalf("an idle pass moved a flag: %+v", f2)
	}
	if _, _, c2 := e.cursor(); !c2.After(*checked) {
		t.Fatalf("checked_at %v didn't advance from %v", c2, checked)
	}
}

// Duplicate index objects (a pod's start re-uploading its journal, or one key
// in many objects): each pass flags a key once; a key already flagged gets
// seq + 1 and keeps its older since.
func TestDuplicateIndexObjects(t *testing.T) {
	e := newEnv(t)
	tl := e.tail()
	t1 := now.Add(-time.Minute)
	e.index(t1, "pod-a", 0, "s:1")
	e.index(t1, "pod-a", 0, "s:1")
	e.index(t1.Add(time.Second), "pod-b", 0, "s:1", "s:1")
	e.pass(tl)
	if f := e.flags(); len(f) != 1 || f["s:1"].seq != 1 || !f["s:1"].since.Equal(t1) {
		t.Fatalf("flags %+v", f)
	}
	if p := e.pods(); len(p) != 0 {
		t.Fatalf("index objects with no upload time wrote ingest_pods %+v", p)
	}
	e.index(now, "pod-a", 0, "s:1")
	e.pass(tl)
	if f := e.flags(); f["s:1"].seq != 2 || !f["s:1"].since.Equal(t1) {
		t.Fatalf("flags %+v, want seq 2 since %v", f, t1)
	}
}

// ingest_pods holds each pod's newest upload time, never moving back, from
// objects read in one pass or many.
func TestIngestPodsNewestTime(t *testing.T) {
	e := newEnv(t)
	tl := e.tail()
	t1, t2 := now.Add(-30*time.Second), now.Add(-20*time.Second)
	e.index(t1, "pod-a", 300, "s:1")
	e.index(t2, "pod-a", 200, "s:1")
	e.pass(tl)
	if p := e.pods()["pod-a"]; p.lastNS != 300 || !p.at.Equal(t2) {
		t.Fatalf("pod-a %+v, want last 300 at %v", p, t2)
	}
	e.index(now, "pod-a", 100, "s:2")
	e.pass(tl)
	if p := e.pods()["pod-a"]; p.lastNS != 300 || !p.at.Equal(now) {
		t.Fatalf("pod-a %+v, want last 300 at %v", p, now)
	}
}

// An index object that becomes visible late, inside the grace window (named
// by an S3 time before the cursor), is flagged on the next pass; the names
// already read aren't read again. One later than the grace is the documented
// gap (the late-file rule of the keys' cursor).
func TestLateObjectWithinGrace(t *testing.T) {
	e := newEnv(t)
	tl := e.tail()
	e.index(now, "pod-a", 0, "s:on-time")
	e.pass(tl)
	e.index(now.Add(-4*time.Minute), "pod-b", 0, "s:late")
	e.index(now.Add(-6*time.Minute), "pod-c", 0, "s:too-late")
	e.store.ResetCounters()
	r := e.pass(tl)
	if r.Objects != 1 || !same(e.keys(), "s:on-time", "s:late") {
		t.Fatalf("pass %+v, flags %v", r, e.keys())
	}
	if c := e.store.Cost(); c.Requests != 2 {
		t.Fatalf("%d store requests, want 1 LIST + 1 GET (the late object only)", c.Requests)
	}
	if f := e.flags(); f["s:on-time"].seq != 1 {
		t.Fatalf("an object read again: %+v", f)
	}
	last, grace, _ := e.cursor()
	if !strings.HasPrefix(last, now.Format(keyflag.TimeFormat)) || len(grace) != 2 {
		t.Fatalf("cursor %q, grace %v", last, grace)
	}
	// The grace window slides: names older than (cursor − 5 min) leave the row.
	e.index(now.Add(5*time.Minute), "pod-a", 0, "s:later")
	e.pass(tl)
	if _, grace, _ := e.cursor(); len(grace) != 2 {
		t.Fatalf("grace %v, want the on-time and the newest names", grace)
	}
}

// Postgres down: passes fail, the cursor holds, and once it's back nothing is
// skipped. Also a connection dropped just before COMMIT: the flags and the
// cursor roll back together.
func TestPostgresDownCursorHolds(t *testing.T) {
	e := newEnv(t)
	e.index(now.Add(-time.Minute), "pod-a", 1, "s:1")
	e.pass(e.tail())
	e.index(now.Add(-30*time.Second), "pod-a", 2, "s:2")

	// Unreachable: nothing listens on the port.
	down, err := pgxpool.New(e.ctx, "postgres://postgres@127.0.0.1:1/x?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	defer down.Close()
	dt := e.tail(func(c *tail.Config) { c.DB = down })
	for i := 0; i < 3; i++ {
		if _, err := dt.Pass(e.ctx); err == nil {
			t.Fatal("a pass succeeded with Postgres unreachable")
		}
	}

	// The connection dies just before COMMIT.
	kill := func(step string) error {
		if step != "commit" {
			return nil
		}
		admin, err := pgx.Connect(e.ctx, e.dsn)
		if err != nil {
			return err
		}
		defer admin.Close(e.ctx)
		_, err = admin.Exec(e.ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity
			WHERE datname = current_database() AND pid <> pg_backend_pid()`)
		return err
	}
	if _, err := e.tail(func(c *tail.Config) { c.Hook = kill }).Pass(e.ctx); err == nil {
		t.Fatal("a pass whose connection died before COMMIT succeeded")
	}
	e.pool.Reset() // the pool's other connections died too
	if !same(e.keys(), "s:1") {
		t.Fatalf("flags %v after the failures, want only s:1", e.keys())
	}
	if p := e.pods()["pod-a"]; p.lastNS != 1 {
		t.Fatalf("ingest_pods moved without its cursor: %+v", p)
	}

	// Back up: the next pass flags what the outage held back.
	if r := e.pass(e.tail()); r.Objects != 1 || !same(e.keys(), "s:1", "s:2") {
		t.Fatalf("pass %+v, flags %v", r, e.keys())
	}
	if p := e.pods()["pod-a"]; p.lastNS != 2 {
		t.Fatalf("ingest_pods %+v", p)
	}
}

// failingStore fails its reads while down.
type failingStore struct {
	tail.Store
	down atomic.Bool
}

func (f *failingStore) Get(ctx context.Context, key string) ([]byte, error) {
	if f.down.Load() {
		return nil, errors.New("s3 unavailable")
	}
	return f.Store.Get(ctx, key)
}

// S3 failing a GET: the pass writes nothing, and the next one reads it all.
func TestStoreErrorCursorHolds(t *testing.T) {
	e := newEnv(t)
	fs := &failingStore{Store: e.store}
	tl := e.tail(func(c *tail.Config) { c.Store = fs })
	e.index(now, "pod-a", 0, "s:1")
	fs.down.Store(true)
	if _, err := tl.Pass(e.ctx); err == nil {
		t.Fatal("a pass succeeded with S3 failing")
	}
	if last, _, _ := e.cursor(); last != "" || len(e.flags()) != 0 {
		t.Fatalf("cursor %q, flags %v", last, e.flags())
	}
	fs.down.Store(false)
	e.pass(tl)
	if !same(e.keys(), "s:1") {
		t.Fatalf("flags %v", e.keys())
	}
}

// An object that isn't an index (garbage) or is gone by the GET is skipped,
// never wedging the tail.
func TestBadObjectsAreSkipped(t *testing.T) {
	e := newEnv(t)
	tl := e.tail()
	bad := events.ResolveUploadKey(keyflag.Template(prefix, "pod-x", 1), now.Add(-time.Second))
	_ = e.store.Put(e.ctx, bad, []byte("not json"))
	_ = e.store.Put(e.ctx, keyflag.Prefix(prefix)+"README", []byte("not an index object"))
	e.index(now, "pod-a", 0, "s:1")
	r := e.pass(tl)
	if r.Objects != 2 || r.Skipped != 1 || !same(e.keys(), "s:1") {
		t.Fatalf("pass %+v, flags %v", r, e.keys())
	}
}

// A long backlog (after an outage) drains in passes of at most MaxObjects, in
// name order, the cursor moving with each.
func TestBacklogDrainsInBoundedPasses(t *testing.T) {
	e := newEnv(t)
	tl := e.tail(func(c *tail.Config) { c.MaxObjects = 2 })
	for i := 0; i < 5; i++ {
		e.index(now.Add(time.Duration(i-5)*time.Second), "pod-a", 0, fmt.Sprintf("s:%d", i))
	}
	var sizes []int
	for i := 0; i < 4; i++ {
		sizes = append(sizes, e.pass(tl).Objects)
	}
	if fmt.Sprint(sizes) != "[2 2 1 0]" || len(e.flags()) != 5 {
		t.Fatalf("passes %v, flags %v", sizes, e.keys())
	}
}

// Two tails at once (the singleton's lock lost): the guarded cursor lets one
// pass commit; the other rolls back and the next pass reads from the new
// cursor. Every key is flagged.
func TestTwoTailsRaceOnTheCursor(t *testing.T) {
	e := newEnv(t)
	var a, b *tail.Tail
	var once sync.Once
	a = e.tail(func(c *tail.Config) {
		c.Hook = func(step string) error {
			if step == "begin" {
				once.Do(func() { // b commits first, after a read the cursor
					if _, err := b.Pass(e.ctx); err != nil {
						t.Errorf("b: %v", err)
					}
				})
			}
			return nil
		}
	})
	b = e.tail()
	e.index(now.Add(-time.Second), "pod-a", 0, "s:1")
	if _, err := a.Pass(e.ctx); !errors.Is(err, tail.ErrCursorMoved) {
		t.Fatalf("a's pass: %v, want ErrCursorMoved", err)
	}
	if f := e.flags(); f["s:1"].seq != 1 {
		t.Fatalf("flags %+v", f)
	}
	e.index(now, "pod-a", 0, "s:2")
	e.pass(a)
	if f := e.flags(); !same(e.keys(), "s:1", "s:2") || f["s:1"].seq != 1 {
		t.Fatalf("flags %+v", f)
	}
}

// A pod crash, end to end: a file lands, the pod dies before its index goes
// out, and at start it uploads the journal's leftovers; the tail flags the key.
func TestPodCrashIsFlaggedFromTheJournal(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(t.TempDir(), "recent")
	put := func(tmpl string, body []byte) error {
		return e.store.Put(e.ctx, events.ResolveUploadKey(tmpl, now), body)
	}
	x, err := keyflag.Open(keyflag.Config{Dir: dir, Prefix: prefix, Incarnation: "pod-1", Put: put, SkipFsync: true})
	if err != nil {
		t.Fatal(err)
	}
	tmpl := keyflag.InTemplate(prefix, "s:crash", "pod-1", 1)
	if err := x.Journal("s:crash", tmpl); err != nil {
		t.Fatal(err)
	}
	x.Landed("s3", events.ResolveUploadKey(tmpl, now))
	// crash: x is never flushed or closed
	tl := e.tail()
	e.pass(tl)
	if len(e.flags()) != 0 {
		t.Fatal("flagged before any index object")
	}
	y, err := keyflag.Open(keyflag.Config{Dir: dir, Prefix: prefix, Incarnation: "pod-2", Put: put, SkipFsync: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := y.Flush(); err != nil {
		t.Fatal(err)
	}
	e.pass(tl)
	if !same(e.keys(), "s:crash") {
		t.Fatalf("flags %v", e.keys())
	}
}

// /lag's tail part: now() − checked_at, on Postgres's clock; 0 before the
// first pass.
func TestAge(t *testing.T) {
	e := newEnv(t)
	if a, err := tail.Age(e.ctx, e.pool); err != nil || a != 0 {
		t.Fatalf("age before any pass %v (%v)", a, err)
	}
	e.pass(e.tail())
	if a, err := tail.Age(e.ctx, e.pool); err != nil || a < 0 || a > 5*time.Second {
		t.Fatalf("age after a pass %v (%v)", a, err)
	}
	if _, err := e.pool.Exec(e.ctx, `UPDATE tail SET checked_at = now() - interval '90 seconds'`); err != nil {
		t.Fatal(err)
	}
	if a, err := tail.Age(e.ctx, e.pool); err != nil || a < 89*time.Second || a > 95*time.Second {
		t.Fatalf("a stuck tail's age %v (%v), want about 90 s", a, err)
	}
}

// The singleton: only the holder of the tail's advisory lock runs passes;
// when it lets go, another worker takes over.
func TestRunLockedIsASingleton(t *testing.T) {
	e := newEnv(t)
	e.index(now, "pod-a", 0, "s:1")
	holder, err := e.pool.Acquire(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(e.ctx, `SELECT pg_advisory_lock($1)`, tail.LockID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		tail.RunLocked(ctx, e.pool, e.tail(func(c *tail.Config) { c.Every = 10 * time.Millisecond }), 10*time.Millisecond)
	}()
	time.Sleep(200 * time.Millisecond)
	if len(e.flags()) != 0 {
		t.Fatal("a pass ran without the lock")
	}
	if _, err := holder.Exec(e.ctx, `SELECT pg_advisory_unlock($1)`, tail.LockID); err != nil {
		t.Fatal(err)
	}
	holder.Release()
	for deadline := time.Now().Add(5 * time.Second); len(e.flags()) == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the lock's new holder never ran a pass")
		}
	}
	cancel()
	<-done
}

var _ tail.Store = (*stream.MemStore)(nil)
