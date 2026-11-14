package state

import (
	"encoding/gob"
	"reflect"
	"testing"
)

type extTestValue struct{ A string }

func init() { gob.Register(extTestValue{}) }

// External inputs are journaled with the records: a checkpoint holds the ones
// set since the last, a decode or a merge gets each one's last value back, and
// setting a value it already holds journals nothing.
func TestExternalsAreJournaledWithTheState(t *testing.T) {
	s := New("s:x")
	if !s.SetExternal("a", extTestValue{"1"}) || !s.SetExternal("b", extTestValue{"2"}) {
		t.Fatal("a new value didn't change")
	}
	if s.SetExternal("a", extTestValue{"1"}) || s.SetExternal("c", nil) {
		t.Fatal("an unchanged value changed")
	}
	if s.Held() != 2 {
		t.Fatalf("held %d", s.Held())
	}
	c1, err := Encode(s)
	if err != nil {
		t.Fatal(err)
	}
	if s.Held() != 0 {
		t.Fatal("encode kept its journal")
	}
	s.SetExternal("a", extTestValue{"3"})
	s.SetExternal("b", nil)
	c2, err := Encode(s)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"a": extTestValue{"3"}}
	for name, chunks := range map[string][][]byte{"decode": {c1, c2}} {
		got, err := Decode(chunks...)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Externals(), want) {
			t.Fatalf("%s: %v", name, got.Externals())
		}
	}
	m, err := Merge(c1, c2)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(m)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Externals(), want) {
		t.Fatalf("merged: %v", got.Externals())
	}
}
