package stream

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
)

// The fencing rule (docs/PIPELINE.md §3.1): a run captures the ETag of every
// object it'll write, writes each conditionally, and drops on any 412. The
// newer holder captures after the older one did (its commit create orders
// them), so the stale run loses whichever writes first.

func capture(t *testing.T, s Store, remembered map[string]string, keys ...string) *Fence {
	t.Helper()
	f := NewFence(s, remembered)
	if err := f.Capture(context.Background(), keys...); err != nil {
		t.Fatal(err)
	}
	return f
}

func wantObj(t *testing.T, s Store, key, want string) {
	t.Helper()
	b, err := s.Get(context.Background(), key)
	if want == "" {
		if !errors.Is(err, process.ErrNotFound) {
			t.Fatalf("%s = %q %v, want absent", key, b, err)
		}
		return
	}
	if err != nil || string(b) != want {
		t.Fatalf("%s = %q %v, want %q", key, b, err, want)
	}
}

func TestFenceFirstWrite(t *testing.T) {
	ctx := context.Background()
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			a, b := capture(t, s, nil, "s/1/head"), capture(t, s, nil, "s/1/head")
			if err := a.Put(ctx, "s/1/head", []byte("a")); err != nil {
				t.Fatalf("first write of an absent object: %v", err)
			}
			if err := b.Put(ctx, "s/1/head", []byte("b")); !errors.Is(err, ErrPrecondition) {
				t.Fatalf("a second creator: %v, want ErrPrecondition", err)
			}
			wantObj(t, s, "s/1/head", "a")
		})
	}
}

// The old holder writes after the new one: 412, and it writes nothing more.
func TestFenceStaleHolderWritesSecond(t *testing.T) {
	ctx := context.Background()
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			keys := []string{"s/1/h/01", "s/1/head"}
			old := capture(t, s, nil, keys...)
			nu := capture(t, s, nil, keys...)
			for _, k := range keys {
				if err := nu.Put(ctx, k, []byte("new")); err != nil {
					t.Fatal(err)
				}
			}
			if err := old.Put(ctx, keys[0], []byte("old")); !errors.Is(err, ErrPrecondition) {
				t.Fatalf("stale write: %v, want ErrPrecondition", err)
			}
			if err := old.Put(ctx, keys[1], []byte("old")); !errors.Is(err, ErrPrecondition) {
				t.Fatalf("a dropped run keeps writing: %v", err)
			}
			wantObj(t, s, keys[0], "new")
			wantObj(t, s, keys[1], "new")
		})
	}
}

// The old holder writes first. The new one either captured after that write
// and overwrites it, or captured before it, gets 412, and its retry overwrites it.
func TestFenceStaleHolderWritesFirst(t *testing.T) {
	ctx := context.Background()
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			const k = "s/1/head"
			if _, err := s.PutIf(ctx, k, []byte("v0"), ""); err != nil {
				t.Fatal(err)
			}
			// Captured after the old write.
			old := capture(t, s, nil, k)
			if err := old.Put(ctx, k, []byte("old")); err != nil {
				t.Fatal(err)
			}
			nu := capture(t, s, nil, k)
			if err := nu.Put(ctx, k, []byte("new")); err != nil {
				t.Fatal(err)
			}
			wantObj(t, s, k, "new")

			// Captured before the old write.
			old = capture(t, s, nil, k)
			nu = capture(t, s, nil, k)
			if err := old.Put(ctx, k, []byte("old2")); err != nil {
				t.Fatal(err)
			}
			if err := nu.Put(ctx, k, []byte("new2")); !errors.Is(err, ErrPrecondition) {
				t.Fatalf("new holder captured before the old write: %v, want ErrPrecondition", err)
			}
			retry := capture(t, s, nil, k)
			if err := retry.Put(ctx, k, []byte("new2")); err != nil {
				t.Fatal(err)
			}
			wantObj(t, s, k, "new2")
		})
	}
}

// Capture, then another writer lands: the run drops at its first 412 and
// writes none of the rest.
func TestFenceDropsAtFirstPrecondition(t *testing.T) {
	ctx := context.Background()
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			f := capture(t, s, nil, "a", "b", "c")
			if _, err := s.PutIf(ctx, "b", []byte("other"), ""); err != nil {
				t.Fatal(err)
			}
			if err := f.Put(ctx, "a", []byte("run")); err != nil {
				t.Fatal(err)
			}
			if err := f.Put(ctx, "b", []byte("run")); !errors.Is(err, ErrPrecondition) {
				t.Fatalf("%v, want ErrPrecondition", err)
			}
			if err := f.Put(ctx, "c", []byte("run")); !errors.Is(err, ErrPrecondition) {
				t.Fatalf("after a 412: %v, want ErrPrecondition", err)
			}
			if !errors.Is(f.Err(), ErrPrecondition) {
				t.Fatalf("Err = %v", f.Err())
			}
			wantObj(t, s, "b", "other")
			wantObj(t, s, "c", "")
		})
	}
}

// A warm run captures from its own last write, with no HEAD; a write by
// anyone else since still fails it.
func TestFenceRemembersItsOwnWrites(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore(NewFakeClock(time.Unix(0, 0)))
	f := capture(t, s, nil, "a", "b")
	for _, k := range []string{"a", "b"} {
		if err := f.Put(ctx, k, []byte("run1")); err != nil {
			t.Fatal(err)
		}
	}
	s.ResetCounters()
	warm := capture(t, s, f.ETags(), "a", "b")
	if n := s.Cost().Requests; n != 0 {
		t.Fatalf("a warm capture made %d requests, want 0", n)
	}
	if err := warm.Put(ctx, "a", []byte("run2")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutIf(ctx, "b", []byte("other"), f.ETags()["b"]); err != nil {
		t.Fatal(err)
	}
	if err := warm.Put(ctx, "b", []byte("run2")); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("remembered ETag after another write: %v, want ErrPrecondition", err)
	}
}

// Writing an object the run didn't capture is a bug, not a lost race.
func TestFenceRefusesUncapturedKeys(t *testing.T) {
	s := NewMemStore(NewFakeClock(time.Unix(0, 0)))
	f := capture(t, s, nil, "a")
	err := f.Put(context.Background(), "b", []byte("x"))
	if err == nil || errors.Is(err, ErrPrecondition) {
		t.Fatalf("uncaptured key: %v", err)
	}
	wantObj(t, s, "b", "")
}
