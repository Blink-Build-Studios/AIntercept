package stream

import (
	"context"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
)

// The store primitives every other piece is built on, against each Store: the
// in-memory emulator the simulations use and the local directory store. (S3 is
// checked by `aintercept storecheck` against a real bucket, storecheck_test.go.)
func stores(t *testing.T) map[string]Store {
	return map[string]Store{
		"mem": NewMemStore(NewFakeClock(time.Unix(1_800_000_000, 0))),
		"dir": DirStore{process.DirStore{Root: t.TempDir()}},
	}
}

func TestPutIfAbsentCreatesOnlyOnce(t *testing.T) {
	ctx := context.Background()
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			ok, err := s.PutIfAbsent(ctx, "a/b/commit-000000000001.json", []byte("first"))
			if err != nil || !ok {
				t.Fatalf("first create: ok=%v err=%v", ok, err)
			}
			ok, err = s.PutIfAbsent(ctx, "a/b/commit-000000000001.json", []byte("second"))
			if err != nil || ok {
				t.Fatalf("second create must lose: ok=%v err=%v", ok, err)
			}
			b, err := s.Get(ctx, "a/b/commit-000000000001.json")
			if err != nil || string(b) != "first" {
				t.Fatalf("the first writer's bytes must stay: %q %v", b, err)
			}
		})
	}
}

func TestPutIfAbsentIsVisibleToListAtOnce(t *testing.T) {
	ctx := context.Background()
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			for _, k := range []string{"p/x-002", "p/x-001", "p/x-003"} {
				if ok, err := s.PutIfAbsent(ctx, k, []byte(k)); !ok || err != nil {
					t.Fatal(ok, err)
				}
			}
			got, err := s.ListAfter(ctx, "p/", "p/x-001")
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 2 || got[0].Key != "p/x-002" || got[1].Key != "p/x-003" {
				t.Fatalf("ListAfter = %+v, want x-002, x-003 in key order", got)
			}
		})
	}
}

func TestStatReportsStoreTime(t *testing.T) {
	ctx := context.Background()
	clock := NewFakeClock(time.Unix(1_800_000_000, 0))
	s := NewMemStore(clock)
	if _, err := s.PutIfAbsent(ctx, "k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	clock.Advance(42 * time.Second)
	info, err := s.Stat(ctx, "k")
	if err != nil {
		t.Fatal(err)
	}
	if !info.Modified.Equal(time.Unix(1_800_000_000, 0)) || info.Size != 1 {
		t.Fatalf("Stat = %+v, want the write's store time and size", info)
	}
	if _, err := s.Stat(ctx, "missing"); err != process.ErrNotFound {
		t.Fatalf("Stat of a missing key: %v, want ErrNotFound", err)
	}
}

func TestMemStoreFaultsAreRetryable(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore(NewFakeClock(time.Unix(0, 0)))
	s.FailNext(2)
	for i := 0; i < 2; i++ {
		if err := s.Put(ctx, "k", []byte("v")); err == nil {
			t.Fatalf("put %d should fail", i)
		}
	}
	if err := s.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("third put: %v", err)
	}
	if _, err := s.Get(ctx, "k"); err != nil {
		t.Fatal(err)
	}
}
