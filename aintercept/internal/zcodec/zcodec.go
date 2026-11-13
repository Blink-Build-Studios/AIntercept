// Package zcodec is the zstd compression for the Parquet files the pipeline
// writes, with a fixed set of encoders and decoders kept for the process's
// life. parquet-go's own zstd codec pools them in a pool the garbage
// collector empties, so a busy process keeps allocating new ones (megabytes
// of window each). These are made once, at most one per CPU, and reused.
package zcodec

import (
	"runtime"

	"github.com/klauspost/compress/zstd"
	"github.com/parquet-go/parquet-go/format"
)

// window is the encoder's window: Parquet pages are small, so a megabyte
// compresses them as well as the default (8 MB) and keeps each encoder small.
const window = 1 << 20

// Codec implements parquet-go's compress.Codec.
type Codec struct {
	enc chan *zstd.Encoder
	dec chan *zstd.Decoder
}

// Shared is the codec every writer and reader uses.
var Shared = New(runtime.GOMAXPROCS(0))

// New is a codec with at most n encoders and n decoders, each made on first need.
func New(n int) *Codec {
	c := &Codec{enc: make(chan *zstd.Encoder, n), dec: make(chan *zstd.Decoder, n)}
	for i := 0; i < n; i++ {
		c.enc <- nil
		c.dec <- nil
	}
	return c
}

func (c *Codec) String() string                            { return "ZSTD" }
func (c *Codec) CompressionCodec() format.CompressionCodec { return format.Zstd }

func (c *Codec) Encode(dst, src []byte) ([]byte, error) {
	e := <-c.enc
	if e == nil {
		var err error
		if e, err = zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1), zstd.WithWindowSize(window),
			zstd.WithZeroFrames(true), zstd.WithEncoderCRC(false)); err != nil {
			c.enc <- nil
			return nil, err
		}
	}
	defer func() { c.enc <- e }()
	return e.EncodeAll(src, dst[:0]), nil
}

func (c *Codec) Decode(dst, src []byte) ([]byte, error) {
	d := <-c.dec
	if d == nil {
		var err error
		if d, err = zstd.NewReader(nil, zstd.WithDecoderConcurrency(1)); err != nil {
			c.dec <- nil
			return nil, err
		}
	}
	defer func() { c.dec <- d }()
	return d.DecodeAll(src, dst[:0])
}
