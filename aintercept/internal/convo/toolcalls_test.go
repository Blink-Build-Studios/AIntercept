package convo

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// Tool calls held whole (docs/INTERCEPTOR_V2.md, T1–T2): a reply's text goes
// on at once; each tool call is held from its first event until complete,
// then decided.

type wireEvent struct{ name, data string }

func (e wireEvent) raw() []byte { return SSE(e.name, []byte(e.data)) }

// An Anthropic reply: text, then a Bash call whose input arrives in pieces.
var anthropicTool = []wireEvent{
	{"message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m-1","content":[],"stop_reason":null,"usage":{"input_tokens":5,"output_tokens":1}}}`},
	{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
	{"ping", `{"type": "ping"}`},
	{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me look."}}`},
	{"content_block_stop", `{"type":"content_block_stop","index":0}`},
	{"content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"Bash","input":{}}}`},
	{"content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":""}}`},
	{"content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\": \"echo first"}}`},
	{"content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":" we bing\"}"}}`},
	{"content_block_stop", `{"type":"content_block_stop","index":1}`},
	{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":30}}`},
	{"message_stop", `{"type":"message_stop"}`},
}

// The same for the Responses API (Codex): a message, then a function call.
var responsesTool = []wireEvent{
	{"response.created", `{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","created_at":1,"status":"in_progress","model":"m-1","output":[],"usage":null}}`},
	{"response.in_progress", `{"type":"response.in_progress","sequence_number":1,"response":{"id":"resp_1","object":"response","created_at":1,"status":"in_progress","model":"m-1","output":[],"usage":null}}`},
	{"response.output_item.added", `{"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"id":"msg_a","type":"message","status":"in_progress","role":"assistant","content":[]}}`},
	{"response.output_text.delta", `{"type":"response.output_text.delta","sequence_number":3,"item_id":"msg_a","output_index":0,"content_index":0,"delta":"Let me look."}`},
	{"response.output_item.done", `{"type":"response.output_item.done","sequence_number":4,"output_index":0,"item":{"id":"msg_a","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Let me look.","annotations":[]}]}}`},
	{"response.output_item.added", `{"type":"response.output_item.added","sequence_number":5,"output_index":1,"item":{"id":"fc_1","type":"function_call","status":"in_progress","arguments":"","call_id":"call_1","name":"shell"}}`},
	{"response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","sequence_number":6,"item_id":"fc_1","output_index":1,"delta":"{\"command\":[\"echo\","}`},
	{"response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","sequence_number":7,"item_id":"fc_1","output_index":1,"delta":"\"first we bing\"]}"}`},
	{"response.function_call_arguments.done", `{"type":"response.function_call_arguments.done","sequence_number":8,"item_id":"fc_1","output_index":1,"arguments":"{\"command\":[\"echo\",\"first we bing\"]}"}`},
	{"response.output_item.done", `{"type":"response.output_item.done","sequence_number":9,"output_index":1,"item":{"id":"fc_1","type":"function_call","status":"completed","arguments":"{\"command\":[\"echo\",\"first we bing\"]}","call_id":"call_1","name":"shell"}}`},
	{"response.completed", `{"type":"response.completed","sequence_number":10,"response":{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"m-1","output":[{"id":"msg_a","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Let me look.","annotations":[]}]},{"id":"fc_1","type":"function_call","status":"completed","arguments":"{\"command\":[\"echo\",\"first we bing\"]}","call_id":"call_1","name":"shell"}],"usage":{"input_tokens":5,"input_tokens_details":{"cached_tokens":0},"output_tokens":30,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":35}}}`},
}

