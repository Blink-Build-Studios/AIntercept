//go:build e2e

// Contract v2 end to end (docs/INTERCEPTOR.md): test/interceptor's second
// observer speaks v2 to the suite's own Interceptor (internal/e2e/interceptor,
// on package intercept): the example rabbit's rules (a prompt saying the
// rabbit's phrase is answered with tricks, the bing phrase with bong), and rules on the reply: a tool call whose arguments say the bing
// phrase is answered with bong, the rabbit's phrase in them replaced, and
// "swordfish" in the reply's text replaced with "[password]". Envoy sends
// api.anthropic.com, chatgpt.com and api.openai.com to it, enforced, or
// observed with x-e2e-mode: observe. The fixture echoes what it receives,
// or, for a request with "fixture_tool" or "fixture_text", replies with that
// tool call and text.
package e2e

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/convo"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/wsframe"
)

var eventsV2File = envOr("E2E_EVENTS_V2", "../../../test/interceptor/out/events-v2.ndjson")

const (
	codexPath = "/backend-api/codex/responses"
	bong      = "then we bong"
)

func postV2(t *testing.T, host, path, body string, observe bool) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", "http://"+envoyAddr+path, strings.NewReader(body))
	req.Host = host
	req.Header.Set("Content-Type", "application/json")
	if observe {
		req.Header.Set("x-e2e-mode", "observe")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s%s: %v", host, path, err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %.500s", resp.StatusCode, b)
	}
	return resp, string(b)
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// upstream reports whether the call reached the fixture.
func upstream(resp *http.Response) bool { return resp.Header.Get("X-Fixture-Received-Sha256") != "" }

// reply is what a client makes of a reply: its text, its tool calls'
// arguments by name, and how it stopped.
type reply struct {
	text  string
	calls map[string]string
	stop  string
}

// readReply reads an Anthropic or Responses reply, an event stream or JSON,
// as a client would.
func readReply(t *testing.T, body string) reply {
	t.Helper()
	r := reply{calls: map[string]string{}}
	if !strings.Contains(body, "data: ") {
		var whole struct {
			Content []struct {
				Type, Text, Name string
				Input            json.RawMessage
			}
			StopReason string `json:"stop_reason"`
			Status     string
		}
		if err := json.Unmarshal([]byte(body), &whole); err != nil {
			t.Fatalf("reply %v: %s", err, body)
		}
		for _, c := range whole.Content {
			r.text += c.Text
			if c.Type == "tool_use" {
				r.calls[c.Name] = string(c.Input)
			}
		}
		r.stop = whole.StopReason + whole.Status
		return r
	}
	var datas []string
	for _, line := range strings.Split(body, "\n") {
		if d, ok := strings.CutPrefix(line, "data: "); ok {
			datas = append(datas, d)
		}
	}
	return readSSEEvents(t, datas)
}

func readSSEEvents(t *testing.T, datas []string) reply {
	t.Helper()
	r := reply{calls: map[string]string{}}
	var name string
	var args strings.Builder
	for _, d := range datas {
		var ev struct {
			Type         string
			ContentBlock *struct{ Type, Name string } `json:"content_block"`
			Delta        json.RawMessage
			Item         *struct{ Type, Name, Arguments string }
			Response     *struct{ Status string }
		}
		if err := json.Unmarshal([]byte(d), &ev); err != nil {
			t.Fatalf("event %v: %s", err, d)
		}
		var delta struct {
			Type, Text  string
			PartialJSON string `json:"partial_json"`
			StopReason  string `json:"stop_reason"`
		}
		_ = json.Unmarshal(ev.Delta, &delta)
		switch ev.Type {
		case "content_block_start":
			if ev.ContentBlock.Type == "tool_use" {
				name = ev.ContentBlock.Name
				args.Reset()
			}
		case "content_block_delta":
			r.text += delta.Text
			args.WriteString(delta.PartialJSON)
		case "content_block_stop":
			if name != "" {
				r.calls[name], name = args.String(), ""
			}
		case "message_delta":
			r.stop = delta.StopReason
		case "response.output_text.delta":
			var s string
			_ = json.Unmarshal(ev.Delta, &s)
			r.text += s
		case "response.output_item.done":
			if ev.Item.Type == "function_call" {
				r.calls[ev.Item.Name] = ev.Item.Arguments
			}
		case "response.completed":
			r.stop = ev.Response.Status
		}
	}
	return r
}

