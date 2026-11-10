package wsframe

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"strings"
	"testing"
)

// encodeFrame builds one frame. key != nil masks it (client→server).
func encodeFrame(fin, rsv1 bool, op byte, payload, key []byte) []byte {
	b0 := op
	if fin {
		b0 |= 0x80
	}
	if rsv1 {
		b0 |= 0x40
	}
	out := []byte{b0}
	mbit := byte(0)
	if key != nil {
		mbit = 0x80
	}
	switch n := len(payload); {
	case n < 126:
		out = append(out, mbit|byte(n))
	case n < 1<<16:
		out = append(out, mbit|126, byte(n>>8), byte(n))
	default:
		l := make([]byte, 8)
		binary.BigEndian.PutUint64(l, uint64(n))
		out = append(append(out, mbit|127), l...)
	}
	if key != nil {
		out = append(out, key...)
		p := append([]byte(nil), payload...)
		for i := range p {
			p[i] ^= key[i%4]
		}
		return append(out, p...)
	}
	return append(out, payload...)
}

// compressor produces permessage-deflate payloads; takeover keeps one
// deflate context across messages, as Codex/OpenAI do.
type compressor struct {
	takeover bool
	buf      bytes.Buffer
	w        *flate.Writer
}

func (c *compressor) compress(t *testing.T, msg string) []byte {
	t.Helper()
	if c.w == nil || !c.takeover {
		c.buf.Reset()
		c.w, _ = flate.NewWriter(&c.buf, flate.BestCompression)
	}
	start := c.buf.Len()
	if _, err := c.w.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
	if err := c.w.Flush(); err != nil {
		t.Fatal(err)
	}
	out := append([]byte(nil), c.buf.Bytes()[start:]...)
	if !bytes.HasSuffix(out, syncFlushTail) {
		t.Fatal("flush did not end with a sync marker")
	}
	return out[:len(out)-4]
}

func decodeAll(t *testing.T, d *Decoder, stream []byte, chunk int) []Message {
	t.Helper()
	var got []Message
	for len(stream) > 0 {
		n := chunk
		if n > len(stream) {
			n = len(stream)
		}
		ms, err := d.Feed(stream[:n])
		if err != nil {
			t.Fatalf("feed: %v", err)
		}
		got = append(got, ms...)
		stream = stream[n:]
	}
	return got
}

func texts(ms []Message) []string {
	var out []string
	for _, m := range ms {
		if m.Opcode == OpText || m.Opcode == OpBinary {
			out = append(out, string(m.Payload))
		}
	}
	return out
}

func TestPlainMaskedFragmentedAndControl(t *testing.T) {
	key := []byte{1, 2, 3, 4}
	big := strings.Repeat("x", 70000) // 64-bit length
	mid := strings.Repeat("y", 300)   // 16-bit length
	var s []byte
	s = append(s, encodeFrame(true, false, OpText, []byte(`{"type":"response.create"}`), key)...)
	s = append(s, encodeFrame(false, false, OpText, []byte(`{"type":"resp`), key)...)
	s = append(s, encodeFrame(true, false, OpPing, []byte("hb"), key)...) // control frame mid-message
	s = append(s, encodeFrame(true, false, OpContinuation, []byte(`onse.done"}`), key)...)
	s = append(s, encodeFrame(true, false, OpBinary, []byte(mid), nil)...)
	s = append(s, encodeFrame(true, false, OpText, []byte(big), nil)...)
	s = append(s, encodeFrame(true, false, OpClose, []byte{0x03, 0xe8, 'b', 'y', 'e'}, key)...)

	for _, chunk := range []int{1, 3, 7, 64, 1 << 20} {
		got := decodeAll(t, NewDecoder(false, false, 1<<20), s, chunk)
		want := []string{`{"type":"response.create"}`, `{"type":"response.done"}`, mid, big}
		if tx := texts(got); strings.Join(tx, "|") != strings.Join(want, "|") {
			t.Fatalf("chunk %d: data messages %q", chunk, tx)
		}
		var ping, closeCode int
		for _, m := range got {
			if m.Opcode == OpPing {
				ping++
			}
			if m.Opcode == OpClose {
				closeCode = m.CloseCode
			}
		}
		if ping != 1 || closeCode != 1000 {
			t.Fatalf("chunk %d: ping=%d close=%d", chunk, ping, closeCode)
		}
	}
}

func TestDeflateContextTakeover(t *testing.T) {
	// Repeated content makes later messages back-reference earlier ones,
	// which only decode if the window carries over.
	msgs := []string{
		`{"type":"response.created","response":{"id":"resp_1","model":"gpt-6.1-sol","status":"in_progress"}}`,
		`{"type":"response.output_text.delta","delta":"hello","response":{"id":"resp_1","model":"gpt-6.1-sol"}}`,
		`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-6.1-sol","status":"completed","usage":{"input_tokens":5}}}`,
	}
	for _, takeover := range []bool{true, false} {
		c := &compressor{takeover: takeover}
		var s []byte
		for i, m := range msgs {
			p := c.compress(t, m)
			if i == 2 { // also fragment a compressed message: RSV1 on the first frame only
				h := len(p) / 2
				s = append(s, encodeFrame(false, true, OpText, p[:h], nil)...)
				s = append(s, encodeFrame(true, false, OpContinuation, p[h:], nil)...)
				continue
			}
			s = append(s, encodeFrame(true, true, OpText, p, nil)...)
		}
		for _, chunk := range []int{1, 5, 1 << 20} {
			got := decodeAll(t, NewDecoder(true, !takeover, 1<<20), s, chunk)
			if tx := texts(got); strings.Join(tx, "|") != strings.Join(msgs, "|") {
				t.Fatalf("takeover=%v chunk=%d: got %q", takeover, chunk, tx)
			}
			for _, m := range got {
				if !m.Compressed {
					t.Fatalf("takeover=%v: message not marked compressed", takeover)
				}
			}
		}
	}
}