// Codex's exec: a custom tool call whose input is free-form text.
var customTool = []wireEvent{
	{"response.created", `{"type":"response.created","sequence_number":0,"response":{"id":"resp_2","object":"response","status":"in_progress","model":"m-1","output":[]}}`},
	{"response.output_item.added", `{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"ctc_1","type":"custom_tool_call","status":"in_progress","call_id":"call_2","name":"exec","input":""}}`},
	{"response.custom_tool_call_input.delta", `{"type":"response.custom_tool_call_input.delta","sequence_number":2,"item_id":"ctc_1","output_index":0,"delta":"echo first "}`},
	{"response.custom_tool_call_input.delta", `{"type":"response.custom_tool_call_input.delta","sequence_number":3,"item_id":"ctc_1","output_index":0,"delta":"we bing"}`},
	{"response.custom_tool_call_input.done", `{"type":"response.custom_tool_call_input.done","sequence_number":4,"item_id":"ctc_1","output_index":0,"input":"echo first we bing"}`},
	{"response.output_item.done", `{"type":"response.output_item.done","sequence_number":5,"output_index":0,"item":{"id":"ctc_1","type":"custom_tool_call","status":"completed","call_id":"call_2","name":"exec","input":"echo first we bing"}}`},
	{"response.completed", `{"type":"response.completed","sequence_number":6,"response":{"id":"resp_2","object":"response","status":"completed","model":"m-1","output":[{"id":"ctc_1","type":"custom_tool_call","status":"completed","call_id":"call_2","name":"exec","input":"echo first we bing"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`},
}

// run feeds a reply event by event and returns what the client gets, and
// after which events something went out.
func run(t *testing.T, format string, ws bool, evs []wireEvent, decide func(ToolCall) Answer) (out []byte, emitted []bool, s *Stream) {
	t.Helper()
	frame := SSE
	if ws {
		frame = func(_ string, data []byte) []byte { return append(append([]byte{}, data...), 0) }
	}
	s = NewStream(format, frame, decide)
	for _, e := range evs {
		raw := e.raw()
		if ws {
			raw = frame("", []byte(e.data))
		}
		o := s.Feed(e.name, []byte(e.data), raw)
		out = append(out, o...)
		emitted = append(emitted, len(o) > 0)
	}
	return out, emitted, s
}

func raws(evs []wireEvent) []byte {
	var b []byte
	for _, e := range evs {
		b = append(b, e.raw()...)
	}
	return b
}

// wsMessages splits run's WebSocket output (each message ends in a 0).
func wsMessages(out []byte) [][]byte {
	var msgs [][]byte
	for _, m := range bytes.Split(bytes.TrimSuffix(out, []byte{0}), []byte{0}) {
		msgs = append(msgs, m)
	}
	return msgs
}

func accept(got *[]ToolCall) func(ToolCall) Answer {
	return func(tc ToolCall) Answer { *got = append(*got, tc); return Answer{} }
}

// Accept: byte for byte as received. Text went on as it came; the tool call
// only when complete.
func TestHoldAnthropicAccept(t *testing.T) {
	var got []ToolCall
	out, emitted, s := run(t, FormatAnthropic, false, anthropicTool, accept(&got))
	if !bytes.Equal(out, raws(anthropicTool)) {
		t.Fatalf("accepted reply changed:\n%s", out)
	}
	want := []bool{true, true, true, true, true, false, false, false, false, true, true, true}
	for i := range want {
		if emitted[i] != want[i] {
			t.Fatalf("emitted after each event %v, want %v (text at once, the call held until its stop)", emitted, want)
		}
	}
	if len(got) != 1 || got[0] != (ToolCall{ID: "toolu_1", Name: "Bash", Arguments: `{"command": "echo first we bing"}`}) {
		t.Fatalf("decided %+v", got)
	}
	if s.Ended() {
		t.Fatal("ended")
	}
}

func TestHoldAnthropicReplace(t *testing.T) {
	out, _, _ := run(t, FormatAnthropic, false, anthropicTool, func(ToolCall) Answer {
		return Answer{Verdict: Replace, Arguments: `{"command":"echo then we bong"}`}
	})
	r := readAnthropicSSE(t, out)
	if r.text != "Let me look." || r.stop != "tool_use" || len(r.calls) != 1 ||
		r.calls[0] != (ToolCall{ID: "toolu_1", Name: "Bash", Arguments: `{"command":"echo then we bong"}`}) {
		t.Fatalf("got %+v", r)
	}
}

