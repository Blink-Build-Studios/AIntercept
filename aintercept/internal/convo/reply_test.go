package convo

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// A synthesized reply is read back with the parsers AIntercept uses for real
// traffic (events: the call's detail, its metadata, its usage) and with a
// strict walk of each format's event sequence, and must be a well-formed
// reply carrying the text, with zero usage.

const replyText = "Well, tricks are for kids. \"Quoted\" <b>& more</b>\nSecond line."

// sseEvents splits an event stream into (name, data) pairs, failing on
// anything that isn't one.
func sseEvents(t *testing.T, body []byte) (names []string, datas [][]byte) {
	t.Helper()
	if !bytes.HasSuffix(body, []byte("\n\n")) {
		t.Fatalf("event stream doesn't end with a blank line: %q", body)
	}
	for _, ev := range strings.Split(strings.TrimSuffix(string(body), "\n\n"), "\n\n") {
		var name, data string
		for _, line := range strings.Split(ev, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
			default:
				t.Fatalf("not an event line: %q", line)
			}
		}
		names, datas = append(names, name), append(datas, []byte(data))
	}
	return names, datas
}

// reply is what a client gets from a reply: its text, its tool calls, how it
// stopped.
type reply struct {
	text  string
	calls []ToolCall
	stop  string
}

