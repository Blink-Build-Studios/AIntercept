package stream

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
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

// Conditional writes (docs/PIPELINE.md §3.1, fencing): If-Match on the current
// ETag lands, anything else is ErrPrecondition and changes nothing. The same
// cases run against S3 (moto) in TestConditionalPutAgainstS3.
func TestConditionalPut(t *testing.T) {
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) { conditionalPutCases(t, s, "cond/") })
	}
}

func conditionalPutCases(t *testing.T, s Store, prefix string) {
	ctx := context.Background()
	key := prefix + "s/abc/head"
	expect := func(want string) {
		t.Helper()
		b, err := s.Get(ctx, key)
		if err != nil || string(b) != want {
			t.Fatalf("object = %q %v, want %q", b, err, want)
		}
	}
	if _, err := s.ETag(ctx, key); !errors.Is(err, process.ErrNotFound) {
		t.Fatalf("ETag of a missing key: %v, want ErrNotFound", err)
	}
	// If-Match on a missing object fails (DO Spaces 412s; AWS 404s: both are a lost race).
	if _, err := s.PutIf(ctx, key, []byte("v0"), "0123456789abcdef0123456789abcdef"); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("If-Match on a missing key: %v, want ErrPrecondition", err)
	}
	// If-None-Match: * creates.
	e1, err := s.PutIf(ctx, key, []byte("v1"), "")
	if err != nil || e1 == "" {
		t.Fatalf("create: %q %v", e1, err)
	}
	if got, err := s.ETag(ctx, key); err != nil || got != e1 {
		t.Fatalf("ETag after create = %q %v, want the create's %q", got, err, e1)
	}
	if strings.Contains(e1, `"`) {
		t.Fatalf("ETags are unquoted: %q", e1)
	}
	// ... and refuses an existing object.
	if _, err := s.PutIf(ctx, key, []byte("other"), ""); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("If-None-Match on an existing key: %v, want ErrPrecondition", err)
	}
	expect("v1")
	// If-Match on the current ETag lands, quoted or not.
	e2, err := s.PutIf(ctx, key, []byte("v2"), e1)
	if err != nil || e2 == "" || e2 == e1 {
		t.Fatalf("If-Match current: %q %v (was %q)", e2, err, e1)
	}
	e3, err := s.PutIf(ctx, key, []byte("v3"), `"`+e2+`"`)
	if err != nil {
		t.Fatalf("If-Match with a quoted current ETag: %v", err)
	}
	// A stale ETag fails and changes nothing.
	if _, err := s.PutIf(ctx, key, []byte("stale"), e1); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("If-Match stale: %v, want ErrPrecondition", err)
	}
	expect("v3")
	if got, _ := s.ETag(ctx, key); got != e3 {
		t.Fatalf("ETag = %q, want %q", got, e3)
	}
}

// Racers holding the same ETag: exactly one lands, the rest get ErrPrecondition.
func TestConditionalPutRacersOneWins(t *testing.T) {
	ctx := context.Background()
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			for _, create := range []bool{true, false} {
				key := fmt.Sprintf("race/%v", create)
				etag := ""
				if !create {
					var err error
					if etag, err = s.PutIf(ctx, key, []byte("base"), ""); err != nil {
						t.Fatal(err)
					}
				}
				const n = 16
				errs := make([]error, n)
				var wg sync.WaitGroup
				for i := range n {
					wg.Add(1)
					go func() {
						defer wg.Done()
						_, errs[i] = s.PutIf(ctx, key, []byte(fmt.Sprint("racer ", i)), etag)
					}()
				}
				wg.Wait()
				won := -1
				for i, err := range errs {
					switch {
					case err == nil && won >= 0:
						t.Fatalf("create=%v: racers %d and %d both won", create, won, i)
					case err == nil:
						won = i
					case !errors.Is(err, ErrPrecondition):
						t.Fatalf("racer %d: %v", i, err)
					}
				}
				if won < 0 {
					t.Fatalf("create=%v: no racer won", create)
				}
				if b, _ := s.Get(ctx, key); string(b) != fmt.Sprint("racer ", won) {
					t.Fatalf("create=%v: object %q, want the winner's", create, b)
				}
			}
		})
	}
}
