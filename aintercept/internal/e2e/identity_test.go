//go:build e2e

// Identity end to end (internal/identity): a Claude Code OAuth token the
// observer hasn't seen is looked up at the fixture's fake profile endpoint
// (OBSERVER_IDENTITY_TEST_ORIGIN), and the Interceptor's callbacks carry the
// verified email: the example redact's (observe and enforce), through the
// main observer, and the v2 suite's.
package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// The fixture's fake token (fixture/cmd/fixture/interceptor.go) and the
// account its profile endpoint names.
const (
	e2eToken   = "sk-ant-oat01-E2E-FAKE-TOKEN"
	e2eEmail   = "e2e@example.com"
	e2eAccount = "e2e-account"
)

// postAs sends a Claude Code call with the fake token to the model host,
// through the main observer (to the example redact) or observer-v2 (the
// suite's Interceptor), observed or enforced.
func postAs(t *testing.T, main, observe bool, body string) {
	t.Helper()
	req, _ := http.NewRequest("POST", "http://"+envoyAddr+"/v1/messages", strings.NewReader(body))
	req.Host = "api.anthropic.com"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "claude-cli/9.9.9 (external, cli)")
	req.Header.Set("Authorization", "Bearer "+e2eToken)
	if main {
		req.Header.Set("x-e2e-observer", "main")
	}
	if observe {
		req.Header.Set("x-e2e-mode", "observe")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %.300s", resp.StatusCode, b)
	}
}

// redactStart is the example redact Interceptor's "call start" log line for
// the call asking for model.
func redactStart(t *testing.T, model string) map[string]any {
	t.Helper()
	for end := time.Now().Add(15 * time.Second); time.Now().Before(end); time.Sleep(500 * time.Millisecond) {
		out, _ := compose("logs", "--no-log-prefix", "redact").Output()
		for _, line := range strings.Split(string(out), "\n") {
			var m map[string]any
			if json.Unmarshal([]byte(line), &m) == nil && m["msg"] == "call start" && m["model"] == model {
				return m
			}
		}
	}
	t.Fatalf("no call start for model %s in the redact Interceptor's log", model)
	return nil
}

func TestIdentityRedact(t *testing.T) {
	for _, mode := range []string{"observe", "enforce"} {
		t.Run(mode, func(t *testing.T) {
			model := "identity-" + mode
			postAs(t, true, mode == "observe", `{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)
			m := redactStart(t, model)
			if m["email"] != e2eEmail || m["verified"] != true || m["account"] != e2eAccount {
				t.Errorf("call start %v: want the verified identity", m)
			}
		})
	}
	// The credential's owner is recorded: a credential row with its account.
	for end := time.Now().Add(15 * time.Second); ; time.Sleep(500 * time.Millisecond) {
		for _, e := range readEvents(t) {
			if e.Phase == events.PhaseCredential && e.Credential != nil && e.Credential.AccountID == e2eAccount && e.KeyHash != "" {
				if strings.Contains(e.KeyHint, "E2E-FAKE") {
					t.Errorf("the key hint shows the token: %s", e.KeyHint)
				}
				return
			}
		}
		if time.Now().After(end) {
			t.Fatal("no credential row")
		}
	}
}

func TestIdentityV2(t *testing.T) {
	postAs(t, false, false, `{"model":"fixture-model","messages":[{"role":"user","content":"who am i"}]}`)
	for end := time.Now().Add(15 * time.Second); ; time.Sleep(500 * time.Millisecond) {
		for _, e := range readEventsFrom(eventsV2File) {
			if v := e.Verdict; e.Phase == events.PhaseVerdict && v != nil && v.Annotations["email"] != "" {
				if v.Annotations["email"] != e2eEmail || v.Annotations["verified"] != "true" || v.Annotations["account"] != e2eAccount {
					t.Fatalf("the v2 Interceptor saw %v", v.Annotations)
				}
				return
			}
		}
		if time.Now().After(end) {
			t.Fatal("no verdict row naming the caller")
		}
	}
}
