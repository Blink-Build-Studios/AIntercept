package extproc

import (
	"bytes"
	"compress/flate"
	"sync"
	"testing"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

type memSink struct {
	mu sync.Mutex
	ev []events.Event
}

func (m *memSink) Emit(e events.Event) error {
	m.mu.Lock()
	m.ev = append(m.ev, e)
	m.mu.Unlock()
	return nil
}
func (m *memSink) Flush() error { return nil }

// deflater compresses messages with one shared context (context takeover).
type deflater struct {
	buf bytes.Buffer
	w   *flate.Writer
}

func (d *deflater) msg(t *testing.T, s string) []byte {
	if d.w == nil {
		d.w, _ = flate.NewWriter(&d.buf, flate.DefaultCompression)
	}
	start := d.buf.Len()
	d.w.Write([]byte(s))
	d.w.Flush()
	out := append([]byte(nil), d.buf.Bytes()[start:]...)
	return out[:len(out)-4] // strip the sync-flush tail, per RFC 7692
}

func wsFrame(payload []byte, mask bool) []byte {
	b := []byte{0x80 | 0x40 | 0x1} // FIN, RSV1 (compressed), text
	mbit := byte(0)
	if mask {
		mbit = 0x80
	}
	if n := len(payload); n < 126 {
		b = append(b, mbit|byte(n))
	} else {
		b = append(b, mbit|126, byte(n>>8), byte(n))
	}
	if mask {
		key := []byte{7, 3, 9, 1}
		b = append(b, key...)
		p := append([]byte(nil), payload...)
		for i := range p {
			p[i] ^= key[i%4]
		}
		return append(b, p...)
	}
	return append(b, payload...)
}

func headers(kv ...string) *extprocv3.HttpHeaders {
	hm := &corev3.HeaderMap{}
	for i := 0; i < len(kv); i += 2 {
		hm.Headers = append(hm.Headers, &corev3.HeaderValue{Key: kv[i], RawValue: []byte(kv[i+1])})
	}
	return &extprocv3.HttpHeaders{Headers: hm}
}

func TestWebSocketTurnBecomesCompleteRow(t *testing.T) {
	sink := &memSink{}
	proc := events.NewProcessor(sink, events.ProcessorConfig{BufferSize: 256})
	srv := New(Config{Processor: proc, InstanceID: "test", KeyHashSalt: "salt"})
	st := &streamState{correlationID: "conn-1"}

	srv.handle(st, &extprocv3.ProcessingRequest{ObservabilityMode: true, Request: &extprocv3.ProcessingRequest_RequestHeaders{
		RequestHeaders: headers(":authority", "chatgpt.com", ":method", "GET", ":path", "/backend-api/codex/responses",
			"upgrade", "websocket", "authorization", "Bearer tok", "chatgpt-account-id", "acct-1", "user-agent", "codex_exec/0.164.0"),
	}})
	srv.handle(st, &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_ResponseHeaders{
		ResponseHeaders: headers(":status", "101", "sec-websocket-extensions", "permessage-deflate"),
	}})
	if st.ws == nil {
		t.Fatal("101 to an upgrade request must start WebSocket decoding")
	}

	up, down := &deflater{}, &deflater{}
	create := `{"type":"response.create","model":"gpt-6.1-sol","stream":true,"reasoning":{"effort":"low"},"previous_response_id":"resp_0",` +
		`"client_metadata":{"x-codex-turn-metadata":"{\"installation_id\":\"inst-1\",\"session_id\":\"sess-1\",\"turn_id\":\"turn-2\"}"},` +
		`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`
	body := func(dir string, b []byte) *extprocv3.ProcessingRequest {
		hb := &extprocv3.HttpBody{Body: b}
		if dir == "up" {
			return &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_RequestBody{RequestBody: hb}}
		}
		return &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_ResponseBody{ResponseBody: hb}}
	}
	srv.handle(st, body("up", wsFrame(up.msg(t, create), true)))

	var stream []byte
	for _, ev := range []string{
		`{"type":"codex.rate_limits","plan_type":"plus","rate_limits":{"primary":{"used_percent":1,"window_minutes":300,"reset_at":1793651151}}}`,
		`{"type":"response.created","response":{"id":"resp_1","model":"gpt-6.1-sol","status":"in_progress"}}`,
		`{"type":"response.output_item.added","item":{"type":"custom_tool_call","name":"exec"}}`,
		`{"type":"response.custom_tool_call_input.delta","delta":"text(await tools.mcp__fs__"}`,
		`{"type":"response.output_item.done","item":{"type":"custom_tool_call","name":"exec","input":"text(await tools.mcp__fs__read_text_file({path:\"/w\"}));"}}`,
		`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-6.1-sol","service_tier":"default","usage":{"input_tokens":120,"input_tokens_details":{"cached_tokens":100},"output_tokens":7}}}`,
	} {
		stream = append(stream, wsFrame(down.msg(t, ev), false)...)
	}
	for i := 0; i < len(stream); i += 9 { // awkward chunk boundaries
		end := i + 9
		if end > len(stream) {
			end = len(stream)
		}
		srv.handle(st, body("down", stream[i:end]))
	}
	srv.wsClose(st)
	proc.Close()

	var turn, conn *events.Event
	for i := range sink.ev {
		switch e := &sink.ev[i]; {
		case e.Phase == events.PhaseComplete && e.Transport == events.TransportWebSocket:
			turn = e
		case e.Phase == events.PhaseWSConnection:
			conn = e
		case e.Phase == events.PhaseComplete:
			t.Fatalf("the upgrade itself must not emit an HTTP complete row: %+v", e)
		}
	}
	if turn == nil || conn == nil {
		t.Fatalf("want a turn row and a connection row, got %d events", len(sink.ev))
	}
	if turn.Model != "gpt-6.1-sol" || turn.Effort != "low" || !turn.Stream || turn.WSTurn != 1 ||
		turn.InputTokens != 120 || turn.OutputTokens != 7 || turn.CacheReadTokens != 100 ||
		turn.MessageID != "resp_1" || turn.PreviousMessageID != "resp_0" || turn.ServiceTier != "default" {
		t.Fatalf("turn fields: %+v", turn)
	}
	var mcp bool
	for _, u := range turn.ToolUses {
		mcp = mcp || (u.Server == "fs" && u.Tool == "read_text_file")
	}
	if !mcp {
		t.Fatalf("tool_uses must include the MCP call from the exec script: %+v", turn.ToolUses)
	}
	if turn.SessionID != "sess-1" || turn.DeviceID != "inst-1" || turn.AccountUUID != "acct-1" ||
		turn.ClientMetadata["turn_id"] != "turn-2" || turn.KeyHash == "" {
		t.Fatalf("turn identity: session=%q device=%q account=%q md=%v", turn.SessionID, turn.DeviceID, turn.AccountUUID, turn.ClientMetadata)
	}
	if turn.Provider["x-codex-plan-type"] != "plus" || turn.Provider["x-codex-primary-used-percent"] != "1" {
		t.Fatalf("in-band rate limits: %v", turn.Provider)
	}
	w := conn.WebSocket
	if w == nil || w.Turns != 1 || !w.Deflate || w.Undecodable || w.MessagesUp != 1 || w.MessagesDown != 6 ||
		w.MessageTypes["outbound:response.completed"] != 1 || conn.AccountUUID != "acct-1" {
		t.Fatalf("connection row: %+v %+v", conn, w)
	}
}

