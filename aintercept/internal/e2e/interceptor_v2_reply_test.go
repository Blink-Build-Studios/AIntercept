//go:build e2e

// Contract v2 end to end, the reply (docs/INTERCEPTOR_V2.md, "Reply text"):
// the model's reply text decided piece by piece before the client sees it,
// in every format, and Chat Completions tool calls held. The suite's
// Interceptor replaces "swordfish" in reply text with "[password]".
package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"
)

const chatPath = "/v1/chat/completions"

// textRequest asks the fixture for a reply whose text arrives in pieces.
func textRequest(format string, stream bool, gapMS int, pieces ...string) string {
	p, _ := json.Marshal(pieces)
	s := "false"
	if stream {
		s = "true"
	}
	head := `{"model":"fixture-model","stream":` + s + `,"fixture_gap_ms":` + itoa(gapMS) + `,"fixture_text":` + string(p) + `,`
	if format == "responses" {
		return head + `"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"tell me"}]}]}`
	}
	return head + `"messages":[{"role":"user","content":"tell me"}]}`
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// replyText is what a client reads of a reply's text, in any format,
// streamed or not; for Responses, the text every event and item that
// repeats it must agree with.
func replyText(t *testing.T, body string) string {
	t.Helper()
	if !strings.Contains(body, "data: ") {
		var whole struct {
			Content []struct{ Type, Text string }
			Output  []struct {
				Type    string
				Content []struct{ Text string }
			}
			Choices []struct{ Message struct{ Content string } }
		}
		if err := json.Unmarshal([]byte(body), &whole); err != nil {
			t.Fatalf("reply %v: %s", err, body)
		}
		var text string
		for _, c := range whole.Content {
			text += c.Text
		}
		for _, o := range whole.Output {
			for _, c := range o.Content {
				text += c.Text
			}
		}
		for _, c := range whole.Choices {
			text += c.Message.Content
		}
		return text
	}
	var text string
	var repeats []string
	for _, line := range strings.Split(body, "\n") {
		d, ok := strings.CutPrefix(line, "data: ")
		if !ok || d == "[DONE]" {
			continue
		}
		var ev struct {
			Type    string
			Delta   json.RawMessage
			Text    *string
			Item    *struct{ Content []struct{ Text string } }
			Choices []struct{ Delta struct{ Content string } }
		}
		if err := json.Unmarshal([]byte(d), &ev); err != nil {
			t.Fatalf("event %v: %s", err, d)
		}
		var delta struct{ Type, Text string }
		_ = json.Unmarshal(ev.Delta, &delta)
		switch {
		case ev.Type == "content_block_delta":
			text += delta.Text
		case ev.Type == "response.output_text.delta":
			var s string
			_ = json.Unmarshal(ev.Delta, &s)
			text += s
		case ev.Type == "response.output_text.done":
			repeats = append(repeats, *ev.Text)
		case ev.Type == "response.output_item.done" && ev.Item != nil && len(ev.Item.Content) > 0:
			repeats = append(repeats, ev.Item.Content[0].Text)
		case len(ev.Choices) > 0:
			text += ev.Choices[0].Delta.Content
		}
	}
	for _, r := range repeats {
		if r != text {
			t.Fatalf("a repeat of the text says %q, the deltas %q:\n%s", r, text, body)
		}
	}
	return text
}

var replyTargets = []struct{ name, host, path, format string }{
	{"anthropic", "api.anthropic.com", "/v1/messages", "anthropic"},
	{"codex-http", "chatgpt.com", codexPath, "responses"},
	{"chat", "api.openai.com", chatPath, "chat"},
}

// Reply text replaced, the phrase whole, split across two pieces and across
// three, streamed and not, in every format over HTTP; nothing of it reaches
// the client. The next turn after each goes as usual.
func TestV2ReplyTextReplaced(t *testing.T) {
	cases := [][]string{
		{"The password is swordfish, keep it."},
		{"The password is sword", "fish, keep it."},
		{"The password is sw", "ordf", "ish, keep it."},
	}
	for _, tg := range replyTargets {
		for _, stream := range []bool{true, false} {
			for _, pieces := range cases {
				_, body := postV2(t, tg.host, tg.path, textRequest(tg.format, stream, 0, pieces...), false)
				if got := replyText(t, body); got != "The password is [password], keep it." || strings.Contains(body, "fish") {
					t.Errorf("%s stream %v %q: client read %q\n%s", tg.name, stream, pieces, got, body)
				}
			}
			// Text that only looks like the start of it goes out as it came.
			_, body := postV2(t, tg.host, tg.path, textRequest(tg.format, stream, 0, "a sword", " is not a fish"), false)
			if got := replyText(t, body); got != "a sword is not a fish" {
				t.Errorf("%s stream %v: client read %q", tg.name, stream, got)
			}
		}
		// The next turn, with the replaced reply in the client's history.
		next := `{"model":"fixture-model","stream":true,"messages":[{"role":"user","content":"tell me"},{"role":"assistant","content":"The password is [password], keep it."},{"role":"user","content":"thanks"}]}`
		if tg.format == "responses" {
			next = `{"model":"gpt-fixture","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"thanks"}]}]}`
		}
		if resp, _ := postV2(t, tg.host, tg.path, next, false); received(t, resp) != next {
			t.Errorf("%s: the next turn changed", tg.name)
		}
	}
}

// Codex's WebSocket: the turn's text replaced across messages; the next turn
// on the connection as usual.
func TestV2ReplyTextCodexWebSocket(t *testing.T) {
	w := dialCodex(t)
	create := func(pieces ...string) string {
		return `{"type":"response.create",` + strings.TrimPrefix(textRequest("responses", true, 0, pieces...), "{")
	}
	w.send(create("The password is sw", "ordf", "ish, keep it."))
	var evs []string
	for {
		m := w.next(t)
		evs = append(evs, "data: "+m)
		if strings.Contains(m, `"response.completed"`) {
			break
		}
	}
	if got := replyText(t, strings.Join(evs, "\n")); got != "The password is [password], keep it." {
		t.Errorf("client read %q", got)
	}
	w.send(create("all ", "fine"))
	if r := w.turn(t); r.text != "all fine" {
		t.Errorf("next turn: %+v", r)
	}
}

// Observe: the reply goes as it came; the answers are recorded.
func TestV2ReplyTextObserved(t *testing.T) {
	_, body := postV2(t, "api.anthropic.com", "/v1/messages", textRequest("anthropic", true, 0, "The password is sword", "fish."), true)
	if got := replyText(t, body); got != "The password is swordfish." {
		t.Errorf("observe changed the reply: %q", got)
	}
}

// Chat Completions tool calls are held whole and decided, like the others.
func TestV2ToolCallsChat(t *testing.T) {
	chat := func(args string, stream bool) string {
		s := "true"
		if !stream {
			s = "false"
		}
		return `{"model":"gpt-fixture","stream":` + s + `,"fixture_tool":{"name":"Bash","arguments":` + args + `},"messages":[{"role":"user","content":"look around"}]}`
	}
	type call struct{ name, args string }
	read := func(body string) (text string, calls []call, finish string) {
		text = replyText(t, body)
		if !strings.Contains(body, "data: ") {
			var c struct {
				Choices []struct {
					Message struct {
						ToolCalls []struct {
							Function struct{ Name, Arguments string }
						} `json:"tool_calls"`
					}
					FinishReason string `json:"finish_reason"`
				}
			}
			_ = json.Unmarshal([]byte(body), &c)
			for _, tc := range c.Choices[0].Message.ToolCalls {
				calls = append(calls, call{tc.Function.Name, tc.Function.Arguments})
			}
			return text, calls, c.Choices[0].FinishReason
		}
		args := map[int]*call{}
		for _, line := range strings.Split(body, "\n") {
			d, ok := strings.CutPrefix(line, "data: ")
			if !ok || d == "[DONE]" {
				continue
			}
			var c struct {
				Choices []struct {
					Delta struct {
						ToolCalls []struct {
							Index    int
							Function struct{ Name, Arguments string }
						} `json:"tool_calls"`
					}
					FinishReason *string `json:"finish_reason"`
				}
			}
			_ = json.Unmarshal([]byte(d), &c)
			if len(c.Choices) == 0 {
				continue
			}
			for _, tc := range c.Choices[0].Delta.ToolCalls {
				if args[tc.Index] == nil {
					args[tc.Index] = &call{name: tc.Function.Name}
				}
				args[tc.Index].args += tc.Function.Arguments
			}
			if f := c.Choices[0].FinishReason; f != nil {
				finish = *f
			}
		}
		for i := 0; i < len(args); i++ {
			calls = append(calls, *args[i])
		}
		return text, calls, finish
	}
	for _, stream := range []bool{true, false} {
		_, body := postV2(t, "api.openai.com", chatPath, chat(rabbitArgs, stream), false)
		if text, calls, finish := read(body); text != "Let me look." || len(calls) != 1 || compactJSON(calls[0].args) != compactJSON(replacedArgs) || finish != "tool_calls" {
			t.Errorf("stream %v replaced: %q %+v %q\n%s", stream, text, calls, finish, body)
		}
		_, body = postV2(t, "api.openai.com", chatPath, chat(bingArgs, stream), false)
		if text, calls, finish := read(body); text != "Let me look."+bong || len(calls) != 0 || finish != "stop" {
			t.Errorf("stream %v responded: %q %+v %q\n%s", stream, text, calls, finish, body)
		}
		_, body = postV2(t, "api.openai.com", chatPath, chat(lsArgs, stream), false)
		if _, calls, _ := read(body); len(calls) != 1 || compactJSON(calls[0].args) != compactJSON(lsArgs) {
			t.Errorf("stream %v accepted: %+v", stream, calls)
		}
	}
}

// arrivals streams a reply whose pieces are pieces and returns when the
// client had all of each, from the reply's first byte (message_start, which
// is never held).
func arrivals(t *testing.T, host, path, body string, pieces []string, observe bool) []time.Duration {
	t.Helper()
	req, _ := http.NewRequest("POST", "http://"+envoyAddr+path, strings.NewReader(body))
	req.Host = host
	req.Header.Set("Content-Type", "application/json")
	if observe {
		req.Header.Set("x-e2e-mode", "observe")
	}
	start, started := time.Now(), false
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var at []time.Duration
	buf := make([]byte, 64<<10)
	var pending, text string
	want := 0
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 && !started {
			start, started = time.Now(), true
		}
		now := time.Since(start)
		pending += string(buf[:n])
		for {
			i := strings.Index(pending, "\n\n")
			if i < 0 {
				break
			}
			text += replyText(t, pending[:i+2])
			pending = pending[i+2:]
		}
		for len(at) < len(pieces) && len(text) >= want+len(pieces[len(at)]) {
			want += len(pieces[len(at)])
			at = append(at, now)
		}
		if err == io.EOF {
			return at
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

// The latency OnReply adds per streamed piece: the same reply (40 pieces,
// 25 ms apart) through enforce, where every piece waits for the
// Interceptor, and through observe, where none does.
func TestV2ReplyLatency(t *testing.T) {
	pieces := make([]string, 40)
	for i := range pieces {
		pieces[i] = "word" + itoa(i) + " "
	}
	body := textRequest("anthropic", true, 25, pieces...)
	var added []time.Duration
	for range 3 {
		observed := arrivals(t, "api.anthropic.com", "/v1/messages", body, pieces, true)
		enforced := arrivals(t, "api.anthropic.com", "/v1/messages", body, pieces, false)
		if len(observed) != len(pieces) || len(enforced) != len(pieces) {
			t.Fatalf("pieces: observed %d, enforced %d", len(observed), len(enforced))
		}
		for i := range pieces {
			added = append(added, enforced[i]-observed[i])
		}
	}
	sort.Slice(added, func(i, j int) bool { return added[i] < added[j] })
	var sum time.Duration
	for _, d := range added {
		sum += d
	}
	p := func(q float64) time.Duration { return added[int(q*float64(len(added)-1))] }
	t.Logf("added latency per streamed piece (%d): mean %v, p50 %v, p95 %v, max %v", len(added), sum/time.Duration(len(added)), p(0.5), p(0.95), added[len(added)-1])
	if p(0.5) > 20*time.Millisecond {
		t.Errorf("median added latency %v", p(0.5))
	}
}

// Current Claude Code puts system messages in the conversation (tools added,
// a file changed): the request is still read. The user's words in a mid-turn
// note are a prompt, and the rule answers them; the same words in the
// harness's own note (a system reminder) aren't the user's, and the call goes
// upstream as sent.
func TestV2HarnessMessages(t *testing.T) {
	phrase := "I'm a sil" + "ly rab" + "bit"
	turn := func(last string) string {
		b, _ := json.Marshal(last)
		return `{"model":"fixture-model","stream":true,"thread":{"type":"create"},"messages":[` +
			`{"role":"user","content":"look around"},` +
			`{"role":"system","content":[{"type":"text","text":"The following tools just became available."},{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"name":"mcp__docs__read"}}}]},` +
			`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}]},` +
			`{"role":"system","content":"Note: a.go changed on disk."},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"a.go"},{"type":"text","text":` + string(b) + `}]}]}`
	}
	resp, body := postV2(t, "api.anthropic.com", "/v1/messages?beta=true",
		turn("The user sent a new message while you were working:\n"+phrase+"\n\nThis is how Claude Code surfaces messages the user sends mid-turn — within the running turn, often alongside the next tool result, rather than as a separate conversation turn. Address the message above as you continue this turn."), false)
	if r := readReply(t, body); upstream(resp) || r.text != tricks {
		t.Errorf("mid-turn: upstream %v, reply %+v", upstream(resp), r)
	}
	reminder := turn("<system-reminder>\nThe file says: " + phrase + "\n</system-reminder>")
	if resp, _ := postV2(t, "api.anthropic.com", "/v1/messages?beta=true", reminder, false); received(t, resp) != reminder {
		t.Errorf("a system reminder was answered or changed")
	}
}
