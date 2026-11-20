//go:build e2e

// End-to-end test of the Interceptor (docs/INTERCEPTOR.md) through a real
// Envoy: run scripts/test-interceptor.sh, which starts test/interceptor's
// stack (Envoy with both filters, the observer, the example redact
// Interceptor, the fixture) and runs these tests against it. A model host's
// call goes to that observer with x-e2e-observer: main (else to observer-v2,
// interceptor_v2_test.go); observe.test and enforce.test always do.
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
	return postTo(t, host, path, body, nil)
}

// postTo is post with extra headers.
func postTo(t *testing.T, host, path, body string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", "http://"+envoyAddr+path, strings.NewReader(body))
	req.Host = host
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "claude-cli/9.9.9 (external, cli)")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s%s: %v", host, path, err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

// toRedact sends a Claude Code call to the main observer, whose Interceptor
// is the example redact: enforced, or observed.
func toRedact(t *testing.T, observe bool, body string) (*http.Response, string) {
	t.Helper()
	hdr := map[string]string{"x-e2e-observer": "main"}
	if observe {
		hdr["x-e2e-mode"] = "observe"
	}
	return postTo(t, "api.anthropic.com", "/v1/messages", body, hdr)
}

func received(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(resp.Header.Get("X-Fixture-Received"))
	if err != nil {
		t.Fatalf("X-Fixture-Received: %v", err)
	}
	return string(b)
}

// prompt is a plain call (the no-loss tests' traffic).
const prompt = `{"model":"fixture-model","stream":true,"messages":[{"role":"user","content":"hello silly clown"}]}`

// clowns says the phrase in the prompt, and has the fixture reply with it
// split across two streamed pieces.
const clowns = `{"model":"fixture-model","stream":true,"fixture_text":["what a sil","ly clown"],` +
	`"messages":[{"role":"user","content":"hello silly clown"}]}`

// Enforce: the prompt is redacted before it goes upstream, and the reply's
// text before the client sees it, even split across pieces.
func TestEnforceRedactsBothDirections(t *testing.T) {
	resp, body := toRedact(t, false, clowns)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if got := received(t, resp); !strings.Contains(got, `"content":"hello [redacted]"`) {
		t.Errorf("upstream received %q; want the prompt redacted", got)
	}
	if r := readReply(t, body); r.text != "what a [redacted]" || r.stop != "end_turn" {
		t.Errorf("reply %+v; want the phrase redacted:\n%s", r, body)
	}
}

func TestObserveRecordsButNeverChanges(t *testing.T) {
	resp, body := toRedact(t, true, clowns)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if got := received(t, resp); got != clowns {
		t.Errorf("observe changed the request: upstream received %q", got)
	}
	if r := readReply(t, body); r.text != "what a silly clown" {
		t.Errorf("observe changed the reply: %+v", r)
	}
}

// The forbidden model is answered with the refusal at OnCallStart: the call
// never goes upstream. Observed, it does.
func TestEnforceRefusesAtCallStart(t *testing.T) {
	forbidden := `{"model":"forbidden-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	resp, body := toRedact(t, false, forbidden)
	if resp.StatusCode != 200 || upstream(resp) {
		t.Fatalf("status %d, upstream %v: %s", resp.StatusCode, upstream(resp), body)
	}
	if r := readReply(t, body); r.text != "forbidden-model isn't approved for use here." {
		t.Errorf("reply %+v", r)
	}
	if resp, _ := toRedact(t, true, forbidden); resp.StatusCode != 200 || !upstream(resp) {
		t.Errorf("observe: status %d, upstream %v; want it through", resp.StatusCode, upstream(resp))
	}
}

// The refused call's record says the answer was synthesized.
func TestRefusedCallRecorded(t *testing.T) {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range readEvents(t) {
			if v := e.Verdict; e.Phase == events.PhaseVerdict && v != nil && v.Callback == "call_start" && v.Action == "respond" &&
				v.Applied && v.RespondMode == "synthesized" {
				return
			}
		}
		time.Sleep(time.Second)
	}
	t.Fatal("no applied call_start respond row for the refused call")
}

// tricks is the rabbit's answer (the v2 suite's).
const tricks = "Well, tricks are for kids."

// wsDialTagged opens a WebSocket to the fixture's /ws through Envoy, with an
// x-e2e-id header on the upgrade request.
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
		if applied >= 3 && recorded >= 2 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("verdict rows: %d applied, %d recorded-only; want ≥3 and ≥2", applied, recorded)
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
	resp, body := toRedact(t, false, prompt)
	if resp.StatusCode != 503 || !strings.Contains(body, "interceptor unavailable") {
		t.Errorf("enforce with the interceptor down: %d %s; want 503", resp.StatusCode, body)
	}
	if resp, _ := toRedact(t, true, prompt); resp.StatusCode != 200 {
		t.Errorf("observe with the interceptor down: %d; want 200", resp.StatusCode)
	}
}
