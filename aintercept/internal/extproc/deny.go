package extproc

import (
	"encoding/json"
	"strings"
)

// The provider's own error formats, so a refused call fails in the client's
// normal error handling rather than as a proxy error. From each provider's
// published API error format.

type denial struct {
	status  int
	reason  string
	host    string
	path    string
	isSSE   bool
	product string
}

func (d denial) message() string {
	if d.reason == "" {
		return "Blocked by your organization's AI usage policy."
	}
	return d.reason
}

func anthropicHost(host string) bool {
	return host == "api.anthropic.com" || strings.HasSuffix(host, ".anthropic.com")
}

func openAIHost(host string) bool {
	return host == "api.openai.com" || host == "chatgpt.com" || strings.HasSuffix(host, ".openai.com") || strings.HasSuffix(host, ".chatgpt.com")
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// body is the error response body for a call refused before its response.
func (d denial) body() []byte {
	msg := d.message()
	switch {
	case anthropicHost(d.host):
		return mustJSON(map[string]any{"type": "error", "error": map[string]any{"type": "permission_error", "message": msg}})
	case openAIHost(d.host):
		return mustJSON(map[string]any{"error": map[string]any{"message": msg, "type": "invalid_request_error", "param": nil, "code": "blocked_by_policy"}})
	case d.host == "generativelanguage.googleapis.com":
		return mustJSON(map[string]any{"error": map[string]any{"code": d.status, "message": msg, "status": "PERMISSION_DENIED"}})
	default:
		return mustJSON(map[string]any{"error": map[string]any{"type": "permission_error", "message": msg}})
	}
}

// sseEvent is the error event that ends a streamed response the
// Interceptor denied after it started.
func (d denial) sseEvent() []byte {
	msg := d.message()
	if anthropicHost(d.host) {
		return append([]byte("event: error\ndata: "), append(mustJSON(map[string]any{"type": "error", "error": map[string]any{"type": "permission_error", "message": msg}}), '\n', '\n')...)
	}
	return append([]byte("event: error\ndata: "), append(mustJSON(map[string]any{"type": "error", "code": "blocked_by_policy", "message": msg, "param": nil}), '\n', '\n')...)
}

// wsMessage is the error message sent to a WebSocket client before the
// connection is closed (the Responses API's error event).
func (d denial) wsMessage() []byte {
	return mustJSON(map[string]any{"type": "error", "status": d.status, "error": map[string]any{"type": "invalid_request_error", "code": "blocked_by_policy", "message": d.message()}})
}
