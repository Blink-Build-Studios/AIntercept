package state

import (
	"errors"
	"math/rand"
	"reflect"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/snap"
)

// Snapshots (docs/PIPELINE.md §2, v8's tests): a decoded snapshot equals the
// warm state it was written from, every index included, and goes on exactly
// as the warm state does: the same deltas for every record after it, and the
// same state at the end.

// keysOf is the records' states, applied in order.
func keysOf(rs []records.Record) map[string]*KeyState {
	out := map[string]*KeyState{}
	for _, r := range rs {
		s := out[r.RoutingKey]
		if s == nil {
			s = New(r.RoutingKey)
			out[r.RoutingKey] = s
		}
		s.Apply(r)
	}
	return out
}

type warmProbe struct {
	Runs  int
	Names []string
}

func TestSnapshotDecodesToTheWarmState(t *testing.T) {
	snap.Register(warmProbe{})
	for seed := int64(1); seed <= 6/seedFrac+1; seed++ {
		rng := rand.New(rand.NewSource(seed))
		rs := shuffled(rng, generateHours(rng, 3, 80), 20)
		half := len(rs) / 2
		warm := keysOf(rs[:half])
		cold := map[string]*KeyState{}
		for k, s := range warm {
			if _, err := Encode(s); err != nil { // the run's journal chunk, as the worker's
				t.Fatal(err)
			}
			b, err := EncodeSnapshot(s, warmProbe{Runs: 3, Names: []string{k}})
			if err != nil {
				t.Fatal(err)
			}
			if !IsSnapshot(b) {
				t.Fatal("a snapshot that isn't one")
			}
			l, err := Load(b, 0)
			if err != nil {
				t.Fatal(err)
			}
			if err := snap.Equal(s, l.State); err != nil {
				t.Fatalf("seed %d key %s: the decoded snapshot isn't the warm state: %v", seed, k, err)
			}
			if !l.Snapshot || l.Replayed != 0 || len(l.Changes) != 0 || !reflect.DeepEqual(l.Warm, warmProbe{Runs: 3, Names: []string{k}}) {
				t.Fatalf("seed %d key %s: loaded %+v", seed, k, l)
			}
			cold[k] = l.State
		}
		for i, r := range rs[half:] {
			k := r.RoutingKey
			if warm[k] == nil {
				warm[k], cold[k] = New(k), New(k)
			}
			dw, dc := warm[k].Apply(r), cold[k].Apply(r)
			if !reflect.DeepEqual(dw, dc) {
				t.Fatalf("seed %d record %d: the decoded state's delta isn't the warm one's:\n%+v\n%+v", seed, half+i, dc, dw)
			}
		}
		for k := range warm {
			if err := snap.Equal(warm[k], cold[k]); err != nil {
				t.Fatalf("seed %d key %s: after the same records: %v", seed, k, err)
			}
		}
	}
}

// The journal stays the key's checkpoint; a snapshot holds its first
// records. Load decodes the snapshot and applies the records after it, from
// whichever chunk holds them (a chunk the snapshot holds part of included),
// and every chunk's externals in order; it reports what the records after the
// snapshot changed (what the snapshot's render state hasn't seen).
func TestLoadAppliesTheJournalAfterTheSnapshot(t *testing.T) {
	snap.Register(int64(0))
	rng := rand.New(rand.NewSource(7))
	rs := generateHours(rng, 1, 120)
	s := New(rs[0].RoutingKey)
	var chunks [][]byte
	encode := func() {
		b, err := Encode(s)
		if err != nil {
			t.Fatal(err)
		}
		chunks = append(chunks, b)
	}
	for _, r := range rs[:30] {
		s.Apply(r)
	}
	s.SetExternal("x", int64(1))
	s.SetExternal("y", int64(1))
	encode() // records 0-29
	for _, r := range rs[30:40] {
		s.Apply(r)
	}
	snapshot, err := EncodeSnapshot(s, nil) // holds 40 records; the journal keeps 30-39
	if err != nil {
		t.Fatal(err)
	}
	if s.Journaled() != 10 {
		t.Fatalf("the snapshot took the journal: %d left", s.Journaled())
	}
	var want changeSet
	for _, r := range rs[40:90] {
		want.add(s.Apply(r).Changes)
	}
	s.SetExternal("x", int64(2))
	s.SetExternal("y", nil)
	encode() // records 30-89
	for _, r := range rs[90:] {
		want.add(s.Apply(r).Changes)
	}
	encode() // records 90-
	for name, l := range map[string]func() (*Loaded, error){
		"every chunk":         func() (*Loaded, error) { return Load(snapshot, 40, chunks...) },
		"from the straddling": func() (*Loaded, error) { return Load(snapshot, 10, chunks[1:]...) },
		"the journal alone":   func() (*Loaded, error) { return Load(nil, 0, chunks...) },
	} {
		got, err := l()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := snap.Equal(s, got.State); err != nil {
			t.Fatalf("%s: isn't the state: %v", name, err)
		}
		if name == "the journal alone" {
			continue
		}
		if got.Replayed != len(rs)-40 || !got.Snapshot {
			t.Fatalf("%s: replayed %d records, want %d", name, got.Replayed, len(rs)-40)
		}
		if !reflect.DeepEqual(got.Changes, want.list()) {
			t.Fatalf("%s: the loaded changes aren't the records' after the snapshot", name)
		}
	}
	if _, err := Load(snapshot, len(rs)+1, chunks...); err == nil {
		t.Fatal("skipped past the journal")
	}
	stale := append([]byte(nil), snapshot...)
	stale[len(snapMagic)] ^= 1
	if _, err := Load(stale, 40, chunks...); !errors.Is(err, ErrStaleSnapshot) {
		t.Fatalf("a snapshot of other types loaded: %v", err)
	}
	if d, err := Decode(chunks...); err != nil || snap.Equal(s, d) != nil {
		t.Fatalf("Decode of the journal: %v", err)
	}
}

// A change carries the row it replaced, and a record that writes one row
// twice keeps the first one's Old.
func TestChangesCarryTheirOldRow(t *testing.T) {
	var d Delta
	rs := rowSet{}
	rs.put(&d, "t", "a", 1)
	rs.put(&d, "t", "a", 2)
	rs.put(&d, "t", "b", 3)
	rs.drop(&d, "t", "b")
	got := lastChanges(d.Changes)
	want := []Change{{Table: "t", ID: "a", Row: 2, Old: nil}, {Table: "t", ID: "b", Row: nil, Old: nil}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
	var c changeSet
	c.add(got)
	c.add([]Change{{Table: "t", ID: "a", Row: 4, Old: 2}})
	if l := c.list(); len(l) != 1 || l[0].Old != nil || l[0].Row != 4 {
		t.Fatalf("net changes %+v: b came and went, a is new", l)
	}
	for i, r := range []any{1, 2} {
		var d Delta
		rs.put(&d, "t", "a", r)
		if d.Changes[0].Old != []any{2, 1}[i] {
			t.Fatalf("Old %v", d.Changes[0].Old)
		}
	}
}
