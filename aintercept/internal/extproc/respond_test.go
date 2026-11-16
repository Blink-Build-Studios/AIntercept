package extproc

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"

	pb2 "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v2"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/convo"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor/interceptortest"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/wsframe"
)

// Respond and tool calls held whole, through the enforcing filter
// (docs/INTERCEPTOR_V2.md, S1–S5, T1–T2).

const tricks = "Well, tricks are for kids."

// bing answers "first we bing" in a request with Respond("then we bong"), and
// a tool call: one whose arguments say "first we bing" with Respond, one that
// says "silly rabbit" with its arguments replaced.
func bing() *interceptortest.FakeV2 {
	return &interceptortest.FakeV2{
		Request: func(q *pb2.RequestMessages) *pb2.RequestVerdict {
			for _, m := range q.GetMessages() {
				for _, p := range m.GetParts() {
					if strings.Contains(p.GetText().GetText(), "first we bing") {
						return &pb2.RequestVerdict{Respond: &pb2.Respond{Text: "then we bong"}}
					}
				}
			}
			return &pb2.RequestVerdict{}
		},
		Tool: func(m *pb2.ResponseMessage) *pb2.MessageVerdict {
			tc := m.GetMessage().GetParts()[0].GetToolCall()
			switch {
			case strings.Contains(tc.GetArguments(), "first we bing"):
				return &pb2.MessageVerdict{Action: &pb2.MessageVerdict_Respond{Respond: &pb2.Respond{Text: "then we bong"}}}
			case strings.Contains(tc.GetArguments(), "silly rabbit"):
				r := m.GetMessage()
				r.GetParts()[0].GetToolCall().Arguments = strings.ReplaceAll(tc.GetArguments(), "silly rabbit", "tricks")
				return &pb2.MessageVerdict{MessageId: r.GetId(), Action: &pb2.MessageVerdict_Replace{Replace: &pb2.Replace{Message: r}}}
			}
			return &pb2.MessageVerdict{}
		},
	}
}

func newBingRig(t *testing.T, f *interceptortest.FakeV2, cfg interceptor.Config) *v2Rig {
	t.Helper()
	sink := &memSink{}
	proc := events.NewProcessor(sink, events.ProcessorConfig{BufferSize: 1024})
	var rows []events.Verdict
	cfg.Content = true
	if cfg.Deadline == 0 {
		cfg.Deadline = 5 * time.Second
	}
	ic := interceptortest.NewClientV2(t, f, cfg, func(_ string, v events.Verdict) { rows = append(rows, v) })
	return &v2Rig{
		enforceRig: enforceRig{t: t, sink: sink, proc: proc, st: &streamState{correlationID: "c", enforce: true, enf: &enforceState{}},
			srv: New(Config{Processor: proc, InstanceID: "t", KeyHashSalt: "s", Interceptor: ic})},
		fake: f, ic: ic, rows: &rows,
	}
}

// completeRow drains the processor and returns the call's complete row.
func (r *v2Rig) completeRow() events.Event {
	r.t.Helper()
	r.proc.Close()
	for _, e := range r.sink.ev {
		if e.Phase == events.PhaseComplete {
			return e
		}
	}
	r.t.Fatalf("no complete row in %d events", len(r.sink.ev))
	return events.Event{}
}

func immediate(t *testing.T, out []*extprocv3.ProcessingResponse) (int, string, []byte) {
	t.Helper()
	if len(out) != 1 || out[0].GetImmediateResponse() == nil {
		t.Fatalf("want an immediate response, got %v", out)
	}
	ir := out[0].GetImmediateResponse()
	ct := ""
	for _, h := range ir.GetHeaders().GetSetHeaders() {
		if h.GetHeader().GetKey() == "content-type" {
			ct = string(h.GetHeader().GetRawValue())
		}
	}
	return int(ir.GetStatus().GetCode()), ct, ir.GetBody()
}

func sseText(body []byte) (text, stop string) {
	for _, line := range strings.Split(string(body), "\n") {
		d, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var ev struct {
			Type  string
			Delta struct {
				Text       string
				StopReason string `json:"stop_reason"`
			}
		}
		_ = json.Unmarshal([]byte(d), &ev)
		switch ev.Type {
		case "content_block_delta":
			text += ev.Delta.Text
		case "message_delta":
			stop = ev.Delta.StopReason
		}
	}
	return text, stop
}

