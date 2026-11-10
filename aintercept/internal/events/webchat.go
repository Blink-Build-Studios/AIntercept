package events

import (
	"bytes"
	"encoding/json"
	"strings"
)

// WebChat is what a consumer chat app's own conversation API carries beyond
// the model call itself (ChatGPT web on chatgpt.com, Claude web on claude.ai
// and the desktop app that wraps it): conversation and org ids, the model the
// user picked vs. the one that served, plan, surface, and file uploads. Set
// on complete rows for those apps' turn and upload requests.
type WebChat struct {
	App            string `json:"app"` // "chatgpt" | "claude"
	ConversationID string `json:"conversation_id,omitempty"`
	OrgID          string `json:"org_id,omitempty"` // claude.ai org (from the path)
	// RequestedModel is the model the user picked ("auto" on ChatGPT, where
	// Model on the row is the model that actually served the turn).
	RequestedModel string `json:"requested_model,omitempty"`
	PlanType       string `json:"plan_type,omitempty"`
	Surface        string `json:"surface,omitempty"`       // ChatGPT client_contextual_info.app_surface
	Trigger        string `json:"trigger,omitempty"`       // ChatGPT turn_attribution.turn_trigger
	ThinkingMode   string `json:"thinking_mode,omitempty"` // claude.ai thinking_mode
	SearchCalls    int    `json:"search_calls,omitempty"`
	Title          string `json:"title,omitempty"` // the app's generated conversation title
	// Limits is the app's usage-limit state after the turn (ChatGPT
	// limits_progress, claude.ai message_limit); the closest thing to token
	// usage these apps expose.
	Limits any `json:"limits,omitempty"`
	// ServerMetadata is ChatGPT's per-turn server_ste_metadata, verbatim
	// (model_slug, plan_type, tool_name, region, server_ttfvt_ms, …).
	ServerMetadata map[string]any `json:"server_metadata,omitempty"`
	// Upload describes a file upload call (not a model turn).
	Upload *WebChatUpload `json:"upload,omitempty"`
}

