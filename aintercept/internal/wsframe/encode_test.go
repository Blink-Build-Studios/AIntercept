package wsframe

import (
	"bytes"
	"testing"
)

// Encoded frames decode back to the same message, at every length encoding,
// masked (client) and not (server).
func TestEncodeRoundTrip(t *testing.T) {
	for _, n := range []int{0, 5, 125, 126, 300, 65535, 65536, 70000} {
		for _, mask := range []bool{false, true} {
			p := bytes.Repeat([]byte("ab"), n/2+1)[:n]
			msgs, err := NewDecoder(false, false, 1<<20).Feed(Encode(OpText, p, mask))
			if err != nil || len(msgs) != 1 || msgs[0].Opcode != OpText || !bytes.Equal(msgs[0].Payload, p) {
				t.Fatalf("n=%d mask=%v: %v %+v", n, mask, err, msgs)
			}
		}
	}
	msgs, _ := NewDecoder(false, false, 1<<20).Feed(CloseFrame(1008, "policy", true))
	if len(msgs) != 1 || msgs[0].Opcode != OpClose || msgs[0].CloseCode != 1008 {
		t.Fatalf("close: %+v", msgs)
	}
}
