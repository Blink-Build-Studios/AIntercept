package state

import (
	"math/rand"
	"reflect"
	"testing"
)

// Every table a Delta writes has one row type, declared in TableTypes, and
// every row the generated sequences emit is of it: the processor registers each
// table's file format from TableTypes, so a row of another type couldn't be
// written.
func TestEveryTableHasOneRowType(t *testing.T) {
	seen := map[string]bool{}
	rng := rand.New(rand.NewSource(7))
	batchN = map[string]int64{}
	rs := append(generateEvents(rng, 3, 200), generateSessions(rng, 4, 200)...)
	states := map[string]*KeyState{}
	for _, r := range shuffled(rng, rs, 20) {
		s := states[r.RoutingKey]
		if s == nil {
			s = New(r.RoutingKey)
			states[r.RoutingKey] = s
		}
		for _, c := range s.Apply(r).Changes {
			seen[c.Table] = true
			want, ok := TableTypes[c.Table]
			if !ok {
				t.Fatalf("table %s isn't in TableTypes", c.Table)
			}
			if c.Row != nil && reflect.TypeOf(c.Row) != reflect.TypeOf(want) {
				t.Fatalf("table %s: row of type %T, declared %T", c.Table, c.Row, want)
			}
		}
	}
	for table, v := range TableTypes {
		found := false
		for _, rt := range RowTypes {
			if reflect.TypeOf(rt) == reflect.TypeOf(v) {
				found = true
			}
		}
		if !found {
			t.Errorf("table %s's row type %T isn't in RowTypes", table, v)
		}
	}
	t.Logf("%d of %d tables written by the generated sequences", len(seen), len(TableTypes))
}
