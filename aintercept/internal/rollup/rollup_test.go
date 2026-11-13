package rollup

import (
	"encoding/json"
	"errors"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream"
)

// --- atomic ---------------------------------------------------------------

func TestRunAppliesNewFilesAndCommits(t *testing.T) {
	e := newEnv(t)
	e.ingest("a", 3)
	e.clock.Advance(time.Second)
	e.ingest("a", 2)
	n, ok, err := e.worker("w1").RunKey(e.ctx, sessionKey("a"))
	if err != nil || !ok || n != 2 {
		t.Fatalf("applied %d committed %v err %v, want 2 true nil", n, ok, err)
	}
	e.check()
	c, _ := ReadNewest(e.ctx, e.store, prefix, sessionKey("a"))
	if c.N != 1 || c.Key != sessionKey("a") || c.LastApplied == "" || len(c.Out) != 1 || len(c.State) != 1 {
		t.Fatalf("commit %+v", c)
	}
	// Nothing new: no commit.
	if n, ok, err := e.worker("w1").RunKey(e.ctx, sessionKey("a")); err != nil || ok || n != 0 {
		t.Fatalf("idle run applied %d committed %v err %v", n, ok, err)
	}
}

func TestCrashAtEachStep(t *testing.T) {
	for _, step := range []string{"listed", "read", "out", "state", "commit", "committed"} {
		t.Run(step, func(t *testing.T) {
			e := newEnv(t)
			key := sessionKey("a")
			e.ingest("a", 3)
			w1 := e.workerWith(func() Config { c := e.cfg("w1"); c.Hook = crashAt(step); return c }())
			if _, _, err := w1.RunKey(e.ctx, key); !errors.Is(err, errCrash) {
				t.Fatalf("crash at %s: err %v", step, err)
			}
			e.clock.Advance(time.Second)
			e.ingest("a", 2)
			w2 := e.worker("w2")
			if _, _, err := w2.RunKey(e.ctx, key); err != nil {
				t.Fatal(err)
			}
			if _, _, err := w2.RunKey(e.ctx, key); err != nil {
				t.Fatal(err)
			}
			e.check()
		})
	}
}

func TestTwoHoldersOnlyOneCommitWins(t *testing.T) {
	e := newEnv(t)
	key := sessionKey("a")
	e.ingest("a", 3)
	w2 := e.worker("w2")
	c1 := e.cfg("w1")
	c1.Hook = func(step, _ string) error {
		if step == "commit" { // w2 runs the same key to completion first
			if _, ok, err := w2.RunKey(e.ctx, key); err != nil || !ok {
				t.Fatalf("w2: committed %v err %v", ok, err)
			}
		}
		return nil
	}
	w1 := e.workerWith(c1)
	if _, ok, err := w1.RunKey(e.ctx, key); err != nil || ok {
		t.Fatalf("w1 lost the race but committed %v err %v", ok, err)
	}
	named := e.committedDirs(key)
	for d := range e.outDirs(key) {
		if !named[d] {
			continue // w1's files exist but no commit names them: readers never see them
		}
	}
	if len(named) != 1 {
		t.Fatalf("commit names %v", named)
	}
	e.check()
	// w1 runs again with a fresh view and stays exact.
	e.clock.Advance(time.Second)
	e.ingest("a", 1)
	if _, _, err := w1.RunKey(e.ctx, key); err != nil {
		t.Fatal(err)
	}
	e.check()
}

func TestFileArrivingMidRunIsNotLost(t *testing.T) {
	e := newEnv(t)
	key := sessionKey("a")
	e.ingest("a", 3)
	c := e.cfg("w1")
	once := false
	c.Hook = func(step, _ string) error {
		if step == "listed" && !once {
			once = true
			e.clock.Advance(time.Second)
			e.ingest("a", 2) // after the LIST: flags the key dirty again
		}
		return nil
	}
	w := e.workerWith(c)
	if err := w.Tick(e.ctx); err != nil {
		t.Fatal(err)
	}
	if ok, _ := e.rdb.SIsMember(e.ctx, "dirty", key).Result(); !ok {
		t.Fatal("a file that arrived mid-run left the key clean")
	}
	e.drain(w)
	e.check()
}