// After the connection row is emitted (response end of stream), the client
// may still send frames; they must not touch the row the drain goroutine is
// serializing. Run with -race.
func TestWebSocketFramesAfterCloseDontTouchEmittedRow(t *testing.T) {
	sink := &memSink{}
	proc := events.NewProcessor(sink, events.ProcessorConfig{BufferSize: 256})
	srv := New(Config{Processor: proc, InstanceID: "test", KeyHashSalt: "salt"})
	st := &streamState{correlationID: "conn-2"}
	srv.handle(st, &extprocv3.ProcessingRequest{ObservabilityMode: true, Request: &extprocv3.ProcessingRequest_RequestHeaders{
		RequestHeaders: headers(":authority", "chatgpt.com", ":method", "GET", ":path", "/ws", "upgrade", "websocket"),
	}})
	srv.handle(st, &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_ResponseHeaders{
		ResponseHeaders: headers(":status", "101"),
	}})
	plain := func(s string) []byte { // unmasked-bit irrelevant here; uncompressed text frame
		f := wsFrame([]byte(s), true)
		f[0] = 0x81
		return f
	}
	up := func(b []byte) *extprocv3.ProcessingRequest {
		return &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_RequestBody{RequestBody: &extprocv3.HttpBody{Body: b}}}
	}
	srv.handle(st, up(plain(`{"type":"hello"}`)))
	srv.wsClose(st)
	for i := 0; i < 200; i++ {
		srv.handle(st, up(plain(`{"type":"late"}`)))
	}
	proc.Close()
	for _, e := range sink.ev {
		if e.Phase == events.PhaseWSConnection {
			if e.WebSocket.MessageTypes["inbound:late"] != 0 || e.WebSocket.MessageTypes["inbound:hello"] != 1 {
				t.Fatalf("message types: %v", e.WebSocket.MessageTypes)
			}
			return
		}
	}
	t.Fatal("no connection row")
}

