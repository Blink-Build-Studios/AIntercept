package rollup

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream/state"
)

// Expiry through the rollup (docs/PIPELINE.md §2, §3): a key's run expires its
// calls by the newest merge commit's newest_ns (global data time), and the
// merge, which keeps each key's earliest deadline in merge_keys (S3), flags a
// key whose deadline it has passed, so a quiet key is revisited.

const ttl = 6 * time.Hour

// ccCall is a Claude Code call of session sid starting at data time start.
func ccCall(id, sid string, start int64) *canon.Request {
	return &canon.Request{CallID: id, CorrelationID: id, StartNS: start, Host: "api.anthropic.com", Method: "POST",
		Path: "/v1/messages", SessionID: sid, KeyHash: "k1", Product: canon.ProductClaudeCode}
}

func (e *env) callRec(kind string, c *canon.Request) records.Record {
	e.n++
	x := *c
	r := records.Record{Kind: kind, Version: records.Version, RecordID: fmt.Sprintf("inc-%020d", e.n), CallID: c.CallID, Call: &x}
	r.RoutingKey = records.RoutingKey(&r)
	return r
}

func (e *env) start(c *canon.Request) records.Record { return e.callRec(records.KindCallStart, c) }

func (e *env) end(c *canon.Request, endNS int64) records.Record {
	x := *c
	x.EndNS, x.Status = endNS, 200
	return e.callRec(records.KindCallEnd, &x)
}

// putFile ingests rs as one file of key sid's, named at time at, carrying
// newest (its pod's newest raw row) as ingest does.
func (e *env) putFile(sid string, rs []records.Record, newest int64, at time.Time) {
	e.t.Helper()
	key := sessionKey(sid)
	b, err := records.EncodeWithNewest(rs, newest)
	if err != nil {
		e.t.Fatal(err)
	}
	e.seq++
	if err := e.store.Put(e.ctx, InPrefix(prefix, key)+InName(at, "inc", e.seq), b); err != nil {
		e.t.Fatal(err)
	}
	keys, _ := json.Marshal([]string{key})
	if err := e.store.Put(e.ctx, RecentName(prefix, at, "inc", e.seq), keys); err != nil {
		e.t.Fatal(err)
	}
	e.all = append(e.all, rs...)
	e.rdb.SAdd(e.ctx, "dirty", key)
}

// checkAt fails unless every key's tables equal one pass over its records,
// then its calls expired at clock.
func (e *env) checkAt(clock int64) {
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
		want := stream.ReferenceAt(e.model, by[key], clock)
		got, err := ReadTables(e.ctx, e.store, prefix, key)
		if err != nil {
			e.t.Fatalf("%s: %v", key, err)
		}
		if !reflect.DeepEqual(norm(got), norm(want)) {
			e.t.Fatalf("%s: tables differ from one pass expired at %d\n got %v\nwant %v", key, clock, got, want)
		}
	}
}

func (e *env) newest(key string) *Commit {
	e.t.Helper()
	c, err := ReadNewest(e.ctx, e.store, prefix, key)
	if err != nil || c == nil {
		e.t.Fatalf("no commit for %s: %v", key, err)
	}
	return c
}

func (e *env) merge(w *Worker) {
	e.t.Helper()
	if _, ran, err := w.MergeTick(e.ctx); err != nil || !ran {
		e.t.Fatalf("merge: ran %v, %v", ran, err)
	}
}

func (e *env) dirty(key string) bool {
	in, _ := e.rdb.SIsMember(e.ctx, "dirty", key).Result()
	return in
}

func expiredRow(e *env, key, id string) bool {
	tables, err := ReadTables(e.ctx, e.store, prefix, key)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, r := range tables[state.TableFactRequests] {
		if r.ID == id && !r.Tomb {
			return true
		}
	}
	return false
}

