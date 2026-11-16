package main

// Routes for the Interceptor end-to-end test (scripts/test-interceptor.sh):
// a provider-shaped event stream and JSON response that echo the request and
// say "silly clown", and a WebSocket echo.
// A request carrying "fixture_tool" gets a reply that calls that tool
// instead, and one carrying "fixture_text" a reply whose text arrives in
// those pieces, "fixture_gap_ms" apart (toolReply). Synthetic content only.

import (
	"bufio"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func interceptorRoutes(mux *http.ServeMux) {
	// POST /v1/messages: an Anthropic-style event stream echoing the request
	// body back as text, then an event that says the phrase.
	mux.HandleFunc("/v1/messages", messages)

	// POST /v1/json: a JSON body echoing the request and saying the phrase.
	mux.HandleFunc("/v1/json", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"received": string(body), "reply": "a silly clown"})
	})

	// GET /ws: a WebSocket that answers each text message with
	// "echo: <message> (silly clown)". No extensions.
	mux.HandleFunc("/ws", wsEcho)

	// Codex's path (contract v2's end-to-end test): its WebSocket, as /ws,
	// and over HTTP, as /v1/messages.
	mux.HandleFunc("/backend-api/codex/responses", func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			wsEcho(w, r)
			return
		}
		messages(w, r)
	})

	// Chat Completions (contract v2's end-to-end test), as /v1/messages.
	mux.HandleFunc("/v1/chat/completions", messages)
}

// messages answers with an event stream echoing the request body. The body
// is also in X-Fixture-Received when it's small enough for a header, and its
// SHA-256 always is in X-Fixture-Received-Sha256.
func messages(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "text/event-stream")
	if len(body) <= 16<<10 {
		w.Header().Set("X-Fixture-Received", base64.StdEncoding.EncodeToString(body))
	}
	sum := sha256.Sum256(body)
	w.Header().Set("X-Fixture-Received-Sha256", hex.EncodeToString(sum[:]))
	if toolReply(w, r.URL.Path, body) {
		return
	}
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
}

// wsEcho is a WebSocket that answers each text message with
// "echo: <message> (silly clown)". No extensions.
func wsEcho(w http.ResponseWriter, r *http.Request) {
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
			if evs := toolEvents("responses", payload); evs != nil {
				gap := requestGap(payload)
				for _, e := range evs {
					_, _ = conn.Write(frame(0x1, e.data))
					time.Sleep(gap)
				}
				continue
			}
			_, _ = conn.Write(frame(0x1, []byte("echo: "+string(payload)+" (silly clown)")))
		}
	}
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

// fixtureTool is a request's "fixture_tool": the tool call its reply makes.
type fixtureTool struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type event struct {
	name string
	data []byte
}

func ev(v map[string]any) event {
	b, _ := json.Marshal(v)
	return event{name: v["type"].(string), data: b}
}

// fixtureReply is what a request asks the fixture's reply to be: its text
// in pieces ("fixture_text"; default "Let me look."), then a tool call
// ("fixture_tool"), the events fixture_gap_ms apart.
type fixtureReply struct {
	Tool  *fixtureTool `json:"fixture_tool"`
	Text  []string     `json:"fixture_text"`
	GapMS int          `json:"fixture_gap_ms"`
}

func requestReply(body []byte) fixtureReply {
	var r fixtureReply
	_ = json.Unmarshal(body, &r)
	return r
}

// requestTool is a request's "fixture_tool" (nil when it has none).
func requestTool(body []byte) *fixtureTool { return requestReply(body).Tool }

func requestGap(body []byte) time.Duration {
	return time.Duration(requestReply(body).GapMS) * time.Millisecond
}

