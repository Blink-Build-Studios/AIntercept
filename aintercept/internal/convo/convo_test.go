package convo

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// Endpoints the tests send to.
const (
	anthropicHost, anthropicPath = "api.anthropic.com", "/v1/messages?beta=true"
	codexHost, codexPath         = "chatgpt.com", "/backend-api/codex/responses"
	openaiHost                   = "api.openai.com"
	responsesPath, chatPath      = "/v1/responses", "/v1/chat/completions"
)

// summary is a message in one line, for table tests: role, then each part.
func summary(m Message) string {
	var parts []string
	for _, p := range m.Parts {
		var s string
		switch p.Kind {
		case KindText:
			s = fmt.Sprintf("text %q", p.Text)
		case KindMedia:
			if p.URL != "" {
				s = "media " + p.URL
			} else {
				s = fmt.Sprintf("media %s %dB", p.MediaType, p.Size)
			}
		case KindToolCall:
			s = fmt.Sprintf("call %s %s %s", p.CallID, p.Name, p.Arguments)
		case KindToolResult:
			s = fmt.Sprintf("result %s %q", p.CallID, p.Text)
			if p.IsError {
				s += " error"
			}
		case KindOpaque:
			s = "opaque"
		case KindToolAdded:
			s = "tool_added " + p.Name
		default:
			s = "?" + string(p.Kind)
		}
		if p.Ref {
			s += fmt.Sprintf(" ref %dB", p.Size)
		}
		parts = append(parts, s)
	}
	return string(m.Role) + ": " + strings.Join(parts, ", ")
}

func summaries(r Request, idx []int) []string {
	out := []string{}
	for _, i := range idx {
		out = append(out, summary(r.Messages[i]))
	}
	return out
}

func all(r Request) []int {
	idx := []int{}
	for i := range r.Messages {
		idx = append(idx, i)
	}
	return idx
}

// mustParse parses a request that must be ok, and checks the invariants every
// parsed request keeps.
func mustParse(t *testing.T, host, path, body string) Request {
	t.Helper()
	r, ok := Parse(host, path, []byte(body))
	if !ok {
		t.Fatalf("Parse(%s%s) not ok for %s", host, path, body)
	}
	checkInvariants(t, r)
	return r
}

// checkInvariants: indexes in order, New ascending and in range, and every
// span inside the body and a whole JSON value, each part inside its message.
func checkInvariants(t *testing.T, r Request) {
	t.Helper()
	inBody := func(s Span) bool { return 0 <= s.Start && s.Start < s.End && s.End <= len(r.Body) }
	for i, m := range r.Messages {
		if m.Index != i {
			t.Errorf("message %d has Index %d", i, m.Index)
		}
		if !inBody(m.Span) {
			t.Errorf("message %d span %+v outside the body (%d bytes)", i, m.Span, len(r.Body))
			continue
		}
		for j, p := range m.Parts {
			if !inBody(p.Span) || !json.Valid(r.Body[p.Span.Start:p.Span.End]) {
				t.Errorf("message %d part %d span %+v isn't a JSON value in the body", i, j, p.Span)
			}
			if p.Span.Start < m.Span.Start || p.Span.End > m.Span.End {
				t.Errorf("message %d part %d span %+v outside the message's %+v", i, j, p.Span, m.Span)
			}
		}
	}
	for k, i := range r.New {
		if i < 0 || i >= len(r.Messages) || (k > 0 && i <= r.New[k-1]) {
			t.Errorf("New %v isn't ascending indexes into %d messages", r.New, len(r.Messages))
		}
	}
}

type newCase struct {
	name    string
	body    string
	all     []string // every message, by summary
	new     []string // the new ones
	chained bool
}

func runNewCases(t *testing.T, host, path string, cases []newCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := mustParse(t, host, path, c.body)
			if got := summaries(r, all(r)); !reflect.DeepEqual(got, c.all) {
				t.Errorf("messages:\n got %q\nwant %q", got, c.all)
			}
			if got := summaries(r, r.New); !reflect.DeepEqual(got, c.new) {
				t.Errorf("new:\n got %q\nwant %q", got, c.new)
			}
			if r.Chained != c.chained {
				t.Errorf("Chained = %v, want %v", r.Chained, c.chained)
			}
		})
	}
}

