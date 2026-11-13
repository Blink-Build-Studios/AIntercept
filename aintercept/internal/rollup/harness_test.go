package rollup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream"
)

const prefix = "p"

var errCrash = errors.New("crash")

// env is one test's world: an in-memory S3 with S3's semantics, a Redis, a
// clock, and every record ingested so far (the reference's input).
type env struct {
	t     testing.TB
	ctx   context.Context
	clock *stream.FakeClock
	store *stream.MemStore
	mr    *miniredis.Miniredis
	rdb   *redis.Client
	model stream.Model
	all   []records.Record
	seq   int64
	n     int64
}

func newEnv(t testing.TB) *env {
	t.Helper()
	clock := stream.NewFakeClock(time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC))
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return &env{t: t, ctx: context.Background(), clock: clock, store: stream.NewMemStore(clock), mr: mr, rdb: rdb, model: stream.CountModel{}}
}

func (e *env) cfg(name string) Config {
	return Config{Store: e.store, Redis: e.rdb, Prefix: prefix, Replica: name, Model: e.model, Clock: e.clock, Parallel: 2}
}

func (e *env) worker(name string) *Worker { return New(e.cfg(name)) }

func (e *env) workerWith(c Config) *Worker { return New(c) }

// sessionKey is a session's routing key.
func sessionKey(sid string) string {
	r := records.Record{Kind: records.KindCallEnd, Call: &canon.Request{SessionID: sid}}
	return records.RoutingKey(&r)
}

// rec is one call record of session sid, at time ts.
func (e *env) rec(sid string, ts int64) records.Record {
	e.n++
	r := records.Record{Kind: records.KindCallEnd, Version: records.Version, RecordID: fmt.Sprintf("inc-%020d", e.n),
		Call: &canon.Request{CallID: fmt.Sprintf("c%d", e.n), SessionID: sid, EndNS: ts}}
	r.RoutingKey = records.RoutingKey(&r)
	return r
}

// ingestAt writes one ingest file of n records for session sid, named at time
// at, as step 1 does: the in/ file, the dirty flag and the recent index. It
// returns the file's name (relative to in/).
func (e *env) ingestAt(sid string, n int, at time.Time) string {
	e.t.Helper()
	key := sessionKey(sid)
	rs := make([]records.Record, n)
	for i := range rs {
		rs[i] = e.rec(sid, at.UnixNano()+int64(i))
	}
	b, err := records.Encode(rs)
	if err != nil {
		e.t.Fatal(err)
	}
	e.seq++
	name := InName(at, "inc", e.seq)
	if err := e.store.Put(e.ctx, InPrefix(prefix, key)+name, b); err != nil {
		e.t.Fatal(err)
	}
	e.all = append(e.all, rs...)
	keys, _ := json.Marshal([]string{key})
	if err := e.store.Put(e.ctx, RecentName(prefix, at, "inc", e.seq), keys); err != nil {
		e.t.Fatal(err)
	}
	e.rdb.SAdd(e.ctx, "dirty", key)
	return name
}

func (e *env) ingest(sid string, n int) string { return e.ingestAt(sid, n, e.clock.Now()) }

// check fails unless every key's committed tables equal one pass over every
// record ingested for it.
func (e *env) check() {
	e.t.Helper()
	by := map[string][]records.Record{}
	for _, r := range e.all {
		by[r.RoutingKey] = append(by[r.RoutingKey], r)
	}
	keys := make([]string, 0, len(by))
	for k := range by {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		want := stream.Reference(e.model, by[key])
		got, err := ReadTables(e.ctx, e.store, prefix, key)
		if err != nil {
			e.t.Fatalf("%s: %v", key, err)
		}
		if !reflect.DeepEqual(norm(got), norm(want)) {
			e.t.Fatalf("%s: tables differ\n got %v\nwant %v", key, got, want)
		}
	}
}

func norm(m map[string][]stream.Row) map[string][]stream.Row {
	out := map[string][]stream.Row{}
	for t, rows := range m {
		if len(rows) > 0 {
			out[t] = stream.MergeRows(rows)
		}
	}
	return out
}

// drain runs w until no key is dirty (bounded).
func (e *env) drain(ws ...*Worker) {
	e.t.Helper()
	for i := 0; i < 200; i++ {
		n, err := e.rdb.SCard(e.ctx, "dirty").Result()
		if err != nil {
			e.t.Fatal(err)
		}
		if n == 0 {
			return
		}
		for _, w := range ws {
			if err := w.Tick(e.ctx); err != nil && !errors.Is(err, stream.ErrInjected) {
				e.t.Fatal(err)
			}
		}
		e.clock.Advance(time.Second)
	}
	e.t.Fatal("drain: keys stayed dirty")
}

func (e *env) committedDirs(key string) map[string]bool {
	c, err := ReadNewest(e.ctx, e.store, prefix, key)
	if err != nil || c == nil {
		e.t.Fatalf("no commit for %s: %v", key, err)
	}
	out := map[string]bool{}
	for _, o := range c.Out {
		out[o.Dir] = true
	}
	return out
}

// outDirs are every out/ directory under key in the store.
func (e *env) outDirs(key string) map[string]bool {
	infos, err := e.store.ListInfo(e.ctx, KeyDir(prefix, key)+"out/")
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]bool{}
	for _, in := range infos {
		rel := strings.TrimPrefix(in.Key, KeyDir(prefix, key))
		out[rel[:strings.Index(rel[len("out/"):], "/")+len("out/")+1]] = true
	}
	return out
}

func crashAt(step string) func(string, string) error {
	return func(s, _ string) error {
		if s == step {
			return errCrash
		}
		return nil
	}
}

// ingestNewest is ingest with the file's newest raw row time set.
func (e *env) ingestNewest(sid string, n int, newest int64) string {
	e.t.Helper()
	key := sessionKey(sid)
	at := e.clock.Now()
	rs := make([]records.Record, n)
	for i := range rs {
		rs[i] = e.rec(sid, at.UnixNano()+int64(i))
	}
	b, err := records.EncodeWithNewest(rs, newest)
	if err != nil {
		e.t.Fatal(err)
	}
	e.seq++
	name := InName(at, "inc", e.seq)
	if err := e.store.Put(e.ctx, InPrefix(prefix, key)+name, b); err != nil {
		e.t.Fatal(err)
	}
	e.all = append(e.all, rs...)
	e.rdb.SAdd(e.ctx, "dirty", key)
	return name
}
