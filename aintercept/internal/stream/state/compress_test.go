package state

import (
	"bytes"
	"encoding/gob"
	"reflect"
	"testing"
)

// A checkpoint is compressed (zstd): a session's journal repeats its event
// metadata and call headers, so the chunk is a fraction of the records' gob.
// A chunk written before compression (plain gob) still decodes and merges.
func TestCheckpointIsCompressed(t *testing.T) {
	s := New("s:z")
	recs := mixed("z", "a", 2_000, 0)
	for _, r := range recs {
		s.Apply(r)
	}
	s.SetExternal("a", extTestValue{"1"})
	var plain bytes.Buffer
	if err := gob.NewEncoder(&plain).Encode(journalChunk{Key: s.Key, Records: s.journal, Externals: s.extJournal}); err != nil {
		t.Fatal(err)
	}
	b, err := Encode(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(b)*5 > plain.Len() {
		t.Errorf("checkpoint %d bytes, its records' gob %d: not compressed", len(b), plain.Len())
	}
	for name, chunks := range map[string][][]byte{"compressed": {b}, "plain gob": {plain.Bytes()}} {
		got, err := Decode(chunks...)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !reflect.DeepEqual(got.Rows(), s.Rows()) || !reflect.DeepEqual(got.Externals(), s.Externals()) {
			t.Fatalf("%s: decodes to another state", name)
		}
	}
	m, err := Merge(plain.Bytes(), b)
	if err != nil {
		t.Fatal(err)
	}
	if len(m)*5 > 2*plain.Len() {
		t.Errorf("a merged checkpoint isn't compressed: %d bytes", len(m))
	}
	if _, err := Decode(m); err != nil {
		t.Fatal(err)
	}
}

// Compaction keeps each external input's last value once: what Decode would
// end with, so a value that changes on every run (other keys' parts of a
// batch) doesn't pile up in the merged chunk. A removal is kept, since the
// merged chunk may follow others that set the value.
func TestMergeKeepsEachExternalOnce(t *testing.T) {
	s := New("s:m")
	var chunks [][]byte
	for i := 0; i < 8; i++ {
		s.SetExternal("parts:b", extTestValue{string(rune('a' + i))})
		if i == 5 {
			s.SetExternal("gone", extTestValue{"x"})
		}
		if i == 6 {
			s.SetExternal("gone", nil)
		}
		c, err := Encode(s)
		if err != nil {
			t.Fatal(err)
		}
		chunks = append(chunks, c)
	}
	m, err := Merge(chunks...)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := unpack(m)
	if err != nil {
		t.Fatal(err)
	}
	var c journalChunk
	if err := gob.NewDecoder(bytes.NewReader(raw)).Decode(&c); err != nil {
		t.Fatal(err)
	}
	if len(c.Externals) != 2 {
		t.Fatalf("merged externals: %v", c.Externals)
	}
	got, err := Decode(m)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]any{"parts:b": extTestValue{"h"}}; !reflect.DeepEqual(got.Externals(), want) {
		t.Fatalf("decoded: %v", got.Externals())
	}
}