// The history says it too; only the newest prompt is new, so only it counts.
const anthropicV2 = `{"model":"fixture-model","stream":true,"messages":[` +
	`{"role":"user","content":"I'm a silly rabbit"},{"role":"assistant","content":"ok"},` +
	`{"role":"user","content":[{"type":"text","text":"I'm a silly rabbit","cache_control":{"type":"ephemeral"}}]}]}`

// S1, S5: synthesized, streamed. The call never reaches the provider.
func TestV2RabbitAnthropicStream(t *testing.T) {
	resp, body := postV2(t, "api.anthropic.com", "/v1/messages", anthropicV2, false)
	if upstream(resp) || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("upstream %v, content type %q", upstream(resp), resp.Header.Get("Content-Type"))
	}
	if r := readReply(t, body); r.text != tricks || r.stop != "end_turn" || !strings.Contains(body, "event: message_stop") {
		t.Errorf("reply %+v:\n%s", r, body)
	}
}

// Synthesized, not streamed: the JSON body.
func TestV2BingAnthropicJSON(t *testing.T) {
	resp, body := postV2(t, "api.anthropic.com", "/v1/messages", `{"model":"fixture-model","messages":[{"role":"user","content":"First we bing"}]}`, false)
	if upstream(resp) || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("upstream %v, content type %q", upstream(resp), resp.Header.Get("Content-Type"))
	}
	if r := readReply(t, body); r.text != bong || r.stop != "end_turn" {
		t.Errorf("reply %+v:\n%s", r, body)
	}
}

// S2: the next turn, with the synthesized reply in the client's history,
// goes upstream as sent.
func TestV2NextTurnAfterSynthesized(t *testing.T) {
	next := `{"model":"fixture-model","stream":true,"messages":[{"role":"user","content":"I'm a silly rabbit"},` +
		`{"role":"assistant","content":[{"type":"text","text":"` + tricks + `"}]},{"role":"user","content":"thanks"}]}`
	resp, body := postV2(t, "api.anthropic.com", "/v1/messages", next, false)
	if got := received(t, resp); got != next {
		t.Errorf("upstream received\n%s\nwant it as sent", got)
	}
	if !strings.Contains(body, "event: message_stop") {
		t.Errorf("reply %s", body)
	}
}

// S3: Claude Code's message threads keep the conversation with the
// provider: the request is rewritten to ask for the text, and goes upstream.
// The next turn in the thread goes as sent.
func TestV2ChainedIsRewritten(t *testing.T) {
	chained := `{"model":"fixture-model","stream":true,"thread":{"previous_message_id":"msg_fixture_1"},"messages":[{"role":"user","content":"first we bing"}]}`
	resp, _ := postV2(t, "api.anthropic.com", "/v1/messages", chained, false)
	if got, want := received(t, resp), strings.Replace(chained, "first we bing", convo.RespondInstruction+bong, 1); got != want {
		t.Errorf("upstream received\n%s\nwant\n%s", got, want)
	}
	next := `{"model":"fixture-model","stream":true,"thread":{"previous_message_id":"msg_fixture_2"},"messages":[{"role":"user","content":"thanks"}]}`
	if resp, _ := postV2(t, "api.anthropic.com", "/v1/messages", next, false); received(t, resp) != next {
		t.Errorf("the next turn changed")
	}
	// Codex over HTTP with previous_response_id: the same.
	codex := `{"model":"gpt-fixture","stream":true,"previous_response_id":"resp_fixture_1","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"first we bing"}]}]}`
	resp, _ = postV2(t, "chatgpt.com", codexPath, codex, false)
	if got, want := received(t, resp), strings.Replace(codex, "first we bing", convo.RespondInstruction+bong, 1); got != want {
		t.Errorf("upstream received\n%s\nwant\n%s", got, want)
	}
}

const responsesV2 = `{"model":"gpt-fixture","instructions":"be brief","stream":true,"input":[` +
	`{"type":"message","role":"user","content":[{"type":"input_text","text":"I'm a silly rabbit"}]},` +
	`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]},` +
	`{"type":"message","role":"user","content":[{"type":"input_text","text":"I'm a silly rabbit"}]}]}`

func TestV2RabbitCodexHTTP(t *testing.T) {
	resp, body := postV2(t, "chatgpt.com", codexPath, responsesV2, false)
	if upstream(resp) {
		t.Fatal("the call reached the provider")
	}
	if r := readReply(t, body); r.text != tricks || r.stop != "completed" {
		t.Errorf("reply %+v:\n%s", r, body)
	}
}

// wsClient reads a WebSocket's messages one at a time.
type wsClient struct {
	c   net.Conn
	r   *bufio.Reader
	dec *wsframe.Decoder
	q   []string
}