const bingPrompt = `{"model":"m-1","stream":true,"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"ok"},{"role":"user","content":"first we bing"}]}`

// S1, S5: synthesized. The call never goes upstream; the client gets the
// reply in its format; the call is recorded with zero usage, marked
// synthesized.
func TestV2RespondSynthesized(t *testing.T) {
	for _, stream := range []bool{true, false} {
		r := newBingRig(t, bing(), interceptor.Config{})
		body := bingPrompt
		if !stream {
			body = strings.Replace(body, `"stream":true`, `"stream":false`, 1)
		}
		r.send(eReqHeaders(false, anthropicCall...))
		status, ct, got := immediate(t, r.send(eReqBody(body, true)))
		if status != 200 {
			t.Fatalf("status %d", status)
		}
		if stream {
			if text, stop := sseText(got); ct != "text/event-stream" || text != "then we bong" || stop != "end_turn" {
				t.Fatalf("stream: %s %q %q\n%s", ct, text, stop, got)
			}
		} else {
			var m struct {
				Content []struct{ Text string }
			}
			if ct != "application/json" || json.Unmarshal(got, &m) != nil || len(m.Content) != 1 || m.Content[0].Text != "then we bong" {
				t.Fatalf("json: %s %s", ct, got)
			}
		}
		e := r.completeRow()
		if e.Status != 200 || e.StopReason != "end_turn" || e.InputTokens != 0 || e.OutputTokens != 0 || e.Model != "m-1" || e.Error != "" {
			t.Fatalf("complete row %+v", e)
		}
		if rows := *r.rows; len(rows) != 1 || rows[0].Action != "respond" || rows[0].RespondMode != "synthesized" || !rows[0].Applied {
			t.Fatalf("rows %+v", rows)
		}
	}
}

// Codex over HTTP: a Responses event stream.
func TestV2RespondSynthesizedResponses(t *testing.T) {
	r := newBingRig(t, bing(), interceptor.Config{})
	r.send(eReqHeaders(false, ":authority", "chatgpt.com", ":method", "POST", ":path", "/backend-api/codex/responses", "content-type", "application/json"))
	_, ct, got := immediate(t, r.send(eReqBody(`{"model":"m-1","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"first we bing"}]}]}`, true)))
	if ct != "text/event-stream" || !bytes.Contains(got, []byte(`"delta":"then we bong"`)) || !bytes.Contains(got, []byte("event: response.completed")) {
		t.Fatalf("%s\n%s", ct, got)
	}
	if e := r.completeRow(); e.StopReason != "completed" || e.Status != 200 {
		t.Fatalf("complete row %+v", e)
	}
}

// S3: rewritten. A chained request goes upstream asking for the text.
func TestV2RespondRewritten(t *testing.T) {
	r := newBingRig(t, bing(), interceptor.Config{})
	body := `{"model":"m-1","stream":true,"thread":{"previous_message_id":"msg_0"},"messages":[{"role":"user","content":"first we bing"}]}`
	r.send(eReqHeaders(false, anthropicCall...))
	out := r.send(eReqBody(body, true))
	if len(out) != 2 {
		t.Fatalf("got %v", out)
	}
	if b, _ := streamed(out[1]); string(b) != strings.Replace(body, "first we bing", convo.RespondInstruction+"then we bong", 1) {
		t.Fatalf("forwarded %s", b)
	}
	if rows := *r.rows; len(rows) != 1 || rows[0].Action != "respond" || rows[0].RespondMode != "rewritten" || !rows[0].Applied {
		t.Fatalf("rows %+v", rows)
	}
}

// Respond at call start: synthesized before OnRequest.
func TestV2RespondAtCallStart(t *testing.T) {
	f := bing()
	f.Start = func(*pb2.CallStart) *pb2.CallVerdict {
		return &pb2.CallVerdict{Action: &pb2.CallVerdict_Respond{Respond: &pb2.Respond{Text: tricks}}}
	}
	r := newBingRig(t, f, interceptor.Config{})
	r.send(eReqHeaders(false, anthropicCall...))
	_, _, got := immediate(t, r.send(eReqBody(`{"model":"m-1","stream":true,"messages":[{"role":"user","content":"hello"}]}`, true)))
	if text, _ := sseText(got); text != tricks {
		t.Fatalf("got %s", got)
	}
	if calls, _, _ := f.Snapshot(); strings.Join(calls, ",") != "start" {
		t.Fatalf("calls %v", calls)
	}
	if rows := *r.rows; len(rows) != 1 || rows[0].Callback != "call_start" || rows[0].RespondMode != "synthesized" {
		t.Fatalf("rows %+v", rows)
	}
}

