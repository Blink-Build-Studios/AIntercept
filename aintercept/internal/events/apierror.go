package events

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// apiErrorCap bounds the error recorded from a provider's error body.
const apiErrorCap = 500

// APIError is why the provider rejected a call, from its error body: "type:
// message" (Anthropic's {"type":"error","error":{...}}, OpenAI's
// {"error":{...}}), or a bare detail; "" when the body says nothing it can
// read. resp is decoded.
func APIError(resp []byte) string {
	var b struct {
		Error *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Code    any    `json:"code"`
		} `json:"error"`
		Detail  any    `json:"detail"`
		Message string `json:"message"`
	}
	if json.Unmarshal(resp, &b) != nil {
		return ""
	}
	var s string
	switch {
	case b.Error != nil && b.Error.Type != "" && b.Error.Message != "":
		s = b.Error.Type + ": " + b.Error.Message
	case b.Error != nil:
		s = b.Error.Type + b.Error.Message
	case b.Detail != nil:
		if d, ok := b.Detail.(string); ok {
			s = d
		}
	default:
		s = b.Message
	}
	s = strings.TrimSpace(s)
	if len(s) > apiErrorCap {
		s = s[:apiErrorCap]
		for len(s) > 0 && !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}
