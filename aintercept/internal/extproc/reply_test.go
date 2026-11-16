package extproc

import (
	"encoding/json"
	"strings"
	"testing"

	pb2 "github.com/Blink-Build-Studios/aintercept/aintercept/interceptor/v2"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/interceptor/interceptortest"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/wsframe"
)

// The reply's text through the enforcing filter (docs/INTERCEPTOR_V2.md,
// "Reply text"): every piece decided by OnReply before the client sees it,
// in every format.

const phrase, redacted = "purple elephant", "[redacted]"

// watchReply replaces the phrase in the reply's text, holding back only a
// tail that could still become it.
func watchReply(m *pb2.ResponseMessage) *pb2.MessageVerdict {
	part := m.GetMessage().GetParts()[0]
	if part.GetText() == nil {
		return &pb2.MessageVerdict{}
	}
	s := part.GetText().GetText()
	keep := 0
	if !m.GetDone() {
		for k := len(phrase) - 1; k > 0; k-- {
			if strings.HasSuffix(s, phrase[:k]) {
				keep = k
				break
			}
		}
	}
	out := strings.ReplaceAll(s, phrase, redacted)
	if out == s {
		return &pb2.MessageVerdict{Keep: int32(keep)}
	}
	part.GetText().Text = out[:len(out)-keep]
	return &pb2.MessageVerdict{MessageId: m.GetMessage().GetId(), Keep: int32(keep), Action: &pb2.MessageVerdict_Replace{Replace: &pb2.Replace{Message: m.GetMessage()}}}
}

type replyFormat struct {
	name    string
	call    []string // request headers
	body    string   // request body
	events  func(pieces ...string) []string
	text    func(t *testing.T, out string) string
	jsonOut string // a whole JSON reply saying the phrase
}

var replyFormats = []replyFormat{
	{
		name: "anthropic", call: anthropicCall,
		body: `{"model":"m-1","stream":true,"messages":[{"role":"user","content":"look"}]}`,
		events: func(pieces ...string) []string {
			evs := []string{
				sseEv("message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m-1","content":[],"usage":{"input_tokens":5,"output_tokens":1}}}`),
				sseEv("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`),
				sseEv("content_block_stop", `{"type":"content_block_stop","index":0}`),
				sseEv("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`),
			}
			for _, p := range pieces {
				evs = append(evs, sseEv("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":`+jsonStr(p)+`}}`))
			}
			return append(evs,
				sseEv("content_block_stop", `{"type":"content_block_stop","index":1}`),
				sseEv("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":30}}`),
				sseEv("message_stop", `{"type":"message_stop"}`))
		},
		text: func(_ *testing.T, out string) string {
			text, _ := sseText([]byte(out))
			return text
		},
		jsonOut: `{"id":"msg_1","type":"message","role":"assistant","model":"m-1","content":[{"type":"text","text":"The purple elephant is here."}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":30}}`,
	},
	{
		name: "codex", call: []string{":authority", "chatgpt.com", ":method", "POST", ":path", "/backend-api/codex/responses", "content-type", "application/json"},
		body: `{"model":"m-1","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"look"}]}]}`,
		events: func(pieces ...string) []string {
			full := strings.Join(pieces, "")
			item := `{"id":"msg_a","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","annotations":[],"text":` + jsonStr(full) + `}]}`
			evs := []string{
				sseEv("response.created", `{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","status":"in_progress","model":"m-1","output":[]}}`),
				sseEv("response.output_item.added", `{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"msg_a","type":"message","status":"in_progress","role":"assistant","content":[]}}`),
			}
			for i, p := range pieces {
				evs = append(evs, sseEv("response.output_text.delta", `{"type":"response.output_text.delta","sequence_number":`+itoa(2+i)+`,"item_id":"msg_a","output_index":0,"content_index":0,"delta":`+jsonStr(p)+`}`))
			}
			n := 2 + len(pieces)
			return append(evs,
				sseEv("response.output_text.done", `{"type":"response.output_text.done","sequence_number":`+itoa(n)+`,"item_id":"msg_a","output_index":0,"content_index":0,"text":`+jsonStr(full)+`}`),
				sseEv("response.output_item.done", `{"type":"response.output_item.done","sequence_number":`+itoa(n+1)+`,"output_index":0,"item":`+item+`}`),
				sseEv("response.completed", `{"type":"response.completed","sequence_number":`+itoa(n+2)+`,"response":{"id":"resp_1","object":"response","status":"completed","model":"m-1","output":[`+item+`],"usage":{"input_tokens":5,"output_tokens":30,"total_tokens":35}}}`))
		},
		text:    func(t *testing.T, out string) string { return responsesText(t, sseDataLines(out)) },
		jsonOut: `{"id":"resp_1","object":"response","status":"completed","model":"m-1","output":[{"id":"msg_a","type":"message","role":"assistant","content":[{"type":"output_text","text":"The purple elephant is here."}]}],"usage":{"input_tokens":5,"output_tokens":30,"total_tokens":35}}`,
	},
	{
		name: "chat", call: []string{":authority", "api.openai.com", ":method", "POST", ":path", "/v1/chat/completions", "content-type", "application/json"},
		body: `{"model":"m-1","stream":true,"messages":[{"role":"user","content":"look"}]}`,
		events: func(pieces ...string) []string {
			chunk := func(delta, finish string) string {
				return "data: " + `{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"m-1","choices":[{"index":0,"delta":` + delta + `,"finish_reason":` + finish + `}]}` + "\n\n"
			}
			evs := []string{chunk(`{"role":"assistant","content":""}`, "null")}
			for _, p := range pieces {
				evs = append(evs, chunk(`{"content":`+jsonStr(p)+`}`, "null"))
			}
			return append(evs, chunk(`{}`, `"stop"`), "data: [DONE]\n\n")
		},
		text: func(t *testing.T, out string) string {
			var text string
			for _, d := range sseDataLines(out) {
				var c struct {
					Choices []struct{ Delta struct{ Content string } }
				}
				_ = json.Unmarshal([]byte(d), &c)
				if len(c.Choices) > 0 {
					text += c.Choices[0].Delta.Content
				}
			}
			return text
		},
		jsonOut: `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"m-1","choices":[{"index":0,"message":{"role":"assistant","content":"The purple elephant is here."},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":30,"total_tokens":35}}`,
	},
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func sseDataLines(out string) []string {
	var ds []string
	for _, line := range strings.Split(out, "\n") {
		if d, ok := strings.CutPrefix(line, "data: "); ok {
			ds = append(ds, d)
		}
	}
	return ds
}