func TestAnthropicNew(t *testing.T) {
	runNewCases(t, anthropicHost, anthropicPath, []newCase{{
		name: "a new prompt after history: only it is new",
		body: `{"model":"claude-x","system":[{"type":"text","text":"You are a coding agent."}],"tools":[{"name":"Bash"}],
			"messages":[
			{"role":"user","content":"hi"},
			{"role":"assistant","content":[{"type":"text","text":"Hello."}]},
			{"role":"user","content":[{"type":"text","text":"fix the flaky timer test"}]}]}`,
		all: []string{`user: text "hi"`, `assistant: text "Hello."`, `user: text "fix the flaky timer test"`},
		new: []string{`user: text "fix the flaky timer test"`},
	}, {
		name: "tool results after a tool call: only they (and the note with them) are new",
		body: `{"messages":[
			{"role":"user","content":"run the tests"},
			{"role":"assistant","content":[{"type":"text","text":"Running them."},
				{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"go test ./..."}},
				{"type":"tool_use","id":"toolu_2","name":"Read","input":{"file_path":"a.go"}}]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"toolu_1","content":"ok","is_error":false},
				{"type":"tool_result","tool_use_id":"toolu_2","is_error":true,"content":[{"type":"text","text":"no such file"}]},
				{"type":"text","text":"<system-reminder>be brief</system-reminder>"}]}]}`,
		all: []string{
			`user: text "run the tests"`,
			`assistant: text "Running them."`,
			`assistant: call toolu_1 Bash {"command":"go test ./..."}`,
			`assistant: call toolu_2 Read {"file_path":"a.go"}`,
			`tool: result toolu_1 "ok"`,
			`tool: result toolu_2 "no such file" error`,
			`system: text "<system-reminder>be brief</system-reminder>"`,
		},
		new: []string{`tool: result toolu_1 "ok"`, `tool: result toolu_2 "no such file" error`,
			`system: text "<system-reminder>be brief</system-reminder>"`},
	}, {
		name: "a message thread continuing a response: every message is new",
		body: `{"thread":{"type":"continue","previous_message_id":"msg_prev"},"messages":[
			{"role":"assistant","content":[{"type":"text","text":"Earlier."}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}]}`,
		all:     []string{`assistant: text "Earlier."`, `tool: result toolu_1 "ok"`},
		new:     []string{`assistant: text "Earlier."`, `tool: result toolu_1 "ok"`},
		chained: true,
	}, {
		name: "a thread's first request isn't chained: the usual rule",
		body: `{"thread":{"type":"new"},"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"user","content":"c"}]}`,
		all:  []string{`user: text "a"`, `assistant: text "b"`, `user: text "c"`},
		new:  []string{`user: text "c"`},
	}, {
		name: "no assistant message yet: all new",
		body: `{"messages":[{"role":"user","content":"a"},{"role":"user","content":"b"}]}`,
		all:  []string{`user: text "a"`, `user: text "b"`},
		new:  []string{`user: text "a"`, `user: text "b"`},
	}, {
		// The spec's rule gives nothing new here: the last message is the
		// assistant's. A trailing assistant message is a prefill the client
		// wrote, so it and the prompt before it are new.
		name: "a prefilled assistant message: it and its prompt are new",
		body: `{"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"},
			{"role":"user","content":"write a haiku"},{"role":"assistant","content":"Here it is:"}]}`,
		all: []string{`user: text "a"`, `assistant: text "b"`, `user: text "write a haiku"`, `assistant: text "Here it is:"`},
		new: []string{`user: text "write a haiku"`, `assistant: text "Here it is:"`},
	}, {
		name: "reasoning is opaque: kept, never inspectable",
		body: `{"messages":[{"role":"user","content":"why"},
			{"role":"assistant","content":[{"type":"thinking","thinking":"private chain","signature":"c2ln"},
				{"type":"redacted_thinking","data":"ZW5j"},{"type":"text","text":"Because."}]},
			{"role":"user","content":"ok"}]}`,
		all: []string{`user: text "why"`, `assistant: opaque, opaque, text "Because."`, `user: text "ok"`},
		new: []string{`user: text "ok"`},
	}, {
		name: "media: inline, by URL, by file id, a PDF and a text document",
		body: `{"messages":[{"role":"user","content":[
			{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAECAw=="}},
			{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}},
			{"type":"document","source":{"type":"file","file_id":"file_1"}},
			{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0="}},
			{"type":"document","source":{"type":"text","media_type":"text/plain","data":"notes"}},
			{"type":"text","text":"what are these?"}]}]}`,
		all: []string{`user: media image/png 4B, media https://example.com/a.png, media file_1, media application/pdf 5B, media text/plain 5B, text "what are these?"`},
		new: []string{`user: media image/png 4B, media https://example.com/a.png, media file_1, media application/pdf 5B, media text/plain 5B, text "what are these?"`},
	}, {
		name: "an image in a tool result belongs to the result",
		body: `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Screenshot","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[
				{"type":"text","text":"captured"},{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"/9j/"}}]}]}]}`,
		all: []string{`assistant: call toolu_1 Screenshot {}`, `tool: result toolu_1 "captured", media image/jpeg 3B`},
		new: []string{`tool: result toolu_1 "captured", media image/jpeg 3B`},
	}, {
		name: "server tools: the call and its result",
		body: `{"messages":[{"role":"user","content":"search"},{"role":"assistant","content":[
			{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"go"}},
			{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[]},
			{"type":"text","text":"Found."}]},{"role":"user","content":"thanks"}]}`,
		all: []string{`user: text "search"`, `assistant: call srvtoolu_1 web_search {"query":"go"}`,
			`tool: result srvtoolu_1 "[]"`, `assistant: text "Found."`, `user: text "thanks"`},
		new: []string{`user: text "thanks"`},
	}})
}

