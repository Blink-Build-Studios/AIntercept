// Package wsframe decodes one direction of a WebSocket connection (RFC 6455)
// from the raw byte chunks Envoy hands the observer: frame parsing across
// chunk boundaries, unmasking, fragmented messages, control frames, and
// permessage-deflate (RFC 7692) with or without context takeover.
//
// It only reads: decoding never affects the proxied connection. A message over
// the size cap is skipped (its bytes discarded as they arrive) and reported
// Truncated, and decoding continues. Only malformed input, or an oversize
// compressed message whose bytes the shared deflate history depends on, stops
// the decoder (Broken) instead of guessing.
package wsframe

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"io"
	"strings"
)

// Opcodes (RFC 6455 §5.2).
const (
	OpContinuation = 0x0
	OpText         = 0x1
	OpBinary       = 0x2
	OpClose        = 0x8
	OpPing         = 0x9
	OpPong         = 0xA
)

// Message is one complete WebSocket message (data messages reassembled from
// their fragments and decompressed) or control frame.
type Message struct {
	Opcode     byte
	Payload    []byte
	Compressed bool // was permessage-deflate compressed on the wire
	CloseCode  int  // OpClose only; 0 if absent
	WireBytes  int  // frame bytes on the wire, headers included
	// Truncated: the message exceeded the cap. Payload holds its first cap
	// bytes (decompressed), or nothing when the wire bytes were skipped.
	Truncated bool
}

// Extensions is the negotiated permessage-deflate configuration, from the
// server's 101 response Sec-WebSocket-Extensions header.
type Extensions struct {
	Deflate                 bool
	ClientNoContextTakeover bool
	ServerNoContextTakeover bool
}

// ParseExtensions reads a Sec-WebSocket-Extensions header value.
func ParseExtensions(h string) Extensions {
	var e Extensions
	for _, ext := range strings.Split(h, ",") {
		params := strings.Split(ext, ";")
		if strings.TrimSpace(strings.ToLower(params[0])) != "permessage-deflate" {
			continue
		}
		e.Deflate = true
		for _, p := range params[1:] {
			switch strings.TrimSpace(strings.ToLower(strings.SplitN(p, "=", 2)[0])) {
			case "client_no_context_takeover":
				e.ClientNoContextTakeover = true
			case "server_no_context_takeover":
				e.ServerNoContextTakeover = true
			}
		}
		return e // the first permessage-deflate offer is the accepted one
	}
	return e
}

// windowSize is the deflate history kept between messages (the maximum
// LZ77 window; a smaller negotiated max_window_bits only references less).
const windowSize = 32 << 10

// ErrTooLarge is returned when an oversize message can't be skipped: it is
// compressed with context takeover, so later messages need its bytes. The
// decoder is Broken from then on.
var ErrTooLarge = errors.New("wsframe: message exceeds size cap")

// maxInflateRatio bounds how far one message may inflate (output beyond the
// cap is discarded, but still costs CPU), relative to the cap.
const maxInflateRatio = 16

// Decoder decodes one direction. Not safe for concurrent use.
type Decoder struct {
	deflate      bool
	noTakeover   bool
	maxMessage   int
	pending      []byte // undecoded bytes carried to the next chunk
	frag         []byte // payload of a fragmented message in progress
	fragOp       byte
	fragCompress bool
	fragWire     int
	fragging     bool
	fragSkip     bool   // the message in progress is oversize; its payload is discarded
	skipLeft     uint64 // payload bytes of the current skipped frame still to discard
	skipFin      bool   // the frame being skipped ends its message
	window       []byte // deflate history (context takeover)
	broken       bool
}

// NewDecoder decodes a direction. deflate and noContextTakeover come from the
// negotiated Extensions for this direction (client→server uses
// ClientNoContextTakeover, server→client ServerNoContextTakeover). maxMessage
// caps what is buffered for one message (its fragments plus the frame being
// read) and its decompressed size.
func NewDecoder(deflate, noContextTakeover bool, maxMessage int) *Decoder {
	return &Decoder{deflate: deflate, noTakeover: noContextTakeover, maxMessage: maxMessage}
}

// Buffered is the bytes the decoder holds between chunks: a partial frame, a
// fragmented message in progress, and the deflate window.
func (d *Decoder) Buffered() int { return len(d.pending) + len(d.frag) + len(d.window) }

// Broken reports whether the decoder hit malformed input or a cap and stopped.
func (d *Decoder) Broken() bool { return d.broken }

