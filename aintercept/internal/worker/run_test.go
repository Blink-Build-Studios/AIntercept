package worker

import (
	"errors"
	"fmt"
	"math/rand"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/rollup"
)

// v5's rollup tests (internal/rollup/rollup_test.go), ported to the Postgres
// commit: the key row is the commit, its n the guard.

// --- atomic ---------------------------------------------------------------

func TestRunAppliesNewFilesAndCommits(t *testing.T) {
	e := newEnv(t)
	key := sessionKey("a")
	e.ingest("a", 3)
	e.clock.Advance(time.Second)
	e.ingest("a", 2)
	res := e.run(e.worker("w1"), key)
	if !res.Committed || res.Applied != 2 {
		t.Fatalf("run %+v, want 2 files applied and committed", res)
	}
	e.check()
	row := e.keyRow(key)
	if row.N != 1 || row.LastApplied == "" || len(row.State) != 1 || row.Day != "2026-11-13" {
		t.Fatalf("key row %+v", row)
	}
	if e.flagOf(key).ok {
		t.Fatal("the run left its flag")
	}
	// Nothing new: no commit, and the stored rows stand.
	if res := e.run(e.worker("w2"), key); res.Committed || res.Applied != 0 {
		t.Fatalf("idle run %+v", res)
	}
	if e.keyRow(key).N != 1 {
		t.Fatal("an idle run committed")
	}
	e.check()
}

func TestCrashAtEachStep(t *testing.T) {
	for _, step := range []string{"read-row", "listed", "read", "state", "commit", "guarded", "rows"} {
		t.Run(step, func(t *testing.T) {
			e := newEnv(t)
			key := sessionKey("a")
			e.ingest("a", 3)
			w1 := e.worker("w1", withHook(crashAt(step)))
			if _, err := w1.RunKey(e.ctx, key); !errors.Is(err, errCrash) {
				t.Fatalf("crash at %s: err %v", step, err)
			}
			if e.keyRow(key).N != 0 || e.count("requests", key) != 0 {
				t.Fatal("a crashed run wrote")
			}
			if f := e.flagOf(key); !f.ok || f.failures != 1 {
				t.Fatalf("after the crash the flag is %+v, want it kept with one failure", f)
			}
			e.clock.Advance(time.Second)
			e.ingest("a", 2)
			w2 := e.worker("w2")
			e.run(w2, key)
			e.run(w2, key)
			e.check()
			if e.flagOf(key).ok {
				t.Fatal("the flag survived a clean run")
			}
		})
	}
}

// A worker that dies mid-run (no error path runs): Postgres aborts its
// transaction and frees its lock, and the flag is still there.
func TestHolderKilledMidRunKeyIsReclaimed(t *testing.T) {
	for _, step := range []string{"listed", "read", "state", "commit", "guarded", "rows"} {
		t.Run(step, func(t *testing.T) {
			e := newEnv(t)
			key := sessionKey("a")
			e.ingest("a", 3)
			var dead *Worker
			dead = e.worker("w1", withHook(func(s, _ string) error {
				if s == step {
					dead.Kill()      // its connections close: the transaction aborts, the locks go
					runtime.Goexit() // and nothing after this runs for the key
				}
				return nil
			}))
			done := make(chan struct{})
			go func() { defer close(done); _ = dead.Tick(e.ctx) }()
			<-done
			if !e.flagOf(key).ok {
				t.Fatal("the dead holder's key lost its flag")
			}
			w2 := e.worker("w2")
			e.drain(w2)
			e.check()
		})
	}
}