// readAnthropicSSE walks an Anthropic event stream strictly: message_start
// first, content blocks opened, filled and closed in index order, then
// message_delta and message_stop.
func readAnthropicSSE(t *testing.T, body []byte) reply {
	t.Helper()
	names, datas := sseEvents(t, body)
	var out reply
	open, next := -1, 0
	var cur *ToolCall
	var args strings.Builder
	for i, d := range datas {
		var ev struct {
			Type         string `json:"type"`
			Index        int    `json:"index"`
			ContentBlock *struct {
				Type, ID, Name, Text string
				Input                json.RawMessage
			} `json:"content_block"`
			Delta *struct {
				Type, Text  string
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
			Message *struct {
				ID, Type, Role, Model string
			} `json:"message"`
		}
		if err := json.Unmarshal(d, &ev); err != nil {
			t.Fatalf("event %d: %v: %s", i, err, d)
		}
		if names[i] != ev.Type {
			t.Fatalf("event %d is named %q but is a %q", i, names[i], ev.Type)
		}
		switch ev.Type {
		case "message_start":
			if i != 0 || ev.Message == nil || ev.Message.Type != "message" || ev.Message.Role != "assistant" || !strings.HasPrefix(ev.Message.ID, "msg_") {
				t.Fatalf("message_start %d: %s", i, d)
			}
		case "content_block_start":
			if open != -1 || ev.Index != next || ev.ContentBlock == nil {
				t.Fatalf("block start %d (open %d, next %d): %s", i, open, next, d)
			}
			open = ev.Index
			if ev.ContentBlock.Type == "tool_use" {
				cur = &ToolCall{ID: ev.ContentBlock.ID, Name: ev.ContentBlock.Name}
				args.Reset()
			}
		case "content_block_delta":
			if ev.Index != open || ev.Delta == nil {
				t.Fatalf("delta %d outside its block: %s", i, d)
			}
			switch ev.Delta.Type {
			case "text_delta":
				out.text += ev.Delta.Text
			case "input_json_delta":
				args.WriteString(ev.Delta.PartialJSON)
			}
		case "content_block_stop":
			if ev.Index != open {
				t.Fatalf("stop %d for block %d, open %d", i, ev.Index, open)
			}
			if cur != nil {
				cur.Arguments = args.String()
				if !json.Valid([]byte(cur.Arguments)) {
					t.Fatalf("tool call %s's input isn't JSON: %s", cur.ID, cur.Arguments)
				}
				out.calls = append(out.calls, *cur)
				cur = nil
			}
			open, next = -1, next+1
		case "message_delta":
			if open != -1 || ev.Delta == nil {
				t.Fatalf("message_delta %d inside a block: %s", i, d)
			}
			out.stop = ev.Delta.StopReason
		case "message_stop":
			if i != len(datas)-1 || out.stop == "" {
				t.Fatalf("message_stop %d of %d, stop %q", i, len(datas), out.stop)
			}
		case "ping":
		default:
			t.Fatalf("unknown event %s", d)
		}
	}
	if names[len(names)-1] != "message_stop" {
		t.Fatalf("doesn't end with message_stop: %v", names)
	}
	return out
}

// readResponsesEvents walks a Responses event sequence strictly: created
// first, completed last, every item added then done, sequence numbers
// ascending. The completed response's output must say what the items did.
func readResponsesEvents(t *testing.T, msgs [][]byte) reply {
	t.Helper()
	var out reply
	var items [][]byte
	open := map[int]bool{}
	seq := int64(-1)
	for i, m := range msgs {
		var ev struct {
			Type           string          `json:"type"`
			SequenceNumber *int64          `json:"sequence_number"`
			OutputIndex    int             `json:"output_index"`
			Item           json.RawMessage `json:"item"`
			Delta          string          `json:"delta"`
			Response       *struct {
				ID     string            `json:"id"`
				Object string            `json:"object"`
				Status string            `json:"status"`
				Output []json.RawMessage `json:"output"`
				Usage  *json.RawMessage  `json:"usage"`
			} `json:"response"`
		}
		if err := json.Unmarshal(m, &ev); err != nil {
			t.Fatalf("event %d: %v: %s", i, err, m)
		}
		if !strings.HasPrefix(string(m), `{"type":`) {
			t.Fatalf("event %d doesn't lead with its type: %s", i, m)
		}
		if ev.SequenceNumber == nil || *ev.SequenceNumber <= seq {
			t.Fatalf("event %d: sequence number not ascending after %d: %s", i, seq, m)
		}
		seq = *ev.SequenceNumber
		switch ev.Type {
		case "response.created":
			if i != 0 || ev.Response == nil || !strings.HasPrefix(ev.Response.ID, "resp_") || ev.Response.Object != "response" {
				t.Fatalf("created %d: %s", i, m)
			}
		case "response.output_item.added":
			if open[ev.OutputIndex] {
				t.Fatalf("item %d added twice", ev.OutputIndex)
			}
			open[ev.OutputIndex] = true
		case "response.output_text.delta":
			out.text += ev.Delta
		case "response.output_item.done":
			if !open[ev.OutputIndex] {
				t.Fatalf("item %d done before added", ev.OutputIndex)
			}
			delete(open, ev.OutputIndex)
			items = append(items, ev.Item)
			var it struct {
				Type, ID, CallID, Name, Arguments string
				Input                             json.RawMessage
			}
			_ = json.Unmarshal(ev.Item, &it)
			switch it.Type {
			case "function_call":
				out.calls = append(out.calls, ToolCall{ID: it.CallID, Name: it.Name, Arguments: it.Arguments})
			case "custom_tool_call":
				out.calls = append(out.calls, ToolCall{ID: it.CallID, Name: it.Name, Arguments: string(it.Input), Custom: true})
			}
		case "response.completed":
			if i != len(msgs)-1 || ev.Response == nil || ev.Response.Status != "completed" || len(open) > 0 {
				t.Fatalf("completed %d of %d (open %v): %s", i, len(msgs), open, m)
			}
			if len(ev.Response.Output) != len(items) {
				t.Fatalf("completed has %d output items, the stream %d", len(ev.Response.Output), len(items))
			}
			for k := range items {
				if !jsonEqual(ev.Response.Output[k], items[k]) {
					t.Fatalf("completed output %d\n%s\nisn't the item streamed\n%s", k, ev.Response.Output[k], items[k])
				}
			}
			out.stop = ev.Response.Status
		}
	}
	if out.stop == "" {
		t.Fatalf("no response.completed")
	}
	return out
}

func jsonEqual(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	xa, _ := json.Marshal(x)
	ya, _ := json.Marshal(y)
	return bytes.Equal(xa, ya)
}

func sseDatas(t *testing.T, body []byte) [][]byte {
	_, datas := sseEvents(t, body)
	return datas
}

// checkRecorded reads a reply the way AIntercept reads a recorded call: its
// text, how it stopped, and zero usage.
func checkRecorded(t *testing.T, body []byte, wantStop string) {
	t.Helper()
	d := events.DetailFromBodies(nil, "", body, "")
	if want := strings.Join(strings.Fields(replyText), " "); d == nil || d.Text != want[:min(len(want), 120)] || len(d.Calls) != 0 {
		t.Errorf("recorded detail %+v, want the text and no calls", d)
	}
	if m := events.ResponseMetaFromBody(body, ""); m.StopReason != wantStop || m.Model != "m-1" || m.MessageID == "" {
		t.Errorf("recorded meta %+v, want stop %q, model m-1 and an id", m, wantStop)
	}
	if u, _ := events.UsageFromResponseBody(body, ""); u != (events.Usage{}) {
		t.Errorf("usage %+v, want zero", u)
	}
}

var (
	anthropicIDs = regexp.MustCompile(`^msg_01[1-9A-HJ-NP-Za-km-z]{22}$`)
	openaiIDs    = regexp.MustCompile(`^(resp|msg)_[0-9a-f]{48}$`)
	chatIDs      = regexp.MustCompile(`^chatcmpl-[0-9A-Za-z]{29}$`)
)

func TestSynthesizeAnthropicStream(t *testing.T) {
	ct, body := Synthesize(FormatAnthropic, "m-1", replyText, true)
	if ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	r := readAnthropicSSE(t, body)
	if r.text != replyText || r.stop != "end_turn" || len(r.calls) != 0 {
		t.Fatalf("got %+v", r)
	}
	var start struct {
		Message struct {
			ID    string
			Usage map[string]int
		}
	}
	_ = json.Unmarshal(sseDatas(t, body)[0], &start)
	if !anthropicIDs.MatchString(start.Message.ID) {
		t.Errorf("id %q isn't in Anthropic's style", start.Message.ID)
	}
	checkRecorded(t, body, "end_turn")
}

func TestSynthesizeAnthropicJSON(t *testing.T) {
	ct, body := Synthesize(FormatAnthropic, "m-1", replyText, false)
	if ct != "application/json" {
		t.Fatalf("content type %q", ct)
	}
	var m struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Role       string `json:"role"`
		StopReason string `json:"stop_reason"`
		Content    []struct{ Type, Text string }
		Usage      map[string]int
	}
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	if !anthropicIDs.MatchString(m.ID) || m.Type != "message" || m.Role != "assistant" || m.StopReason != "end_turn" ||
		len(m.Content) != 1 || m.Content[0].Type != "text" || m.Content[0].Text != replyText {
		t.Fatalf("got %s", body)
	}
	if m.Usage["input_tokens"] != 0 || m.Usage["output_tokens"] != 0 || len(m.Usage) < 2 {
		t.Errorf("usage %v", m.Usage)
	}
	checkRecorded(t, body, "end_turn")
}

