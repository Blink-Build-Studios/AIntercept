package stream

import (
	"context"
	"testing"
	"time"
)

// The per-bucket commit log: create-only, so exactly one writer wins each
// sequence number. That is the fencing every other guarantee rests on.

func TestCommitOneWinnerPerSequence(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore(NewFakeClock(time.Unix(1_800_000_000, 0)))
	l := Layout{Prefix: "x/"}
	a := &Commit{Bucket: 3, Seq: 1, Epoch: 1, Replica: "a"}
	b := &Commit{Bucket: 3, Seq: 1, Epoch: 2, Replica: "b"}
	okA, err := TryCommit(ctx, s, l, a)
	if err != nil || !okA {
		t.Fatalf("a: %v %v", okA, err)
	}
	okB, err := TryCommit(ctx, s, l, b)
	if err != nil || okB {
		t.Fatalf("b must lose the same sequence: %v %v", okB, err)
	}
	got, err := LoadLatest(ctx, s, l, 3)
	if err != nil || got == nil || got.Replica != "a" || got.Seq != 1 {
		t.Fatalf("latest = %+v %v, want a's commit 1", got, err)
	}
}

func TestLoadLatestFollowsTheLog(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore(NewFakeClock(time.Unix(1_800_000_000, 0)))
	l := Layout{Prefix: ""}
	if c, err := LoadLatest(ctx, s, l, 0); err != nil || c != nil {
		t.Fatalf("empty bucket: %+v %v, want nil", c, err)
	}
	for seq := int64(1); seq <= 12; seq++ { // past 9 → 10: names must sort numerically
		if ok, err := TryCommit(ctx, s, l, &Commit{Bucket: 0, Seq: seq, Epoch: 1}); !ok || err != nil {
			t.Fatal(seq, ok, err)
		}
	}
	c, err := LoadLatest(ctx, s, l, 0)
	if err != nil || c.Seq != 12 {
		t.Fatalf("latest = %+v %v, want 12", c, err)
	}
}

func TestCommitKeysAreFixedWidth(t *testing.T) {
	l := Layout{Prefix: "p/"}
	if k := l.CommitKey(7, 9); k != "p/stream/v1/buckets/07/commit-000000000009.json" {
		t.Fatalf("CommitKey = %q", k)
	}
	if k := l.LeaseKey(7, 3); k != "p/stream/v1/leases/bucket=07/epoch-0000000003" {
		t.Fatalf("LeaseKey = %q", k)
	}
}

// A stale owner (one that lost its lease and hasn't noticed) loses the commit
// race: its run files are never named by a commit, so readers never see them,
// and GC removes them after the grace period.
func TestStaleOwnerIsFenced(t *testing.T) {
	ctx := context.Background()
	clock := NewFakeClock(time.Unix(1_800_000_000, 0))
	s := NewMemStore(clock)
	l := Layout{}
	h := newHarness(t, s, clock, l, 1) // one bucket
	h.upload(0, h.gen.batch(20))

	old := h.worker("old", 1)
	if _, err := old.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	h.upload(0, h.gen.batch(20))
	// A new owner takes over at a higher epoch and commits first.
	neu := h.worker("new", 2)
	if _, err := neu.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	// The old owner wakes up with its stale view and tries to commit.
	h.upload(0, h.gen.batch(5))
	_, err := old.Cycle(ctx)
	if err != ErrFenced {
		t.Fatalf("stale owner's cycle: %v, want ErrFenced", err)
	}
	// Its orphaned run files exist but no commit names them; tables equal the
	// reference over exactly what the winning owner committed.
	h.checkEqualsReference(ctx, neu)
	clock.Advance(GCGrace + time.Minute)
	if err := neu.GC(ctx); err != nil {
		t.Fatal(err)
	}
	if n := h.unreferenced(ctx, 0); n != 0 {
		t.Fatalf("%d unreferenced files left after GC", n)
	}
}