func TestTwoHoldersOnlyOneCommitWins(t *testing.T) {
	e := newEnv(t)
	key := sessionKey("a")
	e.ingest("a", 3)
	w2 := e.worker("w2")
	once := false
	w1 := e.worker("w1", withHook(func(step, _ string) error {
		if step == "commit" && !once { // w2 runs the same key to completion first
			once = true
			if res, err := w2.RunKey(e.ctx, key); err != nil || !res.Committed {
				t.Fatalf("w2: %+v %v", res, err)
			}
		}
		return nil
	}))
	res, err := w1.RunKey(e.ctx, key)
	if err != nil || res.Committed || !res.Lost {
		t.Fatalf("w1 lost the race but: %+v %v", res, err)
	}
	if n := e.keyRow(key).N; n != 1 {
		t.Fatalf("key row n %d, want 1 (w2's)", n)
	}
	if f := e.flagOf(key); f.ok && f.failures != 0 {
		t.Fatal("a lost guard was counted as a failure")
	}
	e.check()
	// w1 runs again with a fresh view (its warm state was dropped) and stays exact.
	e.clock.Advance(time.Second)
	e.ingest("a", 1)
	e.run(w1, key)
	e.check()
}

// The old worker commits before the new one, then after it: each time the
// second to commit fails the guard and writes nothing.
func TestGuardBothOrders(t *testing.T) {
	e := newEnv(t)
	key := sessionKey("a")
	e.ingest("a", 2)
	old, fresh := e.worker("old"), e.worker("new")
	e.run(old, key) // old holds a warm state at n = 1
	e.clock.Advance(time.Second)
	e.ingest("a", 1)
	e.run(old, key) // old commits first: n = 2
	// fresh has no warm state: it loads n = 2 cold and commits n = 3.
	e.clock.Advance(time.Second)
	e.ingest("a", 1)
	e.run(fresh, key)
	// old's warm state is at n = 2, the row at 3: it reloads, never commits on top of a stale state.
	e.clock.Advance(time.Second)
	e.ingest("a", 1)
	if res := e.run(old, key); !res.Committed {
		t.Fatalf("old after new: %+v", res)
	}
	if e.keyRow(key).N != 4 {
		t.Fatalf("n %d, want 4", e.keyRow(key).N)
	}
	e.check()
}