func TestSynthesizeResponsesStream(t *testing.T) {
	ct, body := Synthesize(FormatResponses, "m-1", replyText, true)
	if ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	names, datas := sseEvents(t, body)
	for i, d := range datas {
		var ev struct{ Type string }
		_ = json.Unmarshal(d, &ev)
		if names[i] != ev.Type {
			t.Fatalf("event %d named %q is a %q", i, names[i], ev.Type)
		}
	}
	r := readResponsesEvents(t, datas)
	if r.text != replyText || r.stop != "completed" || len(r.calls) != 0 {
		t.Fatalf("got %+v", r)
	}
	checkResponsesIDs(t, datas[len(datas)-1])
	checkRecorded(t, body, "completed")
}

// Over Codex's WebSocket the same events are each a message.
func TestSynthesizeResponsesWebSocket(t *testing.T) {
	msgs := SynthesizeEvents("m-1", replyText)
	r := readResponsesEvents(t, msgs)
	if r.text != replyText || r.stop != "completed" {
		t.Fatalf("got %+v", r)
	}
	checkResponsesIDs(t, msgs[len(msgs)-1])
	// AIntercept records a WebSocket turn as its events, SSE-framed.
	var framed []byte
	for _, m := range msgs {
		framed = append(append(append(framed, "data: "...), m...), "\n\n"...)
	}
	checkRecorded(t, framed, "completed")
}

