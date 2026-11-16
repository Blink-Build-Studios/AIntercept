package convo

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// Reply text, piece by piece (docs/INTERCEPTOR_V2.md, "Reply text"): no text
// reaches the client until it's decided; Accept is byte for byte, Replace,
// Hold, keep (a tail held back), Respond, and a block ending while held, in
// every format.

const phrase, redacted = "purple elephant", "[redacted]"

// textReply is a reply whose text arrives in pieces, in format.
func textReply(format string, pieces ...string) []wireEvent {
	full := strings.Join(pieces, "")
	var evs []wireEvent
	switch format {
	case FormatAnthropic:
		evs = append(evs,
			wireEvent{"message_start", `{"type":"message_start","message":{"id":"msg_t","type":"message","role":"assistant","model":"m-1","content":[],"stop_reason":null,"usage":{"input_tokens":5,"output_tokens":1}}}`},
			wireEvent{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`},
			wireEvent{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}`},
			wireEvent{"content_block_stop", `{"type":"content_block_stop","index":0}`},
			wireEvent{"content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`})
		for i, p := range pieces {
			evs = append(evs, wireEvent{"content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":` + string(jsonString(p)) + `}}`})
			if i == 0 {
				evs = append(evs, wireEvent{"ping", `{"type": "ping"}`})
			}
		}
		evs = append(evs,
			wireEvent{"content_block_stop", `{"type":"content_block_stop","index":1}`},
			wireEvent{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":30}}`},
			wireEvent{"message_stop", `{"type":"message_stop"}`})
	case FormatResponses:
		seq := 0
		ev := func(typ, rest string) {
			evs = append(evs, wireEvent{typ, `{"type":"` + typ + `","sequence_number":` + itoa(seq) + rest + `}`})
			seq++
		}
		resp := func(status, output string) string {
			return `{"id":"resp_t","object":"response","created_at":1,"status":"` + status + `","model":"m-1","output":[` + output + `],"usage":null}`
		}
		reasoning := `{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"gAAA"}`
		item := `{"id":"msg_t","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","annotations":[],"text":` + string(jsonString(full)) + `}]}`
		ev("response.created", `,"response":`+resp("in_progress", ""))
		ev("response.output_item.added", `,"output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}`)
		ev("response.output_item.done", `,"output_index":0,"item":`+reasoning)
		ev("response.output_item.added", `,"output_index":1,"item":{"id":"msg_t","type":"message","status":"in_progress","role":"assistant","content":[]}`)
		ev("response.content_part.added", `,"item_id":"msg_t","output_index":1,"content_index":0,"part":{"type":"output_text","annotations":[],"text":""}`)
		for _, p := range pieces {
			ev("response.output_text.delta", `,"item_id":"msg_t","output_index":1,"content_index":0,"delta":`+string(jsonString(p)))
		}
		ev("response.output_text.done", `,"item_id":"msg_t","output_index":1,"content_index":0,"text":`+string(jsonString(full)))
		ev("response.content_part.done", `,"item_id":"msg_t","output_index":1,"content_index":0,"part":{"type":"output_text","annotations":[],"text":`+string(jsonString(full))+`}`)
		ev("response.output_item.done", `,"output_index":1,"item":`+item)
		ev("response.completed", `,"response":`+strings.Replace(resp("completed", reasoning+","+item), `"usage":null`, `"usage":{"input_tokens":5,"output_tokens":30,"total_tokens":35}`, 1))
	case FormatChat:
		chunk := func(delta, finish string) wireEvent {
			return wireEvent{"", `{"id":"chatcmpl-t","object":"chat.completion.chunk","created":1,"model":"m-1","choices":[{"index":0,"delta":` + delta + `,"logprobs":null,"finish_reason":` + finish + `}]}`}
		}
		evs = append(evs, chunk(`{"role":"assistant","content":"","refusal":null}`, "null"))
		for _, p := range pieces {
			evs = append(evs, chunk(`{"content":`+string(jsonString(p))+`}`, "null"))
		}
		evs = append(evs, chunk(`{}`, `"stop"`),
			wireEvent{"", `{"id":"chatcmpl-t","object":"chat.completion.chunk","created":1,"model":"m-1","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":30,"total_tokens":35}}`},
			wireEvent{"", `[DONE]`})
	}
	return evs
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// feed runs a reply through a Stream, pumping after every event when pump is
// set, and returns what the client gets.
func feed(t *testing.T, format string, ws, pump bool, evs []wireEvent, dec Deciders) ([]byte, *Stream) {
	t.Helper()
	frame := SSE
	if ws {
		frame = func(_ string, data []byte) []byte { return append(append([]byte{}, data...), 0) }
	}
	s := NewStream(format, frame, dec)
	var out []byte
	for _, e := range evs {
		raw := e.raw()
		if ws {
			raw = frame("", []byte(e.data))
		}
		out = append(out, s.Feed(e.name, []byte(e.data), raw)...)
		if pump {
			out = append(out, s.Pump()...)
		}
	}
	return append(out, s.Flush()...), s
}

// readText reads a reply as its client would, strictly, and returns its text,
// tool calls and how it stopped. For Responses, every event and item that
// repeats the text must agree with the deltas.
func readText(t *testing.T, format string, ws bool, out []byte) reply {
	t.Helper()
	switch format {
	case FormatAnthropic:
		return readAnthropicSSE(t, out)
	case FormatResponses:
		r := readResponses(t, ws, out)
		var datas [][]byte
		if ws {
			datas = wsMessages(out)
		} else {
			datas = sseDatas(t, out)
		}
		for _, d := range datas {
			var ev struct {
				Type string
				Text *string
				Part *struct{ Text string }
				Item *struct {
					Type    string
					Content []struct{ Text string }
				}
				Response *struct {
					Output []struct {
						Type    string
						Content []struct{ Text string }
					}
				}
			}
			_ = json.Unmarshal(d, &ev)
			var got []string
			switch {
			case ev.Text != nil:
				got = append(got, *ev.Text)
			case ev.Part != nil && ev.Type == "response.content_part.done":
				got = append(got, ev.Part.Text)
			case ev.Item != nil && ev.Item.Type == "message" && ev.Type == "response.output_item.done":
				got = append(got, ev.Item.Content[0].Text)
			case ev.Response != nil && ev.Type == "response.completed":
				for _, o := range ev.Response.Output {
					if o.Type == "message" {
						got = append(got, o.Content[0].Text)
					}
				}
			}
			for _, g := range got {
				if g != r.text {
					t.Fatalf("%s says %q, the deltas %q", ev.Type, g, r.text)
				}
			}
		}
		return r
	}
	return readChat(t, out)
}

// readChat reads a Chat Completions stream strictly: chunks, one finishing
// chunk, then [DONE] last.
func readChat(t *testing.T, out []byte) reply {
	t.Helper()
	_, datas := sseEvents(t, out)
	var r reply
	args := map[int]*ToolCall{}
	var order []int
	for i, d := range datas {
		if string(d) == "[DONE]" {
			if i != len(datas)-1 || r.stop == "" {
				t.Fatalf("[DONE] at %d of %d, stop %q", i, len(datas), r.stop)
			}
			continue
		}
		var c struct {
			Choices []struct {
				Delta struct {
					Content   string
					ToolCalls []struct {
						Index    int
						ID       string
						Function struct{ Name, Arguments string }
					} `json:"tool_calls"`
				}
				FinishReason *string `json:"finish_reason"`
			}
		}
		if err := json.Unmarshal(d, &c); err != nil {
			t.Fatalf("chunk %d: %v: %s", i, err, d)
		}
		if len(c.Choices) == 0 {
			continue
		}
		ch := c.Choices[0]
		if r.stop != "" && (ch.Delta.Content != "" || len(ch.Delta.ToolCalls) > 0) {
			t.Fatalf("chunk %d after the finish: %s", i, d)
		}
		r.text += ch.Delta.Content
		for _, tc := range ch.Delta.ToolCalls {
			if args[tc.Index] == nil {
				args[tc.Index] = &ToolCall{ID: tc.ID, Name: tc.Function.Name}
				order = append(order, tc.Index)
			}
			args[tc.Index].Arguments += tc.Function.Arguments
		}
		if ch.FinishReason != nil {
			r.stop = *ch.FinishReason
		}
	}
	if string(datas[len(datas)-1]) != "[DONE]" {
		t.Fatal("doesn't end with [DONE]")
	}
	for _, i := range order {
		r.calls = append(r.calls, *args[i])
	}
	return r
}

// cases is each format and transport: Anthropic, Responses over SSE and
// Codex's WebSocket, Chat Completions.
var cases = []struct {
	name   string
	format string
	ws     bool
	stop   string
}{
	{"anthropic", FormatAnthropic, false, "end_turn"},
	{"responses", FormatResponses, false, "completed"},
	{"responses-ws", FormatResponses, true, "completed"},
	{"chat", FormatChat, false, "stop"},
}

func wantRaw(evs []wireEvent, ws bool) []byte {
	if !ws {
		return raws(evs)
	}
	var b []byte
	for _, e := range evs {
		b = append(append(b, e.data...), 0)
	}
	return b
}

func record(asked *[]TextPiece, a func(TextPiece) Answer) func(TextPiece) Answer {
	return func(p TextPiece) Answer { *asked = append(*asked, p); return a(p) }
}

// Accept: byte for byte as received, pumped after each event or not. Unpumped,
// the pieces gather into one decision at the block's end.
func TestTextAccept(t *testing.T) {
	for _, c := range cases {
		evs := textReply(c.format, "Hello, ", "world", ".")
		for _, pump := range []bool{true, false} {
			var asked []TextPiece
			var opaque []string
			out, _ := feed(t, c.format, c.ws, pump, evs, Deciders{
				Text:   record(&asked, func(TextPiece) Answer { return Answer{} }),
				Opaque: func(id string) { opaque = append(opaque, id) },
			})
			if !bytes.Equal(out, wantRaw(evs, c.ws)) {
				t.Fatalf("%s pump %v: accepted reply changed:\n%s", c.name, pump, out)
			}
			want := 3
			if !pump {
				want = 1
			}
			if len(asked) != want || asked[len(asked)-1].Released+asked[len(asked)-1].Pending != "Hello, world." {
				t.Fatalf("%s pump %v: asked %+v", c.name, pump, asked)
			}
			if pump && (asked[1].Released != "Hello, " || asked[1].Pending != "world" || asked[1].Done) {
				t.Fatalf("%s: second call %+v", c.name, asked[1])
			}
			if !pump && !asked[0].Done {
				t.Fatalf("%s: the one call isn't done: %+v", c.name, asked[0])
			}
			if c.format != FormatChat && len(opaque) != 1 {
				t.Fatalf("%s: opaque %v", c.name, opaque)
			}
			if id := asked[0].ID; id != map[string]string{FormatAnthropic: "msg_t:1", FormatResponses: "msg_t:0", FormatChat: "chatcmpl-t:0"}[c.format] {
				t.Fatalf("%s: id %q", c.name, id)
			}
		}
	}
}

// No text reaches the client before it's decided.
func TestTextHeldUntilDecided(t *testing.T) {
	for _, c := range cases {
		evs := textReply(c.format, "secret one", "secret two")
		s := NewStream(c.format, SSE, Deciders{Text: func(TextPiece) Answer { return Answer{Verdict: Hold} }})
		var out []byte
		for _, e := range evs {
			out = append(out, s.Feed(e.name, []byte(e.data), e.raw())...)
			out = append(out, s.Pump()...)
			if strings.Contains(e.data, "secret two") {
				break
			}
		}
		if bytes.Contains(out, []byte("secret")) {
			t.Fatalf("%s: held text went out:\n%s", c.name, out)
		}
	}
}

func TestTextReplace(t *testing.T) {
	for _, c := range cases {
		evs := textReply(c.format, "Hello, ", "world", ".")
		out, _ := feed(t, c.format, c.ws, true, evs, Deciders{Text: func(p TextPiece) Answer {
			return Answer{Verdict: Replace, Text: strings.ToUpper(p.Pending)}
		}})
		if r := readText(t, c.format, c.ws, out); r.text != "HELLO, WORLD." || r.stop != c.stop {
			t.Fatalf("%s: got %+v\n%s", c.name, r, out)
		}
	}
}

// Hold, then Accept: the second call carries both pieces, and what goes out
// is still byte for byte.
func TestTextHoldThenRelease(t *testing.T) {
	for _, c := range cases {
		evs := textReply(c.format, "Hello, ", "world", ".")
		var asked []TextPiece
		out, _ := feed(t, c.format, c.ws, true, evs, Deciders{Text: record(&asked, func(p TextPiece) Answer {
			if p.Pending == "Hello, " {
				return Answer{Verdict: Hold}
			}
			return Answer{}
		})})
		if !bytes.Equal(out, wantRaw(evs, c.ws)) || len(asked) != 3 || asked[1].Pending != "Hello, world" || asked[1].Released != "" {
			t.Fatalf("%s: asked %+v\n%s", c.name, asked, out)
		}
	}
}

// watch replaces a phrase across pieces, holding back only a tail that could
// still become it (keep), as the library's watches do.
func watch(p TextPiece) Answer {
	s := p.Pending
	keep := 0
	if !p.Done {
		for k := len(phrase) - 1; k > 0; k-- {
			if strings.HasSuffix(s, phrase[:k]) {
				keep = k
				break
			}
		}
	}
	out := strings.ReplaceAll(s, phrase, redacted)
	if out == s {
		return Answer{Keep: keep}
	}
	return Answer{Verdict: Replace, Text: out[:len(out)-keep], Keep: keep}
}

// A phrase split across two and three pieces is seen whole and replaced; the
// text before it isn't held back.
func TestTextSplitMatch(t *testing.T) {
	for _, c := range cases {
		for _, pieces := range [][]string{
			{"The purple ele", "phant is here."},
			{"The pur", "ple ele", "phant is here."},
			{"A purple elephant", ", and ", "a purple", " elephant."},
		} {
			var asked []TextPiece
			out, _ := feed(t, c.format, c.ws, true, textReply(c.format, pieces...), Deciders{Text: record(&asked, watch)})
			want := strings.ReplaceAll(strings.Join(pieces, ""), phrase, redacted)
			if r := readText(t, c.format, c.ws, out); r.text != want || r.stop != c.stop || bytes.Contains(out, []byte("elephant")) {
				t.Fatalf("%s %q: got %+v\n%s", c.name, pieces, r, out)
			}
			if pieces[0] == "The pur" && (asked[1].Released != "The " || asked[1].Pending != "purple ele") {
				t.Fatalf("%s: the text before the match was held back: %+v", c.name, asked)
			}
		}
	}
}

// Respond mid-reply: what was released stays, the text ends the reply
// cleanly, and nothing more of the upstream's goes out.
func TestTextRespond(t *testing.T) {
	for _, c := range cases {
		evs := textReply(c.format, "Hello, ", "world", ".")
		out, s := feed(t, c.format, c.ws, true, evs, Deciders{Text: func(p TextPiece) Answer {
			if p.Pending == "world" {
				return Answer{Verdict: Respond, Text: replyText}
			}
			return Answer{}
		}})
		if r := readText(t, c.format, c.ws, out); r.text != "Hello, "+replyText || r.stop != c.stop || !s.Ended() {
			t.Fatalf("%s: got %+v\n%s", c.name, r, out)
		}
		if bytes.Contains(out, []byte(`"input_tokens":5,"output_tokens":30`)) || bytes.Contains(out, []byte(`"completion_tokens":30`)) {
			t.Fatalf("%s: the upstream's ending went out:\n%s", c.name, out)
		}
	}
}

// A block that ends while its text is held: nothing can stay held, so the
// last call is done and a Hold then releases it.
func TestTextBlockEndsWhileHeld(t *testing.T) {
	for _, c := range cases {
		evs := textReply(c.format, "Hello, ", "world", ".")
		var asked []TextPiece
		out, _ := feed(t, c.format, c.ws, true, evs, Deciders{Text: record(&asked, func(TextPiece) Answer { return Answer{Verdict: Hold} })})
		last := asked[len(asked)-1]
		if !bytes.Equal(out, wantRaw(evs, c.ws)) || !last.Done || last.Pending != "Hello, world." {
			t.Fatalf("%s: asked %+v\n%s", c.name, asked, out)
		}
	}
}

// Stop (the Interceptor failed, fail closed): the held text never goes out.
func TestTextStop(t *testing.T) {
	for _, c := range cases {
		out, s := feed(t, c.format, c.ws, true, textReply(c.format, "secret"), Deciders{Text: func(TextPiece) Answer { return Answer{Verdict: Stop} }})
		if !s.Ended() || bytes.Contains(out, []byte("secret")) {
			t.Fatalf("%s:\n%s", c.name, out)
		}
	}
}

// Without a text decider, text passes as it comes.
func TestTextNoDecider(t *testing.T) {
	for _, c := range cases {
		evs := textReply(c.format, "Hello, ", "world")
		if out, _ := feed(t, c.format, c.ws, false, evs, Deciders{}); !bytes.Equal(out, wantRaw(evs, c.ws)) {
			t.Fatalf("%s:\n%s", c.name, out)
		}
	}
}

// ── Chat Completions tool calls ──────────────────────────────────────────────

var chatTool = []wireEvent{
	{"", `{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"m-1","choices":[{"index":0,"delta":{"role":"assistant","content":"Let me look."},"finish_reason":null}]}`},
	{"", `{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"m-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"shell","arguments":""}}]},"finish_reason":null}]}`},
	{"", `{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"m-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"cmd\":\"rm"}}]},"finish_reason":null}]}`},
	{"", `{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"m-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":" -rf /\"}"}}]},"finish_reason":null}]}`},
	{"", `{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"m-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_2","type":"function","function":{"name":"read","arguments":"{}"}}]},"finish_reason":null}]}`},
	{"", `{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"m-1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`},
	{"", `[DONE]`},
}

func TestChatToolCalls(t *testing.T) {
	var got []ToolCall
	out, _ := feed(t, FormatChat, false, true, chatTool, Deciders{ToolCall: accept(&got)})
	if !bytes.Equal(out, raws(chatTool)) || len(got) != 2 || got[0] != (ToolCall{ID: "call_1", Name: "shell", Arguments: `{"cmd":"rm -rf /"}`}) || got[1].ID != "call_2" {
		t.Fatalf("decided %+v\n%s", got, out)
	}
	// Held until complete: nothing of the first call goes out before the second starts.
	s := NewStream(FormatChat, SSE, Deciders{ToolCall: accept(&got)})
	for i, e := range chatTool[:4] {
		if o := s.Feed(e.name, []byte(e.data), e.raw()); i > 0 && len(o) > 0 {
			t.Fatalf("event %d released %s", i, o)
		}
	}
	out, _ = feed(t, FormatChat, false, true, chatTool, Deciders{ToolCall: func(tc ToolCall) Answer {
		if tc.ID == "call_1" {
			return Answer{Verdict: Replace, Arguments: `{"cmd":"ls"}`}
		}
		return Answer{}
	}})
	if r := readChat(t, out); len(r.calls) != 2 || r.calls[0].Arguments != `{"cmd":"ls"}` || r.stop != "tool_calls" || bytes.Contains(out, []byte("rm -rf")) {
		t.Fatalf("got %+v\n%s", r, out)
	}
	out, s = feed(t, FormatChat, false, true, chatTool, Deciders{ToolCall: func(ToolCall) Answer { return Answer{Verdict: Respond, Text: replyText} }})
	if r := readChat(t, out); len(r.calls) != 0 || r.text != "Let me look."+replyText || r.stop != "stop" || !s.Ended() {
		t.Fatalf("got %+v\n%s", r, out)
	}
}

// ── Whole JSON replies ───────────────────────────────────────────────────────

const chatBody = `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"m-1","choices":[{"index":0,"message":{"role":"assistant","content":"A purple elephant.","tool_calls":[{"id":"call_1","type":"function","function":{"name":"shell","arguments":"{\"cmd\":\"ls\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":30,"total_tokens":35}}`

func TestDecideBodyText(t *testing.T) {
	anthropic := `{"id":"msg_1","type":"message","role":"assistant","model":"m-1","content":[{"type":"thinking","thinking":"hmm","signature":"x"},{"type":"text","text":"A purple elephant."}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":30}}`
	responses := `{"id":"resp_1","object":"response","status":"completed","model":"m-1","output":[{"id":"rs_1","type":"reasoning","summary":[]},{"id":"msg_a","type":"message","role":"assistant","content":[{"type":"output_text","text":"A purple elephant."}]}],"usage":{"input_tokens":5,"output_tokens":30,"total_tokens":35}}`
	for _, c := range []struct{ format, body, id string }{
		{FormatAnthropic, anthropic, "msg_1:1"}, {FormatResponses, responses, "msg_a:0"}, {FormatChat, chatBody, "chatcmpl-1:0"},
	} {
		var asked []TextPiece
		var opaque []string
		dec := Deciders{Text: record(&asked, func(TextPiece) Answer { return Answer{} }), Opaque: func(id string) { opaque = append(opaque, id) },
			ToolCall: func(ToolCall) Answer { return Answer{} }}
		out, _, err := DecideBody(c.format, []byte(c.body), dec)
		if err != nil || string(out) != c.body || len(asked) != 1 || asked[0] != (TextPiece{ID: c.id, Pending: "A purple elephant.", Done: true}) {
			t.Fatalf("%s accept: %v %+v\n%s", c.format, err, asked, out)
		}
		if c.format != FormatChat && len(opaque) != 1 {
			t.Fatalf("%s: opaque %v", c.format, opaque)
		}
		dec.Text = func(p TextPiece) Answer {
			return Answer{Verdict: Replace, Text: strings.ReplaceAll(p.Pending, phrase, redacted)}
		}
		out, _, err = DecideBody(c.format, []byte(c.body), dec)
		if want := strings.Replace(c.body, phrase, redacted, 1); err != nil || string(out) != want {
			t.Fatalf("%s replace: %v\n%s", c.format, err, out)
		}
		dec.Text = func(TextPiece) Answer { return Answer{Verdict: Respond, Text: replyText} }
		out, _, err = DecideBody(c.format, []byte(c.body), dec)
		if err != nil || !json.Valid(out) || bytes.Contains(out, []byte("elephant")) || !bytes.Contains(out, jsonString(replyText)) {
			t.Fatalf("%s respond: %v\n%s", c.format, err, out)
		}
	}
}

func TestDecideChatBodyToolCalls(t *testing.T) {
	var got []ToolCall
	out, _, err := DecideBody(FormatChat, []byte(chatBody), Deciders{ToolCall: accept(&got)})
	if err != nil || string(out) != chatBody || len(got) != 1 || got[0] != (ToolCall{ID: "call_1", Name: "shell", Arguments: `{"cmd":"ls"}`}) {
		t.Fatalf("%v %+v\n%s", err, got, out)
	}
	out, _, _ = DecideBody(FormatChat, []byte(chatBody), Deciders{ToolCall: func(ToolCall) Answer { return Answer{Verdict: Replace, Arguments: `{"cmd":"pwd"}`} }})
	if want := strings.Replace(chatBody, `{\"cmd\":\"ls\"}`, `{\"cmd\":\"pwd\"}`, 1); string(out) != want {
		t.Fatalf("replace:\n%s", out)
	}
	out, _, _ = DecideBody(FormatChat, []byte(chatBody), Deciders{ToolCall: func(ToolCall) Answer { return Answer{Verdict: Respond, Text: "no"} }})
	var m struct {
		Choices []struct {
			Message      map[string]any
			FinishReason string `json:"finish_reason"`
		}
	}
	if json.Unmarshal(out, &m) != nil || m.Choices[0].Message["content"] != "A purple elephant.no" || m.Choices[0].Message["tool_calls"] != nil || m.Choices[0].FinishReason != "stop" {
		t.Fatalf("respond:\n%s", out)
	}
}
