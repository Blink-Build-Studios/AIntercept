package stream

import (
	"context"
	"testing"
	"time"
)

func leaser(s Store, l Layout, replica string, buckets int) *Leaser {
	return &Leaser{S: s, L: l, Replica: replica, Buckets: buckets, TTL: 30 * time.Second}
}

// Atomic cases.

func TestLeaseClaimFree(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore(NewFakeClock(time.Unix(1_800_000_000, 0)))
	a := leaser(s, Layout{}, "a", 4)
	owned, err := a.Tick(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(owned) != 4 {
		t.Fatalf("a lone replica claims every bucket: %v", owned)
	}
	for b, ep := range owned {
		if ep != 1 {
			t.Fatalf("bucket %d epoch %d, want 1", b, ep)
		}
	}
}

func TestLeaseRenewKeepsOwnership(t *testing.T) {
	ctx := context.Background()
	clock := NewFakeClock(time.Unix(1_800_000_000, 0))
	s := NewMemStore(clock)
	a := leaser(s, Layout{}, "a", 2)
	b := leaser(s, Layout{}, "b", 2)
	if _, err := a.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ { // a renews every 10 s; b never gets in
		clock.Advance(10 * time.Second)
		if _, err := a.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	got, err := b.Tick(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// b is live now, so a will hand one bucket over (balancing) — but b may
	// not take a bucket a still holds live.
	for bk := range got {
		if r, _, live, _ := a.Owner(ctx, bk); live && r == "a" {
			t.Fatalf("b took bucket %d while a held it live", bk)
		}
	}
}

func TestLeaseTakeoverAfterTTLByStoreTime(t *testing.T) {
	ctx := context.Background()
	clock := NewFakeClock(time.Unix(1_800_000_000, 0))
	s := NewMemStore(clock)
	a := leaser(s, Layout{}, "a", 1)
	b := leaser(s, Layout{}, "b", 1)
	if _, err := a.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	clock.Advance(29 * time.Second)
	if got, _ := b.Tick(ctx); len(got) != 0 {
		t.Fatalf("taken before the TTL: %v", got)
	}
	clock.Advance(2 * time.Second) // a stopped renewing 31 s ago
	got, err := b.Tick(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != 2 {
		t.Fatalf("b's takeover epoch = %v, want bucket 0 at epoch 2", got)
	}
}

func TestLeaseGracefulReleaseFreesAtOnce(t *testing.T) {
	ctx := context.Background()
	clock := NewFakeClock(time.Unix(1_800_000_000, 0))
	s := NewMemStore(clock)
	a := leaser(s, Layout{}, "a", 1)
	b := leaser(s, Layout{}, "b", 1)
	if _, err := a.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.Release(ctx, 0); err != nil {
		t.Fatal(err)
	}
	got, err := b.Tick(ctx)
	if err != nil || len(got) != 1 {
		t.Fatalf("released bucket not claimable at once: %v %v", got, err)
	}
}

func TestLeaseTwoClaimersOneWinner(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore(NewFakeClock(time.Unix(1_800_000_000, 0)))
	a := leaser(s, Layout{}, "a", 1)
	b := leaser(s, Layout{}, "b", 1)
	ga, _ := a.Tick(ctx)
	gb, _ := b.Tick(ctx)
	if len(ga)+len(gb) != 1 {
		t.Fatalf("one bucket, two owners: a=%v b=%v", ga, gb)
	}
}

// Sequences: replicas joining and leaving converge on ceil(B/live) each, and
// no bucket ever has two live owners.
func TestLeaseBalancingConverges(t *testing.T) {
	ctx := context.Background()
	clock := NewFakeClock(time.Unix(1_800_000_000, 0))
	s := NewMemStore(clock)
	const buckets = 8
	ls := map[string]*Leaser{}
	tickAll := func() map[string]map[int]int64 {
		out := map[string]map[int]int64{}
		for n, l := range ls {
			o, err := l.Tick(ctx)
			if err != nil {
				t.Fatal(err)
			}
			out[n] = o
		}
		clock.Advance(10 * time.Second)
		return out
	}
	check := func(want int) {
		var owned map[string]map[int]int64
		for i := 0; i < 10; i++ {
			owned = tickAll()
		}
		seen := map[int]string{}
		for n, o := range owned {
			if len(o) > want {
				t.Fatalf("%s holds %d buckets, want at most %d (%d live)", n, len(o), want, len(ls))
			}
			for b := range o {
				if prev, dup := seen[b]; dup {
					t.Fatalf("bucket %d owned by %s and %s", b, prev, n)
				}
				seen[b] = n
			}
		}
		if len(seen) != buckets {
			t.Fatalf("%d of %d buckets owned", len(seen), buckets)
		}
	}
	ls["a"] = leaser(s, Layout{}, "a", buckets)
	check(8)
	ls["b"] = leaser(s, Layout{}, "b", buckets)
	ls["c"] = leaser(s, Layout{}, "c", buckets)
	check(3)
	delete(ls, "b") // b dies without releasing: its buckets free up after the TTL
	check(4)
}