func checkResponsesIDs(t *testing.T, completed []byte) {
	t.Helper()
	var c struct {
		Response struct {
			ID     string
			Model  string
			Output []struct{ ID, Type, Role, Status string }
			Usage  map[string]any
		}
	}
	if err := json.Unmarshal(completed, &c); err != nil {
		t.Fatal(err)
	}
	if !openaiIDs.MatchString(c.Response.ID) || len(c.Response.Output) != 1 || !openaiIDs.MatchString(c.Response.Output[0].ID) ||
		c.Response.Output[0].Type != "message" || c.Response.Output[0].Role != "assistant" || c.Response.Output[0].Status != "completed" {
		t.Errorf("completed %s", completed)
	}
	for _, k := range []string{"input_tokens", "output_tokens", "total_tokens", "input_tokens_details", "output_tokens_details"} {
		if _, ok := c.Response.Usage[k]; !ok {
			t.Errorf("usage lacks %s: %v", k, c.Response.Usage)
		}
	}
}

func TestSynthesizeResponsesJSON(t *testing.T) {
	ct, body := Synthesize(FormatResponses, "m-1", replyText, false)
	if ct != "application/json" {
		t.Fatalf("content type %q", ct)
	}
	var r struct {
		Object, Status string
		Output         []struct {
			Content []struct{ Type, Text string }
		}
	}
	if err := json.Unmarshal(body, &r); err != nil || r.Object != "response" || r.Status != "completed" ||
		len(r.Output) != 1 || len(r.Output[0].Content) != 1 || r.Output[0].Content[0].Text != replyText {
		t.Fatalf("got %v %s", err, body)
	}
	checkResponsesIDs(t, []byte(`{"response":`+string(body)+`}`))
	// (AIntercept reads a Responses stop reason from the stream's terminal event only.)
	checkRecorded(t, body, "")
}

func TestSynthesizeChatStream(t *testing.T) {
	ct, body := Synthesize(FormatChat, "m-1", replyText, true)
	if ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	names, datas := sseEvents(t, body)
	if string(datas[len(datas)-1]) != "[DONE]" {
		t.Fatalf("doesn't end with [DONE]: %s", body)
	}
	var text, finish, id string
	for i, d := range datas[:len(datas)-1] {
		var c struct {
			ID, Object, Model string
			Choices           []struct {
				Index        int
				Delta        struct{ Role, Content string }
				FinishReason *string `json:"finish_reason"`
			}
		}
		if err := json.Unmarshal(d, &c); err != nil || names[i] != "" || c.Object != "chat.completion.chunk" || c.Model != "m-1" || len(c.Choices) != 1 {
			t.Fatalf("chunk %d: %v %s", i, err, d)
		}
		if id != "" && c.ID != id {
			t.Fatalf("chunk ids differ")
		}
		id = c.ID
		text += c.Choices[0].Delta.Content
		if f := c.Choices[0].FinishReason; f != nil {
			finish = *f
		}
	}
	if text != replyText || finish != "stop" || !chatIDs.MatchString(id) {
		t.Fatalf("text %q finish %q id %q", text, finish, id)
	}
	if m := events.ResponseMetaFromBody(body, ""); m.StopReason != "stop" || m.Model != "m-1" {
		t.Errorf("recorded meta %+v", m)
	}
}

func TestSynthesizeChatJSON(t *testing.T) {
	ct, body := Synthesize(FormatChat, "m-1", replyText, false)
	if ct != "application/json" {
		t.Fatalf("content type %q", ct)
	}
	var c struct {
		ID, Object, Model string
		Choices           []struct {
			Message      struct{ Role, Content string }
			FinishReason string `json:"finish_reason"`
		}
		Usage map[string]int
	}
	if err := json.Unmarshal(body, &c); err != nil || c.Object != "chat.completion" || !chatIDs.MatchString(c.ID) || len(c.Choices) != 1 ||
		c.Choices[0].Message.Role != "assistant" || c.Choices[0].Message.Content != replyText || c.Choices[0].FinishReason != "stop" {
		t.Fatalf("got %v %s", err, body)
	}
	if u, ok := events.UsageFromResponseBody(body, ""); !ok || u != (events.Usage{}) {
		t.Errorf("usage %+v %v", u, ok)
	}
}

