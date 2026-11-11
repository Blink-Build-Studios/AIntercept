package main

// Routes for the Interceptor end-to-end test (scripts/test-interceptor.sh):
// a provider-shaped event stream and JSON response that echo the request and
// say "silly clown", and a WebSocket echo. Synthetic content only.

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func interceptorRoutes(mux *http.ServeMux) {
	// POST /v1/messages: an Anthropic-style event stream echoing the request
	// body back as text, then an event that says the phrase.
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Fixture-Received", base64.StdEncoding.EncodeToString(body))
		f, _ := w.(http.Flusher)
		send := func(event string, v any) {
			b, _ := json.Marshal(v)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
			if f != nil {
				f.Flush()
			}
		}
		send("message_start", map[string]any{"type": "message_start", "message": map[string]any{"model": "fixture-model", "usage": map[string]int{"input_tokens": 12}}})
		send("content_block_delta", map[string]any{"type": "content_block_delta", "delta": map[string]string{"type": "text_delta", "text": "you sent: " + string(body)}})
		send("content_block_delta", map[string]any{"type": "content_block_delta", "delta": map[string]string{"type": "text_delta", "text": "what a silly clown"}})
		send("message_delta", map[string]any{"type": "message_delta", "delta": map[string]string{"stop_reason": "end_turn"}, "usage": map[string]int{"output_tokens": 7}})
		send("message_stop", map[string]any{"type": "message_stop"})
	})

	// POST /v1/json: a JSON body echoing the request and saying the phrase.
	mux.HandleFunc("/v1/json", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"received": string(body), "reply": "a silly clown"})
	})

	// GET /ws: a WebSocket that answers each text message with
	// "echo: <message> (silly clown)". No extensions.
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "websocket only", http.StatusBadRequest)
			return
		}
		key := r.Header.Get("Sec-WebSocket-Key")
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijack", http.StatusInternalServerError)
			return
		}
		conn, rw, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		h := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(h[:]))
		rw.Flush()
		for {
			op, payload, err := readFrame(rw.Reader)
			if err != nil {
				return
			}
			switch op {
			case 0x8:
				_, _ = conn.Write(frame(0x8, payload))
				return
			case 0x9:
				_, _ = conn.Write(frame(0xA, payload))
			case 0x1:
				_, _ = conn.Write(frame(0x1, []byte("echo: "+string(payload)+" (silly clown)")))
			}
		}
	})
}

// readFrame reads one (masked, unfragmented) client frame.
func readFrame(r *bufio.Reader) (byte, []byte, error) {
	var h [2]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	n := uint64(h[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return 0, nil, err
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return 0, nil, err
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	var key [4]byte
	masked := h[1]&0x80 != 0
	if masked {
		if _, err := io.ReadFull(r, key[:]); err != nil {
			return 0, nil, err
		}
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(r, p); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range p {
			p[i] ^= key[i%4]
		}
	}
	return h[0] & 0x0f, p, nil
}

// frame builds an unmasked server frame.
func frame(op byte, p []byte) []byte {
	out := []byte{0x80 | op}
	switch n := len(p); {
	case n < 126:
		out = append(out, byte(n))
	case n <= 0xffff:
		out = append(out, 126)
		out = binary.BigEndian.AppendUint16(out, uint16(n))
	default:
		out = append(out, 127)
		out = binary.BigEndian.AppendUint64(out, uint64(n))
	}
	return append(out, p...)
}
