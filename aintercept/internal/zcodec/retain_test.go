package zcodec

import (
	"bytes"
	"runtime"
	"testing"
)

// A pooled decoder doesn't keep what it decoded alive (a buffer the size of
// its output): a key's snapshot is ~100 MB decoded, and every pooled decoder
// holding its last one would cost the worker that much each.
func TestDecodeDoesntKeepItsOutput(t *testing.T) {
	c := Shared
	const size = 100_000_000
	z, err := c.Encode(nil, bytes.Repeat([]byte("abcdefghij0123456789"), size/20))
	if err != nil {
		t.Fatal(err)
	}
	heap := func() int64 {
		runtime.GC()
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return int64(m.HeapAlloc)
	}
	before := heap()
	out, err := c.Decode(nil, z)
	if err != nil || len(out) != size {
		t.Fatalf("decoded %d bytes: %v", len(out), err)
	}
	out = nil
	if kept := heap() - before; kept > size/4 {
		t.Fatalf("the decoder keeps %d bytes alive after a %d-byte decode", kept, size)
	}
}