// A request it can't read can't be answered: it goes upstream as sent, the
// Respond recorded, not applied.
func TestV2RespondUnreadable(t *testing.T) {
	f := bing()
	f.Start = func(*pb2.CallStart) *pb2.CallVerdict {
		return &pb2.CallVerdict{Action: &pb2.CallVerdict_Respond{Respond: &pb2.Respond{Text: tricks}}}
	}
	r := newBingRig(t, f, interceptor.Config{})
	r.send(eReqHeaders(false, ":authority", "api.anthropic.com", ":method", "POST", ":path", "/v1/messages/count_tokens"))
	body := `{"messages":[{"role":"user","content":"hello"}]}`
	out := r.send(eReqBody(body, true))
	if b, _ := streamed(out[len(out)-1]); string(b) != body {
		t.Fatalf("got %v", out)
	}
	if rows := *r.rows; len(rows) != 1 || rows[0].Applied || rows[0].Action != "respond" {
		t.Fatalf("rows %+v", rows)
	}
}

func wsOpen(r *v2Rig) {
	r.send(eReqHeaders(false, ":authority", "chatgpt.com", ":method", "GET", ":path", "/backend-api/codex/responses", "upgrade", "websocket"))
	r.send(eRespHeaders(":status", "101", "upgrade", "websocket"))
}

func wsDecode(t *testing.T, out *extprocv3.ProcessingResponse) []string {
	t.Helper()
	b, _ := streamed(out)
	msgs, err := wsframe.NewDecoder(false, false, 1<<20).Feed(b)
	if err != nil {
		t.Fatal(err)
	}
	var s []string
	for _, m := range msgs {
		s = append(s, string(m.Payload))
	}
	return s
}

// Codex's WebSocket: the response.create never goes upstream; the client
// gets the events, and the turn is recorded. The next turn goes as usual.
func TestV2RespondWebSocket(t *testing.T) {
	r := newBingRig(t, bing(), interceptor.Config{})
	wsOpen(r)
	create := `{"type":"response.create","model":"m-1","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"first we bing"}]}]}`
	out := r.send(eReqBody(string(wsframe.Encode(wsframe.OpText, []byte(create), true)), false))
	var up, down []string
	for _, o := range out {
		if o.GetRequestBody() != nil {
			b, _ := streamed(o)
			if len(b) > 0 {
				up = append(up, string(b))
			}
		} else {
			down = append(down, wsDecode(t, o)...)
		}
	}
	if len(up) != 0 || len(down) < 4 || !strings.Contains(strings.Join(down, ""), `"delta":"then we bong"`) ||
		!strings.HasPrefix(down[len(down)-1], `{"type":"response.completed"`) {
		t.Fatalf("to server %q, to client %q", up, down)
	}
	next := `{"type":"response.create","model":"m-1","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`
	out = r.send(eReqBody(string(wsframe.Encode(wsframe.OpText, []byte(next), true)), false))
	if len(out) != 1 || strings.Join(wsDecode(t, out[0]), "") != next {
		t.Fatalf("next turn %v", out)
	}
	r.srv.wsClose(r.st)
	r.proc.Close()
	turns := 0
	for _, e := range r.sink.ev {
		if e.Phase == events.PhaseComplete && e.Transport == events.TransportWebSocket && e.StopReason == "completed" {
			turns++
		}
	}
	if turns != 1 {
		t.Fatalf("%d completed turns recorded, want the synthesized one", turns)
	}
}

// A chained response.create is rewritten and goes upstream.
func TestV2RespondWebSocketChained(t *testing.T) {
	r := newBingRig(t, bing(), interceptor.Config{})
	wsOpen(r)
	create := `{"type":"response.create","model":"m-1","previous_response_id":"resp_0","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"first we bing"}]}]}`
	out := r.send(eReqBody(string(wsframe.Encode(wsframe.OpText, []byte(create), true)), false))
	if len(out) != 1 || strings.Join(wsDecode(t, out[0]), "") != strings.Replace(create, "first we bing", convo.RespondInstruction+"then we bong", 1) {
		t.Fatalf("got %v", out)
	}
}