// A call with no ending is written once the merge's newest data passes its
// start + 6 h, not before; the commit says when its key's next call expires.
func TestRunExpiresByTheMergesClock(t *testing.T) {
	e := newEnv(t)
	e.model = stream.StateModel{}
	w := e.worker("w1")
	t0 := e.clock.Now().UnixNano()
	a := sessionKey("a")

	e.putFile("a", []records.Record{e.start(ccCall("c1", "a", t0))}, t0, e.clock.Now())
	if _, _, err := w.RunKey(e.ctx, a); err != nil {
		t.Fatal(err)
	}
	if got := e.newest(a).ExpiresNS; got != t0+int64(ttl) {
		t.Fatalf("commit expires_ns %d, want start + 6 h %d", got, t0+int64(ttl))
	}
	// The newest data anywhere reaches exactly start + 6 h: not yet.
	e.clock.Advance(time.Second)
	e.putFile("b", []records.Record{e.start(ccCall("c2", "b", t0+int64(ttl)))}, t0+int64(ttl), e.clock.Now())
	e.drain(w)
	e.merge(w)
	if e.dirty(a) {
		t.Fatal("the merge flagged a key with nothing due")
	}
	if _, ok, err := w.RunKey(e.ctx, a); err != nil || ok {
		t.Fatalf("a run with nothing due committed (%v, %v)", ok, err)
	}
	// One nanosecond past it: the merge flags the key, and its run writes the call.
	e.clock.Advance(time.Second)
	e.putFile("b", []records.Record{e.start(ccCall("c3", "b", t0+int64(ttl)+1))}, t0+int64(ttl)+1, e.clock.Now())
	e.drain(w)
	e.merge(w)
	if !e.dirty(a) {
		t.Fatal("the merge didn't flag the key whose call is due")
	}
	e.drain(w)
	if !expiredRow(e, a, "c1") {
		t.Fatal("the call wasn't written")
	}
	if got := e.newest(a).ExpiresNS; got != 0 {
		t.Fatalf("commit expires_ns %d after the call expired, want 0", got)
	}
	e.merge(w)
	if e.dirty(a) {
		t.Fatal("the merge flagged the key again after its call expired")
	}
	e.checkAt(t0 + int64(ttl) + 1)
}

// A key that gets no more data is still revisited: its deadline is in the
// merge's own commit (merge_keys), so a flag Redis loses is set again by the
// next merge.
func TestMergeRevisitsAQuietKey(t *testing.T) {
	e := newEnv(t)
	e.model = stream.StateModel{}
	w := e.worker("w1")
	t0 := e.clock.Now().UnixNano()
	quiet := sessionKey("quiet")
	e.putFile("quiet", []records.Record{e.start(ccCall("c1", "quiet", t0))}, t0, e.clock.Now())
	e.drain(w)
	e.merge(w)
	// Only another session gets data, past the quiet key's deadline.
	busy := func(id string, ts int64) {
		e.clock.Advance(time.Minute)
		e.putFile("busy", []records.Record{e.start(ccCall(id, "busy", ts))}, ts, e.clock.Now())
		e.drain(w)
	}
	busy("b1", t0+7*int64(time.Hour))
	e.merge(w)
	if !e.dirty(quiet) {
		t.Fatal("the merge didn't flag the quiet key")
	}
	e.mr.FlushAll() // Redis loses the flag (and everything else)
	busy("b2", t0+7*int64(time.Hour)+1)
	if expiredRow(e, quiet, "c1") {
		t.Fatal("expired with no flag")
	}
	e.merge(w)
	if !e.dirty(quiet) {
		t.Fatal("the next merge didn't flag the quiet key again")
	}
	e.drain(w)
	if !expiredRow(e, quiet, "c1") {
		t.Fatal("the quiet key's call never expired")
	}
	e.checkAt(t0 + 7*int64(time.Hour) + 1)
}

// The merge commit keeps each key's deadline: a key's row in merge_keys.
func TestMergeKeepsEachKeysDeadline(t *testing.T) {
	e := newEnv(t)
	e.model = stream.StateModel{}
	w := e.worker("w1")
	t0 := e.clock.Now().UnixNano()
	e.putFile("a", []records.Record{e.start(ccCall("c1", "a", t0))}, t0, e.clock.Now())
	e.drain(w)
	e.merge(w)
	mc, err := ReadNewestMerge(e.ctx, e.store, prefix)
	if err != nil || mc == nil {
		t.Fatalf("no merge commit: %v", err)
	}
	rows, err := readTableFile(e.ctx, e.store, MergeDir(prefix)+TableFile(mc.Index.Dir, TableMergeKeys), TableMergeKeys)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Value.(MergeKey).ExpiresNS != t0+int64(ttl) {
		t.Fatalf("merge_keys %+v, want the key's deadline %d", rows, t0+int64(ttl))
	}
}

