package events

import (
	"encoding/json"
	"strings"
)

// Attachments counts the images and files sent with a model request. Files
// travel inside the model protocol itself (inline base64 or by reference), not
// through a separate upload, so they're read from the request body. Only
// counts and sizes are kept, never the content.
//
// With clients that resend the whole history each turn (Claude Code over
// plain messages, OpenAI chat), an attachment is counted again on every later
// turn; with threaded / incremental clients (Codex WebSocket, Claude Code
// message threads) it's counted on the turn that adds it.
type Attachments struct {
	Images int `json:"images,omitempty"`
	Files  int `json:"files,omitempty"` // documents / PDFs / other files
	// InlineBytes estimates the decoded size of attachments sent inline
	// (base64 data URLs or data fields).
	InlineBytes int `json:"inline_bytes,omitempty"`
	// ByReference counts attachments sent as a URL or file id rather than
	// inline.
	ByReference int `json:"by_reference,omitempty"`
}

// AttachmentsFromRequestBody scans a request's conversation for image/file
// parts: Anthropic (image, document blocks), OpenAI chat (image_url, file,
// input_audio), OpenAI Responses incl. Codex (input_image, input_file), and
// Gemini (inlineData, fileData). nil when there are none.
func AttachmentsFromRequestBody(body []byte) *Attachments {
	var req struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Input    json.RawMessage `json:"input"`
		Contents []struct {
			Parts []json.RawMessage `json:"parts"`
		} `json:"contents"`
	}
	if json.Unmarshal(body, &req) != nil {
		return nil
	}
	a := &Attachments{}
	for _, m := range req.Messages {
		a.addParts(m.Content)
	}
	if len(req.Input) > 0 && req.Input[0] == '[' {
		var items []struct {
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(req.Input, &items) == nil {
			for _, it := range items {
				a.addParts(it.Content)
			}
		}
	}
	for _, c := range req.Contents {
		for _, p := range c.Parts {
			var gp struct {
				InlineData *struct {
					MimeType string `json:"mimeType"`
					Data     string `json:"data"`
				} `json:"inlineData"`
				FileData *struct {
					MimeType string `json:"mimeType"`
				} `json:"fileData"`
			}
			if json.Unmarshal(p, &gp) != nil {
				continue
			}
			switch {
			case gp.InlineData != nil:
				a.count(strings.HasPrefix(gp.InlineData.MimeType, "image/"), base64Size(gp.InlineData.Data), false)
			case gp.FileData != nil:
				a.count(strings.HasPrefix(gp.FileData.MimeType, "image/"), 0, true)
			}
		}
	}
	if *a == (Attachments{}) {
		return nil
	}
	return a
}

// contentPart covers the image/file part shapes of Anthropic and OpenAI.
type contentPart struct {
	Type   string    `json:"type"`
	Source *struct { // Anthropic image / document
		Type   string `json:"type"` // base64 | url | file
		Data   string `json:"data"`
		FileID string `json:"file_id"`
	} `json:"source"`
	ImageURL json.RawMessage `json:"image_url"` // Responses: string; chat: {"url": …}
	FileID   string          `json:"file_id"`
	FileData string          `json:"file_data"` // Responses input_file
	File     *struct {       // chat "file" part
		FileData string `json:"file_data"`
		FileID   string `json:"file_id"`
	} `json:"file"`
	InputAudio *struct {
		Data string `json:"data"`
	} `json:"input_audio"`
}

func (a *Attachments) addParts(raw json.RawMessage) {
	if len(raw) == 0 || raw[0] != '[' {
		return
	}
	var parts []contentPart
	if json.Unmarshal(raw, &parts) != nil {
		return
	}
	for _, p := range parts {
		switch p.Type {
		case "image", "document": // Anthropic
			if p.Source == nil {
				continue
			}
			a.count(p.Type == "image", base64Size(p.Source.Data), p.Source.Type != "base64")
		case "input_image", "image_url": // OpenAI Responses / chat
			url := imageURL(p.ImageURL)
			a.count(true, dataURLSize(url), url == "" || !strings.HasPrefix(url, "data:"))
		case "input_file": // OpenAI Responses
			a.count(false, dataURLSize(p.FileData)+base64Size(p.FileData), p.FileData == "")
		case "file": // OpenAI chat
			if p.File != nil {
				a.count(false, dataURLSize(p.File.FileData)+base64Size(p.File.FileData), p.File.FileData == "")
			}
		case "input_audio":
			if p.InputAudio != nil {
				a.count(false, base64Size(p.InputAudio.Data), false)
			}
		}
	}
}

func (a *Attachments) count(image bool, inlineBytes int, byRef bool) {
	if image {
		a.Images++
	} else {
		a.Files++
	}
	a.InlineBytes += inlineBytes
	if byRef {
		a.ByReference++
	}
}

// imageURL reads an image_url that is a string (Responses) or {"url": …} (chat).
func imageURL(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var o struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(raw, &o)
	return o.URL
}

// dataURLSize estimates the decoded size of a data: URL's base64 payload.
func dataURLSize(u string) int {
	if !strings.HasPrefix(u, "data:") {
		return 0
	}
	if i := strings.Index(u, ";base64,"); i >= 0 {
		return base64Size(u[i+len(";base64,"):])
	}
	return 0
}

// base64Size estimates the decoded size of a base64 string (no data: prefix).
func base64Size(s string) int {
	if s == "" || strings.HasPrefix(s, "data:") {
		return 0
	}
	return len(strings.TrimRight(s, "=")) * 3 / 4
}