// Feed adds the next chunk of this direction's bytes and returns every message
// completed by it. After an error the decoder is Broken and Feed returns nil.
func (d *Decoder) Feed(chunk []byte) ([]Message, error) {
	if d.broken {
		return nil, nil
	}
	var out []Message
	if d.skipLeft > 0 { // the rest of an oversize frame: discard as it arrives
		n := min(uint64(len(chunk)), d.skipLeft)
		d.skipLeft -= n
		d.fragWire += int(n)
		chunk = chunk[n:]
		if d.skipLeft > 0 {
			return nil, nil
		}
		if d.skipFin {
			out = append(out, d.finishSkipped())
		}
	}
	d.pending = append(d.pending, chunk...)
	for {
		h, ok, err := parseHeader(d.pending)
		if err != nil {
			return out, d.fail(err)
		}
		if !ok {
			break
		}
		if h.opcode >= 0x8 && (h.length > 125 || !h.fin) {
			return out, d.fail(errors.New("wsframe: invalid control frame"))
		}
		// One budget for the message: its fragments so far plus this frame.
		if h.opcode < 0x8 && (d.fragSkip && h.opcode == OpContinuation ||
			h.length > uint64(d.maxMessage) || uint64(len(d.frag))+h.length > uint64(d.maxMessage)) {
			done, err := d.skipFrame(h)
			if err != nil {
				return out, d.fail(err)
			}
			if done != nil {
				out = append(out, *done)
			}
			if d.skipLeft > 0 {
				break
			}
			continue
		}
		end := h.hdr + int(h.length)
		if len(d.pending) < end {
			break
		}
		f := frame{fin: h.fin, rsv1: h.rsv1, opcode: h.opcode, payload: append([]byte(nil), d.pending[h.hdr:end]...)}
		if h.key != nil {
			for i := range f.payload {
				f.payload[i] ^= h.key[i%4]
			}
		}
		d.pending = d.pending[end:]
		m, done, err := d.assemble(f, end)
		if err != nil {
			return out, d.fail(err)
		}
		if done {
			out = append(out, m)
		}
	}
	if len(d.pending) == 0 {
		d.pending = nil // release the backing array between frames
	}
	return out, nil
}

// skipFrame discards an oversize data frame (and marks its message skipped),
// consuming what is buffered and leaving skipLeft for the rest. It returns the
// truncated message when this frame ends it.
func (d *Decoder) skipFrame(h header) (*Message, error) {
	if h.opcode != OpContinuation {
		if d.fragging {
			return nil, errors.New("wsframe: new message before previous finished")
		}
		d.fragOp, d.fragCompress, d.fragWire, d.fragging = h.opcode, h.rsv1 && d.deflate, 0, true
	} else if !d.fragging {
		return nil, errors.New("wsframe: continuation without a message")
	}
	// Later messages are inflated against this one's output; without it the
	// history is unknown, so stop rather than mis-decode.
	if d.fragCompress && !d.noTakeover {
		return nil, ErrTooLarge
	}
	d.fragSkip, d.frag = true, nil
	d.fragWire += h.hdr
	avail := uint64(len(d.pending) - h.hdr)
	if avail < h.length {
		d.fragWire += int(avail)
		d.skipLeft, d.skipFin = h.length-avail, h.fin
		d.pending = nil
		return nil, nil
	}
	d.fragWire += int(h.length)
	d.pending = d.pending[h.hdr+int(h.length):]
	if !h.fin {
		return nil, nil
	}
	m := d.finishSkipped()
	return &m, nil
}

// finishSkipped ends a skipped message.
func (d *Decoder) finishSkipped() Message {
	m := Message{Opcode: d.fragOp, Compressed: d.fragCompress, WireBytes: d.fragWire, Truncated: true}
	d.frag, d.fragging, d.fragSkip, d.skipFin = nil, false, false, false
	return m
}

func (d *Decoder) fail(err error) error {
	d.broken = true
	d.pending, d.frag, d.window = nil, nil, nil
	return err
}

type frame struct {
	fin     bool
	rsv1    bool
	opcode  byte
	payload []byte // unmasked
}

// header is a parsed frame header.
type header struct {
	fin, rsv1 bool
	opcode    byte
	length    uint64
	key       []byte // masking key, nil when unmasked
	hdr       int    // header bytes
}

