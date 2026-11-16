package convo

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// edited applies one edit and checks what every Replace must keep: the
// result is valid JSON, every byte outside the message's span is as sent, and
// the request parses back with the part holding the new content.
func edited(t *testing.T, host, path, body string, msg, part int, content string) string {
	t.Helper()
	r := mustParse(t, host, path, body)
	out, err := r.Apply([]Edit{{Message: msg, Part: part, Content: content}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	span := r.Messages[msg].Span
	tail := len(body) - span.End
	if !bytes.Equal(out[:span.Start], r.Body[:span.Start]) || len(out) < span.Start+tail || !bytes.Equal(out[len(out)-tail:], r.Body[span.End:]) {
		t.Fatalf("bytes outside the message's span changed:\n got %s\nfrom %s", out, body)
	}
	back := mustParse(t, host, path, string(out))
	if len(back.Messages) != len(r.Messages) {
		t.Fatalf("re-parsed %d messages, want %d: %s", len(back.Messages), len(r.Messages), out)
	}
	if got := string(back.Messages[msg].Parts[part].Content()); got != content {
		t.Fatalf("re-parsed content %q, want %q: %s", got, content, out)
	}
	return string(out)
}

func TestReplaceAnthropic(t *testing.T) {
	const history = `{"model":"m","system":"s","messages":[{"role":"user","content":"first"},{"role":"assistant","content":[{"type":"text","text":"ok"}]},`
	cases := []struct {
		name, body string
		msg, part  int
		content    string
		want       string
	}{{
		name: "a string prompt",
		body: history + `{"role":"user","content":"I'm a silly rabbit"}]}`,
		msg:  2, content: "Reply with: tricks",
		want: history + `{"role":"user","content":"Reply with: tricks"}]}`,
	}, {
		name: "a text block keeps its other fields (cache_control)",
		body: history + `{"role":"user","content":[{"type":"text","text":"I'm a silly rabbit","cache_control":{"type":"ephemeral"}}]}]}`,
		msg:  2, content: "tricks",
		want: history + `{"role":"user","content":[{"type":"text","text":"tricks","cache_control":{"type":"ephemeral"}}]}]}`,
	}, {
		name: "the second text block of a message; the image beside it stays",
		body: history + `{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAEC"}},{"type":"text","text":"a"}, {"type":"text","text":"b"}]}]}`,
		msg:  2, part: 2, content: "B <&> \"q\"\n✓",
		want: history + `{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAEC"}},{"type":"text","text":"a"}, {"type":"text","text":"B <&> \"q\"\n✓"}]}]}`,
	}, {
		name: "a tool result as a string",
		body: history + `{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"secret=1","is_error":false}]}]}`,
		msg:  2, content: "secret=[hidden]",
		want: history + `{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"secret=[hidden]","is_error":false}]}]}`,
	}, {
		name: "a tool result's text blocks become one; its image stays",
		body: history + `{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"a"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAEC"}},{"type":"text","text":"b"}]}]}]}`,
		msg:  2, content: "a and b",
		want: history + `{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"a and b"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAEC"}}]}]}]}`,
	}, {
		name: "a tool call's input (a prefill's)",
		body: history + `{"role":"user","content":"go"},{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}]}`,
		msg:  3, content: `{"command":"pwd"}`,
		want: history + `{"role":"user","content":"go"},{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"pwd"}}]}]}`,
	}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := edited(t, anthropicHost, anthropicPath, c.body, c.msg, c.part, c.content); got != c.want {
				t.Errorf("got\n%s\nwant\n%s", got, c.want)
			}
		})
	}
}