func TestResponsesNew(t *testing.T) {
	cases := []newCase{{
		name: "a new prompt after history: only it (and its context) is new",
		body: `{"model":"gpt-x","instructions":"You are a coding agent.","input":[
			{"type":"additional_tools","tools":[{"type":"function","name":"exec"}]},
			{"type":"message","role":"developer","content":[{"type":"input_text","text":"<permissions>"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"fix it"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done."}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>x</environment_context>"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"now the docs"}]}]}`,
		all: []string{`system: text "<permissions>"`, `user: text "fix it"`, `assistant: text "Done."`,
			`system: text "<environment_context>x</environment_context>"`, `user: text "now the docs"`},
		new: []string{`system: text "<environment_context>x</environment_context>"`, `user: text "now the docs"`},
	}, {
		name: "tool results after tool calls: only they are new",
		body: `{"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"run the tests"}]},
			{"type":"reasoning","summary":[],"encrypted_content":"ZW5j"},
			{"type":"function_call","call_id":"call_1","name":"exec_command","arguments":"{\"cmd\":\"go test\"}"},
			{"type":"custom_tool_call","call_id":"call_2","name":"exec","input":"text(1)"},
			{"type":"function_call_output","call_id":"call_1","output":"ok"},
			{"type":"custom_tool_call_output","call_id":"call_2","output":[{"type":"input_text","text":"1"}]}]}`,
		all: []string{`user: text "run the tests"`, `assistant: opaque`,
			`assistant: call call_1 exec_command {"cmd":"go test"}`, `assistant: call call_2 exec "text(1)"`,
			`tool: result call_1 "ok"`, `tool: result call_2 "1"`},
		new: []string{`tool: result call_1 "ok"`, `tool: result call_2 "1"`},
	}, {
		name: "chained with previous_response_id: every item is new",
		body: `{"previous_response_id":"resp_prev","input":[
			{"type":"function_call","call_id":"call_1","name":"exec_command","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`,
		all:     []string{`assistant: call call_1 exec_command {}`, `tool: result call_1 "ok"`},
		new:     []string{`assistant: call call_1 exec_command {}`, `tool: result call_1 "ok"`},
		chained: true,
	}, {
		name: "input as a string",
		body: `{"input":"fix it"}`,
		all:  []string{`user: text "fix it"`},
		new:  []string{`user: text "fix it"`},
	}, {
		name: "a message without a type, content as a string",
		body: `{"input":[{"role":"user","content":"fix it"},{"role":"assistant","content":"ok"},{"role":"user","content":"more"}]}`,
		all:  []string{`user: text "fix it"`, `assistant: text "ok"`, `user: text "more"`},
		new:  []string{`user: text "more"`},
	}, {
		name: "media: an inline image, by file id, a file",
		body: `{"input":[{"type":"message","role":"user","content":[
			{"type":"input_image","image_url":"data:image/png;base64,AAECAw==","detail":"auto"},
			{"type":"input_image","file_id":"file_1"},
			{"type":"input_file","filename":"a.pdf","file_data":"data:application/pdf;base64,JVBERi0="},
			{"type":"input_text","text":"what are these?"}]}]}`,
		all: []string{`user: media image/png 4B, media file_1, media application/pdf 5B, text "what are these?"`},
		new: []string{`user: media image/png 4B, media file_1, media application/pdf 5B, text "what are these?"`},
	}, {
		name: "encrypted content in a message is opaque",
		body: `{"input":[{"type":"message","role":"assistant","content":[{"type":"encrypted_content","encrypted_content":"ZW5j"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"go on"}]}]}`,
		all: []string{`assistant: opaque`, `user: text "go on"`},
		new: []string{`user: text "go on"`},
	}, {
		name: "a prewarm with no input: nothing",
		body: `{"generate":false,"input":[]}`,
		all:  []string{},
		new:  []string{},
	}}
	t.Run("codex", func(t *testing.T) { runNewCases(t, codexHost, codexPath, cases) })
	t.Run("api", func(t *testing.T) { runNewCases(t, openaiHost, responsesPath, cases) })
}

