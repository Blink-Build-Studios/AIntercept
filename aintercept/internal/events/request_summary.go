package events

import (
	"errors"
	"strings"
)

// Products whose calls AIntercept recognizes by their client headers.
const (
	ProductClaudeCode = "claude_code"
	ProductCodex      = "codex"
)

// ProductOf names the product a request came from: by its user agent, Codex's
// originator header, or a Claude Code session id on an Anthropic call. ""
// when the client isn't one AIntercept recognizes.
func ProductOf(host, userAgent, originator, sessionID string) string {
	switch {
	case sessionID != "" && host == "api.anthropic.com", strings.HasPrefix(userAgent, "claude-cli/"), strings.HasPrefix(userAgent, "claude-code/"):
		return ProductClaudeCode
	case strings.HasPrefix(originator, "codex"), strings.HasPrefix(userAgent, "codex"):
		return ProductCodex
	}
	return ""
}

// RequestSummary is what a request says about itself, parsed from its headers
// and body before it goes upstream: the model and settings, the tools it
// declares, and the identity the client sends. No content.
type RequestSummary struct {
	Model          string
	Effort         string
	Stream         bool
	MaxTokens      int
	ToolsAvailable []ToolRef
	Prewarm        bool
	Attachments    int
	// Identity the client sends: Claude Code's metadata.user_id, Codex's turn
	// metadata and account header.
	AccountID string
	DeviceID  string
	SessionID string
	// Client is the identifying client headers (user agent, originator, …).
	Client  map[string]string
	Product string
}

// SummarizeRequest parses a request. body may be compressed per the request's
// content-encoding header, and may be nil (a request with no body).
func SummarizeRequest(host, path string, headers map[string]string, body []byte) RequestSummary {
	if enc := headerValue(headers, "content-encoding"); enc != "" && len(body) > 0 {
		if plain, err := decodeBody(body, enc); err == nil {
			body = plain
		}
	}
	var s RequestSummary
	if len(body) > 0 {
		s.Model = ModelFromRequestBody(body)
		s.ToolsAvailable, _ = ToolsFromRequestBody(body)
		rm := RequestMetaFromBody(body)
		s.Effort, s.Stream, s.MaxTokens, s.Prewarm = rm.Effort, rm.Stream, rm.MaxTokens, rm.Prewarm
		if s.Effort == "" {
			s.Effort = rm.ThinkingType
		}
		s.DeviceID, s.AccountID, s.SessionID = rm.UserID.DeviceID, rm.UserID.AccountUUID, rm.UserID.SessionID
		if a := AttachmentsFromRequestBody(body); a != nil {
			s.Attachments = a.Images + a.Files
		}
	}
	if md := codexTurnMetadata(headerValue(headers, "x-codex-turn-metadata"), body); md != nil {
		str := func(k string) string { v, _ := md[k].(string); return v }
		s.SessionID = firstNonEmpty(s.SessionID, str("session_id"))
		s.DeviceID = firstNonEmpty(s.DeviceID, str("installation_id"))
	}
	s.AccountID = firstNonEmpty(s.AccountID, headerValue(headers, "chatgpt-account-id"))
	s.SessionID = firstNonEmpty(s.SessionID, headerValue(headers, "x-claude-code-session-id"))
	if s.Model == "" {
		s.Model = ModelFromPath(path)
	}
	s.Client = pickHeaders(headers, clientHeaders)
	s.Product = ProductOf(host, headerValue(headers, "user-agent"), headerValue(headers, "originator"), s.SessionID)
	return s
}

// ErrUndecodable is returned by DecodeBody for an encoding it can't decode,
// or a body that decodes past the size cap.
var ErrUndecodable = errors.New("events: body can't be decoded")

// DecodeBody decompresses a body per its content-encoding (gzip, br, zstd).
// An empty or identity encoding returns body unchanged. Unlike the extractors'
// best-effort decoding, it fails rather than return a partial body.
func DecodeBody(body []byte, encoding string) ([]byte, error) {
	if e := strings.ToLower(strings.TrimSpace(encoding)); e == "" || e == "identity" {
		return body, nil
	}
	if !decodable(encoding) {
		return nil, ErrUndecodable
	}
	out, err := decodeBody(body, encoding)
	if err != nil || len(out) >= maxDecodedBody {
		return nil, ErrUndecodable
	}
	return out, nil
}