func TestLimitsAndMalformed(t *testing.T) {
	// An oversize frame is skipped and reported truncated; decoding continues.
	d := NewDecoder(false, false, 100)
	stream := append(encodeFrame(true, false, OpText, make([]byte, 101), nil), encodeFrame(true, false, OpText, []byte("next"), nil)...)
	for _, chunk := range []int{1, 7, 1 << 20} {
		d = NewDecoder(false, false, 100)
		got := decodeAll(t, d, stream, chunk)
		if len(got) != 2 || !got[0].Truncated || got[0].Payload != nil || got[0].WireBytes != 103 ||
			string(got[1].Payload) != "next" || got[1].Truncated || d.Broken() {
			t.Fatalf("chunk %d: oversize then normal: %+v broken=%v", chunk, got, d.Broken())
		}
	}
	// Fragments that add up past the cap: the whole message is skipped.
	d = NewDecoder(false, false, 100)
	s := append(encodeFrame(false, false, OpText, make([]byte, 60), nil), encodeFrame(false, false, OpContinuation, make([]byte, 60), nil)...)
	s = append(s, encodeFrame(true, false, OpContinuation, []byte("tail"), nil)...)
	s = append(s, encodeFrame(true, false, OpText, []byte("ok"), nil)...)
	if got := decodeAll(t, d, s, 3); len(got) != 2 || !got[0].Truncated || string(got[1].Payload) != "ok" {
		t.Fatalf("oversize fragmented message: %+v", got)
	}
	// Continuation with no message started.
	d = NewDecoder(false, false, 100)
	if _, err := d.Feed(encodeFrame(true, false, OpContinuation, []byte("x"), nil)); err == nil || !d.Broken() {
		t.Fatal("orphan continuation must fail")
	}
	if ms, err := d.Feed(encodeFrame(true, false, OpText, []byte("x"), nil)); ms != nil || err != nil {
		t.Fatal("a broken decoder must stay quiet")
	}
	// A control frame over 125 bytes is malformed.
	d = NewDecoder(false, false, 1000)
	if _, err := d.Feed(encodeFrame(true, false, OpPing, make([]byte, 126), nil)); err == nil {
		t.Fatal("oversize control frame must fail")
	}
	// Inflating past the cap: truncated, window kept, the next message decodes.
	c := &compressor{}
	big := c.compress(t, strings.Repeat("a", 10000))
	small := c.compress(t, "after")
	d = NewDecoder(true, false, 1000)
	got := decodeAll(t, d, append(encodeFrame(true, true, OpText, big, nil), encodeFrame(true, true, OpText, small, nil)...), 1<<20)
	if len(got) != 2 || !got[0].Truncated || len(got[0].Payload) != 1000 || string(got[1].Payload) != "after" {
		t.Fatalf("inflated past cap: %+v", got)
	}
	// Compression bomb: inflating far past the cap stops the decoder.
	bomb := (&compressor{}).compress(t, strings.Repeat("a", 20000))
	d = NewDecoder(true, true, 1000)
	if _, err := d.Feed(encodeFrame(true, true, OpText, bomb, nil)); err != ErrTooLarge {
		t.Fatalf("bomb: %v", err)
	}
	// Oversize compressed message with context takeover can't be skipped.
	d = NewDecoder(true, false, 10)
	if _, err := d.Feed(encodeFrame(true, true, OpText, c.compress(t, "q8Zr2Lw0Xk7Vb5Np3Ty1Jm9Hd4Gc6Fs"), nil)); err != ErrTooLarge {
		t.Fatalf("oversize compressed with takeover: %v", err)
	}
}

func TestParseExtensions(t *testing.T) {
	cases := map[string]Extensions{
		"permessage-deflate":                                                         {Deflate: true},
		"permessage-deflate; client_max_window_bits=15":                              {Deflate: true},
		"permessage-deflate; server_no_context_takeover; client_no_context_takeover": {Deflate: true, ServerNoContextTakeover: true, ClientNoContextTakeover: true},
		"":          {},
		"x-unknown": {},
	}
	for h, want := range cases {
		if got := ParseExtensions(h); got != want {
			t.Errorf("ParseExtensions(%q) = %+v, want %+v", h, got, want)
		}
	}
}

// Any bytes, any chunking: never panics, never returns more than the cap.
func FuzzFeed(f *testing.F) {
	c := &compressor{takeover: true}
	t := &testing.T{}
	f.Add(encodeFrame(true, true, OpText, c.compress(t, `{"type":"x"}`), nil), uint8(3))
	f.Add(encodeFrame(true, false, OpText, []byte("hi"), []byte{9, 9, 9, 9}), uint8(1))
	f.Fuzz(func(t *testing.T, data []byte, chunk uint8) {
		d := NewDecoder(true, false, 4096)
		step := int(chunk%16) + 1
		for len(data) > 0 {
			n := step
			if n > len(data) {
				n = len(data)
			}
			ms, _ := d.Feed(data[:n])
			for _, m := range ms {
				if len(m.Payload) > 4096 {
					t.Fatalf("payload %d exceeds cap", len(m.Payload))
				}
			}
			data = data[n:]
		}
	})
}
