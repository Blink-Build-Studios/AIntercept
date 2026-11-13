package worker

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// Calls that never end (docs/PIPELINE.md §2, #39): a run expires its key's
// calls by data time, max(keys.newest_row_ns), read once per tick; the due
// calls singleton flags a key whose deadline (keys.expires_ns) data time has
// passed, so a quiet key is revisited. (v5's expire_test.go, ported.)

const ttl = 6 * time.Hour

// expired is whether a call is written (requests holds ended calls only).
func (e *env) expired(key, callID string) bool {
	e.t.Helper()
	var n int
	if err := e.db.QueryRow(e.ctx, "SELECT count(*) FROM requests WHERE key = $1 AND call_id = $2", key, callID).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n > 0
}

func (e *env) dueCalls(w *Worker) {
	e.t.Helper()
	if err := w.FlagDueCalls(e.ctx); err != nil {
		e.t.Fatal(err)
	}
}

// A call with no ending is written once data time passes its start + 6 h, not
// before; the key row says when its next call expires.
func TestRunExpiresByDataTime(t *testing.T) {
	e := newEnv(t)
	w := e.worker("w1")
	t0 := e.clock.Now().UnixNano()
	a := sessionKey("a")
	e.putFile([]records.Record{e.start(ccCall("c1", "a", t0))}, t0, e.clock.Now())
	e.run(w, a)
	if got := e.keyRow(a).ExpiresNS; got != t0+int64(ttl) {
		t.Fatalf("expires_ns %d, want start + 6 h %d", got, t0+int64(ttl))
	}
	// Data time reaches exactly start + 6 h: not yet.
	e.clock.Advance(time.Second)
	e.putFile([]records.Record{e.start(ccCall("c2", "b", t0+int64(ttl)))}, t0+int64(ttl), e.clock.Now())
	e.run(w, sessionKey("b"))
	e.dueCalls(w)
	if e.flagOf(a).ok {
		t.Fatal("due calls flagged a key with nothing due")
	}
	if res := e.run(w, a); res.Committed {
		t.Fatal("a run with nothing due committed")
	}
	// One nanosecond past it: due calls flag the key, and its run writes the call.
	e.clock.Advance(time.Second)
	e.putFile([]records.Record{e.start(ccCall("c3", "b", t0+int64(ttl)+1))}, t0+int64(ttl)+1, e.clock.Now())
	e.run(w, sessionKey("b"))
	e.dueCalls(w)
	if !e.flagOf(a).ok {
		t.Fatal("due calls didn't flag the key whose call is due")
	}
	e.drain(w)
	if !e.expired(a, "c1") {
		t.Fatal("the call wasn't written")
	}
	if got := e.keyRow(a).ExpiresNS; got != 0 {
		t.Fatalf("expires_ns %d after the call expired, want 0", got)
	}
	e.dueCalls(w)
	if e.flagOf(a).ok {
		t.Fatal("flagged again after its call expired")
	}
	e.checkAt(t0 + int64(ttl) + 1)
}

// A key that gets no more data is still revisited: its deadline is in its key
// row, and the due-calls singleton, on whichever worker holds its lock, flags
// it once data time passes it.
func TestDueCallsRevisitAQuietKey(t *testing.T) {
	e := newEnv(t)
	w1, w2 := e.worker("w1"), e.worker("w2")
	t0 := e.clock.Now().UnixNano()
	quiet := sessionKey("quiet")
	e.putFile([]records.Record{e.start(ccCall("c1", "quiet", t0))}, t0, e.clock.Now())
	e.drain(w1)
	if err := w2.Tick(e.ctx); err != nil {
		t.Fatal(err)
	}
	if !contains(w1.Singletons(), "due-calls") || contains(w2.Singletons(), "due-calls") {
		t.Fatalf("singletons: w1 %v, w2 %v", w1.Singletons(), w2.Singletons())
	}
	// Only another session gets data, past the quiet key's deadline: the ticks
	// that run it also run the singleton (every 30 s), which flags the quiet key.
	e.clock.Advance(time.Minute)
	e.putFile([]records.Record{e.start(ccCall("b1", "busy", t0+7*int64(time.Hour)))}, t0+7*int64(time.Hour), e.clock.Now())
	e.drain(w1) // nothing but the singleton flags the quiet key
	if !e.expired(quiet, "c1") {
		t.Fatal("the quiet key's call never expired")
	}
	e.checkAt(t0 + 7*int64(time.Hour))
	// Its holder gone, the singleton moves.
	w1.Kill()
	if err := w2.Tick(e.ctx); err != nil {
		t.Fatal(err)
	}
	if !contains(w2.Singletons(), "due-calls") {
		t.Fatal("the singleton didn't move to w2")
	}
}

// Sessions whose calls start, end in time, end hours late or never, as data
// time moves on over two days; files named up to 4 min late; workers crash,
// stall and race; due calls run now and then. Once it settles, every key
// equals one pass with its calls expired at the newest data time.
func TestChaosExpiry(t *testing.T) {
	for seed := int64(1); seed <= 2; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) { chaosExpiry(t, seed) })
	}
}

func chaosExpiry(t *testing.T, seed int64) {
	e := newEnv(t)
	rng := rand.New(rand.NewSource(seed))
	names := []string{"w0", "w1", "w2"}
	ws := []*Worker{e.worker("w0"), e.worker("w1"), e.worker("w2")}
	sids := []string{"a", "b", "c", "d"}
	open := map[string][]*canon.Request{}
	data, final := e.clock.Now().UnixNano(), int64(0)
	e.store.FailRate(0.05, rng)
	for i := 0; i < 70; i++ {
		k := rng.Intn(len(ws))
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
				j := 0
				if rng.Intn(2) == 0 {
					j = rng.Intn(n)
				}
				c := open[sid][j]
				open[sid] = append(open[sid][:j], open[sid][j+1:]...)
				end := data
				if rng.Intn(3) == 0 {
					end = c.StartNS + int64(rng.Intn(5*60))*int64(time.Minute) // in time, arriving late
				}
				rs = append(rs, e.end(c, end))
				newest = max(newest, end)
			}
			if len(rs) > 0 {
				late := time.Duration(rng.Intn(240)) * time.Second
				e.store.FailRate(0, nil)
				e.putFile(rs, newest, e.clock.Now().Add(-late))
				e.store.FailRate(0.05, rng)
				final = max(final, newest)
			}
		case x < 12:
			_ = ws[k].Tick(e.ctx)
		case x < 15:
			_ = ws[k].FlagDueCalls(e.ctx)
		case x < 16:
			ws[k].Kill()
			ws[k] = e.worker(names[k])
		case x < 17:
			e.terminate(ws[k].LockPID())
		default:
			e.clock.Advance(time.Duration(rng.Intn(90)) * time.Second)
		}
	}
	e.store.FailRate(0, nil)
	e.clock.Advance(time.Hour) // past any quarantine
	for i := 0; ; i++ {
		if i > 20 {
			t.Fatal("settle: keys kept being flagged")
		}
		e.drain(ws...)
		if err := ws[0].FlagDueCalls(e.ctx); err != nil {
			t.Fatal(err)
		}
		if e.flags() == 0 {
			break
		}
	}
	e.checkAt(final)
}
