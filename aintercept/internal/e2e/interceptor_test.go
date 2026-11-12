//go:build e2e

// End-to-end test of the Interceptor (docs/INTERCEPTOR.md) through a real
// Envoy: run scripts/test-interceptor.sh, which starts test/interceptor's
// stack (Envoy with both filters, the observer, the example redact
// Interceptor, the fixture) and runs these tests against it.
package e2e

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/wsframe"
)

var (
	envoyAddr  = envOr("E2E_ENVOY", "localhost:10000")
	eventsFile = envOr("E2E_EVENTS", "../../../test/interceptor/out/events.ndjson")
	composeDir = envOr("E2E_COMPOSE_DIR", "../../../test/interceptor")
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func post(t *testing.T, host, path, body string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", "http://"+envoyAddr+path, strings.NewReader(body))
	req.Host = host
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "claude-cli/9.9.9 (external, cli)")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s%s: %v", host, path, err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func received(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(resp.Header.Get("X-Fixture-Received"))
	if err != nil {
		t.Fatalf("X-Fixture-Received: %v", err)
	}
	return string(b)
}

const prompt = `{"model":"fixture-model","stream":true,"messages":[{"role":"user","content":"hello silly clown"}]}`

func TestEnforceRedactsBothDirections(t *testing.T) {
	resp, body := post(t, "enforce.test", "/v1/messages", prompt)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if got := received(t, resp); strings.Contains(got, "silly clown") || !strings.Contains(got, "[redacted]") {
		t.Errorf("upstream received %q; want the phrase redacted", got)
	}
	if strings.Contains(body, "silly clown") {
		t.Errorf("client received the phrase: %s", body)
	}
	if !strings.Contains(body, "what a [redacted]") {
		t.Errorf("response event not redacted: %s", body)
	}
	if !strings.Contains(body, "event: message_stop") {
		t.Errorf("stream didn't finish: %s", body)
	}
}

func TestObserveRecordsButNeverChanges(t *testing.T) {
	resp, body := post(t, "observe.test", "/v1/messages", prompt)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if got := received(t, resp); !strings.Contains(got, "silly clown") {
		t.Errorf("observe changed the request: upstream received %q", got)
	}
	if !strings.Contains(body, "what a silly clown") {
		t.Errorf("observe changed the response: %s", body)
	}
}

