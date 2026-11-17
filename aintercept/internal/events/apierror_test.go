package events

import (
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
)

// A rejected call says why: the provider's error type and message, from its
// error body, become the call's Error (calls.error), so the record and the UI
// can say why Claude Code's 400s happened.
func TestAPIErrorOnTheCall(t *testing.T) {
	gz := func(s string) []byte {
		var b bytes.Buffer
		w := gzip.NewWriter(&b)
		_, _ = w.Write([]byte(s))
		_ = w.Close()
		return b.Bytes()
	}
	long := strings.Repeat("x", 2000)
	for _, c := range []struct {
		name   string
		status int
		was    string
		resp   []byte
		enc    string
		want   string
	}{
		{"anthropic", 400, "", []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"messages: an empty list resumes a paused turn, but the previous message is waiting for tool results"},"request_id":"req_synthetic"}`), "",
			"invalid_request_error: messages: an empty list resumes a paused turn, but the previous message is waiting for tool results"},
		{"openai", 429, "", []byte(`{"error":{"message":"Rate limit reached.","type":"rate_limit_error","param":null,"code":"rate_limit_exceeded"}}`), "",
			"rate_limit_error: Rate limit reached."},
		{"detail", 401, "", []byte(`{"detail":"Unauthorized"}`), "", "Unauthorized"},
		{"gzip", 400, "", gz(`{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`), "gzip", "invalid_request_error: bad"},
		{"capped", 400, "", []byte(`{"error":{"type":"t","message":"` + long + `"}}`), "", "t: " + long[:apiErrorCap-len("t: ")]},
		{"ok call", 200, "", []byte(`{"type":"error","error":{"type":"x","message":"y"}}`), "", ""},
		{"not json", 502, "", []byte(`<html>Bad gateway</html>`), "", ""},
		{"denied keeps its reason", 403, "denied by interceptor: no", []byte(`{"type":"error","error":{"type":"permission_error","message":"no"}}`), "", "denied by interceptor: no"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := Event{Phase: PhaseComplete, Host: "api.anthropic.com", Path: "/v1/messages", Status: c.status, Error: c.was}
			e.AttachBodies([]byte(`{"model":"m","messages":[]}`), c.resp, c.enc, nil)
			e.ExtractDeferred()
			if e.Error != c.want {
				t.Fatalf("error %q, want %q", e.Error, c.want)
			}
		})
	}
}
