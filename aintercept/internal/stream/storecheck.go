package stream

import (
	"context"
	"fmt"
	"time"
)

// StoreReport is what StoreCheck found about a store.
type StoreReport struct {
	CreateOnly     bool   // a second create of the same key is refused, and the first's bytes stay
	ListAfterWrite bool   // a new object is listed at once
	ReadAfterWrite bool   // a new object is read back at once
	StoreTime      bool   // Stat gives the store's write time
	Detail         string // what failed
}

// OK: the store has every property the processor rests on.
func (r StoreReport) OK() bool {
	return r.CreateOnly && r.ListAfterWrite && r.ReadAfterWrite && r.StoreTime
}

// StoreCheck tests a store for the primitives the bucketed processor needs
// (§0.3), under prefix, and cleans up after itself.
func StoreCheck(ctx context.Context, s Store, prefix string) (StoreReport, error) {
	var r StoreReport
	key := fmt.Sprintf("%sstorecheck-%d", prefix, time.Now().UnixNano())
	defer s.Delete(ctx, key)
	ok, err := s.PutIfAbsent(ctx, key, []byte("first"))
	if err != nil {
		return r, fmt.Errorf("create: %w", err)
	}
	if !ok {
		r.Detail = "a fresh key was refused"
		return r, nil
	}
	got, err := s.Get(ctx, key)
	r.ReadAfterWrite = err == nil && string(got) == "first"
	again, err := s.PutIfAbsent(ctx, key, []byte("second"))
	if err != nil {
		return r, fmt.Errorf("second create: %w", err)
	}
	got, _ = s.Get(ctx, key)
	r.CreateOnly = !again && string(got) == "first"
	if !r.CreateOnly {
		r.Detail = "a second create of the same key wasn't refused (no If-None-Match support)"
	}
	objs, err := s.ListAfter(ctx, prefix, "")
	if err != nil {
		return r, err
	}
	for _, o := range objs {
		if o.Key == key {
			r.ListAfterWrite = true
		}
	}
	info, err := s.Stat(ctx, key)
	r.StoreTime = err == nil && !info.Modified.IsZero()
	return r, nil
}