func (w *wsClient) send(s string) { _, _ = w.c.Write(wsframe.Encode(wsframe.OpText, []byte(s), true)) }

func (w *wsClient) next(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 4096)
	for len(w.q) == 0 {
		n, err := w.r.Read(buf)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		msgs, err := w.dec.Feed(buf[:n])
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		for _, m := range msgs {
			w.q = append(w.q, string(m.Payload))
		}
	}
	m := w.q[0]
	w.q = w.q[1:]
	return m
}

// turn reads a turn's events up to response.completed.
func (w *wsClient) turn(t *testing.T) reply {
	t.Helper()
	var evs []string
	for {
		m := w.next(t)
		evs = append(evs, m)
		var ev struct{ Type string }
		if json.Unmarshal([]byte(m), &ev) == nil && ev.Type == "response.completed" {
			return readSSEEvents(t, evs)
		}
	}
}

func dialCodex(t *testing.T) *wsClient {
	c, r := wsDialPath(t, "chatgpt.com", codexPath)
	t.Cleanup(func() { _, _ = c.Write(wsframe.CloseFrame(1000, "", true)); c.Close() })
	return &wsClient{c: c, r: r, dec: wsframe.NewDecoder(false, false, 1<<20)}
}

func TestV2RabbitCodexWebSocket(t *testing.T) {
	w := dialCodex(t)
	w.send(`{"type":"response.create",` + strings.TrimPrefix(responsesV2, "{"))
	if r := w.turn(t); r.text != tricks || r.stop != "completed" {
		t.Errorf("reply %+v", r)
	}
	// The next turn on the connection reaches the server.
	next := `{"type":"response.create","model":"gpt-fixture","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"thanks"}]}]}`
	w.send(next)
	if got := w.next(t); got != "echo: "+next+" (silly clown)" {
		t.Errorf("next turn got %s", got)
	}
	// A message that isn't a request goes as sent.
	w.send(`{"type":"ping","note":"I'm a silly rabbit"}`)
	if got := w.next(t); !strings.Contains(got, `"note":"I'm a silly rabbit"`) {
		t.Errorf("a non-request message changed: %s", got)
	}
}