func TestLateFileInGraceAppliedOnce(t *testing.T) {
	e := newEnv(t)
	key := sessionKey("a")
	t0 := e.clock.Now()
	e.ingestAt("a", 2, t0.Add(10*time.Minute))
	e.clock.Advance(10 * time.Minute)
	w := e.worker("w1")
	if _, _, err := w.RunKey(e.ctx, key); err != nil {
		t.Fatal(err)
	}
	e.ingestAt("a", 3, t0.Add(8*time.Minute)) // named before the last applied, inside the grace window
	if n, _, err := w.RunKey(e.ctx, key); err != nil || n != 1 {
		t.Fatalf("late file: applied %d err %v", n, err)
	}
	if n, _, err := w.RunKey(e.ctx, key); err != nil || n != 0 {
		t.Fatalf("re-run applied %d err %v", n, err)
	}
	if n, _, err := e.worker("w2").RunKey(e.ctx, key); err != nil || n != 0 {
		t.Fatalf("a fresh worker re-applied %d err %v", n, err)
	}
	e.check()
}

func TestSweepReflagsFromRecentIndex(t *testing.T) {
	e := newEnv(t)
	key := sessionKey("a")
	e.ingest("a", 1)
	e.rdb.Del(e.ctx, "dirty") // the SADD was lost
	if err := e.worker("w1").Sweep(e.ctx); err != nil {
		t.Fatal(err)
	}
	if ok, _ := e.rdb.SIsMember(e.ctx, "dirty", key).Result(); !ok {
		t.Fatal("sweep didn't re-flag the key")
	}
	// Older than the sweep span: not re-flagged.
	e.rdb.Del(e.ctx, "dirty")
	e.clock.Advance(20 * time.Minute)
	if err := e.worker("w1").Sweep(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := e.rdb.SCard(e.ctx, "dirty").Result(); n != 0 {
		t.Fatalf("sweep re-flagged %d stale keys", n)
	}
}

func TestRedisWipedMidRun(t *testing.T) {
	e := newEnv(t)
	key := sessionKey("a")
	e.ingest("a", 3)
	c := e.cfg("w1")
	c.Hook = func(step, _ string) error {
		if step == "out" {
			e.mr.FlushAll()
		}
		return nil
	}
	w := e.workerWith(c)
	if err := w.Tick(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Second)
	e.ingest("a", 2)
	e.mr.FlushAll() // and again: the new file's flag is lost too
	if err := e.worker("w2").Sweep(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.drain(e.worker("w2"))
	e.check()
	_ = key
}

func TestLockExpiresMidRun(t *testing.T) {
	e := newEnv(t)
	key := sessionKey("a")
	e.ingest("a", 3)
	w2 := e.worker("w2")
	c := e.cfg("w1")
	once := false
	c.Hook = func(step, _ string) error {
		if step == "commit" && !once {
			once = true
			e.mr.FastForward(2 * time.Minute) // w1's lock expires (a GC pause)
			e.rdb.SAdd(e.ctx, "dirty", key)
			if err := w2.Tick(e.ctx); err != nil { // w2 takes the key and commits
				t.Fatal(err)
			}
		}
		return nil
	}
	w1 := e.workerWith(c)
	if err := w1.Tick(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Second)
	e.ingest("a", 2)
	e.mr.FastForward(2 * time.Minute)
	e.drain(w1, w2)
	e.check()
}

func TestLocksAreExclusiveAndReleased(t *testing.T) {
	e := newEnv(t)
	key := sessionKey("a")
	e.ingest("a", 1)
	w1, w2 := e.worker("w1"), e.worker("w2")
	if err := w1.Tick(e.ctx); err != nil {
		t.Fatal(err)
	}
	if v, _ := e.rdb.Get(e.ctx, "lock:"+key).Result(); v != "w1" {
		t.Fatalf("lock holder %q, want w1", v)
	}
	e.clock.Advance(time.Second)
	e.ingest("a", 1)
	if err := w2.Tick(e.ctx); err != nil { // locked by w1: w2 leaves it
		t.Fatal(err)
	}
	if ok, _ := e.rdb.SIsMember(e.ctx, "dirty", key).Result(); !ok {
		t.Fatal("w2 took a key w1 holds out of dirty")
	}
	if err := w1.Tick(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.check()
	// Idle past the release time: w1 lets go.
	e.clock.Advance(2 * time.Minute)
	if err := w1.Tick(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := e.rdb.Exists(e.ctx, "lock:"+key).Result(); n != 0 {
		t.Fatal("idle key still locked")
	}
	e.ingest("a", 1)
	if err := w1.Tick(e.ctx); err != nil {
		t.Fatal(err)
	}
	w1.Release(e.ctx)
	if n, _ := e.rdb.Exists(e.ctx, "lock:"+key).Result(); n != 0 {
		t.Fatal("Release left a lock")
	}
	e.check()
}

func TestCommittedSetAndLag(t *testing.T) {
	e := newEnv(t)
	key := sessionKey("a")
	e.ingest("a", 1)
	w := e.worker("w1")
	if err := w.Sweep(e.ctx); err != nil { // notices the dirty key (lag starts)
		t.Fatal(err)
	}
	e.clock.Advance(30 * time.Second)
	if lag, err := w.Lag(e.ctx); err != nil || lag < 30*time.Second {
		t.Fatalf("lag %v err %v, want ≥ 30s", lag, err)
	}
	if err := w.Tick(e.ctx); err != nil {
		t.Fatal(err)
	}
	if ok, _ := e.rdb.SIsMember(e.ctx, "committed", key).Result(); !ok {
		t.Fatal("committed set missing the key")
	}
	if lag, err := w.Lag(e.ctx); err != nil || lag != 0 {
		t.Fatalf("lag %v err %v after the run, want 0", lag, err)
	}
}

func TestHTTP(t *testing.T) {
	e := newEnv(t)
	srv := httptest.NewServer(e.worker("w1").Handler())
	defer srv.Close()
	for _, path := range []string{"/healthz", "/lag"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("%s: %v %v", path, err, resp)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if path == "/lag" {
			var v struct {
				Oldest float64 `json:"oldest_unapplied_seconds"`
			}
			if err := json.Unmarshal(b, &v); err != nil || !strings.Contains(string(b), "oldest_unapplied_seconds") {
				t.Fatalf("/lag body %s", b)
			}
		}
	}
}

// --- compaction -----------------------------------------------------------

func TestCompactionKeepsOutputsBoundedAndGCs(t *testing.T) {
	e := newEnv(t)
	key := sessionKey("a")
	c := e.cfg("w1")
	c.Fanout = 4
	w := e.workerWith(c)
	for i := 0; i < 140; i++ {
		e.clock.Advance(time.Second)
		e.ingest("a", 1)
		if _, _, err := w.RunKey(e.ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	cm, _ := ReadNewest(e.ctx, e.store, prefix, key)
	if len(cm.Out) > 4*4 || len(cm.State) > 4*4 {
		t.Fatalf("after 140 runs: %d out dirs, %d state chunks", len(cm.Out), len(cm.State))
	}
	e.check()
	// Past the GC grace, superseded files are deleted.
	e.clock.Advance(11 * time.Minute)
	e.ingest("a", 1)
	if _, _, err := w.RunKey(e.ctx, key); err != nil {
		t.Fatal(err)
	}
	named := e.committedDirs(key)
	cm, _ = ReadNewest(e.ctx, e.store, prefix, key)
	stale := 0
	for d := range e.outDirs(key) {
		if !named[d] {
			stale++
		}
	}
	if stale > len(cm.Garbage) {
		t.Fatalf("%d unnamed out dirs left, garbage pending %d", stale, len(cm.Garbage))
	}
	e.check()
}

// --- sequences ------------------------------------------------------------

// Every order of: a file arrives, a run, a crashed run, a second worker's
// run, a run that compacts (fanout 2). Afterwards a drain must be exact.
func TestEveryOrder(t *testing.T) {
	steps := []string{"arrive", "run", "crash", "second", "compact"}
	permute(len(steps), func(order []int) {
		e := newEnv(t)
		key := sessionKey("a")
		e.ingest("a", 2)
		cfg := e.cfg("w1")
		cfg.Fanout = 2
		w1, w2 := e.workerWith(cfg), e.worker("w2")
		crashCfg := e.cfg("w3")
		crashCfg.Hook = crashAt("state")
		for _, i := range order {
			e.clock.Advance(time.Second)
			switch steps[i] {
			case "arrive":
				e.ingest("a", 1)
			case "run":
				_, _, _ = w1.RunKey(e.ctx, key)
			case "crash":
				_, _, _ = e.workerWith(crashCfg).RunKey(e.ctx, key)
			case "second":
				_, _, _ = w2.RunKey(e.ctx, key)
			case "compact":
				for j := 0; j < 3; j++ {
					e.ingest("a", 1)
					_, _, _ = w1.RunKey(e.ctx, key)
				}
			}
		}
		e.rdb.SAdd(e.ctx, "dirty", key)
		e.drain(w1)
		e.check()
	})
}

func permute(n int, fn func([]int)) {
	p := make([]int, n)
	for i := range p {
		p[i] = i
	}
	var rec func(k int)
	rec = func(k int) {
		if k == n {
			fn(append([]int(nil), p...))
			return
		}
		for i := k; i < n; i++ {
			p[k], p[i] = p[i], p[k]
			rec(k + 1)
			p[k], p[i] = p[i], p[k]
		}
	}
	rec(0)
}

// --- generated: chaos -----------------------------------------------------

// Several workers, several keys, random ingest (named up to 4 min late, inside
// the grace window), random crashes (a worker replaced without releasing its
// locks), lock expiry, Redis wiped, S3 faults (half of them ambiguous). Once
// the faults stop and the keys drain, every key's tables equal one pass.
func TestChaos(t *testing.T) {
	seeds := 5
	for seed := int64(1); seed <= int64(seeds); seed++ {
		e := newEnv(t)
		rng := rand.New(rand.NewSource(seed))
		ws := []*Worker{e.worker("w0"), e.worker("w1"), e.worker("w2")}
		sids := []string{"a", "b", "c", "d", "e", "f"}
		e.store.FailRate(0.05, rng)
		for i := 0; i < 160; i++ {
			switch x := rng.Intn(20); {
			case x < 8:
				late := time.Duration(rng.Intn(240)) * time.Second
				e.store.FailRate(0, nil)
				e.ingestAt(sids[rng.Intn(len(sids))], 1+rng.Intn(3), e.clock.Now().Add(-late))
				e.store.FailRate(0.05, rng)
			case x < 15:
				_ = ws[rng.Intn(len(ws))].Tick(e.ctx)
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
		e.drain(ws...)
		e.check()
	}
}

// --- flat cost ------------------------------------------------------------

// A run's store work follows the new input, not the key's history.
func TestFlatCostInHistory(t *testing.T) {
	e := newEnv(t)
	key := sessionKey("a")
	w := e.worker("w1")
	measure := func() float64 {
		e.store.ResetCounters()
		const runs = 32
		for i := 0; i < runs; i++ {
			e.clock.Advance(time.Second)
			e.ingest("a", 1)
			if _, _, err := w.RunKey(e.ctx, key); err != nil {
				t.Fatal(err)
			}
		}
		return float64(e.store.Cost().Requests) / runs
	}
	small := measure()
	for i := 0; i < 8; i++ { // ~300 more runs of history
		measure()
	}
	large := measure()
	if large > small*1.5+3 {
		t.Fatalf("store requests per run grew with history: %.1f → %.1f", small, large)
	}
	_ = stream.Row{}
}

// A replica that dies mid-run (SIGKILL: no error path, no re-flag) after it
// has taken a key off dirty must not strand the key once its lock expires,
// even with no new input and nothing left in the recent index for the sweep.
func TestHolderKilledMidRunKeyIsReclaimed(t *testing.T) {
	for _, step := range []string{"listed", "read", "out", "state", "commit"} {
		t.Run(step, func(t *testing.T) {
			e := newEnv(t)
			key := sessionKey("a")
			e.ingest("a", 3)
			c := e.cfg("w1")
			c.Hook = func(s, _ string) error {
				if s == step {
					runtime.Goexit() // the process is gone: nothing after this runs for the key
				}
				return nil
			}
			dead := e.workerWith(c)
			_ = dead.Tick(e.ctx)
			if in, _ := e.rdb.SIsMember(e.ctx, "dirty", key).Result(); in {
				t.Fatalf("precondition: the dead holder left %s flagged", key)
			}
			// Long after: the lock has expired and the recent index has aged out.
			e.clock.Advance(20 * time.Minute)
			e.mr.FastForward(20 * time.Minute)
			w2 := e.worker("w2")
			for i := 0; i < 5; i++ {
				if err := w2.Tick(e.ctx); err != nil {
					t.Fatal(err)
				}
				e.clock.Advance(time.Second)
			}
			e.check()
		})
	}
}

// A commit carries the newest raw row time of every ingest file it applied,
// never moving back, through runs and compaction.
func TestCommitCarriesNewestRawRow(t *testing.T) {
	e := newEnv(t)
	key := sessionKey("a")
	e.ingestNewest("a", 2, 500)
	w := e.worker("w1")
	if _, _, err := w.RunKey(e.ctx, key); err != nil {
		t.Fatal(err)
	}
	c, _ := ReadNewest(e.ctx, e.store, prefix, key)
	if c.NewestRowNS != 500 {
		t.Fatalf("newest %d, want 500", c.NewestRowNS)
	}
	e.clock.Advance(time.Second)
	e.ingestNewest("a", 1, 300) // an older pod's file: the newest stays
	if _, _, err := w.RunKey(e.ctx, key); err != nil {
		t.Fatal(err)
	}
	c, _ = ReadNewest(e.ctx, e.store, prefix, key)
	if c.NewestRowNS != 500 {
		t.Fatalf("newest %d after an older file, want 500", c.NewestRowNS)
	}
	for i := 0; i < 40; i++ { // enough runs to compact
		e.clock.Advance(time.Second)
		e.ingest("a", 1)
		if _, _, err := w.RunKey(e.ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	c, _ = ReadNewest(e.ctx, e.store, prefix, key)
	if c.NewestRowNS != 500 {
		t.Fatalf("newest %d after compaction, want 500", c.NewestRowNS)
	}
	e.check()
}
