package extproc

import (
	"fmt"
	"strings"
	"testing"

	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// sse is a stream of `data:` events (Codex's form) or `event:` + `data:` (Anthropic's).
func sse(named bool, evs ...string) []byte {
	var b strings.Builder
	for _, ev := range evs {
		if named {
			typ := ev[strings.Index(ev, `"type":"`)+8:]
			fmt.Fprintf(&b, "event: %s\n", typ[:strings.IndexByte(typ, '"')])
		}
		fmt.Fprintf(&b, "data: %s\n\n", ev)
	}
	return []byte(b.String())
}

// httpCall runs one streamed HTTP call through the server, the response in
// awkward chunks, and returns its complete row.
func httpCall(t *testing.T, host, path string, resp []byte) events.Event {
	t.Helper()
	sink := &memSink{}
	proc := events.NewProcessor(sink, events.ProcessorConfig{BufferSize: 256})
	srv := New(Config{Processor: proc, InstanceID: "test", KeyHashSalt: "salt"})
	st := &streamState{correlationID: "call-1"}
	srv.handle(st, &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_RequestHeaders{
		RequestHeaders: headers(":authority", host, ":method", "POST", ":path", path),
	}})
	srv.handle(st, &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_RequestBody{
		RequestBody: &extprocv3.HttpBody{Body: []byte(`{"model":"m","stream":true}`), EndOfStream: true},
	}})
	srv.handle(st, &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_ResponseHeaders{
		ResponseHeaders: headers(":status", "200", "content-type", "text/event-stream"),
	}})
	for i := 0; i < len(resp); i += 7919 {
		end := min(i+7919, len(resp))
		srv.handle(st, &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_ResponseBody{
			ResponseBody: &extprocv3.HttpBody{Body: resp[i:end], EndOfStream: end == len(resp)},
		}})
	}
	proc.Close()
	for _, e := range sink.ev {
		if e.Phase == events.PhaseComplete {
			return e
		}
	}
	t.Fatalf("no complete row in %d events", len(sink.ev))
	return events.Event{}
}

// A Codex response well past the buffer's cap (they run to 1 MB) keeps its
// usage and outcome: response.completed comes last.
func TestCodexResponsePastTheCapKeepsItsUsage(t *testing.T) {
	item := `{"type":"response.output_item.done","item":{"type":"function_call","name":"exec_command","arguments":"` + strings.Repeat("x", 60_000) + `"}}`
	evs := []string{`{"type":"response.created","response":{"id":"resp_1","model":"gpt-6.1-sol"}}`}
	for range 15 { // about 900 KB of items and deltas
		evs = append(evs, item, `{"type":"response.output_text.delta","delta":"`+strings.Repeat("y", 1_000)+`"}`)
	}
	evs = append(evs, `{"type":"response.completed","response":{"id":"resp_1","model":"gpt-6.1-sol","status":"completed",`+
		`"usage":{"input_tokens":31200,"input_tokens_details":{"cached_tokens":28400},"output_tokens":640,"output_tokens_details":{"reasoning_tokens":212}}}}`)
	body := sse(false, evs...)
	if len(body) <= respBufferCap {
		t.Fatalf("response is %d bytes; the test needs it past the %d-byte cap", len(body), respBufferCap)
	}
	c := httpCall(t, "chatgpt.com", "/backend-api/codex/responses", body)
	if c.InputTokens != 31200 || c.OutputTokens != 640 || c.CacheReadTokens != 28400 || c.ReasoningTokens != 212 {
		t.Fatalf("usage lost past the cap: in %d out %d cache %d reasoning %d", c.InputTokens, c.OutputTokens, c.CacheReadTokens, c.ReasoningTokens)
	}
	if c.ResponseBytes != len(body) {
		t.Fatalf("response bytes %d, want %d", c.ResponseBytes, len(body))
	}
}