// A chained response.create on the WebSocket is rewritten.
func TestV2ChainedCodexWebSocket(t *testing.T) {
	w := dialCodex(t)
	create := `{"type":"response.create","model":"gpt-fixture","previous_response_id":"resp_fixture_1","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"first we bing"}]}]}`
	w.send(create)
	if got, want := w.next(t), "echo: "+strings.Replace(create, "first we bing", convo.RespondInstruction+bong, 1)+" (silly clown)"; got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

// ── Options: an Error at call start, and per turn ────────────────────────────

// A Claude Code fast request is refused with Anthropic's own 400 (and
// x-should-retry: false), and never reaches the provider; the same request
// at standard speed goes on. Observed, the answer is recorded, not applied.
func TestV2FastModeRefused(t *testing.T) {
	fast := `{"model":"fixture-model","stream":true,"speed":"fast","output_config":{"effort":"high"},"messages":[{"role":"user","content":"hi"}]}`
	req, _ := http.NewRequest("POST", "http://"+envoyAddr+"/v1/messages", strings.NewReader(fast))
	req.Host = "api.anthropic.com"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-beta", "fast-mode-2026-02-01")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var e struct {
		Type  string
		Error struct{ Type, Message string }
	}
	if resp.StatusCode != 400 || json.Unmarshal(b, &e) != nil || e.Type != "error" || e.Error.Type != "invalid_request_error" ||
		e.Error.Message != "Fast mode is not enabled for your organization." {
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	if resp.Header.Get("x-should-retry") != "false" || upstream(resp) {
		t.Fatalf("x-should-retry %q, reached the provider %v", resp.Header.Get("x-should-retry"), upstream(resp))
	}
	standard := strings.Replace(fast, `"speed":"fast",`, "", 1)
	if resp, _ := postV2(t, "api.anthropic.com", "/v1/messages", standard, false); !upstream(resp) {
		t.Error("a standard request didn't reach the provider")
	}
	if resp, _ := postV2(t, "api.anthropic.com", "/v1/messages", fast, true); !upstream(resp) {
		t.Error("observed, the refusal was applied")
	}
}

// Codex's WebSocket: a turn that asks for the priority tier gets the API's
// error event and never goes upstream; the connection stays open and the
// next turn reaches the server.
func TestV2PriorityTurnRefused(t *testing.T) {
	w := dialCodex(t)
	w.send(`{"type":"response.create","model":"gpt-fixture","service_tier":"priority","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	var e struct {
		Type   string
		Status int
		Error  struct{ Message string }
	}
	if got := w.next(t); json.Unmarshal([]byte(got), &e) != nil || e.Type != "error" || e.Status != 403 ||
		e.Error.Message != "The priority tier isn't enabled here." {
		t.Fatalf("got %s", got)
	}
	next := `{"type":"response.create","model":"gpt-fixture","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"thanks"}]}]}`
	w.send(next)
	if got := w.next(t); got != "echo: "+next+" (silly clown)" {
		t.Errorf("next turn got %s", got)
	}
}

// ── Tool calls held whole ────────────────────────────────────────────────────

func toolRequest(format, args string, stream bool) string {
	tool := `"fixture_tool":{"name":"Bash","arguments":` + args + `}`
	s := "true"
	if !stream {
		s = "false"
	}
	if format == "anthropic" {
		return `{"model":"fixture-model","stream":` + s + `,` + tool + `,"messages":[{"role":"user","content":"look around"}]}`
	}
	return `{"model":"gpt-fixture","stream":` + s + `,` + tool + `,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"look around"}]}]}`
}

const (
	rabbitArgs   = `{"command":"echo I'm a silly rabbit"}`
	replacedArgs = `{"command":"echo Well, tricks are for kids."}`
	bingArgs     = `{"command":"echo first we bing"}`
	lsArgs       = `{"command":"ls"}`
)

// T1, T2, P3: the reply's text is decided as it streams; its tool call is
// held, then released as it was, or with new arguments.
func TestV2ToolCallsAnthropic(t *testing.T) {
	for _, stream := range []bool{true, false} {
		for args, want := range map[string]string{lsArgs: lsArgs, rabbitArgs: replacedArgs} {
			_, body := postV2(t, "api.anthropic.com", "/v1/messages", toolRequest("anthropic", args, stream), false)
			r := readReply(t, body)
			if r.text != "Let me look." || r.stop != "tool_use" || compactJSON(r.calls["Bash"]) != compactJSON(want) {
				t.Errorf("stream %v args %s: reply %+v\n%s", stream, args, r, body)
			}
		}
	}
}

// S4: Respond at a tool call ends the reply with the text; no tool call
// reaches the client, so the agent stops.
func TestV2ToolCallRespondedAnthropic(t *testing.T) {
	for _, stream := range []bool{true, false} {
		_, body := postV2(t, "api.anthropic.com", "/v1/messages", toolRequest("anthropic", bingArgs, stream), false)
		if r := readReply(t, body); r.text != "Let me look."+bong || r.stop != "end_turn" || len(r.calls) != 0 {
			t.Errorf("stream %v: reply %+v\n%s", stream, r, body)
		}
	}
}

func TestV2ToolCallsCodexHTTP(t *testing.T) {
	_, body := postV2(t, "chatgpt.com", codexPath, toolRequest("responses", rabbitArgs, true), false)
	if r := readReply(t, body); r.text != "Let me look." || compactJSON(r.calls["Bash"]) != compactJSON(replacedArgs) || r.stop != "completed" {
		t.Errorf("reply %+v\n%s", r, body)
	}
	_, body = postV2(t, "chatgpt.com", codexPath, toolRequest("responses", bingArgs, true), false)
	if r := readReply(t, body); r.text != "Let me look."+bong || len(r.calls) != 0 || r.stop != "completed" {
		t.Errorf("reply %+v\n%s", r, body)
	}
}

func TestV2ToolCallsCodexWebSocket(t *testing.T) {
	w := dialCodex(t)
	create := func(args string) string {
		return `{"type":"response.create",` + strings.TrimPrefix(toolRequest("responses", args, true), "{")
	}
	w.send(create(rabbitArgs))
	if r := w.turn(t); r.text != "Let me look." || compactJSON(r.calls["Bash"]) != compactJSON(replacedArgs) {
		t.Errorf("replaced: %+v", r)
	}
	w.send(create(bingArgs))
	if r := w.turn(t); r.text != "Let me look."+bong || len(r.calls) != 0 {
		t.Errorf("responded: %+v", r)
	}
	// The next turn after a responded one: as usual.
	w.send(create(lsArgs))
	if r := w.turn(t); compactJSON(r.calls["Bash"]) != compactJSON(lsArgs) {
		t.Errorf("next turn: %+v", r)
	}
}

func compactJSON(s string) string {
	var v any
	if json.Unmarshal([]byte(s), &v) != nil {
		return "invalid: " + s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// A long history isn't re-sent: this call's history is 6 MiB, and OnRequest
// carries only the newest prompt, which is answered.
func TestV2LongHistoryNotResent(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"model":"fixture-model","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"ok"}`)
	filler := strings.Repeat("history ", 64<<10) // 512 KiB
	for i := 0; i < 6; i++ {
		b.WriteString(`,{"role":"user","content":"` + filler + `"},{"role":"assistant","content":"` + filler + `"}`)
	}
	b.WriteString(`,{"role":"user","content":"I'm a silly rabbit"}]}`)
	body := b.String()
	if len(body) < 6<<20 {
		t.Fatalf("history is %d bytes", len(body))
	}
	resp, out := postV2(t, "api.anthropic.com", "/v1/messages", body, false)
	if r := readReply(t, out); upstream(resp) || r.text != tricks {
		t.Errorf("upstream %v, reply %+v", upstream(resp), r)
	}
	// Without the rabbit it goes upstream, every byte as sent.
	plain := strings.Replace(body, "I'm a silly rabbit", "hello", 1)
	if resp, _ := postV2(t, "api.anthropic.com", "/v1/messages", plain, false); resp.Header.Get("X-Fixture-Received-Sha256") != sha(plain) {
		t.Errorf("upstream received a different body")
	}
}

func TestV2ObserveRecordsButNeverChanges(t *testing.T) {
	resp, _ := postV2(t, "api.anthropic.com", "/v1/messages", anthropicV2, true)
	if got := received(t, resp); got != anthropicV2 {
		t.Errorf("observe changed the request: %s", got)
	}
}

// TestV2VerdictsRecorded runs after the others: each Respond is a verdict row
// saying how it was delivered, applied when enforced, recorded when
// observed; tool calls' and reply text's answers are rows with the
// message's id; a synthesized
// call has its complete row, with zero usage.
func TestV2VerdictsRecorded(t *testing.T) {
	deadline := time.Now().Add(15 * time.Second)
	for {
		n := map[string]int{}
		complete := map[string]events.Event{}
		var synthesized []string
		all := readEventsFrom(eventsV2File)
		for _, e := range all {
			if e.Phase == events.PhaseComplete && e.Transport != events.TransportWebSocket {
				complete[e.CorrelationID] = e
			}
		}
		for _, e := range all {
			v := e.Verdict
			if e.Phase != events.PhaseVerdict || v == nil {
				continue
			}
			key := v.Callback + "/" + v.Action + "/" + v.RespondMode
			if !v.Applied {
				key += "/observed"
			}
			n[key]++
			if key == "request/respond/synthesized" && e.CorrelationID != "" {
				synthesized = append(synthesized, e.CorrelationID)
			}
			if (v.Callback == "tool_call" || v.Callback == "reply") && v.MessageID == "" {
				t.Fatalf("tool call row without the call's id: %+v", v)
			}
		}
		done := n["request/respond/synthesized"] >= 5 && n["request/respond/rewritten"] >= 3 &&
			n["tool_call/replace/"] >= 4 && n["tool_call/respond/synthesized"] >= 6 && n["request/respond//observed"] >= 1 &&
			n["reply/replace/"] >= 18 && n["reply/replace//observed"] >= 1
		if done {
			for _, id := range synthesized {
				c, ok := complete[id]
				if !ok {
					continue // a WebSocket turn (the connection's turns share its id)
				}
				if c.Status != 200 || c.InputTokens != 0 || c.OutputTokens != 0 || c.StopReason == "" {
					t.Errorf("synthesized call's complete row %+v", c)
				}
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("v2 verdict rows %v", n)
		}
		time.Sleep(time.Second)
	}
}

func readEventsFrom(path string) []events.Event {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []events.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var e events.Event
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

// wsDialPath opens a WebSocket to host and path through Envoy.
func wsDialPath(t *testing.T, host, path string) (net.Conn, *bufio.Reader) {
	t.Helper()
	c, err := net.DialTimeout("tcp", envoyAddr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetDeadline(time.Now().Add(20 * time.Second))
	var nonce [16]byte
	_, _ = rand.Read(nonce[:])
	_, _ = io.WriteString(c, "GET "+path+" HTTP/1.1\r\nHost: "+host+"\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: "+base64.StdEncoding.EncodeToString(nonce[:])+"\r\n"+
		"Sec-WebSocket-Extensions: permessage-deflate; client_max_window_bits\r\n\r\n")
	r := bufio.NewReader(c)
	status, _ := r.ReadString('\n')
	if !strings.Contains(status, "101") {
		t.Fatalf("upgrade: %q", status)
	}
	for {
		l, err := r.ReadString('\n')
		if err != nil || l == "\r\n" {
			break
		}
	}
	return c, r
}