func TestReplaceResponses(t *testing.T) {
	const history = `{"model":"m","instructions":"i","input":[{"type":"message","role":"developer","content":"d"},{"type":"message","role":"user","content":[{"type":"input_text","text":"first"}]},{"type":"reasoning","encrypted_content":"x"},`
	cases := []struct {
		name, body, path string
		msg, part        int
		content, want    string
	}{{
		name: "an input_text prompt", path: codexPath,
		body: history + `{"type":"message","role":"user","content":[{"type":"input_text","text":"I'm a silly rabbit"}]}]}`,
		msg:  3, content: "tricks",
		want: history + `{"type":"message","role":"user","content":[{"type":"input_text","text":"tricks"}]}]}`,
	}, {
		name: "a function call's output", path: codexPath,
		body: history + `{"type":"function_call","call_id":"c1","name":"shell","arguments":"{\"cmd\":\"ls\"}"},{"type":"function_call_output","call_id":"c1","output":"a\nb"}]}`,
		msg:  4, content: "a",
		want: history + `{"type":"function_call","call_id":"c1","name":"shell","arguments":"{\"cmd\":\"ls\"}"},{"type":"function_call_output","call_id":"c1","output":"a"}]}`,
	}, {
		name: "a function call's arguments", path: responsesPath,
		body: history + `{"type":"function_call","call_id":"c1","name":"shell","arguments":"{\"cmd\":\"ls\"}"}]}`,
		msg:  3, content: `{"cmd":"pwd"}`,
		want: history + `{"type":"function_call","call_id":"c1","name":"shell","arguments":"{\"cmd\":\"pwd\"}"}]}`,
	}, {
		name: "a custom tool call's input", path: responsesPath,
		body: history + `{"type":"custom_tool_call","call_id":"c1","name":"apply_patch","input":"*** Begin"}]}`,
		msg:  3, content: `"*** End"`,
		want: history + `{"type":"custom_tool_call","call_id":"c1","name":"apply_patch","input":"*** End"}]}`,
	}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			host := codexHost
			if c.path == responsesPath {
				host = openaiHost
			}
			if got := edited(t, host, c.path, c.body, c.msg, c.part, c.content); got != c.want {
				t.Errorf("got\n%s\nwant\n%s", got, c.want)
			}
		})
	}
	// A plain-string input.
	if got := edited(t, openaiHost, responsesPath, `{"input":"I'm a silly rabbit","model":"m"}`, 0, 0, "tricks"); got != `{"input":"tricks","model":"m"}` {
		t.Errorf("string input: %s", got)
	}
}

func TestReplaceChat(t *testing.T) {
	const history = `{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"first"},`
	cases := []struct {
		name, body    string
		msg, part     int
		content, want string
	}{{
		name: "a string prompt",
		body: history + `{"role":"assistant","content":"ok"},{"role":"user","content":"I'm a silly rabbit","name":"u"}]}`,
		msg:  3, content: "tricks",
		want: history + `{"role":"assistant","content":"ok"},{"role":"user","content":"tricks","name":"u"}]}`,
	}, {
		name: "a text part",
		body: history + `{"role":"assistant","content":"ok"},{"role":"user","content":[{"type":"text","text":"I'm a silly rabbit"}]}]}`,
		msg:  3, content: "tricks",
		want: history + `{"role":"assistant","content":"ok"},{"role":"user","content":[{"type":"text","text":"tricks"}]}]}`,
	}, {
		name: "a tool message",
		body: history + `{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"out"}]}`,
		msg:  3, content: "OUT",
		want: history + `{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"OUT"}]}`,
	}, {
		name: "a tool call's arguments",
		body: history + `{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]}]}`,
		msg:  2, content: `{"a":1}`,
		want: history + `{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{\"a\":1}"}}]}]}`,
	}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := edited(t, openaiHost, chatPath, c.body, c.msg, c.part, c.content); got != c.want {
				t.Errorf("got\n%s\nwant\n%s", got, c.want)
			}
		})
	}
}

// Several edits in one request, in any order, each in place.
func TestReplaceSeveral(t *testing.T) {
	body := `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"one"},{"type":"tool_result","tool_use_id":"b","content":"two"},{"type":"text","text":"three"}]}]}`
	r := mustParse(t, anthropicHost, anthropicPath, body)
	out, err := r.Apply([]Edit{{Message: 2, Part: 0, Content: "3"}, {Message: 0, Part: 0, Content: "1"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"1"},{"type":"tool_result","tool_use_id":"b","content":"two"},{"type":"text","text":"3"}]}]}`; string(out) != want {
		t.Fatalf("got %s", out)
	}
}

// Edits it can't put on the wire are errors, never a broken request.
func TestReplaceErrors(t *testing.T) {
	cases := []struct {
		name, host, path, body string
		edit                   Edit
		err                    string
	}{
		{"media", anthropicHost, anthropicPath, `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAEC"}}]}]}`, Edit{0, 0, "x"}, "media"},
		{"opaque", openaiHost, responsesPath, `{"input":[{"type":"reasoning","encrypted_content":"x"},{"type":"message","role":"user","content":"a"}]}`, Edit{0, 0, "x"}, "opaque"},
		{"arguments that aren't JSON", anthropicHost, anthropicPath, `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"n","input":{}}]}]}`, Edit{0, 0, "{nope"}, "JSON"},
		{"a server tool's structured result", anthropicHost, anthropicPath, `{"messages":[{"role":"user","content":[{"type":"web_search_tool_result","tool_use_id":"t","content":[{"type":"web_search_result","url":"u"}]}]}]}`, Edit{0, 0, "x"}, "structured"},
		{"a tool result holding a block it doesn't know", anthropicHost, anthropicPath, `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"text","text":"a"},{"type":"search_result","source":"s"}]}]}]}`, Edit{0, 0, "x"}, "block"},
		{"no such part", anthropicHost, anthropicPath, `{"messages":[{"role":"user","content":"a"}]}`, Edit{0, 3, "x"}, "no part"},
		{"no such message", anthropicHost, anthropicPath, `{"messages":[{"role":"user","content":"a"}]}`, Edit{5, 0, "x"}, "no message"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := mustParse(t, c.host, c.path, c.body)
			if _, err := r.Apply([]Edit{c.edit}); err == nil || !strings.Contains(err.Error(), c.err) {
				t.Fatalf("err %v, want one mentioning %q", err, c.err)
			}
		})
	}
}