// responsesText is a Responses reply's text from its deltas; every event and
// item that repeats it must agree.
func responsesText(t *testing.T, datas []string) string {
	t.Helper()
	var text string
	var repeats []string
	for _, d := range datas {
		var ev struct {
			Type, Delta, Text string
			Item              *struct{ Content []struct{ Text string } }
			Response          *struct {
				Output []struct{ Content []struct{ Text string } }
			}
		}
		_ = json.Unmarshal([]byte(d), &ev)
		switch ev.Type {
		case "response.output_text.delta":
			text += ev.Delta
		case "response.output_text.done":
			repeats = append(repeats, ev.Text)
		case "response.output_item.done":
			repeats = append(repeats, ev.Item.Content[0].Text)
		case "response.completed":
			repeats = append(repeats, ev.Response.Output[0].Content[0].Text)
		}
	}
	for _, r := range repeats {
		if r != text {
			t.Fatalf("a repeat says %q, the deltas %q", r, text)
		}
	}
	return text
}

// replyRig is a rig with f as the Interceptor and a request in format sent,
// its response headers in.
func replyRig(t *testing.T, f *interceptortest.FakeV2, cfg interceptor.Config, rf replyFormat, contentType string) *v2Rig {
	t.Helper()
	r := newBingRig(t, f, cfg)
	r.send(eReqHeaders(false, rf.call...))
	r.send(eReqBody(rf.body, true))
	r.send(eRespHeaders(":status", "200", "content-type", contentType))
	return r
}

// stream sends each event as its own chunk and returns what the client got.
func (r *v2Rig) stream(evs []string) string {
	var got []byte
	for i, ev := range evs {
		for _, o := range r.send(eRespBody([]byte(ev), i == len(evs)-1)) {
			b, _ := streamed(o)
			got = append(got, b...)
		}
	}
	return string(got)
}