// WebChatUpload is one file a user uploaded through a chat app.
type WebChatUpload struct {
	FileID   string `json:"file_id,omitempty"`
	Name     string `json:"name,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	Size     int64  `json:"size,omitempty"`
	UseCase  string `json:"use_case,omitempty"` // ChatGPT: multimodal, ace_upload, …
	Kind     string `json:"kind,omitempty"`     // claude.ai file_kind: blob, image, document
}

// applyWebChat fills a complete row from a chat app's conversation or upload
// request. A no-op for other hosts and paths.
func (e *Event) applyWebChat(path string, req, resp []byte, encoding string) {
	path = strings.SplitN(path, "?", 2)[0]
	switch e.Host {
	case "chatgpt.com":
		switch {
		case path == "/backend-api/f/conversation" || path == "/backend-api/conversation":
			e.applyChatGPTTurn(req, resp, encoding)
		case path == "/backend-api/files":
			e.WebChat = &WebChat{App: "chatgpt", Upload: chatGPTUpload(req, resp, encoding)}
		}
	case "claude.ai":
		parts := strings.Split(strings.Trim(path, "/"), "/")
		// api/organizations/<org>/chat_conversations/<conv>/completion (or retry_completion)
		if len(parts) == 6 && parts[1] == "organizations" && parts[3] == "chat_conversations" &&
			strings.HasSuffix(parts[5], "completion") {
			e.applyClaudeTurn(parts[2], parts[4], req, resp, encoding)
		}
		// The signed-in account (claude.ai's session cookie is opaque, so this
		// is where its identity appears); joined to the session's other rows by
		// key_hash, the hash of that cookie.
		if path == "/api/account" || path == "/api/auth/verify_google" ||
			(len(parts) == 4 && parts[0] == "edge-api" && parts[1] == "bootstrap" && parts[3] == "app_start") {
			if id := claudeAccount(resp, encoding); id != nil {
				e.Auth = id
			}
		}
		// api/organizations/<org>/conversations/<conv>/wiggle/upload-file
		if len(parts) == 7 && parts[1] == "organizations" && parts[6] == "upload-file" {
			e.WebChat = &WebChat{App: "claude", OrgID: parts[2], ConversationID: parts[4],
				Upload: claudeUpload(resp, encoding)}
		}
	}
}

// applyClaudeTurn adds claude.ai's turn context. The response is Anthropic
// Messages SSE, so model, tool uses and stop reason come from the generic
// extractors; this adds the request settings, attachments and limit state.
func (e *Event) applyClaudeTurn(org, conv string, req, resp []byte, encoding string) {
	wc := &WebChat{App: "claude", OrgID: org, ConversationID: conv}
	var r struct {
		Model        string `json:"model"`
		Effort       string `json:"effort"`
		ThinkingMode string `json:"thinking_mode"`
		Files        []any  `json:"files"`
		Attachments  []struct {
			FileSize int `json:"file_size"`
		} `json:"attachments"`
		ParentMessageUUID string `json:"parent_message_uuid"`
	}
	if json.Unmarshal(req, &r) == nil {
		wc.RequestedModel, wc.ThinkingMode = r.Model, r.ThinkingMode
		if e.Effort == "" {
			e.Effort = r.Effort
		}
		if e.PreviousMessageID == "" {
			e.PreviousMessageID = r.ParentMessageUUID
		}
		// files: uploaded file ids; attachments: text extracted client-side.
		if n := len(r.Files) + len(r.Attachments); n > 0 {
			if e.Attachments == nil {
				e.Attachments = &Attachments{}
			}
			e.Attachments.Files += n
			e.Attachments.ByReference += len(r.Files)
			for _, a := range r.Attachments {
				e.Attachments.InlineBytes += a.FileSize
			}
		}
	}
	forEachSSEData(resp, encoding, func(b []byte) {
		var ev struct {
			Type         string `json:"type"`
			MessageLimit any    `json:"message_limit"`
		}
		if json.Unmarshal(b, &ev) == nil && ev.Type == "message_limit" {
			wc.Limits = ev.MessageLimit
		}
	})
	e.WebChat = wc
}

// claudeUpload reads claude.ai's upload-file response.
func claudeUpload(resp []byte, encoding string) *WebChatUpload {
	raw, err := decodeBody(resp, encoding)
	if err != nil {
		return nil
	}
	var r struct {
		FileUUID  string `json:"file_uuid"`
		FileName  string `json:"file_name"`
		MediaType string `json:"media_type"`
		SizeBytes int64  `json:"size_bytes"`
		FileKind  string `json:"file_kind"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return nil
	}
	return &WebChatUpload{FileID: r.FileUUID, Name: r.FileName, MimeType: r.MediaType, Size: r.SizeBytes, Kind: r.FileKind}
}

// forEachSSEData calls f with each SSE "data:" payload of a (possibly
// compressed) response body.
func forEachSSEData(body []byte, encoding string, f func([]byte)) {
	raw, err := decodeBody(body, encoding)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if d, ok := strings.CutPrefix(strings.TrimSpace(line), "data:"); ok {
			f([]byte(strings.TrimSpace(d)))
		}
	}
}

// claudeAccount reads the signed-in account from a claude.ai response: the
// object itself (/api/account) or its leading "account" member (bootstrap,
// login). Bootstrap responses are large and may be truncated by the capture
// cap, so the body is read as a stream and only up to the account.
func claudeAccount(resp []byte, encoding string) *AuthIdentity {
	raw, err := decodeBody(resp, encoding)
	if err != nil || len(raw) == 0 {
		return nil
	}
	type account struct {
		TaggedID     string `json:"tagged_id"`
		UUID         string `json:"uuid"`
		EmailAddress string `json:"email_address"`
		FullName     string `json:"full_name"`
	}
	toID := func(a account) *AuthIdentity {
		if a.EmailAddress == "" && a.UUID == "" {
			return nil
		}
		return &AuthIdentity{Email: a.EmailAddress, Name: a.FullName, UserID: a.TaggedID, AccountID: a.UUID}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil
	}
	var top account
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			break
		}
		key, _ := t.(string)
		switch key {
		case "account":
			var a account
			if dec.Decode(&a) != nil {
				return nil
			}
			return toID(a)
		case "tagged_id", "uuid", "email_address", "full_name":
			var v string
			if dec.Decode(&v) != nil {
				return toID(top)
			}
			switch key {
			case "tagged_id":
				top.TaggedID = v
			case "uuid":
				top.UUID = v
			case "email_address":
				top.EmailAddress = v
			case "full_name":
				top.FullName = v
			}
		default:
			var skip json.RawMessage
			if dec.Decode(&skip) != nil {
				return toID(top)
			}
		}
	}
	return toID(top)
}
