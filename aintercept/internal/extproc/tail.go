package extproc

import (
	"bytes"
	"strings"
)

// finalEventCap bounds one final event kept past the response buffer's cap.
// Codex's response.completed repeats the turn's whole output, so it can be
// large; past this it is dropped like any other overflow.
const finalEventCap = 8 << 20

// finalEvents are the events that carry a call's usage and outcome, at the end
// of its stream: Anthropic's message_delta (output tokens, stop reason) and
// message_stop; Codex / Responses API's terminal response.* events and error.
var finalEvents = map[string]bool{
	"message_delta":       true,
	"message_stop":        true,
	"response.completed":  true,
	"response.done":       true,
	"response.failed":     true,
	"response.incomplete": true,
	"error":               true,
}

// sseTail keeps a streamed (SSE) response's final events once the response
// buffer is full. The buffer keeps the head (the model, tool calls, input
// usage); the usage the extractors need most often comes last, so without
// this a response over the cap lost its token counts and stop reason.
type sseTail struct {
	pending []byte // the bytes since the last event boundary
	partial bool   // pending began mid-event (where the buffer's cap cut it)
	kept    []byte // the final events, each ending in a blank line
}

// feed takes the bytes past the cap. The first call's bytes start wherever
// the cap cut the stream, so their first event is partial and isn't kept.
func (t *sseTail) feed(b []byte) {
	if t.pending == nil && t.kept == nil {
		t.partial = true
	}
	t.pending = append(t.pending, b...)
	for {
		i, n := eventEnd(t.pending)
		if i < 0 {
			if len(t.pending) > finalEventCap { // one oversized event: drop it, and the rest of it
				t.pending, t.partial = t.pending[:0], true
			}
			return
		}
		ev := t.pending[:i]
		if !t.partial && finalEvents[sseType(ev)] && len(t.kept)+len(ev) <= finalEventCap {
			t.kept = append(append(t.kept, ev...), '\n', '\n')
		}
		t.partial = false
		t.pending = append(t.pending[:0], t.pending[i+n:]...)
	}
}

// held is the bytes the tail holds, for the memory budget.
func (t *sseTail) held() int { return len(t.pending) + len(t.kept) }

// eventEnd finds the blank line that ends the first event: its index and length.
func eventEnd(b []byte) (int, int) {
	lf, crlf := bytes.Index(b, []byte("\n\n")), bytes.Index(b, []byte("\r\n\r\n"))
	switch {
	case lf < 0 && crlf < 0:
		return -1, 0
	case crlf >= 0 && (lf < 0 || crlf < lf):
		return crlf, 4
	default:
		return lf, 2
	}
}

// sseType is an event's type: its `event:` line, else its data's "type".
func sseType(ev []byte) string {
	for _, line := range strings.Split(string(ev), "\n") {
		line = strings.TrimRight(line, "\r")
		if v, ok := strings.CutPrefix(line, "event:"); ok {
			return strings.TrimSpace(v)
		}
		if v, ok := strings.CutPrefix(line, "data:"); ok {
			return messageType([]byte(strings.TrimSpace(v)))
		}
	}
	return ""
}

// withTail is the response buffer with the kept final events after it, on
// lines of their own (the buffer may end mid-line where the cap cut it).
func withTail(buf []byte, t *sseTail) []byte {
	if t == nil || len(t.kept) == 0 {
		return buf
	}
	out := make([]byte, 0, len(buf)+2+len(t.kept))
	return append(append(append(out, buf...), '\n', '\n'), t.kept...)
}