// A phrase split across three pieces is replaced, in every format; the
// thinking block is announced; nothing of the phrase reaches the client.
func TestV2ReplyTextReplaced(t *testing.T) {
	for _, rf := range replyFormats {
		t.Run(rf.name, func(t *testing.T) {
			f := &interceptortest.FakeV2{Reply: watchReply}
			r := replyRig(t, f, interceptor.Config{}, rf, "text/event-stream")
			out := r.stream(rf.events("The pur", "ple ele", "phant is here."))
			if got := rf.text(t, out); got != "The [redacted] is here." || strings.Contains(out, "elephant") {
				t.Fatalf("text %q\n%s", got, out)
			}
			r.ic.Close()
			opaque := 0
			for _, m := range r.fake.RepliesSeen() {
				if m.GetMessage().GetParts()[0].GetOpaque() != nil {
					opaque++
				}
			}
			if want := map[bool]int{true: 1, false: 0}[rf.name == "anthropic"]; opaque != want {
				t.Fatalf("%d opaque notices, want %d", opaque, want)
			}
			if rows := *r.rows; len(rows) == 0 || rows[len(rows)-1].Callback != "reply" || rows[len(rows)-1].Action != "replace" {
				t.Fatalf("rows %+v", rows)
			}
		})
	}
}

// Accepted, the reply is byte for byte as the provider sent it.
func TestV2ReplyTextAccepted(t *testing.T) {
	for _, rf := range replyFormats {
		t.Run(rf.name, func(t *testing.T) {
			f := &interceptortest.FakeV2{Reply: func(*pb2.ResponseMessage) *pb2.MessageVerdict { return &pb2.MessageVerdict{} }}
			r := replyRig(t, f, interceptor.Config{}, rf, "text/event-stream")
			evs := rf.events("Hello, ", "world.")
			if out := r.stream(evs); out != strings.Join(evs, "") {
				t.Fatalf("accepted reply changed:\n%s", out)
			}
		})
	}
}

// Pieces that arrive while a decision is outstanding go in the next call: with
// more of the stream waiting, nothing is asked until it's all in.
func TestV2ReplyPiecesGather(t *testing.T) {
	rf := replyFormats[0]
	f := &interceptortest.FakeV2{Reply: func(*pb2.ResponseMessage) *pb2.MessageVerdict { return &pb2.MessageVerdict{} }}
	r := replyRig(t, f, interceptor.Config{}, rf, "text/event-stream")
	evs := rf.events("one ", "two ", "three")
	waiting := 0
	r.st.enf.more = func() bool { return waiting > 0 }
	for i, ev := range evs {
		waiting = 0
		if i >= 4 && i < 6 { // the first two pieces: more is waiting
			waiting = 1
		}
		r.send(eRespBody([]byte(ev), i == len(evs)-1))
	}
	var texts []string
	for _, m := range f.RepliesSeen() {
		if p := m.GetMessage().GetParts()[0]; p.GetText() != nil {
			texts = append(texts, p.GetText().GetText())
		}
	}
	if len(texts) != 1 || texts[0] != "one two three" {
		t.Fatalf("OnReply got %q: want one call with all three pieces", texts)
	}
}

// Respond mid-reply: the text released stays, the reply ends with the
// Interceptor's text, and the provider's ending never reaches the client.
func TestV2ReplyRespond(t *testing.T) {
	for _, rf := range replyFormats {
		t.Run(rf.name, func(t *testing.T) {
			f := &interceptortest.FakeV2{Reply: func(m *pb2.ResponseMessage) *pb2.MessageVerdict {
				if strings.Contains(m.GetMessage().GetParts()[0].GetText().GetText(), "world") {
					return &pb2.MessageVerdict{Action: &pb2.MessageVerdict_Respond{Respond: &pb2.Respond{Text: "then we bong"}}}
				}
				return &pb2.MessageVerdict{}
			}}
			r := replyRig(t, f, interceptor.Config{}, rf, "text/event-stream")
			out := r.stream(rf.events("Hello, ", "world", "!"))
			if got := rf.text(t, out); got != "Hello, then we bong" || strings.Contains(out, `output_tokens":30`) || strings.Count(out, "[DONE]") > 1 {
				t.Fatalf("text %q\n%s", got, out)
			}
			if rows := *r.rows; len(rows) != 1 || rows[0].Action != "respond" || rows[0].RespondMode != "synthesized" {
				t.Fatalf("rows %+v", rows)
			}
		})
	}
}

// The Interceptor fails, fail closed: the held text never goes out, and the
// reply ends with the provider's error.
func TestV2ReplyRefused(t *testing.T) {
	rf := replyFormats[0]
	f := &interceptortest.FakeV2{Reply: func(*pb2.ResponseMessage) *pb2.MessageVerdict {
		return &pb2.MessageVerdict{Keep: 1000} // can't be applied
	}}
	r := replyRig(t, f, interceptor.Config{}, rf, "text/event-stream")
	out := r.stream(rf.events("secret", " text"))
	if strings.Contains(out, "secret") || !strings.Contains(out, "event: error") {
		t.Fatalf("got\n%s", out)
	}
}