// Claude Code's output tokens and stop reason come last too (message_delta).
func TestAnthropicResponsePastTheCapKeepsItsOutputUsage(t *testing.T) {
	evs := []string{`{"type":"message_start","message":{"id":"msg_1","model":"claude-x","usage":{"input_tokens":12,"cache_read_input_tokens":40000,"output_tokens":1}}}`}
	for range 700 {
		evs = append(evs, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"`+strings.Repeat("z", 1_000)+`"}}`)
	}
	evs = append(evs, `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9876}}`, `{"type":"message_stop"}`)
	c := httpCall(t, "api.anthropic.com", "/v1/messages", sse(true, evs...))
	if c.InputTokens != 12 || c.CacheReadTokens != 40000 || c.OutputTokens != 9876 || c.StopReason != "end_turn" {
		t.Fatalf("usage past the cap: in %d cache %d out %d stop %q", c.InputTokens, c.CacheReadTokens, c.OutputTokens, c.StopReason)
	}
}

// A WebSocket turn whose completed items pass the cap keeps response.completed.
func TestWebSocketTurnPastTheCapKeepsItsUsage(t *testing.T) {
	sink := &memSink{}
	proc := events.NewProcessor(sink, events.ProcessorConfig{BufferSize: 256})
	srv := New(Config{Processor: proc, InstanceID: "test", KeyHashSalt: "salt"})
	st := &streamState{correlationID: "conn-1"}
	srv.handle(st, &extprocv3.ProcessingRequest{ObservabilityMode: true, Request: &extprocv3.ProcessingRequest_RequestHeaders{
		RequestHeaders: headers(":authority", "chatgpt.com", ":method", "GET", ":path", "/backend-api/codex/responses",
			"upgrade", "websocket", "authorization", "Bearer tok", "user-agent", "codex_exec/0.164.0"),
	}})
	srv.handle(st, &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_ResponseHeaders{
		ResponseHeaders: headers(":status", "101", "sec-websocket-extensions", "permessage-deflate"),
	}})
	up, dn := &deflater{}, &deflater{}
	down := func(b []byte) *extprocv3.ProcessingRequest {
		return &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_ResponseBody{ResponseBody: &extprocv3.HttpBody{Body: b}}}
	}
	srv.handle(st, &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_RequestBody{RequestBody: &extprocv3.HttpBody{
		Body: wsFrame(up.msg(t, `{"type":"response.create","model":"gpt-6.1-sol"}`), true),
	}}})
	item := `{"type":"response.output_item.done","item":{"type":"function_call","name":"exec_command","arguments":"` + strings.Repeat("x", 100_000) + `"}}`
	for range 8 { // 800 KB of completed items
		srv.handle(st, down(wsFrame(dn.msg(t, item), false)))
	}
	srv.handle(st, down(wsFrame(dn.msg(t, `{"type":"response.completed","response":{"id":"resp_1","model":"gpt-6.1-sol","usage":{"input_tokens":50,"output_tokens":5}}}`), false)))
	srv.wsClose(st)
	proc.Close()
	for _, e := range sink.ev {
		if e.Phase == events.PhaseComplete && e.Transport == events.TransportWebSocket {
			if e.InputTokens != 50 || e.OutputTokens != 5 {
				t.Fatalf("turn usage lost past the cap: in %d out %d", e.InputTokens, e.OutputTokens)
			}
			return
		}
	}
	t.Fatal("no turn row")
}

// The tail drops the event the cap cut into, keeps only final events, and
// reads CRLF-separated streams.
func TestSSETailKeepsWholeFinalEvents(t *testing.T) {
	var tail sseTail
	tail.feed([]byte("ype\":\"response.completed\",\"cut\":true}\n\n")) // the end of an event the cap cut
	tail.feed([]byte("data: {\"type\":\"response.output_text.delta\"}\n\ndata: {\"type\":\"response.comp"))
	tail.feed([]byte("leted\",\"n\":1}\r\n\r\nevent: error\ndata: {}\n\n"))
	got := string(tail.kept)
	want := "data: {\"type\":\"response.completed\",\"n\":1}\n\nevent: error\ndata: {}\n\n"
	if got != want {
		t.Fatalf("kept %q, want %q", got, want)
	}
	if b := withTail([]byte("head"), &tail); !strings.HasPrefix(string(b), "head\n\ndata: ") {
		t.Fatalf("the tail must start on its own line: %q", b[:20])
	}
}
