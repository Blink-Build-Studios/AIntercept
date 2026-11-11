package wsframe

import (
	"crypto/rand"
	"encoding/binary"
)

// Encode builds one unfragmented, uncompressed WebSocket frame (RFC 6455
// §5.2). Frames a client sends must be masked; mask chooses a random key.
func Encode(opcode byte, payload []byte, mask bool) []byte {
	n := len(payload)
	out := make([]byte, 0, n+14)
	out = append(out, 0x80|opcode&0x0f)
	maskBit := byte(0)
	if mask {
		maskBit = 0x80
	}
	switch {
	case n < 126:
		out = append(out, maskBit|byte(n))
	case n <= 0xffff:
		out = append(out, maskBit|126)
		out = binary.BigEndian.AppendUint16(out, uint16(n))
	default:
		out = append(out, maskBit|127)
		out = binary.BigEndian.AppendUint64(out, uint64(n))
	}
	if !mask {
		return append(out, payload...)
	}
	var key [4]byte
	_, _ = rand.Read(key[:])
	out = append(out, key[:]...)
	start := len(out)
	out = append(out, payload...)
	for i := range out[start:] {
		out[start+i] ^= key[i%4]
	}
	return out
}

// CloseFrame builds a close frame with a status code and reason (truncated
// to fit a control frame's 125-byte payload).
func CloseFrame(code int, reason string, mask bool) []byte {
	p := binary.BigEndian.AppendUint16(nil, uint16(code))
	if len(reason) > 123 {
		reason = reason[:123]
	}
	return Encode(OpClose, append(p, reason...), mask)
}
