package stream

import (
	"context"
	"math/rand"
	"reflect"
	"sort"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream/state"
)

// The processor writes exactly the stream state's tables, each registered.
func TestStateModelTablesAreTheStateTables(t *testing.T) {
	var want []string
	for name := range state.TableTypes {
		want = append(want, name)
	}
	sort.Strings(want)
	got := StateModel{}.Tables()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tables %v, want %v", got, want)
	}
	for _, name := range got {
		if _, err := TableFor(name); err != nil {
			t.Fatal(err)
		}
	}
}

// The model's rows don't depend on the order a key's records come in, and a
// state checkpointed and restored mid-way carries on to the same rows.
func TestStateModelIsOrderFreeAndResumes(t *testing.T) {
	recs := newGen(3, 12).batch(1500)
	want := Reference(StateModel{}, recs)
	if len(nonEmpty(want)) < 3 {
		t.Fatalf("the synthetic records make only %d tables", len(nonEmpty(want)))
	}
	m := StateModel{}
	rng := rand.New(rand.NewSource(9))
	sh := append([]records.Record(nil), recs...)
	rng.Shuffle(len(sh), func(i, j int) { sh[i], sh[j] = sh[j], sh[i] })
	states := map[string]any{}
	got := map[string][]Row{}
	for i := range sh {
		r := &sh[i]
		st, ok := states[r.RoutingKey]
		if !ok {
			st = m.New(r.RoutingKey)
		}
		if i == len(sh)/2 { // checkpoint every key and restore it
			for k, s := range states {
				b, err := m.Encode(s)
				if err != nil {
					t.Fatal(err)
				}
				if states[k], err = m.Decode(k, b); err != nil {
					t.Fatal(err)
				}
			}
			if s, ok := states[r.RoutingKey]; ok {
				st = s
			}
		}
		for table, rows := range m.Apply(st, r) {
			got[table] = append(got[table], rows...)
		}
		states[r.RoutingKey] = st
	}
	for table, rows := range got {
		got[table] = MergeRows(rows)
	}
	if !tablesEqual(nonEmpty(got), nonEmpty(want)) {
		t.Fatalf("order and a checkpoint changed the tables: %s", diffTables(got, want))
	}
}

// The distributed replay with the real tables: ingest pods, replicas, chaos;
// every checkpoint and the end equal one pass over everything uploaded.
func TestStateModelPipelineSmoke(t *testing.T) {
	cfg := simCfg(4, 3, 3)
	cfg.Model = StateModel{}
	res, err := Simulate(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Checkpoints < 4 || res.Mismatches != 0 {
		t.Fatalf("%+v", res)
	}
	t.Logf("%+v", res)
}