// Ids differ from reply to reply.
func TestSynthesizeFreshIDs(t *testing.T) {
	_, a := Synthesize(FormatAnthropic, "m", "x", false)
	_, b := Synthesize(FormatAnthropic, "m", "x", false)
	if bytes.Equal(a, b) {
		t.Fatal("two replies are identical")
	}
}

// ── Rewritten: the provider keeps the conversation ──────────────────────────

func TestRewrite(t *testing.T) {
	cases := []struct {
		name, host, path, body, want string
	}{{
		name: "anthropic thread: the newest prompt's last text",
		host: anthropicHost, path: anthropicPath,
		body: `{"model":"m","thread":{"previous_message_id":"msg_1"},"messages":[{"role":"user","content":[{"type":"text","text":"<reminder>"},{"type":"text","text":"first we bing","cache_control":{"type":"ephemeral"}}]}]}`,
		want: `{"model":"m","thread":{"previous_message_id":"msg_1"},"messages":[{"role":"user","content":[{"type":"text","text":"<reminder>"},{"type":"text","text":"` + RespondInstruction + `then we bong","cache_control":{"type":"ephemeral"}}]}]}`,
	}, {
		name: "responses previous_response_id: a string prompt",
		host: codexHost, path: codexPath,
		body: `{"model":"m","previous_response_id":"resp_1","input":"first we bing"}`,
		want: `{"model":"m","previous_response_id":"resp_1","input":"` + RespondInstruction + `then we bong"}`,
	}, {
		name: "responses: tool output then a user note: the note",
		host: codexHost, path: codexPath,
		body: `{"previous_response_id":"resp_1","input":[{"type":"function_call_output","call_id":"c","output":"ok"},{"type":"message","role":"user","content":[{"type":"input_text","text":"first we bing"}]}]}`,
		want: `{"previous_response_id":"resp_1","input":[{"type":"function_call_output","call_id":"c","output":"ok"},{"type":"message","role":"user","content":[{"type":"input_text","text":"` + RespondInstruction + `then we bong"}]}]}`,
	}, {
		name: "only tool results: the last one",
		host: codexHost, path: codexPath,
		body: `{"previous_response_id":"resp_1","input":[{"type":"function_call_output","call_id":"a","output":"one"},{"type":"function_call_output","call_id":"b","output":"two"}]}`,
		want: `{"previous_response_id":"resp_1","input":[{"type":"function_call_output","call_id":"a","output":"one"},{"type":"function_call_output","call_id":"b","output":"` + RespondInstruction + `then we bong"}]}`,
	}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := mustParse(t, c.host, c.path, c.body)
			got, err := r.Rewrite("then we bong")
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != c.want {
				t.Fatalf("got\n%s\nwant\n%s", got, c.want)
			}
		})
	}
}

// Only the new messages are rewritten: history that says the same is as sent.
func TestRewriteOnlyTheNew(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"first we bing"},{"role":"assistant","content":"ok"},{"role":"user","content":"first we bing"}]}`
	r := mustParse(t, anthropicHost, anthropicPath, body)
	got, err := r.Rewrite("then we bong")
	if err != nil {
		t.Fatal(err)
	}
	want := `{"messages":[{"role":"user","content":"first we bing"},{"role":"assistant","content":"ok"},{"role":"user","content":"` + RespondInstruction + `then we bong"}]}`
	if string(got) != want {
		t.Fatalf("got %s", got)
	}
}

func TestRewriteNothingToRewrite(t *testing.T) {
	r := mustParse(t, anthropicHost, anthropicPath, `{"thread":{"previous_message_id":"m"},"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.test/a.png"}}]}]}`)
	if _, err := r.Rewrite("x"); err == nil {
		t.Fatal("rewrote a request with no text")
	}
}
