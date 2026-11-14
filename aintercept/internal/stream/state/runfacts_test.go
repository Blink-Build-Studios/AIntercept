package state_test

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/drawer"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/render"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/rollup"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream/state"
)

// The run's running values (docs/PIPELINE.md §2, v8): after every record,
// every wire set or taken back, and after calls expire, the state's Facts give
// what rollup.FactsOf computes from every row, and its held batches and
// unwired calls what the drawer's walks over every row find, without reading
// the rows. A snapshot holds them (TestSnapshotDecodesToTheWarmState).
func checkFacts(t *testing.T, label string, key string, s *state.KeyState) {
	t.Helper()
	// The batches it holds and its calls with no wire: the run's running
	// values, as the drawer's walks over every row find them.
	if got, want := s.HeldBatches(), drawer.Batches(s.Rows()); !reflect.DeepEqual(got, want) && len(got)+len(want) > 0 {
		t.Fatalf("%s: held batches %v, the rows' %v", label, got, want)
	}
	for _, b := range drawer.Batches(s.Rows()) {
		if !s.HoldsBatch(b) {
			t.Fatalf("%s: doesn't hold %s", label, b)
		}
	}
	want := map[string]int64{}
	for id, end := range drawer.EndedCalls(s.Rows()) {
		if _, ok := s.External(render.WireID(id)); !ok {
			want[id] = end
		}
	}
	if got := s.Unwired(); !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: unwired %v, the rows' %v", label, got, want)
	}
	for _, now := range []int64{1_700_000_000_000_000_000, 1_800_000_000_000_000_000} {
		got, want := rollup.FactsFrom(key, s.Facts(), now), rollup.FactsOf(key, s.Rows(), now)
		if got != want {
			t.Fatalf("%s: running facts %+v, the rows' %+v", label, got, want)
		}
	}
}

func TestRunningFactsAreTheRows(t *testing.T) {
	for _, c := range state.AtomicCases() {
		state.ResetBatches()
		var rs []records.Record
		if c.Before != nil {
			rs = c.Before()
		}
		rs = append(rs, c.Rec())
		o := state.NewStream(nil)
		for i, r := range rs {
			o.Apply(r)
			checkFacts(t, fmt.Sprintf("%s record %d", c.Name, i), r.RoutingKey, o.States()[r.RoutingKey])
		}
		for k, s := range o.States() {
			s.Expire(1 << 62)
			checkFacts(t, c.Name+" expired", k, s)
		}
	}
	for seed := int64(1); seed <= 8/state.SeedFrac+1; seed++ {
		rng := rand.New(rand.NewSource(seed))
		state.ResetBatches()
		rs := state.Shuffled(rng, state.GenerateHours(rng, 3, 100), 20)
		o := state.NewStream(nil)
		for i, r := range rs {
			o.Apply(r)
			s := o.States()[r.RoutingKey]
			// Wires set, missing and taken back, as the worker's runs do.
			for id := range drawer.EndedCalls(s.Rows()) {
				switch rng.Intn(4) {
				case 0:
					s.SetExternal(render.WireID(id), drawer.Missing{At: 1})
				case 1:
					s.SetExternal(render.WireID(id), nil)
				}
			}
			checkFacts(t, fmt.Sprintf("seed %d record %d", seed, i), r.RoutingKey, s)
			if i == len(rs)/2 {
				for k, s := range o.States() {
					s.Expire(1 << 62)
					checkFacts(t, fmt.Sprintf("seed %d expired", seed), k, s)
				}
			}
		}
	}
	// A key with no rows takes its commit's day; an a: key has none; a c: key its own.
	for _, key := range []string{"s:empty", "a:acct", "c:cred:2026-11-04", "ip:10.0.0.1:2026-11-04T07"} {
		checkFacts(t, key, key, state.New(key))
	}
}