// toolEvents is the reply to a request with "fixture_tool" or
// "fixture_text", as events in format ("anthropic", "responses" or "chat"):
// the text, then the tool call, its arguments in two pieces. Nil for any
// other request.
func toolEvents(format string, body []byte) []event {
	rr := requestReply(body)
	tool := rr.Tool
	if tool == nil && rr.Text == nil {
		return nil
	}
	pieces := rr.Text
	if pieces == nil {
		pieces = []string{"Let me look."}
	}
	full := strings.Join(pieces, "")
	var args string
	var half int
	if tool != nil {
		args = string(tool.Arguments)
		half = len(args) / 2
	}
	switch format {
	case "anthropic":
		evs := []event{
			ev(map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_fixture", "type": "message", "role": "assistant", "model": "fixture-model", "content": []any{}, "usage": map[string]int{"input_tokens": 12, "output_tokens": 1}}}),
			ev(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]string{"type": "text", "text": ""}}),
		}
		for _, p := range pieces {
			evs = append(evs, ev(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "text_delta", "text": p}}))
		}
		evs = append(evs, ev(map[string]any{"type": "content_block_stop", "index": 0}))
		stop := "end_turn"
		if tool != nil {
			stop = "tool_use"
			evs = append(evs,
				ev(map[string]any{"type": "content_block_start", "index": 1, "content_block": map[string]any{"type": "tool_use", "id": "toolu_fixture", "name": tool.Name, "input": map[string]any{}}}),
				ev(map[string]any{"type": "content_block_delta", "index": 1, "delta": map[string]string{"type": "input_json_delta", "partial_json": args[:half]}}),
				ev(map[string]any{"type": "content_block_delta", "index": 1, "delta": map[string]string{"type": "input_json_delta", "partial_json": args[half:]}}),
				ev(map[string]any{"type": "content_block_stop", "index": 1}))
		}
		return append(evs,
			ev(map[string]any{"type": "message_delta", "delta": map[string]string{"stop_reason": stop}, "usage": map[string]int{"output_tokens": 9}}),
			ev(map[string]any{"type": "message_stop"}))
	case "chat":
		chunk := func(delta map[string]any, finish any) event {
			return event{data: mustJSON(map[string]any{"id": "chatcmpl-fixture", "object": "chat.completion.chunk", "created": 1, "model": "fixture-model",
				"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})}
		}
		evs := []event{chunk(map[string]any{"role": "assistant", "content": ""}, nil)}
		for _, p := range pieces {
			evs = append(evs, chunk(map[string]any{"content": p}, nil))
		}
		finish := "stop"
		if tool != nil {
			finish = "tool_calls"
			evs = append(evs,
				chunk(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call_fixture", "type": "function", "function": map[string]string{"name": tool.Name, "arguments": args[:half]}}}}, nil),
				chunk(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]string{"arguments": args[half:]}}}}, nil))
		}
		return append(evs, chunk(map[string]any{}, finish),
			event{data: mustJSON(map[string]any{"id": "chatcmpl-fixture", "object": "chat.completion.chunk", "created": 1, "model": "fixture-model", "choices": []any{},
				"usage": map[string]int{"prompt_tokens": 12, "completion_tokens": 9, "total_tokens": 21}})},
			event{data: []byte("[DONE]")})
	}
	msg := map[string]any{"id": "msg_fixture", "type": "message", "status": "completed", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": full, "annotations": []any{}}}}
	resp := func(status string, output []any, usage any) map[string]any {
		return map[string]any{"id": "resp_fixture", "object": "response", "status": status, "model": "fixture-model", "output": output, "usage": usage}
	}
	seq := 0
	next := func(m map[string]any) event {
		m["sequence_number"] = seq
		seq++
		return ev(m)
	}
	evs := []event{
		next(map[string]any{"type": "response.created", "response": resp("in_progress", []any{}, nil)}),
		next(map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"id": "msg_fixture", "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}}),
	}
	for _, p := range pieces {
		evs = append(evs, next(map[string]any{"type": "response.output_text.delta", "item_id": "msg_fixture", "output_index": 0, "content_index": 0, "delta": p}))
	}
	evs = append(evs,
		next(map[string]any{"type": "response.output_text.done", "item_id": "msg_fixture", "output_index": 0, "content_index": 0, "text": full}),
		next(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": msg}))
	output := []any{msg}
	if tool != nil {
		call := map[string]any{"id": "fc_fixture", "type": "function_call", "status": "completed", "arguments": args, "call_id": "call_fixture", "name": tool.Name}
		output = append(output, call)
		evs = append(evs,
			next(map[string]any{"type": "response.output_item.added", "output_index": 1, "item": map[string]any{"id": "fc_fixture", "type": "function_call", "status": "in_progress", "arguments": "", "call_id": "call_fixture", "name": tool.Name}}),
			next(map[string]any{"type": "response.function_call_arguments.delta", "item_id": "fc_fixture", "output_index": 1, "delta": args[:half]}),
			next(map[string]any{"type": "response.function_call_arguments.delta", "item_id": "fc_fixture", "output_index": 1, "delta": args[half:]}),
			next(map[string]any{"type": "response.function_call_arguments.done", "item_id": "fc_fixture", "output_index": 1, "arguments": args}),
			next(map[string]any{"type": "response.output_item.done", "output_index": 1, "item": call}))
	}
	return append(evs, next(map[string]any{"type": "response.completed", "response": resp("completed", output, map[string]int{"input_tokens": 12, "output_tokens": 9, "total_tokens": 21})}))
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// toolReply answers a request with "fixture_tool" (false for any other): an
// event stream, or with "stream": false a JSON body, in the path's format.
func toolReply(w http.ResponseWriter, path string, body []byte) bool {
	format := "anthropic"
	switch {
	case strings.HasPrefix(path, "/backend-api/codex/"):
		format = "responses"
	case path == "/v1/chat/completions":
		format = "chat"
	}
	evs := toolEvents(format, body)
	if evs == nil {
		return false
	}
	var req struct {
		Stream *bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &req)
	if req.Stream != nil && !*req.Stream { // the whole reply
		var whole []byte
		rr := requestReply(body)
		text := "Let me look."
		if rr.Text != nil {
			text = strings.Join(rr.Text, "")
		}
		switch format {
		case "anthropic":
			content := []any{map[string]string{"type": "text", "text": text}}
			stop := "end_turn"
			if tool := rr.Tool; tool != nil {
				content, stop = append(content, map[string]any{"type": "tool_use", "id": "toolu_fixture", "name": tool.Name, "input": tool.Arguments}), "tool_use"
			}
			whole, _ = json.Marshal(map[string]any{"id": "msg_fixture", "type": "message", "role": "assistant", "model": "fixture-model",
				"content": content, "stop_reason": stop, "usage": map[string]int{"input_tokens": 12, "output_tokens": 9}})
		case "chat":
			msg := map[string]any{"role": "assistant", "content": text}
			finish := "stop"
			if tool := rr.Tool; tool != nil {
				msg["tool_calls"], finish = []any{map[string]any{"id": "call_fixture", "type": "function", "function": map[string]string{"name": tool.Name, "arguments": string(tool.Arguments)}}}, "tool_calls"
			}
			whole, _ = json.Marshal(map[string]any{"id": "chatcmpl-fixture", "object": "chat.completion", "created": 1, "model": "fixture-model",
				"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish}},
				"usage":   map[string]int{"prompt_tokens": 12, "completion_tokens": 9, "total_tokens": 21}})
		default: // the completed response
			var last struct {
				Response json.RawMessage `json:"response"`
			}
			_ = json.Unmarshal(evs[len(evs)-1].data, &last)
			whole = last.Response
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(whole)
		return true
	}
	w.Header().Set("Content-Type", "text/event-stream")
	f, _ := w.(http.Flusher)
	gap := requestGap(body)
	for _, e := range evs {
		if e.name != "" {
			fmt.Fprintf(w, "event: %s\n", e.name)
		}
		fmt.Fprintf(w, "data: %s\n\n", e.data)
		if f != nil {
			f.Flush()
		}
		time.Sleep(gap)
	}
	return true
}