// Respond: the tool call and the rest of the reply become the text; the
// reply ends cleanly (end_turn), and nothing after it goes out.
func TestHoldAnthropicRespond(t *testing.T) {
	out, emitted, s := run(t, FormatAnthropic, false, anthropicTool, func(ToolCall) Answer {
		return Answer{Verdict: Respond, Text: replyText}
	})
	r := readAnthropicSSE(t, out)
	if r.text != "Let me look."+replyText || r.stop != "end_turn" || len(r.calls) != 0 {
		t.Fatalf("got %+v", r)
	}
	if !s.Ended() || emitted[10] || emitted[11] {
		t.Fatalf("ended %v, emitted %v: the upstream's own ending must not follow", s.Ended(), emitted)
	}
}

// Stop (the Interceptor failed, fail closed): the held call never goes out;
// the caller ends the reply with its error.
func TestHoldAnthropicStop(t *testing.T) {
	out, _, s := run(t, FormatAnthropic, false, anthropicTool, func(ToolCall) Answer { return Answer{Verdict: Stop} })
	if !s.Ended() || bytes.Contains(out, []byte("toolu_1")) || !bytes.Equal(out, raws(anthropicTool[:5])) {
		t.Fatalf("got %s", out)
	}
}

// Two calls in a reply: each decided on its own, in order.
func TestHoldAnthropicTwoCalls(t *testing.T) {
	evs := append([]wireEvent{}, anthropicTool[:10]...)
	evs = append(evs,
		wireEvent{"content_block_start", `{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_2","name":"Read","input":{}}}`},
		wireEvent{"content_block_stop", `{"type":"content_block_stop","index":2}`},
	)
	evs = append(evs, anthropicTool[10:]...)
	var got []ToolCall
	out, _, _ := run(t, FormatAnthropic, false, evs, func(tc ToolCall) Answer {
		got = append(got, tc)
		if tc.ID == "toolu_2" {
			return Answer{Verdict: Respond, Text: "no reading"}
		}
		return Answer{}
	})
	r := readAnthropicSSE(t, out)
	// A call with no input pieces has the input it started with.
	if len(got) != 2 || got[1].Arguments != "{}" || len(r.calls) != 1 || r.text != "Let me look.no reading" || r.stop != "end_turn" {
		t.Fatalf("decided %+v; got %+v", got, r)
	}
}

func TestHoldResponsesAccept(t *testing.T) {
	for _, ws := range []bool{false, true} {
		var got []ToolCall
		out, emitted, _ := run(t, FormatResponses, ws, responsesTool, accept(&got))
		want := raws(responsesTool)
		if ws {
			want = nil
			for _, e := range responsesTool {
				want = append(append(want, e.data...), 0)
			}
		}
		if !bytes.Equal(out, want) {
			t.Fatalf("ws %v: accepted reply changed:\n%s", ws, out)
		}
		if emitted[3] != true || emitted[5] || emitted[6] || emitted[7] || emitted[8] || !emitted[9] {
			t.Fatalf("ws %v: emitted %v", ws, emitted)
		}
		if len(got) != 1 || got[0] != (ToolCall{ID: "call_1", Name: "shell", Arguments: `{"command":["echo","first we bing"]}`}) {
			t.Fatalf("decided %+v", got)
		}
	}
}

func readResponses(t *testing.T, ws bool, out []byte) reply {
	t.Helper()
	if ws {
		return readResponsesEvents(t, wsMessages(out))
	}
	names, datas := sseEvents(t, out)
	for i, d := range datas {
		var ev struct{ Type string }
		_ = json.Unmarshal(d, &ev)
		if names[i] != ev.Type {
			t.Fatalf("event %d named %q is a %q", i, names[i], ev.Type)
		}
	}
	return readResponsesEvents(t, datas)
}

// Replace: the new arguments in every event that carries them, and in the
// completed response.
func TestHoldResponsesReplace(t *testing.T) {
	for _, ws := range []bool{false, true} {
		out, _, _ := run(t, FormatResponses, ws, responsesTool, func(ToolCall) Answer {
			return Answer{Verdict: Replace, Arguments: `{"command":["echo","then we bong"]}`}
		})
		r := readResponses(t, ws, out)
		if r.text != "Let me look." || len(r.calls) != 1 || r.calls[0].Arguments != `{"command":["echo","then we bong"]}` {
			t.Fatalf("ws %v: got %+v", ws, r)
		}
		if bytes.Contains(out, []byte("first we bing")) {
			t.Fatalf("ws %v: the old arguments went out:\n%s", ws, out)
		}
	}
}