// A whole JSON reply: its text decided before it's released.
func TestV2ReplyJSON(t *testing.T) {
	for _, rf := range replyFormats {
		t.Run(rf.name, func(t *testing.T) {
			f := &interceptortest.FakeV2{Reply: watchReply}
			r := replyRig(t, f, interceptor.Config{}, rf, "application/json")
			out := r.stream([]string{rf.jsonOut})
			if out != strings.Replace(rf.jsonOut, phrase, redacted, 1) {
				t.Fatalf("got\n%s", out)
			}
		})
	}
}

// Codex's WebSocket: the turn's text replaced across messages.
func TestV2ReplyWebSocket(t *testing.T) {
	r := newBingRig(t, &interceptortest.FakeV2{Reply: watchReply}, interceptor.Config{})
	wsOpen(r)
	r.send(eReqBody(string(wsframe.Encode(wsframe.OpText, []byte(`{"type":"response.create","model":"m-1","input":"look"}`), true)), false))
	var got []string
	for _, ev := range replyFormats[1].events("The pur", "ple ele", "phant is here.") {
		_, data, _ := strings.Cut(strings.TrimSpace(ev), "data: ")
		got = append(got, wsDecode(t, r.send(eRespBody(wsframe.Encode(wsframe.OpText, []byte(data), false), false))[0])...)
	}
	if text := responsesText(t, got); text != "The [redacted] is here." || strings.Contains(strings.Join(got, ""), "elephant") {
		t.Fatalf("text %q\n%s", text, strings.Join(got, "\n"))
	}
}

// Chat Completions tool calls are held whole and decided, as the others.
func TestV2ChatToolCall(t *testing.T) {
	rf := replyFormats[2]
	r := replyRig(t, bing(), interceptor.Config{}, rf, "text/event-stream")
	chunk := func(delta, finish string) string {
		return "data: " + `{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"m-1","choices":[{"index":0,"delta":` + delta + `,"finish_reason":` + finish + `}]}` + "\n\n"
	}
	out := r.stream([]string{
		chunk(`{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"shell","arguments":""}}]}`, "null"),
		chunk(`{"tool_calls":[{"index":0,"function":{"arguments":"{\"cmd\":\"echo the"}}]}`, "null"),
		chunk(`{"tool_calls":[{"index":0,"function":{"arguments":"n\"}"}}]}`, "null"),
		chunk(`{}`, `"tool_calls"`),
		"data: [DONE]\n\n",
	})
	if tc := r.fake.ToolCallsSeen(); len(tc) != 1 || tc[0].GetMessage().GetParts()[0].GetToolCall().GetArguments() != `{"cmd":"echo then"}` {
		t.Fatalf("OnToolCall %v\n%s", tc, out)
	}
}

// Observe: the same calls, queued; the answers recorded, never applied, and
// the reply goes on as it came.
func TestV2ObserveReply(t *testing.T) {
	r := newV2Rig(t, false)
	r.fake.Reply = func(m *pb2.ResponseMessage) *pb2.MessageVerdict {
		if p := m.GetMessage().GetParts()[0]; p.GetText() != nil {
			p.GetText().Text = strings.ToUpper(p.GetText().GetText())
			return &pb2.MessageVerdict{Action: &pb2.MessageVerdict_Replace{Replace: &pb2.Replace{Message: m.GetMessage()}}}
		}
		return &pb2.MessageVerdict{}
	}
	rf := replyFormats[0]
	r.srv.handle(r.st, eReqHeaders(false, rf.call...))
	r.srv.handle(r.st, eReqBody(rf.body, true))
	r.srv.handle(r.st, eRespHeaders(":status", "200", "content-type", "text/event-stream"))
	evs := rf.events("The pur", "ple ele", "phant is here.")
	for i, ev := range evs {
		r.srv.handle(r.st, eRespBody([]byte(ev), i == len(evs)-1))
	}
	r.ic.Close()
	if len(r.fake.RepliesSeen()) < 3 {
		t.Fatalf("OnReply got %v", r.fake.RepliesSeen())
	}
	var replies int
	for _, row := range *r.rows {
		if row.Callback == "reply" {
			replies++
			if row.Applied || row.Mode != "observe" {
				t.Fatalf("row %+v", row)
			}
		}
	}
	if replies == 0 {
		t.Fatalf("rows %+v", *r.rows)
	}
}
