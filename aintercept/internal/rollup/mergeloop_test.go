package rollup

import (
	"testing"
	"time"
)

// The merge loop in the rollup replicas (docs/PIPELINE.md §3): every period,
// the replica holding lock:merge runs the merge over the keys in `committed`;
// the others skip. The lock expires, so another replica takes over when the
// holder dies. If Redis loses `committed`, the run takes its keys from the
// recent index.

// committedKey ingests into session sid and runs its key, so it's committed.
func (e *env) committedKey(w *Worker, sid string) string {
	e.t.Helper()
	e.ingest(sid, 2)
	if _, ok, err := w.RunKey(e.ctx, sessionKey(sid)); err != nil || !ok {
		e.t.Fatalf("run %s: %v %v", sid, ok, err)
	}
	return sessionKey(sid)
}

func (e *env) mergeN() int64 {
	e.t.Helper()
	m, err := ReadNewestMerge(e.ctx, e.store, prefix)
	if err != nil {
		e.t.Fatal(err)
	}
	if m == nil {
		return 0
	}
	return m.N
}

func TestMergeTickRunsOnTheLockHolderOnly(t *testing.T) {
	e := newEnv(t)
	a, b := e.worker("a"), e.worker("b")
	e.committedKey(a, "s1")
	res, ran, err := a.MergeTick(e.ctx)
	if err != nil || !ran || res.Keys != 1 || e.mergeN() != 1 {
		t.Fatalf("a: ran %v, %+v, %v; merge %d", ran, res, err, e.mergeN())
	}
	if got, _ := e.rdb.Get(e.ctx, "lock:merge").Result(); got != "a" {
		t.Fatalf("lock:merge = %q, want a", got)
	}
	e.committedKey(a, "s2")
	if _, ran, err := b.MergeTick(e.ctx); err != nil || ran {
		t.Fatalf("b ran while a holds lock:merge: %v %v", ran, err)
	}
	if n, _ := e.rdb.SCard(e.ctx, "committed").Result(); n != 1 {
		t.Fatalf("b touched committed: %d left", n)
	}
	if _, ran, _ := a.MergeTick(e.ctx); !ran || e.mergeN() != 2 {
		t.Fatalf("the holder's next tick: ran %v, merge %d", ran, e.mergeN())
	}
}

func TestMergeLockMovesWhenTheHolderDies(t *testing.T) {
	e := newEnv(t)
	a, b := e.worker("a"), e.worker("b")
	e.committedKey(a, "s1")
	if _, ran, _ := a.MergeTick(e.ctx); !ran {
		t.Fatal("a didn't run")
	}
	e.committedKey(b, "s2")
	e.mr.FastForward(a.cfg.LockTTL + time.Second) // a is gone: its lock expires
	if res, ran, err := b.MergeTick(e.ctx); err != nil || !ran || res.Keys != 1 {
		t.Fatalf("b after a's lock expired: ran %v, %+v, %v", ran, res, err)
	}
}

func TestMergeTickRecoversKeysAfterARedisWipe(t *testing.T) {
	e := newEnv(t)
	a := e.worker("a")
	e.committedKey(a, "s1")
	if _, ran, _ := a.MergeTick(e.ctx); !ran {
		t.Fatal("a didn't run")
	}
	e.committedKey(a, "s2")
	e.mr.FlushAll() // committed, the lock and the merge's marker are gone
	res, ran, err := a.MergeTick(e.ctx)
	if err != nil || !ran || !res.Recovered || res.Keys == 0 {
		t.Fatalf("after the wipe: ran %v, %+v, %v", ran, res, err)
	}
	if res, _, _ := a.MergeTick(e.ctx); res.Recovered {
		t.Fatal("recovered again on a Redis that hasn't lost anything since")
	}
}

func TestReleaseLetsGoOfTheMergeLock(t *testing.T) {
	e := newEnv(t)
	a := e.worker("a")
	if _, ran, _ := a.MergeTick(e.ctx); !ran {
		t.Fatal("a didn't take the lock")
	}
	a.ReleaseMerge(e.ctx)
	if n, _ := e.rdb.Exists(e.ctx, "lock:merge").Result(); n != 0 {
		t.Fatal("lock:merge still held after release")
	}
}