func TestHoldResponsesRespond(t *testing.T) {
	for _, ws := range []bool{false, true} {
		out, emitted, s := run(t, FormatResponses, ws, responsesTool, func(ToolCall) Answer {
			return Answer{Verdict: Respond, Text: replyText}
		})
		r := readResponses(t, ws, out)
		if r.text != "Let me look."+replyText || len(r.calls) != 0 || r.stop != "completed" {
			t.Fatalf("ws %v: got %+v", ws, r)
		}
		if !s.Ended() || emitted[10] {
			t.Fatalf("ws %v: the upstream's completed went out", ws)
		}
	}
}

func TestHoldCustomToolCall(t *testing.T) {
	var got []ToolCall
	run(t, FormatResponses, true, customTool, accept(&got))
	if len(got) != 1 || got[0] != (ToolCall{ID: "call_2", Name: "exec", Arguments: `"echo first we bing"`, Custom: true}) {
		t.Fatalf("decided %+v", got)
	}
	out, _, _ := run(t, FormatResponses, true, customTool, func(ToolCall) Answer {
		return Answer{Verdict: Replace, Arguments: `"echo then we bong"`}
	})
	r := readResponsesEvents(t, wsMessages(out))
	if len(r.calls) != 1 || r.calls[0].Arguments != `"echo then we bong"` || bytes.Contains(out, []byte("bing")) {
		t.Fatalf("got %+v\n%s", r, out)
	}
}

// A call larger than the hold cap goes out unheld, undecided.
func TestHoldOverCap(t *testing.T) {
	defer func(n int) { holdCap = n }(holdCap)
	holdCap = 200
	decided := 0
	out, _, s := run(t, FormatAnthropic, false, anthropicTool, func(ToolCall) Answer { decided++; return Answer{Verdict: Stop} })
	if decided != 0 || !bytes.Equal(out, raws(anthropicTool)) || s.Unheld() != 1 {
		t.Fatalf("decided %d, unheld %d:\n%s", decided, s.Unheld(), out)
	}
}

