package zcodec

import (
	"bytes"
	"fmt"
	"runtime"
	"sync"
	"testing"
)

// What's encoded decodes back, from many goroutines at once.
func TestRoundTripConcurrent(t *testing.T) {
	c := New(4)
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				src := bytes.Repeat([]byte(fmt.Sprintf("row %d of %d; ", i, g)), 200)
				enc, err := c.Encode(nil, src)
				if err != nil {
					t.Error(err)
					return
				}
				dec, err := c.Decode(nil, enc)
				if err != nil || !bytes.Equal(dec, src) {
					t.Errorf("round trip failed: %v", err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}

// Encoders survive garbage collections: once warm, encoding allocates no new
// encoder (a fresh one costs megabytes), however often the collector runs.
func TestEncodersSurviveCollections(t *testing.T) {
	c := New(1)
	src := bytes.Repeat([]byte("a parquet page's worth of values; "), 4000)
	dst := make([]byte, 0, len(src))
	if _, err := c.Encode(dst, src); err != nil {
		t.Fatal(err)
	}
	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)
	for i := 0; i < 20; i++ {
		runtime.GC()
		if _, err := c.Encode(dst, src); err != nil {
			t.Fatal(err)
		}
	}
	runtime.ReadMemStats(&m1)
	if per := (m1.TotalAlloc - m0.TotalAlloc) / 20; per > 256<<10 {
		t.Fatalf("each encode after a collection allocates %d KiB: the encoder isn't kept", per>>10)
	}
}