// ── Tool calls held whole ────────────────────────────────────────────────────

func sseEv(name, data string) string { return "event: " + name + "\ndata: " + data + "\n\n" }

func anthropicToolStream(args string) []string {
	return []string{
		sseEv("message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m-1","content":[],"usage":{"input_tokens":5,"output_tokens":1}}}`),
		sseEv("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		sseEv("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me look."}}`),
		sseEv("content_block_stop", `{"type":"content_block_stop","index":0}`),
		sseEv("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"Bash","input":{}}}`),
		sseEv("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":`+jsonStr(args[:5])+`}}`),
		sseEv("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":`+jsonStr(args[5:])+`}}`),
		sseEv("content_block_stop", `{"type":"content_block_stop","index":1}`),
		sseEv("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":30}}`),
		sseEv("message_stop", `{"type":"message_stop"}`),
	}
}

func jsonStr(s string) string { b, _ := json.Marshal(s); return string(b) }

// toolRig is a rig with an Anthropic request sent.
func toolRig(t *testing.T, f *interceptortest.FakeV2, cfg interceptor.Config) *v2Rig {
	r := newBingRig(t, f, cfg)
	r.send(eReqHeaders(false, anthropicCall...))
	out := r.send(eReqBody(`{"model":"m-1","stream":true,"messages":[{"role":"user","content":"look"}]}`, true))
	if strings.Join(removed(out[0]), ",") != "accept-encoding" {
		t.Fatalf("request headers %v: want accept-encoding removed, so replies can be read", removed(out[0]))
	}
	return r
}

// T1, T2: text goes on as it comes; the tool call is held until complete,
// then released as it was (Accept).
func TestV2ToolCallHeldThenAccepted(t *testing.T) {
	r := toolRig(t, bing(), interceptor.Config{})
	if out := r.send(eRespHeaders(":status", "200", "content-type", "text/event-stream")); len(out) != 1 || strings.Join(removed(out[0]), ",") != "content-length" {
		t.Fatalf("response headers %v", out)
	}
	evs := anthropicToolStream(`{"command":"ls"}`)
	var got []byte
	for i, ev := range evs {
		out := r.send(eRespBody([]byte(ev), i == len(evs)-1))
		b, _ := streamed(out[0])
		if held := i >= 4 && i <= 6; held != (len(b) == 0) {
			t.Fatalf("event %d: released %q", i, b)
		}
		got = append(got, b...)
	}
	if string(got) != strings.Join(evs, "") {
		t.Fatalf("accepted reply changed:\n%s", got)
	}
	if tc := r.fake.ToolCallsSeen(); len(tc) != 1 || tc[0].GetMessage().GetParts()[0].GetToolCall().GetArguments() != `{"command":"ls"}` {
		t.Fatalf("OnToolCall %v", tc)
	}
}

// P3: replaced arguments reach the client.
func TestV2ToolCallReplaced(t *testing.T) {
	r := toolRig(t, bing(), interceptor.Config{})
	r.send(eRespHeaders(":status", "200", "content-type", "text/event-stream"))
	var got []byte
	for _, ev := range anthropicToolStream(`{"command":"echo I'm a silly rabbit"}`) {
		b, _ := streamed(r.send(eRespBody([]byte(ev), false))[0])
		got = append(got, b...)
	}
	if !bytes.Contains(got, []byte(`"partial_json":"{\"command\":\"echo I'm a tricks\"}"`)) || bytes.Contains(got, []byte("silly")) {
		t.Fatalf("got %s", got)
	}
	if rows := *r.rows; len(rows) != 1 || rows[0].Callback != "tool_call" || rows[0].Action != "replace" || rows[0].MessageID != "toolu_1" {
		t.Fatalf("rows %+v", rows)
	}
}

// S4: Respond ends the reply with the text; the upstream's rest is dropped.
func TestV2ToolCallResponded(t *testing.T) {
	r := toolRig(t, bing(), interceptor.Config{})
	r.send(eRespHeaders(":status", "200", "content-type", "text/event-stream"))
	evs := anthropicToolStream(`{"command":"echo first we bing"}`)
	var got []byte
	ended := false
	for i, ev := range evs {
		out := r.send(eRespBody([]byte(ev), i == len(evs)-1))
		if ended && len(out) != 0 {
			t.Fatalf("event %d after the end: %v", i, out)
		}
		for _, o := range out {
			b, eos := streamed(o)
			got = append(got, b...)
			ended = ended || eos
		}
	}
	if text, stop := sseText(got); !ended || text != "Let me look.then we bong" || stop != "end_turn" || bytes.Contains(got, []byte("toolu_1")) {
		t.Fatalf("ended %v text %q stop %q\n%s", ended, text, stop, got)
	}
	if rows := *r.rows; len(rows) != 1 || rows[0].Action != "respond" || rows[0].RespondMode != "synthesized" {
		t.Fatalf("rows %+v", rows)
	}
}

// The Interceptor late on a tool call: fail closed ends the reply with the
// provider's error; the call never reaches the client.
func TestV2ToolCallFailClosed(t *testing.T) {
	f := bing()
	f.Delay = 100 * time.Millisecond
	r := toolRig(t, f, interceptor.Config{Deadline: 150 * time.Millisecond})
	f.Delay = 300 * time.Millisecond
	r.send(eRespHeaders(":status", "200", "content-type", "text/event-stream"))
	var got []byte
	for _, ev := range anthropicToolStream(`{"command":"ls"}`) {
		for _, o := range r.send(eRespBody([]byte(ev), false)) {
			b, _ := streamed(o)
			got = append(got, b...)
		}
	}
	if !bytes.Contains(got, []byte("event: error")) || bytes.Contains(got, []byte("toolu_1")) {
		t.Fatalf("got %s", got)
	}
}

// A whole JSON reply: its tool calls decided before it's released.
func TestV2ToolCallJSON(t *testing.T) {
	r := toolRig(t, bing(), interceptor.Config{})
	if out := r.send(eRespHeaders(":status", "200", "content-type", "application/json")); len(out) != 0 {
		t.Fatalf("headers released before the body: %v", out)
	}
	body := `{"id":"msg_1","type":"message","role":"assistant","model":"m-1","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"echo I'm a silly rabbit"}}],"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`
	if out := r.send(eRespBody([]byte(body[:20]), false)); len(out) != 0 {
		t.Fatalf("released early: %v", out)
	}
	out := r.send(eRespBody([]byte(body[20:]), true))
	if len(out) != 2 || strings.Join(removed(out[0]), ",") != "content-length,content-encoding" {
		t.Fatalf("got %v", out)
	}
	if b, _ := streamed(out[1]); string(b) != strings.Replace(body, "silly rabbit", "tricks", 1) {
		t.Fatalf("got %s", b)
	}
}