// A reply that fails mid-call: the held events and the error go out,
// undecided; so does a call cut off by the end of the stream.
func TestHoldReleasedOnFailure(t *testing.T) {
	evs := append(append([]wireEvent{}, anthropicTool[:7]...), wireEvent{"error", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`})
	decided := 0
	out, _, s := run(t, FormatAnthropic, false, evs, func(ToolCall) Answer { decided++; return Answer{} })
	if decided != 0 || !bytes.Equal(out, raws(evs)) {
		t.Fatalf("decided %d:\n%s", decided, out)
	}
	out, _, s = run(t, FormatAnthropic, false, anthropicTool[:7], func(ToolCall) Answer { decided++; return Answer{} })
	if out = append(out, s.Flush()...); decided != 0 || !bytes.Equal(out, raws(anthropicTool[:7])) {
		t.Fatalf("decided %d:\n%s", decided, out)
	}
}

// ── Whole JSON bodies ────────────────────────────────────────────────────────

const anthropicBody = `{"id":"msg_1","type":"message","role":"assistant","model":"m-1","content":[{"type":"text","text":"Let me look."},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"echo first we bing"}},{"type":"tool_use","id":"toolu_2","name":"Read","input":{"file_path":"/a"}}],"stop_reason":"tool_use","usage":{"input_tokens":5,"output_tokens":30}}`

const responsesBody = `{"id":"resp_1","object":"response","status":"completed","model":"m-1","output":[{"id":"msg_a","type":"message","role":"assistant","content":[{"type":"output_text","text":"Let me look."}]},{"id":"fc_1","type":"function_call","status":"completed","arguments":"{\"command\":\"echo first we bing\"}","call_id":"call_1","name":"shell"}],"usage":{"input_tokens":5,"output_tokens":30,"total_tokens":35}}`

func TestDecideBodyAccept(t *testing.T) {
	for _, c := range []struct{ format, body string }{{FormatAnthropic, anthropicBody}, {FormatResponses, responsesBody}} {
		var got []ToolCall
		out, stopped, err := DecideBody(c.format, []byte(c.body), accept(&got))
		if err != nil || stopped || string(out) != c.body {
			t.Fatalf("%s: %v %v %s", c.format, err, stopped, out)
		}
		if c.format == FormatAnthropic && (len(got) != 2 || got[0].Arguments != `{"command":"echo first we bing"}` || got[1].ID != "toolu_2") {
			t.Fatalf("decided %+v", got)
		}
		if c.format == FormatResponses && (len(got) != 1 || got[0] != (ToolCall{ID: "call_1", Name: "shell", Arguments: `{"command":"echo first we bing"}`})) {
			t.Fatalf("decided %+v", got)
		}
	}
}

func TestDecideBodyReplace(t *testing.T) {
	out, _, err := DecideBody(FormatAnthropic, []byte(anthropicBody), func(tc ToolCall) Answer {
		if tc.ID == "toolu_1" {
			return Answer{Verdict: Replace, Arguments: `{"command":"echo then we bong"}`}
		}
		return Answer{}
	})
	if want := strings.Replace(anthropicBody, `{"command":"echo first we bing"}`, `{"command":"echo then we bong"}`, 1); err != nil || string(out) != want {
		t.Fatalf("%v\n%s", err, out)
	}
	out, _, err = DecideBody(FormatResponses, []byte(responsesBody), func(ToolCall) Answer {
		return Answer{Verdict: Replace, Arguments: `{"command":"echo then we bong"}`}
	})
	if want := strings.Replace(responsesBody, `first we bing`, `then we bong`, 1); err != nil || string(out) != want {
		t.Fatalf("%v\n%s", err, out)
	}
}

// Respond: the reply ends at the call with the text; the calls after it are
// never asked about.
func TestDecideBodyRespond(t *testing.T) {
	asked := 0
	out, _, err := DecideBody(FormatAnthropic, []byte(anthropicBody), func(ToolCall) Answer {
		asked++
		return Answer{Verdict: Respond, Text: replyText}
	})
	var m struct {
		Content []struct {
			Type, Text string
		}
		StopReason string `json:"stop_reason"`
	}
	if err != nil || json.Unmarshal(out, &m) != nil || asked != 1 || len(m.Content) != 2 || m.Content[0].Text != "Let me look." ||
		m.Content[1].Type != "text" || m.Content[1].Text != replyText || m.StopReason != "end_turn" {
		t.Fatalf("%v asked %d\n%s", err, asked, out)
	}
	out, _, err = DecideBody(FormatResponses, []byte(responsesBody), func(ToolCall) Answer {
		return Answer{Verdict: Respond, Text: replyText}
	})
	var r struct {
		Status string
		Output []struct {
			Type    string
			Content []struct{ Type, Text string }
		}
	}
	if err != nil || json.Unmarshal(out, &r) != nil || r.Status != "completed" || len(r.Output) != 2 || r.Output[1].Type != "message" ||
		r.Output[1].Content[0].Text != replyText {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestDecideBodyStop(t *testing.T) {
	_, stopped, err := DecideBody(FormatAnthropic, []byte(anthropicBody), func(ToolCall) Answer { return Answer{Verdict: Stop} })
	if err != nil || !stopped {
		t.Fatalf("%v %v", err, stopped)
	}
}

func TestDecideBodyNotAReply(t *testing.T) {
	for _, b := range []string{`{"type":"error","error":{"message":"x"}}`, `not json`, `[]`} {
		var got []ToolCall
		out, stopped, err := DecideBody(FormatAnthropic, []byte(b), accept(&got))
		if err != nil || stopped || string(out) != b || len(got) != 0 {
			t.Fatalf("%s: %v %v %s", b, err, stopped, out)
		}
	}
}