func TestChatNew(t *testing.T) {
	runNewCases(t, openaiHost, chatPath, []newCase{{
		name: "a new prompt after history: only it is new",
		body: `{"model":"gpt-x","messages":[{"role":"system","content":"be brief"},
			{"role":"user","content":"hi"},{"role":"assistant","content":"Hello."},
			{"role":"user","content":[{"type":"text","text":"fix it"}]}]}`,
		all: []string{`system: text "be brief"`, `user: text "hi"`, `assistant: text "Hello."`, `user: text "fix it"`},
		new: []string{`user: text "fix it"`},
	}, {
		name: "tool results after tool calls: only they are new",
		body: `{"messages":[{"role":"user","content":"run the tests"},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"call_1","type":"function","function":{"name":"shell","arguments":"{\"cmd\":\"go test\"}"}},
				{"id":"call_2","type":"function","function":{"name":"read","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"ok"},
			{"role":"tool","tool_call_id":"call_2","content":[{"type":"text","text":"done"}]}]}`,
		all: []string{`user: text "run the tests"`, `assistant: call call_1 shell {"cmd":"go test"}`,
			`assistant: call call_2 read {}`, `tool: result call_1 "ok"`, `tool: result call_2 "done"`},
		new: []string{`tool: result call_1 "ok"`, `tool: result call_2 "done"`},
	}, {
		name: "an assistant message with text and a tool call",
		body: `{"messages":[{"role":"user","content":"a"},
			{"role":"assistant","content":"Checking.","tool_calls":[{"id":"call_1","type":"function","function":{"name":"shell","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"ok"}]}`,
		all: []string{`user: text "a"`, `assistant: text "Checking."`, `assistant: call call_1 shell {}`, `tool: result call_1 "ok"`},
		new: []string{`tool: result call_1 "ok"`},
	}, {
		name: "media: an inline image, by URL, audio",
		body: `{"messages":[{"role":"user","content":[
			{"type":"image_url","image_url":{"url":"data:image/png;base64,AAECAw=="}},
			{"type":"image_url","image_url":{"url":"https://example.com/a.png"}},
			{"type":"input_audio","input_audio":{"data":"AAEC","format":"wav"}},
			{"type":"text","text":"what are these?"}]}]}`,
		all: []string{`user: media image/png 4B, media https://example.com/a.png, media audio/wav 3B, text "what are these?"`},
		new: []string{`user: media image/png 4B, media https://example.com/a.png, media audio/wav 3B, text "what are these?"`},
	}})
}