func TestFileArrivingMidRunIsNotLost(t *testing.T) {
	e := newEnv(t)
	key := sessionKey("a")
	e.ingest("a", 3)
	once := false
	w := e.worker("w1", withHook(func(step, _ string) error {
		if step == "listed" && !once {
			once = true
			e.clock.Advance(time.Second)
			e.ingest("a", 2) // after the LIST: the tail flags the key again
		}
		return nil
	}))
	start := e.clock.Now()
	if err := w.Tick(e.ctx); err != nil {
		t.Fatal(err)
	}
	f := e.flagOf(key)
	if !f.ok {
		t.Fatal("a file that arrived mid-run left the key unflagged")
	}
	if !f.since.Equal(start) {
		t.Fatalf("the surviving flag's since %v, want the run's start %v", f.since, start)
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
	e.run(w, key)
	e.ingestAt("a", 3, t0.Add(8*time.Minute)) // named before the last applied, inside the grace window
	if res := e.run(w, key); res.Applied != 1 {
		t.Fatalf("late file: %+v", res)
	}
	if res := e.run(w, key); res.Applied != 0 {
		t.Fatalf("re-run: %+v", res)
	}
	if res := e.run(e.worker("w2"), key); res.Applied != 0 {
		t.Fatalf("a fresh worker re-applied: %+v", res)
	}
	e.check()
}

// A commit carries the newest raw row time of every ingest file it applied,
// never moving back.
func TestCommitCarriesNewestRawRow(t *testing.T) {
	e := newEnv(t)
	key := sessionKey("a")
	at := e.clock.Now()
	e.putFile([]records.Record{e.event("a", "tengu_x", 100, "")}, 500, at)
	w := e.worker("w1")
	e.run(w, key)
	if got := e.keyRow(key).NewestRowNS; got != 500 {
		t.Fatalf("newest %d, want 500", got)
	}
	e.clock.Advance(time.Second)
	e.putFile([]records.Record{e.event("a", "tengu_x", 200, "")}, 300, e.clock.Now()) // an older pod's file
	e.run(w, key)
	if got := e.keyRow(key).NewestRowNS; got != 500 {
		t.Fatalf("newest %d after an older file, want 500", got)
	}
	// Rows newer than any file's mark count too (the merge's rawNewest).
	e.clock.Advance(time.Second)
	e.putFile([]records.Record{e.event("a", "tengu_x", 900, "")}, 0, e.clock.Now())
	e.run(w, key)
	if got := e.keyRow(key).NewestRowNS; got != 900 {
		t.Fatalf("newest %d, want the event's row time 900", got)
	}
	e.check()
}

// --- compaction -----------------------------------------------------------

func TestCompactionKeepsChunksBoundedAndGCs(t *testing.T) {
	e := newEnv(t)
	key := sessionKey("a")
	w := e.worker("w1", func(c *Config) { c.Fanout = 4 })
	for i := 0; i < 70; i++ {
		e.clock.Advance(time.Second)
		e.ingest("a", 1)
		e.run(w, key)
	}
	row := e.keyRow(key)
	if len(row.State) > 4*4 {
		t.Fatalf("after 70 runs: %d state chunks", len(row.State))
	}
	if len(row.Garbage) == 0 {
		t.Fatal("compaction left no garbage")
	}
	e.check()
	// A cold load from the compacted chunks is exact.
	e.clock.Advance(time.Second)
	e.ingest("a", 1)
	e.run(e.worker("w2"), key)
	e.check()
	// Past the GC grace, superseded chunks are deleted: only the named ones and garbage remain.
	e.clock.Advance(11 * time.Minute)
	e.ingest("a", 1)
	e.run(w, key)
	row = e.keyRow(key)
	infos, err := e.store.ListInfo(e.ctx, rollup.KeyDir(prefix, key)+"state/")
	if err != nil {
		t.Fatal(err)
	}
	named := map[string]bool{}
	for _, p := range row.State {
		named[p] = true
	}
	for _, g := range row.Garbage {
		named[g.Path] = true
	}
	for _, in := range infos {
		if rel := strings.TrimPrefix(in.Key, rollup.KeyDir(prefix, key)); !named[rel] {
			t.Errorf("%s is neither named nor garbage", rel)
		}
	}
	e.check()
}

// --- sequences ------------------------------------------------------------

// Every order of: a file arrives, a run, a crashed run, a second worker's run,
// a run that compacts (fanout 2). Afterwards a drain is exact.
func TestEveryOrder(t *testing.T) {
	e := newEnv(t)
	steps := []string{"arrive", "run", "crash", "second", "compact"}
	cfg := func(c *Config) { c.Fanout = 2 }
	w1, w2 := e.worker("w1", cfg), e.worker("w2")
	w3 := e.worker("w3", withHook(crashAt("rows")))
	i := 0
	permute(len(steps), func(order []int) {
		i++
		sid := fmt.Sprintf("a%d", i)
		key := sessionKey(sid)
		e.ingest(sid, 2)
		for _, s := range order {
			e.clock.Advance(time.Second)
			switch steps[s] {
			case "arrive":
				e.ingest(sid, 1)
			case "run":
				_, _ = w1.RunKey(e.ctx, key)
			case "crash":
				_, _ = w3.RunKey(e.ctx, key)
			case "second":
				_, _ = w2.RunKey(e.ctx, key)
			case "compact":
				for j := 0; j < 3; j++ {
					e.ingest(sid, 1)
					_, _ = w1.RunKey(e.ctx, key)
				}
			}
		}
		e.flag(key)
		for j := 0; e.flagOf(key).ok; j++ {
			if j > 10 {
				t.Fatalf("%v: the key stayed flagged", order)
			}
			_, _ = w1.RunKey(e.ctx, key)
		}
		e.checkAt(0, key)
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

// --- flat cost ------------------------------------------------------------

// A run's store work follows the new input, not the key's history.
func TestFlatCostInHistory(t *testing.T) {
	e := newEnv(t)
	key := sessionKey("a")
	w := e.worker("w1")
	measure := func() float64 {
		e.store.ResetCounters()
		const runs = 24
		for i := 0; i < runs; i++ {
			e.clock.Advance(time.Second)
			e.ingest("a", 1)
			e.run(w, key)
		}
		return float64(e.store.Cost().Requests) / runs
	}
	small := measure()
	for i := 0; i < 6; i++ {
		measure()
	}
	large := measure()
	if large > small*1.5+3 {
		t.Fatalf("store requests per run grew with history: %.1f → %.1f", small, large)
	}
}

// --- generated: chaos -----------------------------------------------------

// Several workers, several keys, random ingest (named up to 4 min late, inside
// the grace window), crashes (a worker replaced, its connections closed),
// stalls (its lock connection closed by the server while it keeps running),
// runs crashing at a random step, dropped run connections, S3 faults (half of
// them ambiguous), ticks racing. Once the faults stop and the keys drain,
// every key's stored inputs equal one pass.
func TestChaos(t *testing.T) {
	for seed := int64(1); seed <= 3; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) { chaos(t, seed) })
	}
}

func chaos(t *testing.T, seed int64) {
	e := newEnv(t)
	rng := rand.New(rand.NewSource(seed))
	crashSteps := []string{"read-row", "listed", "read", "state", "commit", "guarded", "rows", "commit-result", "committed"}
	var mu sync.Mutex            // the runs call the hook in parallel
	crash := map[string]string{} // a worker's next run crashes at this step
	setCrash := func(name, step string) { mu.Lock(); crash[name] = step; mu.Unlock() }
	mk := func(name string) *Worker {
		return e.worker(name, withHook(func(step, _ string) error {
			mu.Lock()
			defer mu.Unlock()
			if crash[name] == step {
				crash[name] = ""
				return errCrash
			}
			return nil
		}))
	}
	names := []string{"w0", "w1", "w2"}
	ws := []*Worker{mk("w0"), mk("w1"), mk("w2")}
	sids := []string{"a", "b", "c", "d", "e", "f"}
	e.store.FailRate(0.05, rng)
	for i := 0; i < 120; i++ {
		k := rng.Intn(len(ws))
		switch x := rng.Intn(20); {
		case x < 8:
			late := time.Duration(rng.Intn(240)) * time.Second
			e.store.FailRate(0, nil)
			e.ingestAt(sids[rng.Intn(len(sids))], 1+rng.Intn(3), e.clock.Now().Add(-late))
			e.store.FailRate(0.05, rng)
		case x < 13:
			_ = ws[k].Tick(e.ctx)
		case x < 14: // two workers tick at once
			j := (k + 1) % len(ws)
			done := make(chan struct{})
			go func() { defer close(done); _ = ws[j].Tick(e.ctx) }()
			_ = ws[k].Tick(e.ctx)
			<-done
		case x < 15:
			ws[k].Kill() // crashed: memory gone, its locks freed by Postgres
			ws[k] = mk(names[k])
		case x < 16:
			e.terminate(ws[k].LockPID()) // stalled: keepalives closed its lock connection
		case x < 17:
			setCrash(names[k], crashSteps[rng.Intn(len(crashSteps))])
		case x < 18:
			if _, err := e.db.Exec(e.ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name = $1 AND datname = current_database()",
				runConnName(names[k])); err != nil {
				t.Fatal(err)
			}
		default:
			e.clock.Advance(time.Duration(rng.Intn(90)) * time.Second)
		}
	}
	e.store.FailRate(0, nil)
	for _, k := range names {
		setCrash(k, "")
	}
	e.clock.Advance(2 * time.Minute)
	// Failures from the chaos may have quarantined a key: an hour on, it's retried.
	e.clock.Advance(time.Hour)
	e.drain(ws...)
	e.check()
	lag, err := ws[0].Lag(e.ctx)
	if err != nil || lag != 0 {
		t.Fatalf("lag %v %v after the drain", lag, err)
	}
}
