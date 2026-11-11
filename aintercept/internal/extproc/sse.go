package extproc

import (
	"bytes"
	"strings"
)

// sseSplitter cuts a streamed event-stream body into whole events as chunks
// arrive, for the Interceptor's OnMessage.
type sseSplitter struct {
	pending []byte
}

// sseEvent is one server-sent event: its raw bytes (with the blank line that
// ends it), its name and its data.
type sseEvent struct {
	raw  []byte
	name string
	data []byte
}

// feed adds a chunk and returns the events it completes.
func (s *sseSplitter) feed(chunk []byte) []sseEvent {
	s.pending = append(s.pending, chunk...)
	var out []sseEvent
	for {
		i, n := eventEnd(s.pending)
		if i < 0 {
			break
		}
		raw := s.pending[:i+n]
		out = append(out, parseSSE(append([]byte(nil), raw...)))
		s.pending = s.pending[i+n:]
	}
	if len(s.pending) == 0 {
		s.pending = nil
	}
	return out
}

// rest returns and clears bytes that never completed an event (a stream
// that ended without a final blank line).
func (s *sseSplitter) rest() []byte {
	r := s.pending
	s.pending = nil
	return r
}

func parseSSE(raw []byte) sseEvent {
	ev := sseEvent{raw: raw}
	var data [][]byte
	for _, line := range bytes.Split(raw, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		switch {
		case bytes.HasPrefix(line, []byte("event:")):
			ev.name = strings.TrimSpace(string(line[len("event:"):]))
		case bytes.HasPrefix(line, []byte("data:")):
			d := line[len("data:"):]
			data = append(data, bytes.TrimPrefix(d, []byte(" ")))
		}
	}
	ev.data = bytes.Join(data, []byte("\n"))
	return ev
}

// withData re-serializes an event with new data, keeping its other fields
// (event, id, retry) in place.
func (e sseEvent) withData(data []byte) []byte {
	var out bytes.Buffer
	wrote := false
	writeData := func() {
		for _, l := range bytes.Split(data, []byte("\n")) {
			out.WriteString("data: ")
			out.Write(l)
			out.WriteByte('\n')
		}
		wrote = true
	}
	for _, line := range bytes.Split(bytes.TrimRight(e.raw, "\r\n"), []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if bytes.HasPrefix(line, []byte("data:")) {
			if !wrote {
				writeData()
			}
			continue
		}
		out.Write(line)
		out.WriteByte('\n')
	}
	if !wrote {
		writeData()
	}
	out.WriteByte('\n')
	return out.Bytes()
}