func TestMessageType(t *testing.T) {
	for in, want := range map[string]string{
		`{"type":"response.create","x":1}`:   "response.create",
		` { "type" : "a.b" }`:                "a.b",
		`{"x":1,"type":"later"}`:             "later",
		`not json`:                           "<non-json>",
		`{"type":"response.completed","resp`: "response.completed", // truncated message
	} {
		if got := messageType([]byte(in)); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

// A turn's start is a row of its own (at response.create), so a turn that's
// still streaming can be shown: it names the turn and its Codex identity.
func TestWebSocketTurnStartRow(t *testing.T) {
	sink := &memSink{}
	proc := events.NewProcessor(sink, events.ProcessorConfig{BufferSize: 256})
	srv := New(Config{Processor: proc, InstanceID: "test", KeyHashSalt: "salt"})
	st := &streamState{correlationID: "conn-3"}
	srv.handle(st, &extprocv3.ProcessingRequest{ObservabilityMode: true, Request: &extprocv3.ProcessingRequest_RequestHeaders{
		RequestHeaders: headers(":authority", "chatgpt.com", ":method", "GET", ":path", "/backend-api/codex/responses",
			"upgrade", "websocket", "authorization", "Bearer tok", "user-agent", "codex_exec/0.164.0", "originator", "codex_exec"),
	}})
	srv.handle(st, &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_ResponseHeaders{
		ResponseHeaders: headers(":status", "101"),
	}})
	create := `{"type":"response.create","model":"gpt-6.1-sol",` +
		`"client_metadata":{"x-codex-turn-metadata":"{\"thread_id\":\"thr-1\",\"turn_id\":\"turn-1\"}"}}`
	frame := wsFrame([]byte(create), true)
	frame[0] = 0x81 // uncompressed text
	srv.handle(st, &extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_RequestBody{
		RequestBody: &extprocv3.HttpBody{Body: frame}}})
	proc.Close()

	var start *events.Event
	for i := range sink.ev {
		if sink.ev[i].Phase == events.PhaseWSTurnStart {
			start = &sink.ev[i]
		}
	}
	if start == nil {
		t.Fatalf("no turn start row among %d events", len(sink.ev))
	}
	if start.WSTurn != 1 || start.Transport != events.TransportWebSocket || start.ClientMetadata["thread_id"] != "thr-1" ||
		start.Client["originator"] != "codex_exec" || start.KeyHash == "" || start.Host != "chatgpt.com" {
		t.Fatalf("turn start row: %+v", start)
	}
}
