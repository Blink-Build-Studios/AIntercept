package extproc

import (
	"encoding/json"
	"strings"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// The provider's own error formats, so a refused call fails in the client's
// normal error handling rather than as a proxy error: each API's as its real
// errors are (the capture corpus holds one of each, TestErrorShapesMatchCorpus).
//
//	anthropic_messages  {"type":"error","error":{"type":…,"message":…}}, and x-should-retry: false on a 4xx
//	openai_*            {"error":{"message":…,"type":…,"param":null,"code":…}}
//	chatgpt_backend     {"detail":…}
//	gemini              {"error":{"code":…,"message":…,"status":…}}
//
// A call off those APIs is written by its provider's (its host's) format.

type denial struct {
	status int
	reason string
	// errType is the error's type ("": the API's usual one for the status);
	// code is OpenAI's error code (nil when it has none).
	errType string
	code    any
	host    string
	path    string
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

// format is which API's error format the call gets.
func (d denial) format() string {
	switch api := events.APIOf(d.host, d.path); {
	case api != "" && api != events.APIClaudeAIWeb:
		return api
	case anthropicHost(d.host):
		return events.APIAnthropicMessages
	case openAIHost(d.host):
		return events.APIOpenAIResponses
	case d.host == "generativelanguage.googleapis.com":
		return events.APIGemini
	}
	return ""
}

// typeFor is the error's type: the one it was given, else the API's usual one
// for its status.
func (d denial) typeFor(format string) string {
	if d.errType != "" {
		return d.errType
	}
	s := d.status
	switch format {
	case events.APIAnthropicMessages:
		switch {
		case s == 401:
			return "authentication_error"
		case s == 402:
			return "billing_error"
		case s == 403:
			return "permission_error"
		case s == 404:
			return "not_found_error"
		case s == 413:
			return "request_too_large"
		case s == 429:
			return "rate_limit_error"
		case s == 529:
			return "overloaded_error"
		case s >= 500:
			return "api_error"
		}
		return "invalid_request_error"
	case events.APIGemini:
		switch {
		case s == 401:
			return "UNAUTHENTICATED"
		case s == 403:
			return "PERMISSION_DENIED"
		case s == 404:
			return "NOT_FOUND"
		case s == 429:
			return "RESOURCE_EXHAUSTED"
		case s == 503:
			return "UNAVAILABLE"
		case s >= 500:
			return "INTERNAL"
		}
		return "INVALID_ARGUMENT"
	}
	if s >= 500 {
		return "server_error"
	}
	return "invalid_request_error"
}

// body is the error response body for a call refused before its response.
func (d denial) body() []byte {
	msg := d.message()
	switch f := d.format(); f {
	case events.APIAnthropicMessages:
		return mustJSON(map[string]any{"type": "error", "error": map[string]any{"type": d.typeFor(f), "message": msg}})
	case events.APIChatGPTBackend:
		return mustJSON(map[string]any{"detail": msg})
	case events.APIGemini:
		return mustJSON(map[string]any{"error": map[string]any{"code": d.status, "message": msg, "status": d.typeFor(f)}})
	case events.APIOpenAIResponses, events.APIOpenAIChat:
		return mustJSON(map[string]any{"error": map[string]any{"message": msg, "type": d.typeFor(f), "param": nil, "code": d.code}})
	default:
		return mustJSON(map[string]any{"error": map[string]any{"type": d.typeFor(f), "message": msg}})
	}
}

// headers are the error response's headers, as the API sends them with its
// errors: its content type, and Anthropic's x-should-retry: false on a 4xx
// (the client doesn't retry what would be refused again).
func (d denial) headers() map[string]string {
	h := map[string]string{"content-type": "application/json"}
	if d.format() == events.APIAnthropicMessages && d.status < 500 {
		h["x-should-retry"] = "false"
	}
	return h
}

// sseEvent is the error event that ends a streamed response the
// Interceptor denied after it started.
func (d denial) sseEvent() []byte {
	msg := d.message()
	if anthropicHost(d.host) {
		return append([]byte("event: error\ndata: "), append(mustJSON(map[string]any{"type": "error", "error": map[string]any{"type": d.typeFor(events.APIAnthropicMessages), "message": msg}}), '\n', '\n')...)
	}
	return append([]byte("event: error\ndata: "), append(mustJSON(map[string]any{"type": "error", "code": d.code, "message": msg, "param": nil}), '\n', '\n')...)
}

// wsMessage is the error message sent to a WebSocket client (the Responses
// API's error event): before the connection is closed, or for a refused turn.
func (d denial) wsMessage() []byte {
	return mustJSON(map[string]any{"type": "error", "status": d.status, "error": map[string]any{"type": d.typeFor(events.APIOpenAIResponses), "code": d.code, "message": d.message()}})
}