func TestEnforceWholeBody(t *testing.T) {
	resp, body := post(t, "enforce.test", "/v1/json", `{"note":"silly clown"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("response isn't JSON: %v: %s", err, body)
	}
	if got["reply"] != "a [redacted]" || strings.Contains(got["received"], "silly clown") {
		t.Errorf("got %v; want both directions redacted", got)
	}
}

func TestEnforceDenyAtCallStart(t *testing.T) {
	resp, body := post(t, "enforce.test", "/v1/messages", `{"model":"forbidden-model","messages":[]}`)
	if resp.StatusCode != 403 {
		t.Fatalf("status %d, want 403: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "forbidden-model isn't approved") || !strings.Contains(body, "permission_error") {
		t.Errorf("deny body %s", body)
	}
	// The same request observed goes through.
	if resp, _ := post(t, "observe.test", "/v1/messages", `{"model":"forbidden-model","messages":[]}`); resp.StatusCode != 200 {
		t.Errorf("observe: status %d, want 200", resp.StatusCode)
	}
}

// wsDial opens a WebSocket to the fixture's /ws through Envoy.
func wsDial(t *testing.T, host string) (net.Conn, *bufio.Reader) {
	t.Helper()
	return wsDialTagged(t, host, "")
}

// wsDialTagged is wsDial with an x-e2e-id header on the upgrade request.
func wsDialTagged(t *testing.T, host, id string) (net.Conn, *bufio.Reader) {
	t.Helper()
	tag := ""
	if id != "" {
		tag = "x-e2e-id: " + id + "\r\n"
	}
	c, err := net.DialTimeout("tcp", envoyAddr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetDeadline(time.Now().Add(20 * time.Second))
	var nonce [16]byte
	_, _ = rand.Read(nonce[:])
	_, _ = io.WriteString(c, "GET /ws HTTP/1.1\r\nHost: "+host+"\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		tag+"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: "+base64.StdEncoding.EncodeToString(nonce[:])+"\r\n"+
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

func wsRead(t *testing.T, r *bufio.Reader) (byte, string) {
	t.Helper()
	dec := wsframe.NewDecoder(false, false, 1<<20)
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		msgs, err := dec.Feed(buf[:n])
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(msgs) > 0 {
			return msgs[0].Opcode, string(msgs[0].Payload)
		}
	}
}

func TestEnforceWebSocket(t *testing.T) {
	c, r := wsDial(t, "enforce.test")
	defer c.Close()
	_, _ = c.Write(wsframe.Encode(wsframe.OpText, []byte("hello silly clown"), true))
	_, got := wsRead(t, r)
	if want := "echo: hello [redacted] ([redacted])"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	_, _ = c.Write(wsframe.CloseFrame(1000, "", true))
}

func TestObserveWebSocket(t *testing.T) {
	c, r := wsDial(t, "observe.test")
	defer c.Close()
	_, _ = c.Write(wsframe.Encode(wsframe.OpText, []byte("hello silly clown"), true))
	_, got := wsRead(t, r)
	if want := "echo: hello silly clown (silly clown)"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	_, _ = c.Write(wsframe.CloseFrame(1000, "", true))
}

// TestDeniedCallRecorded: a refused call still gets its complete row, with
// the status it was refused with and why.
func TestDeniedCallRecorded(t *testing.T) {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range readEvents(t) {
			if e.Phase == events.PhaseComplete && e.Status == 403 && strings.Contains(e.Error, "forbidden-model") && e.InterceptMode == "enforce" {
				return
			}
		}
		time.Sleep(time.Second)
	}
	t.Fatal("no complete row for the denied call")
}

func readEvents(t *testing.T) []events.Event {
	f, err := os.Open(eventsFile)
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

// TestVerdictsRecorded runs after the others: every verdict above is a row.
func TestVerdictsRecorded(t *testing.T) {
	deadline := time.Now().Add(15 * time.Second)
	for {
		applied, recorded := countVerdicts(t)
		if applied >= 5 && recorded >= 3 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("verdict rows: %d applied, %d recorded-only; want ≥5 and ≥3", applied, recorded)
		}
		time.Sleep(time.Second)
	}
}

func countVerdicts(t *testing.T) (applied, recorded int) {
	for _, e := range readEvents(t) {
		if e.Phase != events.PhaseVerdict || e.Verdict == nil {
			continue
		}
		if e.Verdict.Applied {
			applied++
		} else {
			recorded++
		}
	}
	return applied, recorded
}

// TestZFailClosed stops the Interceptor: enforced calls are refused, observed
// ones go through untouched. (Z: runs last.)
func TestZFailClosed(t *testing.T) {
	compose := func(args ...string) *exec.Cmd {
		a := append([]string{"compose", "--project-directory", composeDir}, strings.Fields(os.Getenv("E2E_COMPOSE_FILES"))...)
		return exec.Command("docker", append(a, args...)...)
	}
	if err := compose("stop", "redact").Run(); err != nil {
		t.Skipf("can't stop the interceptor: %v", err)
	}
	defer compose("start", "redact").Run()
	resp, body := post(t, "enforce.test", "/v1/messages", prompt)
	if resp.StatusCode != 503 || !strings.Contains(body, "interceptor unavailable") {
		t.Errorf("enforce with the interceptor down: %d %s; want 503", resp.StatusCode, body)
	}
	if resp, _ := post(t, "observe.test", "/v1/messages", prompt); resp.StatusCode != 200 {
		t.Errorf("observe with the interceptor down: %d; want 200", resp.StatusCode)
	}
}