// A text part over the inline limit (by reference) is replaced like any other.
func TestReplaceByReference(t *testing.T) {
	big := strings.Repeat("x", 70<<10)
	body := `{"messages":[{"role":"user","content":"` + big + `"}]}`
	r := mustParse(t, anthropicHost, anthropicPath, body)
	if !r.Messages[0].Parts[0].Ref {
		t.Fatal("want the part by reference")
	}
	out, err := r.Apply([]Edit{{0, 0, "short"}})
	if err != nil || string(out) != `{"messages":[{"role":"user","content":"short"}]}` {
		t.Fatalf("got %s, %v", out, err)
	}
}

// ── Message ids ──────────────────────────────────────────────────────────────

func ids(t *testing.T, host, path, body, seed string) []string {
	t.Helper()
	return mustParse(t, host, path, body).IDs(seed)
}

// The same message has the same id on every call that resends it, though the
// client moves its cache markers; the next call's new message gets a new one.
func TestIDsStableAcrossCalls(t *testing.T) {
	call1 := `{"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}]}`
	call2 := `{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]},{"role":"assistant","content":[{"type":"text","text":"hello"},{"type":"tool_use","id":"t1","name":"n","input":{"a": 1}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"r","cache_control":{"type":"ephemeral"}}]}]}`
	a, b := ids(t, anthropicHost, anthropicPath, call1, "s1"), ids(t, anthropicHost, anthropicPath, call2, "s1")
	if len(a) != 1 || len(b) != 4 || a[0] != b[0] {
		t.Fatalf("ids %v then %v: want the first message's id kept", a, b)
	}
	seen := map[string]bool{}
	for _, id := range b {
		if seen[id] || len(id) != 32 {
			t.Fatalf("ids %v: want 4 distinct 32-character ids", b)
		}
		seen[id] = true
	}
	if c := ids(t, anthropicHost, anthropicPath, call1, "s2"); c[0] == a[0] {
		t.Fatal("another session gave the same id")
	}
}

// The same words twice are two messages, with two ids; and a message's id
// depends on everything before it.
func TestIDsRepeatedContent(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"yes"},{"role":"assistant","content":"ok"},{"role":"user","content":"yes"}]}`
	got := ids(t, anthropicHost, anthropicPath, body, "s")
	if got[0] == got[2] {
		t.Fatalf("ids %v: the two \"yes\" share an id", got)
	}
	other := ids(t, anthropicHost, anthropicPath, `{"messages":[{"role":"user","content":"no"},{"role":"assistant","content":"ok"},{"role":"user","content":"yes"}]}`, "s")
	if other[2] == got[2] {
		t.Fatal("a different history gave the last message the same id")
	}
}

// A chained request's messages chain to the response it continues: the same
// new item continuing two responses gets two ids.
func TestIDsChained(t *testing.T) {
	a := mustParse(t, openaiHost, responsesPath, `{"previous_response_id":"resp_1","input":[{"type":"message","role":"user","content":"yes"}]}`)
	b := mustParse(t, openaiHost, responsesPath, `{"previous_response_id":"resp_2","input":[{"type":"message","role":"user","content":"yes"}]}`)
	if a.Prev != "resp_1" || b.Prev != "resp_2" {
		t.Fatalf("Prev %q, %q", a.Prev, b.Prev)
	}
	if a.IDs("s")[0] == b.IDs("s")[0] {
		t.Fatal("same id continuing two responses")
	}
	th := mustParse(t, anthropicHost, anthropicPath, `{"thread":{"type":"continue","previous_message_id":"msg_9"},"messages":[{"role":"user","content":"a"}]}`)
	if th.Prev != "msg_9" {
		t.Fatalf("thread Prev %q", th.Prev)
	}
}

// json.Valid is the bar every Apply result clears; a sanity check that the
// helper above isn't vacuous.
func TestEditedIsValidJSON(t *testing.T) {
	out := edited(t, anthropicHost, anthropicPath, `{"messages":[{"role":"user","content":"a"}]}`, 0, 0, " \x00\"")
	if !json.Valid([]byte(out)) {
		t.Fatal(out)
	}
}