// parseHeader parses the frame header at the start of b. ok=false means b
// doesn't yet hold the whole header.
func parseHeader(b []byte) (h header, ok bool, err error) {
	if len(b) < 2 {
		return h, false, nil
	}
	h.fin, h.rsv1, h.opcode = b[0]&0x80 != 0, b[0]&0x40 != 0, b[0]&0x0f
	masked := b[1]&0x80 != 0
	h.length, h.hdr = uint64(b[1]&0x7f), 2
	switch h.length {
	case 126:
		if len(b) < 4 {
			return h, false, nil
		}
		h.length, h.hdr = uint64(binary.BigEndian.Uint16(b[2:4])), 4
	case 127:
		if len(b) < 10 {
			return h, false, nil
		}
		h.length, h.hdr = binary.BigEndian.Uint64(b[2:10]), 10
		if h.length>>63 != 0 {
			return h, false, errors.New("wsframe: invalid frame length")
		}
	}
	if masked {
		if len(b) < h.hdr+4 {
			return h, false, nil
		}
		h.key, h.hdr = b[h.hdr:h.hdr+4], h.hdr+4
	}
	return h, true, nil
}

// assemble handles control frames and (fragmented) data messages.
func (d *Decoder) assemble(f frame, wire int) (Message, bool, error) {
	if f.opcode >= 0x8 { // control frames: never fragmented, never compressed
		m := Message{Opcode: f.opcode, Payload: f.payload, WireBytes: wire}
		if f.opcode == OpClose && len(f.payload) >= 2 {
			m.CloseCode = int(binary.BigEndian.Uint16(f.payload[:2]))
		}
		return m, true, nil
	}
	if f.opcode != OpContinuation { // first frame of a message
		if d.fragging {
			return Message{}, false, errors.New("wsframe: new message before previous finished")
		}
		d.fragOp, d.fragCompress, d.frag, d.fragWire, d.fragging = f.opcode, f.rsv1 && d.deflate, nil, 0, true
	} else if !d.fragging {
		return Message{}, false, errors.New("wsframe: continuation without a message")
	}
	d.frag = append(d.frag, f.payload...)
	d.fragWire += wire
	if !f.fin {
		return Message{}, false, nil
	}
	m := Message{Opcode: d.fragOp, Payload: d.frag, Compressed: d.fragCompress, WireBytes: d.fragWire}
	d.frag, d.fragging = nil, false
	if m.Compressed {
		plain, truncated, err := d.inflate(m.Payload)
		if err != nil {
			return Message{}, false, err
		}
		m.Payload, m.Truncated = plain, truncated
	}
	return m, true, nil
}

// syncFlushTail is the empty stored block the sender strips from each
// compressed message (RFC 7692 §7.2.1); it is re-appended before inflating.
var syncFlushTail = []byte{0x00, 0x00, 0xff, 0xff}

// inflate decompresses one message. Each compressed message ends on a block
// boundary, so the only state carried between messages is the LZ77 window,
// supplied as a preset dictionary. Output past the cap is discarded (the
// message is reported truncated), but still inflated so the window stays right.
func (d *Decoder) inflate(p []byte) (out []byte, truncated bool, err error) {
	in := append(append(make([]byte, 0, len(p)+4), p...), syncFlushTail...)
	r := flate.NewReaderDict(bytes.NewReader(in), d.window)
	buf := make([]byte, 32<<10)
	var tail []byte // the output's last bytes, for the window
	total := 0
	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			total += n
			if total > maxInflateRatio*d.maxMessage {
				return nil, false, ErrTooLarge
			}
			if keep := min(n, d.maxMessage-len(out)); keep > 0 {
				out = append(out, buf[:keep]...)
			}
			truncated = truncated || total > d.maxMessage
			if !d.noTakeover {
				tail = append(tail, buf[:n]...)
				if len(tail) > 2*windowSize {
					tail = append([]byte(nil), tail[len(tail)-windowSize:]...)
				}
			}
		}
		// The stream deliberately has no final block, so running out of input
		// after the sync flush is the normal end of a message.
		if rerr == io.EOF || errors.Is(rerr, io.ErrUnexpectedEOF) {
			break
		}
		if rerr != nil {
			return nil, false, rerr
		}
	}
	if !d.noTakeover {
		d.window = append(d.window, tail...)
		if len(d.window) > windowSize {
			d.window = append([]byte(nil), d.window[len(d.window)-windowSize:]...)
		}
	}
	return out, truncated, nil
}