// --- generated: chaos ------------------------------------------------------

// Sessions whose calls start, end in time, end hours late or never, as data
// time moves on over two days; files named up to 4 min late; workers crash,
// locks expire, Redis is wiped, S3 fails, merges run now and then. Once it
// settles, every key equals one pass over its records with its calls expired
// at the newest data time.
func TestChaosExpiry(t *testing.T) {
	for seed := int64(1); seed <= 2; seed++ {
		e := newEnv(t)
		e.model = stream.StateModel{}
		rng := rand.New(rand.NewSource(seed))
		ws := []*Worker{e.worker("w0"), e.worker("w1"), e.worker("w2")}
		sids := []string{"a", "b", "c", "d"}
		open := map[string][]*canon.Request{}
		data, final := e.clock.Now().UnixNano(), int64(0)
		e.store.FailRate(0.05, rng)
		for i := 0; i < 80; i++ {
			switch x := rng.Intn(20); {
			case x < 8:
				data += int64(rng.Intn(150)) * int64(time.Minute)
				sid := sids[rng.Intn(len(sids))]
				if rng.Intn(4) == 0 { // the session of the oldest call still open: likely expired by now
					for _, x := range sids {
						if len(open[x]) > 0 && (len(open[sid]) == 0 || open[x][0].StartNS < open[sid][0].StartNS) {
							sid = x
						}
					}
				}
				var rs []records.Record
				newest := data
				if rng.Intn(3) > 0 {
					c := ccCall(fmt.Sprintf("c%d", i), sid, data)
					rs = append(rs, e.start(c))
					open[sid] = append(open[sid], c)
				}
				if n := len(open[sid]); n > 0 && rng.Intn(2) == 0 {
					k := 0 // often its oldest call, likely expired by now
					if rng.Intn(2) == 0 {
						k = rng.Intn(n)
					}
					c := open[sid][k]
					open[sid] = append(open[sid][:k], open[sid][k+1:]...)
					end := data // ended now (maybe hours after its start)
					if rng.Intn(3) == 0 {
						end = c.StartNS + int64(rng.Intn(5*60))*int64(time.Minute) // in time, arriving late
					}
					rs = append(rs, e.end(c, end))
					newest = max(newest, end)
				}
				if len(rs) > 0 {
					late := time.Duration(rng.Intn(240)) * time.Second
					e.store.FailRate(0, nil)
					e.putFile(sid, rs, newest, e.clock.Now().Add(-late))
					e.store.FailRate(0.05, rng)
					final = max(final, newest)
				}
			case x < 12:
				_ = ws[rng.Intn(len(ws))].Tick(e.ctx)
			case x < 15:
				_, _, _ = ws[rng.Intn(len(ws))].MergeTick(e.ctx)
			case x < 16:
				k := rng.Intn(len(ws))
				ws[k] = e.worker(ws[k].cfg.Replica) // crashed: memory gone, locks left to expire
			case x < 17:
				e.mr.FlushAll()
			case x < 18:
				_ = ws[rng.Intn(len(ws))].Sweep(e.ctx)
			default:
				d := time.Duration(rng.Intn(90)) * time.Second
				e.clock.Advance(d)
				e.mr.FastForward(d)
			}
		}
		e.store.FailRate(0, nil)
		e.clock.Advance(2 * time.Minute)
		e.mr.FastForward(2 * time.Minute)
		if err := ws[0].Sweep(e.ctx); err != nil {
			t.Fatal(err)
		}
		e.settle(ws)
		e.checkAt(final)
	}
}

// settle drains the keys and merges until a merge flags nothing more.
func (e *env) settle(ws []*Worker) {
	e.t.Helper()
	for i := 0; i < 20; i++ {
		e.drain(ws...)
		if _, _, err := ws[0].MergeTick(e.ctx); err != nil && !errors.Is(err, ErrMergeFenced) {
			e.t.Fatal(err)
		}
		dirty, _ := e.rdb.SCard(e.ctx, "dirty").Result()
		committed, _ := e.rdb.SCard(e.ctx, "committed").Result()
		if dirty == 0 && committed == 0 {
			return
		}
		e.clock.Advance(time.Second)
	}
	e.t.Fatal("settle: keys kept being flagged")
}