// N4: the same prompt from Claude Code, Codex (HTTP and WebSocket) and Chat
// Completions parses to the same message. Spans are where it sits in each
// body, so they differ.
func TestSamePromptSameMessage(t *testing.T) {
	const png = "iVBORw0KGgo="
	bodies := []struct{ host, path, body string }{
		{anthropicHost, anthropicPath, `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + png + `"}},{"type":"text","text":"fix the flaky timer test"}]}]}`},
		{codexHost, codexPath, `{"input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,` + png + `"},{"type":"input_text","text":"fix the flaky timer test"}]}]}`},
		{openaiHost, responsesPath, `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,` + png + `"},{"type":"input_text","text":"fix the flaky timer test"}]}]}`},
		{openaiHost, chatPath, `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + png + `"}},{"type":"text","text":"fix the flaky timer test"}]}]}`},
	}
	var want Message
	for i, b := range bodies {
		r := mustParse(t, b.host, b.path, b.body)
		if len(r.New) != 1 {
			t.Fatalf("%s: new %v", b.host, r.New)
		}
		got := withoutSpans(r.Messages[r.New[0]])
		if i == 0 {
			want = got
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s%s:\n got %+v\nwant %+v", b.host, b.path, got, want)
		}
	}
	ws, ok := ParseWebSocket(codexHost, codexPath, []byte(`{"type":"response.create","model":"gpt-x",`+bodies[1].body[1:]))
	if !ok || len(ws.New) != 1 || !reflect.DeepEqual(withoutSpans(ws.Messages[ws.New[0]]), want) {
		t.Errorf("websocket: ok=%v %+v", ok, ws.Messages)
	}
	if want.Role != RoleUser || len(want.Parts) != 2 || want.Parts[1].Text != "fix the flaky timer test" ||
		want.Parts[0].MediaType != "image/png" || !bytes.Equal(want.Parts[0].Content(), mustB64(png)) {
		t.Errorf("the prompt: %+v", want)
	}
}

func withoutSpans(m Message) Message {
	m.Span = Span{}
	m.Parts = append([]Part(nil), m.Parts...)
	for i := range m.Parts {
		m.Parts[i].Span = Span{}
	}
	return m
}

func mustB64(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// Content as a string and as blocks parse to the same message.
func TestStringAndBlocksAgree(t *testing.T) {
	pairs := []struct{ host, path, str, blocks string }{
		{anthropicHost, anthropicPath, `{"messages":[{"role":"user","content":"fix it"}]}`,
			`{"messages":[{"role":"user","content":[{"type":"text","text":"fix it"}]}]}`},
		{anthropicHost, anthropicPath, `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"ok"}]}]}`,
			`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"text","text":"ok"}]}]}]}`},
		{codexHost, codexPath, `{"input":"fix it"}`,
			`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"fix it"}]}]}`},
		{codexHost, codexPath, `{"input":[{"type":"function_call_output","call_id":"c","output":"ok"}]}`,
			`{"input":[{"type":"function_call_output","call_id":"c","output":[{"type":"input_text","text":"ok"}]}]}`},
		{openaiHost, chatPath, `{"messages":[{"role":"user","content":"fix it"}]}`,
			`{"messages":[{"role":"user","content":[{"type":"text","text":"fix it"}]}]}`},
	}
	for _, p := range pairs {
		a, b := mustParse(t, p.host, p.path, p.str), mustParse(t, p.host, p.path, p.blocks)
		if len(a.Messages) != 1 || len(b.Messages) != 1 || !reflect.DeepEqual(withoutSpans(a.Messages[0]), withoutSpans(b.Messages[0])) {
			t.Errorf("%s:\n string %+v\n blocks %+v", p.str, a.Messages, b.Messages)
		}
	}
}

// Content over the inline limit goes by reference: size, media type and a
// preview, the full content still there.
func TestLargePartsByReference(t *testing.T) {
	big := strings.Repeat("é", 60<<10) + "the end" // 120 KiB of 2-byte runes: the preview must cut on a rune
	bigJSON, _ := json.Marshal(big)
	img := bytes.Repeat([]byte{0x89, 'P', 'N', 'G'}, 20<<10) // 80 KiB
	imgB64 := base64.StdEncoding.EncodeToString(img)
	small := []byte("tiny")
	smallB64 := base64.StdEncoding.EncodeToString(small)

	bodies := []struct{ name, host, path, body string }{
		{"anthropic", anthropicHost, anthropicPath, `{"messages":[
			{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":` + string(bigJSON) + `},
				{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + imgB64 + `"}},
				{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + smallB64 + `"}}]}]}`},
		{"responses", codexHost, codexPath, `{"input":[
			{"type":"function_call","call_id":"t1","name":"read","arguments":"{}"},
			{"type":"function_call_output","call_id":"t1","output":` + string(bigJSON) + `},
			{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,` + imgB64 + `"},
				{"type":"input_image","image_url":"data:image/png;base64,` + smallB64 + `"}]}]}`},
		{"chat", openaiHost, chatPath, `{"messages":[
			{"role":"assistant","tool_calls":[{"id":"t1","type":"function","function":{"name":"read","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"t1","content":` + string(bigJSON) + `},
			{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + imgB64 + `"}},
				{"type":"image_url","image_url":{"url":"data:image/png;base64,` + smallB64 + `"}}]}]}`},
	}
	for _, b := range bodies {
		t.Run(b.name, func(t *testing.T) {
			r := mustParse(t, b.host, b.path, b.body)
			var parts []Part
			for _, i := range r.New {
				parts = append(parts, r.Messages[i].Parts...)
			}
			if len(parts) != 3 {
				t.Fatalf("new parts: %+v", parts)
			}
			res, bigImg, smallImg := parts[0], parts[1], parts[2]
			if res.Kind != KindToolResult || !res.Ref || res.Text != "" || res.Size != len(big) || res.CallID != "t1" {
				t.Errorf("large tool result: %+v", withoutContent(res))
			}
			if len(res.Preview) > previewBytes || len(res.Preview) < previewBytes-3 || !utf8.ValidString(res.Preview) || !strings.HasPrefix(big, res.Preview) {
				t.Errorf("preview: %d bytes, valid=%v", len(res.Preview), utf8.ValidString(res.Preview))
			}
			if string(res.Content()) != big {
				t.Errorf("tool result content: %d bytes, want %d", len(res.Content()), len(big))
			}
			if bigImg.Kind != KindMedia || !bigImg.Ref || bigImg.Preview != "" || bigImg.Size != len(img) || bigImg.MediaType != "image/png" {
				t.Errorf("large image: %+v", withoutContent(bigImg))
			}
			if !bytes.Equal(bigImg.Content(), img) {
				t.Errorf("image content: %d bytes, want %d", len(bigImg.Content()), len(img))
			}
			if smallImg.Ref || smallImg.Size != len(small) || !bytes.Equal(smallImg.Content(), small) {
				t.Errorf("small image: %+v", withoutContent(smallImg))
			}
		})
	}
}

func withoutContent(p Part) Part { p.content = ""; return p }

// The inline limit is a parameter; text and tool arguments over it go by reference.
func TestInlineLimit(t *testing.T) {
	r, ok := Parser{InlineLimit: 4}.Parse(anthropicHost, anthropicPath, []byte(`{"messages":[
		{"role":"user","content":"hello"},
		{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]},
		{"role":"user","content":"ok"}]}`))
	if !ok {
		t.Fatal("not ok")
	}
	text, call, short := r.Messages[0].Parts[0], r.Messages[1].Parts[0], r.Messages[2].Parts[0]
	if !text.Ref || text.Text != "" || text.Preview != "hello" || text.Size != 5 || string(text.Content()) != "hello" {
		t.Errorf("text: %+v", text)
	}
	if !call.Ref || call.Arguments != "" || call.Name != "Bash" || call.Preview != `{"command":"ls"}` || string(call.Content()) != `{"command":"ls"}` {
		t.Errorf("tool call: %+v", call)
	}
	if short.Ref || short.Text != "ok" {
		t.Errorf("short text: %+v", short)
	}
}

// Opaque parts are kept with their size and span, never their content.
func TestOpaque(t *testing.T) {
	r := mustParse(t, codexHost, codexPath, `{"input":[{"type":"reasoning","summary":[{"type":"summary_text","text":"private"}],"encrypted_content":"ZW5jcnlwdGVk"}]}`)
	p := r.Messages[0].Parts[0]
	if p.Kind != KindOpaque || p.Content() != nil || p.Text != "" || p.Preview != "" || p.Size != p.Span.End-p.Span.Start || p.Size == 0 {
		t.Errorf("opaque: %+v", p)
	}
	r = mustParse(t, anthropicHost, anthropicPath, `{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"private","signature":"c2ln"}]}]}`)
	if p := r.Messages[0].Parts[0]; p.Kind != KindOpaque || p.Content() != nil || strings.Contains(fmt.Sprintf("%+v", withoutContent(p)), "private") {
		t.Errorf("thinking: %+v", p)
	}
}

// A part's span is what step 3's Replace will rewrite: putting new bytes there
// leaves every other byte of the body as sent, and the result still parses.
func TestSpansRewriteInPlace(t *testing.T) {
	cases := []struct{ host, path, body, replacement string }{
		{anthropicHost, anthropicPath, `{"messages":[{"role":"user","content":"old"},{"role":"assistant","content":"x"},{"role":"user","content":[{"type":"text","text":"fix it"},{"type":"text","text":"<system-reminder>r</system-reminder>"}]}],"stream":true}`,
			`{"type":"text","text":"REPLACED"}`},
		{anthropicHost, anthropicPath, `{"messages":[{"role":"user","content":"fix it"}],"stream":true}`, `"REPLACED"`},
		{codexHost, codexPath, `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"fix it"}]}],"stream":true}`,
			`{"type":"input_text","text":"REPLACED"}`},
		{openaiHost, chatPath, `{"messages":[ {"role":"user", "content": "fix it"} ],"stream":true}`, `"REPLACED"`},
	}
	for _, c := range cases {
		r := mustParse(t, c.host, c.path, c.body)
		m := r.Messages[r.New[0]]
		p := m.Parts[0]
		if p.Text != "fix it" {
			t.Fatalf("%s: first new part %+v", c.body, p)
		}
		out := string(r.Body[:p.Span.Start]) + c.replacement + string(r.Body[p.Span.End:])
		r2 := mustParse(t, c.host, c.path, out)
		if got := r2.Messages[r2.New[0]].Parts[0].Text; got != "REPLACED" || len(r2.Messages) != len(r.Messages) {
			t.Errorf("%s: after the rewrite, %q in %d messages", c.body, got, len(r2.Messages))
		}
	}
}

func TestWebSocket(t *testing.T) {
	r, ok := ParseWebSocket(codexHost, codexPath, []byte(`{"type":"response.create","model":"gpt-x","previous_response_id":"resp_1",
		"input":[{"type":"custom_tool_call_output","call_id":"call_1","output":"ok"}]}`))
	if !ok || !r.Chained || r.Format != FormatResponses || !reflect.DeepEqual(summaries(r, r.New), []string{`tool: result call_1 "ok"`}) {
		t.Errorf("chained turn: ok=%v %+v", ok, r)
	}
	checkInvariants(t, r)
	for _, msg := range []string{
		`{"type":"response.interrupt"}`,
		`{"input":"no type"}`,
		`{"type":"response.create","messages":[]}`,
		``, `not json`,
	} {
		if _, ok := ParseWebSocket(codexHost, codexPath, []byte(msg)); ok {
			t.Errorf("ParseWebSocket(%q) ok", msg)
		}
	}
	if _, ok := ParseWebSocket(anthropicHost, "/v1/messages", []byte(`{"type":"response.create","input":"x"}`)); ok {
		t.Error("a WebSocket on another endpoint is ok")
	}
}

// Formats it doesn't read, and malformed bodies: not ok, never a panic.
func TestNotOK(t *testing.T) {
	cases := []struct{ host, path, body string }{
		// Endpoints that aren't in this step: ChatGPT and claude.ai on the web, side calls.
		{"chatgpt.com", "/backend-api/f/conversation", `{"messages":[{"id":"m","author":{"role":"user"},"content":{"content_type":"text","parts":["hi"]}}]}`},
		{"claude.ai", "/api/organizations/org/chat_conversations/c/completion", `{"prompt":"hi"}`},
		{anthropicHost, "/v1/messages/count_tokens", `{"messages":[{"role":"user","content":"hi"}]}`},
		{anthropicHost, "/v1/complete", `{"prompt":"hi"}`},
		{"example.com", "/v1/messages", `{"messages":[{"role":"user","content":"hi"}]}`},
		{openaiHost, "/v1/embeddings", `{"input":"hi"}`},
		// Empty and malformed bodies, and bodies that aren't a conversation. (Parts
		// of one it doesn't know are opaque: TestUnknownIsOpaque.)
		{anthropicHost, anthropicPath, ``},
		{anthropicHost, anthropicPath, `not json`},
		{anthropicHost, anthropicPath, `[1,2]`},
		{anthropicHost, anthropicPath, `{}`},
		{anthropicHost, anthropicPath, `{"messages":"hi"}`},
		{anthropicHost, anthropicPath, `{"messages":[{"role":"user","content":"hi"}]} trailing`},
		{codexHost, codexPath, ``},
		{codexHost, codexPath, `{"input":5}`},
		{openaiHost, chatPath, `{"messages":{}}`},
		// A body for another format on an endpoint.
		{anthropicHost, anthropicPath, `{"input":"hi"}`},
		{codexHost, codexPath, `{"messages":[{"role":"user","content":"hi"}]}`},
		{openaiHost, chatPath, `{"input":"hi"}`},
	}
	for _, c := range cases {
		if r, ok := Parse(c.host, c.path, []byte(c.body)); ok {
			t.Errorf("Parse(%s%s, %q) ok: %+v", c.host, c.path, c.body, r)
		}
	}
}

// Every prefix of a valid body is malformed JSON: not ok, and no panic.
func TestTruncatedBodies(t *testing.T) {
	bodies := []struct{ host, path, body string }{
		{anthropicHost, anthropicPath, `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAEC"}}]},{"type":"text","text":"hi"}]}]}`},
		{codexHost, codexPath, `{"previous_response_id":"r","input":[{"type":"function_call","call_id":"c","name":"n","arguments":"{}"},{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AAEC"}]}]}`},
		{openaiHost, chatPath, `{"messages":[{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"n","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c","content":"ok"}]}`},
	}
	for _, b := range bodies {
		mustParse(t, b.host, b.path, b.body)
		for n := 0; n < len(b.body); n++ {
			if _, ok := Parse(b.host, b.path, []byte(b.body[:n])); ok {
				t.Errorf("prefix %q ok", b.body[:n])
			}
		}
	}
}

// Whatever the body, Parse doesn't panic, and what it parses keeps the invariants.
func FuzzParse(f *testing.F) {
	f.Add(`{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"n","input":{}}]}]}`, 0)
	f.Add(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},{"type":"function_call_output","call_id":"c","output":"ok"}]}`, 1)
	f.Add(`{"messages":[{"role":"tool","tool_call_id":"c","content":[{"type":"text","text":"ok"}]}]}`, 2)
	endpoints := [][2]string{{anthropicHost, anthropicPath}, {codexHost, codexPath}, {openaiHost, chatPath}}
	f.Fuzz(func(t *testing.T, body string, which int) {
		e := endpoints[(which%3+3)%3]
		if r, ok := (Parser{InlineLimit: 8}).Parse(e[0], e[1], []byte(body)); ok {
			checkInvariants(t, r)
		}
	})
}
