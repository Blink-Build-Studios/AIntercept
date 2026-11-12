package stream

import (
	"reflect"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// The stand-in model and the row machinery the pipeline is tested with.

// State files (never queried) hold each key's state as an opaque blob.
func TestStateFileRoundTrip(t *testing.T) {
	rows := []StateRow{{Key: "s:a", Data: []byte(`{"n":1}`)}, {Key: "s:b", Data: []byte("x")}}
	b, err := EncodeState(rows)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeState(b)
	if err != nil || !reflect.DeepEqual(got, rows) {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestMergeRowsNewestWinsAndTombstonesDrop(t *testing.T) {
	older := []Row{{Key: "k", ID: "1", Value: "a"}, {Key: "k", ID: "2", Value: "b"}}
	newer := []Row{{Key: "k", ID: "1", Value: "A"}, {Key: "k", ID: "2", Tomb: true}, {Key: "j", ID: "3", Value: "c"}}
	got := MergeRows(older, newer)
	want := []Row{{Key: "j", ID: "3", Value: "c"}, {Key: "k", ID: "1", Value: "A"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

func TestCountModelIsOrderFree(t *testing.T) {
	g := newGen(5, 4)
	rs := g.batch(300)
	want := Reference(CountModel{}, rs)
	rev := make([]records.Record, len(rs))
	for i := range rs {
		rev[len(rs)-1-i] = rs[i]
	}
	// Reference sorts within a key, so feed the model directly in reverse.
	m := CountModel{}
	states := map[string]any{}
	got := map[string][]Row{}
	for i := range rev {
		r := &rev[i]
		st, ok := states[r.RoutingKey]
		if !ok {
			st = m.New(r.RoutingKey)
			states[r.RoutingKey] = st
		}
		for table, rows := range m.Apply(st, r) {
			got[table] = MergeRows(got[table], rows)
		}
	}
	if !reflect.DeepEqual(canonical(got), canonical(want)) {
		t.Fatal("the stand-in model depends on order")
	}
}

func TestRecordTSPicksTheRecordsOwnTime(t *testing.T) {
	cases := []struct {
		r    records.Record
		want int64
	}{
		{records.Record{Call: &canon.Request{StartNS: 5, EndNS: 9}}, 9},
		{records.Record{Call: &canon.Request{StartNS: 5}}, 5},
		{records.Record{Event: &canon.Event{RowNS: 3, NS: 4}}, 3},
		{records.Record{Account: &records.Account{NS: 7}}, 7},
	}
	for i, c := range cases {
		if got := RecordTS(&c.r); got != c.want {
			t.Errorf("case %d: %d, want %d", i, got, c.want)
		}
	}
}
