package state

import (
	"math/rand"
	"sort"
	"testing"
)

// The ordered map against a plain map and a sort, under random sets and deletes.
func TestOmapAgainstAMap(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	m := newOmap[int64, int](func(a, b int64) bool { return a < b })
	ref := map[int64]int{}
	for i := 0; i < 20000; i++ {
		k := int64(rng.Intn(3000))
		if rng.Intn(3) == 0 {
			m.Delete(k)
			delete(ref, k)
		} else {
			m.Set(k, i)
			ref[k] = i
		}
		if i%997 == 0 || i == 19999 {
			var keys []int64
			for k := range ref {
				keys = append(keys, k)
			}
			sort.Slice(keys, func(a, b int) bool { return keys[a] < keys[b] })
			var got []int64
			m.All(func(k int64, v int) bool {
				if ref[k] != v {
					t.Fatalf("value of %d: %d, want %d", k, v, ref[k])
				}
				got = append(got, k)
				return true
			})
			if len(got) != len(keys) || m.Len() != len(keys) {
				t.Fatalf("len %d/%d, want %d", len(got), m.Len(), len(keys))
			}
			for j := range keys {
				if got[j] != keys[j] {
					t.Fatalf("order at %d: %d, want %d", j, got[j], keys[j])
				}
			}
			for q := int64(-1); q < 3001; q += 37 {
				j := sort.Search(len(keys), func(j int) bool { return keys[j] > q }) - 1
				fk, _, ok := m.Floor(q)
				if (j >= 0) != ok || ok && fk != keys[j] {
					t.Fatalf("Floor(%d) = %d %v, want index %d", q, fk, ok, j)
				}
			}
		}
	}
}
