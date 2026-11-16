//go:build e2e

// Contract v2 end to end (docs/INTERCEPTOR_V2.md): test/interceptor's second
// observer speaks v2 to the example rabbit Interceptor
// (aintercept/examples/rabbit), which replaces "I'm a silly rabbit" in the
// new user messages of each call. Envoy sends api.anthropic.com and
// chatgpt.com to it, enforced, or observed with x-e2e-mode: observe.
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

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/wsframe"
)

var eventsV2File = envOr("E2E_EVENTS_V2", "../../../test/interceptor/out/events-v2.ndjson")

// asked is what the rabbit Interceptor puts in place of the phrase.
const asked = "Reply with exactly this sentence and nothing else: Well, tricks are for kids."

const codexPath = "/backend-api/codex/responses"

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
	return resp, string(b)
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// The history says it too; only the newest prompt is new, so only it changes.
const anthropicV2 = `{"model":"fixture-model","stream":true,"messages":[` +
	`{"role":"user","content":"I'm a silly rabbit"},{"role":"assistant","content":"ok"},` +
	`{"role":"user","content":[{"type":"text","text":"I'm a silly rabbit","cache_control":{"type":"ephemeral"}}]}]}`

func newestReplaced(body string) string {
	i := strings.LastIndex(body, "I'm a silly rabbit")
	return body[:i] + asked + body[i+len("I'm a silly rabbit"):]
}

func TestV2EnforceReplacesTheNewPrompt(t *testing.T) {
	resp, body := postV2(t, "api.anthropic.com", "/v1/messages", anthropicV2, false)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if got, want := received(t, resp), newestReplaced(anthropicV2); got != want {
		t.Errorf("upstream received\n%s\nwant (history as sent, newest replaced)\n%s", got, want)
	}
	if !strings.Contains(body, "what a silly clown") || !strings.Contains(body, "event: message_stop") {
		t.Errorf("v2 changed or cut the response: %s", body)
	}
}

const responsesV2 = `{"model":"gpt-fixture","instructions":"be brief","stream":true,"input":[` +
	`{"type":"message","role":"user","content":[{"type":"input_text","text":"I'm a silly rabbit"}]},` +
	`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]},` +
	`{"type":"message","role":"user","content":[{"type":"input_text","text":"I'm a silly rabbit"}]}]}`

func TestV2EnforceCodexHTTP(t *testing.T) {
	resp, body := postV2(t, "chatgpt.com", codexPath, responsesV2, false)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if got, want := received(t, resp), newestReplaced(responsesV2); got != want {
		t.Errorf("upstream received\n%s\nwant\n%s", got, want)
	}
}

func TestV2EnforceCodexWebSocket(t *testing.T) {
	c, r := wsDialPath(t, "chatgpt.com", codexPath)
	defer c.Close()
	create := `{"type":"response.create",` + strings.TrimPrefix(responsesV2, "{")
	_, _ = c.Write(wsframe.Encode(wsframe.OpText, []byte(create), true))
	_, got := wsRead(t, r)
	if want := "echo: " + newestReplaced(create) + " (silly clown)"; got != want {
		t.Errorf("got\n%s\nwant (the server's message as sent, the request's newest prompt replaced)\n%s", got, want)
	}
	// A message that isn't a request goes as sent.
	_, _ = c.Write(wsframe.Encode(wsframe.OpText, []byte(`{"type":"ping","note":"I'm a silly rabbit"}`), true))
	if _, got := wsRead(t, r); !strings.Contains(got, `"note":"I'm a silly rabbit"`) {
		t.Errorf("a non-request message changed: %s", got)
	}
	_, _ = c.Write(wsframe.CloseFrame(1000, "", true))
}

// A long history isn't re-sent: the rabbit Interceptor keeps gRPC's default
// 4 MiB receive limit, and this call's history is 6 MiB. Were it in
// OnRequest, the call would fail (and enforce refuse it, 503). Upstream gets
// every byte as sent but the newest prompt.
func TestV2LongHistoryNotResent(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"model":"fixture-model","messages":[{"role":"user","content":"I'm a silly rabbit"},{"role":"assistant","content":"ok"}`)
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
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %.300s", resp.StatusCode, out)
	}
	if got, want := resp.Header.Get("X-Fixture-Received-Sha256"), sha(newestReplaced(body)); got != want {
		t.Errorf("upstream received a body with sha256 %s, want %s (the newest prompt replaced, all else as sent)", got, want)
	}
}

func TestV2ObserveRecordsButNeverChanges(t *testing.T) {
	resp, body := postV2(t, "api.anthropic.com", "/v1/messages", anthropicV2, true)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if got := received(t, resp); got != anthropicV2 {
		t.Errorf("observe changed the request: %s", got)
	}
}

// TestV2VerdictsRecorded runs after the others: each Replace is a verdict
// row with its message's id, applied when enforced, recorded when observed.
func TestV2VerdictsRecorded(t *testing.T) {
	deadline := time.Now().Add(15 * time.Second)
	for {
		applied, observed := 0, 0
		for _, e := range readEventsFrom(eventsV2File) {
			v := e.Verdict
			if e.Phase != events.PhaseVerdict || v == nil || v.Callback != "request" || v.Action != "replace" || v.MessageID == "" {
				continue
			}
			if v.Applied {
				applied++
			} else {
				observed++
			}
		}
		if applied >= 4 && observed >= 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("v2 replace rows: %d applied, %d observed; want ≥4 and ≥1", applied, observed)
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