// Codex's WebSocket: a function call held and replaced; the completed
// response says the same.
func TestV2ToolCallWebSocket(t *testing.T) {
	r := newBingRig(t, bing(), interceptor.Config{})
	wsOpen(r)
	r.send(eReqBody(string(wsframe.Encode(wsframe.OpText, []byte(`{"type":"response.create","model":"m-1","input":"look"}`), true)), false))
	args := `{"command":["echo","I'm a silly rabbit"]}`
	item := `{"id":"fc_1","type":"function_call","status":"completed","arguments":` + jsonStr(args) + `,"call_id":"call_1","name":"shell"}`
	server := []string{
		`{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","status":"in_progress","model":"m-1","output":[]}}`,
		`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"fc_1","type":"function_call","status":"in_progress","arguments":"","call_id":"call_1","name":"shell"}}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":2,"item_id":"fc_1","output_index":0,"delta":` + jsonStr(args) + `}`,
		`{"type":"response.output_item.done","sequence_number":3,"output_index":0,"item":` + item + `}`,
		`{"type":"response.completed","sequence_number":4,"response":{"id":"resp_1","object":"response","status":"completed","model":"m-1","output":[` + item + `],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
	}
	var got []string
	for i, m := range server {
		out := r.send(eRespBody(wsframe.Encode(wsframe.OpText, []byte(m), false), false))
		msgs := wsDecode(t, out[0])
		if held := i == 1 || i == 2; held != (len(msgs) == 0) {
			t.Fatalf("message %d: released %q", i, msgs)
		}
		got = append(got, msgs...)
	}
	all := strings.Join(got, "\n")
	if strings.Contains(all, "silly rabbit") || strings.Count(all, "I'm a tricks") != 3 {
		t.Fatalf("got\n%s", all)
	}
}
