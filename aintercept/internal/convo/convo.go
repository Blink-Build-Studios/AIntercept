// Package convo turns one model request into provider-neutral messages and
// says which of them are new in this call (docs/INTERCEPTOR_V2.md, "The
// model" and "What's new in a call"). It keeps no state: what's new is read
// from the request alone.
//
// It reads Anthropic Messages (Claude Code, the SDKs), OpenAI Responses (Codex
// over HTTP or its WebSocket, the SDKs) and OpenAI Chat Completions. Any other
// request is not ok, and the caller passes it through untouched.
package convo

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"strings"
	"unicode/utf8"
)

// Formats a Request can be in.
const (
	FormatAnthropic = "anthropic_messages"
	FormatResponses = "openai_responses"
	FormatChat      = "openai_chat"
)

// Role is who a message is from.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant" // the model: its reply, its reasoning, and each tool call it made
	RoleTool      Role = "tool"      // a tool call's result
)

// Kind is what a Part holds.
type Kind string

const (
	KindText       Kind = "text"        // prompt or reply text
	KindMedia      Kind = "media"       // an image or a file
	KindToolCall   Kind = "tool_call"   // the model asks to run a tool
	KindToolResult Kind = "tool_result" // what the tool returned
	KindOpaque     Kind = "opaque"      // reasoning, encrypted content: present, never inspectable
)

// DefaultInlineLimit is the size over which a part's content goes by
// reference (the spec's default; a deployment setting).
const DefaultInlineLimit = 64 << 10

// previewBytes bounds a by-reference part's Preview.
const previewBytes = 1 << 10

// Request is one parsed model request.
type Request struct {
	Format string
	// Messages are all of the request's messages, in order. System prompts,
	// developer instructions and tool catalogs aren't messages and are left out.
	Messages []Message
	// New are the indexes (into Messages) of the messages this call adds.
	New []int
	// Chained: the request continues a previous response the provider keeps
	// (Claude Code's message threads, previous_response_id), so it carries only
	// what's new.
	Chained bool
	// Prev is the response a chained request continues: its
	// previous_response_id, or its thread's previous_message_id.
	Prev string
	// Body is the request body the spans point into.
	Body []byte
}

// Message is one turn of the conversation: a prompt, a reply, one tool call,
// one tool result.
type Message struct {
	Role  Role
	Parts []Part
	Index int // position in Messages, from 0
	// Span is the message's bytes in Body, what Replace rewrites. One message
	// on the wire can be several here (an Anthropic message holding text and
	// two tool calls is three), so per format:
	//   - Anthropic: the content: its string, or the run of content blocks.
	//   - Responses: the input item (or the input, when it's a string).
	//   - Chat: the content for text, the tool_calls element for a tool call,
	//     the whole message for a tool result.
	Span Span
}

// Part is one piece of a message. Which fields are set depends on Kind.
type Part struct {
	Kind Kind

	Text      string // KindText: the text. KindToolResult: the result as text. Empty when Ref.
	MediaType string // KindMedia: image/png, application/pdf, … ("" when the request doesn't say)
	URL       string // KindMedia sent by reference (a URL or a file id): no bytes in the body
	CallID    string // KindToolCall, KindToolResult, and KindMedia inside a tool result
	Name      string // KindToolCall: the tool
	Arguments string // KindToolCall: its arguments as JSON (a custom tool's free-form input as a JSON string). Empty when Ref.
	IsError   bool   // KindToolResult: the client marked it an error

	// Size is the content's size in bytes: the text (UTF-8), the arguments, the
	// media's decoded bytes. For KindOpaque, the size of its bytes in Body.
	Size int
	// Ref: the content is over the inline limit, so Text and Arguments are left
	// empty and Preview holds its start. Content still returns all of it.
	Ref     bool
	Preview string // Ref: the first 1 KiB of text; empty for binary media

	// Span is the part's bytes in Body: its content block or item, or the
	// string, when a message's content is a plain string. A tool result's
	// span holds the spans of the media inside it.
	Span Span

	content string // the full content: text, arguments, or media (base64 when b64)
	b64     bool
}

// Span is a range of bytes in Request.Body, [Start, End).
type Span struct{ Start, End int }

// Content is the part's full content, whether or not it was inlined: the
// text, the arguments, or the media's decoded bytes. Nil for KindOpaque and
// for media sent by reference.
func (p Part) Content() []byte {
	if p.content == "" {
		return nil
	}
	if !p.b64 {
		return []byte(p.content)
	}
	b, err := base64.StdEncoding.DecodeString(p.content)
	if err != nil {
		b, _ = base64.RawStdEncoding.DecodeString(p.content)
	}
	return b
}

// Parser parses requests with a given inline limit.
type Parser struct {
	// InlineLimit is the size over which a part's content goes by reference;
	// zero means DefaultInlineLimit.
	InlineLimit int
}

// Parse parses a request with the default inline limit. See Parser.Parse.
func Parse(host, path string, body []byte) (Request, bool) { return Parser{}.Parse(host, path, body) }

// ParseWebSocket parses a WebSocket client message with the default inline
// limit. See Parser.ParseWebSocket.
func ParseWebSocket(host, path string, msg []byte) (Request, bool) {
	return Parser{}.ParseWebSocket(host, path, msg)
}

// Parse parses a model request's body (decoded per its content-encoding) sent
// to host and path. ok is false for a request in a format it doesn't read:
// another endpoint, a body that isn't that endpoint's request, or a malformed
// one.
func (p Parser) Parse(host, path string, body []byte) (Request, bool) {
	format := formatOf(host, path)
	if format == "" || !json.Valid(body) {
		return Request{}, false
	}
	return p.parse(format, body)
}

// ParseWebSocket parses a client message on a Responses WebSocket (Codex's
// chatgpt.com /backend-api/codex/responses): a response.create carries the
// same body as an HTTP request. Any other message is not ok.
func (p Parser) ParseWebSocket(host, path string, msg []byte) (Request, bool) {
	if formatOf(host, path) != FormatResponses || !json.Valid(msg) {
		return Request{}, false
	}
	var m struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(msg, &m) != nil || m.Type != "response.create" {
		return Request{}, false
	}
	return p.parse(FormatResponses, msg)
}

// formatOf is the format of the model requests an endpoint takes ("" for any
// other endpoint, including the side calls next to them, like token counts).
func formatOf(host, path string) string {
	host = strings.ToLower(host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	path, _, _ = strings.Cut(path, "?")
	switch {
	case host == "api.anthropic.com" && path == "/v1/messages":
		return FormatAnthropic
	case host == "chatgpt.com" && path == "/backend-api/codex/responses",
		host == "api.openai.com" && path == "/v1/responses":
		return FormatResponses
	case host == "api.openai.com" && path == "/v1/chat/completions":
		return FormatChat
	}
	return ""
}

func (p Parser) parse(format string, body []byte) (Request, bool) {
	top, ok := members(root(body))
	if !ok {
		return Request{}, false
	}
	b := &builder{limit: p.InlineLimit}
	if b.limit <= 0 {
		b.limit = DefaultInlineLimit
	}
	r := Request{Format: format, Body: body}
	switch format {
	case FormatAnthropic:
		ok = b.anthropic(top)
		var thread struct {
			PreviousMessageID string `json:"previous_message_id"`
		}
		if top["thread"].kind() == '{' && json.Unmarshal(top["thread"].raw, &thread) == nil {
			r.Prev = thread.PreviousMessageID
		}
	case FormatResponses:
		ok = b.responses(top)
		r.Prev, _ = top["previous_response_id"].str()
	case FormatChat:
		ok = b.chat(top)
	}
	if !ok {
		return Request{}, false
	}
	r.Chained = r.Prev != ""
	r.Messages = b.msgs
	from := b.cut
	if r.Chained {
		from = 0
	}
	r.New = []int{}
	for i := from; i < len(r.Messages); i++ {
		r.New = append(r.New, i)
	}
	return r, true
}

// builder collects a request's messages, item by item, and where the new ones
// start.
//
// What's new: the messages after the last output item (the model's reply, its
// reasoning, a tool call) that the client followed with an item of its own.
// That is the spec's "after the last assistant message", except when the
// request ends with output: that output is the client's own (an Anthropic
// prefill), so it and what the client sent before it are new, where the
// spec's rule would find nothing new.
type builder struct {
	limit int
	msgs  []Message
	cut   int  // index of the first new message
	out   bool // the last item was output
}

// item records the start of one item of the conversation, and whether it is
// the model's output. Call it before adding the item's messages.
func (b *builder) item(output bool) {
	if !output && b.out {
		b.cut = len(b.msgs)
	}
	b.out = output
}

// add appends a message holding parts; nothing for none.
func (b *builder) add(role Role, span Span, parts ...Part) {
	if len(parts) == 0 {
		return
	}
	b.msgs = append(b.msgs, Message{Role: role, Parts: parts, Index: len(b.msgs), Span: span})
}

// fill sets a part's inline content, or makes it a reference when the
// content is over the limit.
func (b *builder) fill(p Part) Part {
	if p.Kind == KindOpaque || p.content == "" {
		return p
	}
	if p.Size > b.limit {
		p.Ref = true
		if !p.b64 && (p.Kind != KindMedia || strings.HasPrefix(p.MediaType, "text/")) {
			p.Preview = preview(p.content)
		}
		return p
	}
	switch p.Kind {
	case KindText, KindToolResult:
		p.Text = p.content
	case KindToolCall:
		p.Arguments = p.content
	}
	return p
}

func (b *builder) text(s string, span Span) Part {
	return b.fill(Part{Kind: KindText, Size: len(s), Span: span, content: s})
}

func (b *builder) toolCall(id, name, args string, span Span) Part {
	return b.fill(Part{Kind: KindToolCall, CallID: id, Name: name, Size: len(args), Span: span, content: args})
}

// toolResult is a tool result whose content is text, or a list of parts:
// their text, joined, with any media following the result in the message.
// Parts it doesn't know go in as their JSON.
func (b *builder) toolResult(callID string, isError bool, content value, span Span) []Part {
	res := Part{Kind: KindToolResult, CallID: callID, IsError: isError, Span: span}
	var media []Part
	switch content.kind() {
	case '"':
		res.content, _ = content.str()
	case '[':
		var texts []string
		parts, _ := elements(content)
		for _, v := range parts {
			switch p, isText := b.contentPart(v); {
			case isText:
				texts = append(texts, p.content)
			case p.Kind == KindMedia:
				p.CallID = callID
				media = append(media, p)
			default:
				texts = append(texts, string(v.raw))
			}
		}
		res.content = strings.Join(texts, "\n")
	case 0, 'n':
	default:
		res.content = string(content.raw)
	}
	res.Size = len(res.content)
	return append([]Part{b.fill(res)}, media...)
}

// contentPart reads one part of a message's content in any of the formats:
// text, an image or a file, or opaque. isText says it's text.
func (b *builder) contentPart(v value) (p Part, isText bool) {
	var c struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
		Source  *struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
			URL       string `json:"url"`
			FileID    string `json:"file_id"`
		} `json:"source"` // Anthropic image, document
		ImageURL json.RawMessage `json:"image_url"` // Responses: a string; Chat: {"url": …}
		FileID   string          `json:"file_id"`
		FileData string          `json:"file_data"` // Responses input_file
		FileURL  string          `json:"file_url"`
		File     *struct {
			FileData string `json:"file_data"`
			FileID   string `json:"file_id"`
		} `json:"file"` // Chat
		InputAudio *struct {
			Data   string `json:"data"`
			Format string `json:"format"`
		} `json:"input_audio"`
	}
	if json.Unmarshal(v.raw, &c) != nil {
		return opaque(v), false
	}
	switch c.Type {
	case "text", "input_text", "output_text":
		return b.text(c.Text, v.span), true
	case "refusal":
		return b.text(c.Refusal, v.span), true
	case "image", "document": // Anthropic
		switch s := c.Source; {
		case s == nil:
		case s.Type == "base64":
			return b.media(s.MediaType, s.Data, true, "", v.span), false
		case s.Type == "text":
			return b.media(s.MediaType, s.Data, false, "", v.span), false
		case s.Type == "url":
			return b.media("", "", false, s.URL, v.span), false
		case s.Type == "file":
			return b.media("", "", false, s.FileID, v.span), false
		}
	case "input_image", "image_url": // Responses, Chat
		if u := imageURL(c.ImageURL); u != "" {
			return b.dataURL(u, v.span), false
		}
		if c.FileID != "" {
			return b.media("", "", false, c.FileID, v.span), false
		}
	case "input_file", "file": // Responses, Chat
		data, id := c.FileData, c.FileID
		if c.File != nil {
			data, id = c.File.FileData, c.File.FileID
		}
		switch {
		case strings.HasPrefix(data, "data:"):
			return b.dataURL(data, v.span), false
		case data != "":
			return b.media("", data, true, "", v.span), false
		case id != "":
			return b.media("", "", false, id, v.span), false
		case c.FileURL != "":
			return b.media("", "", false, c.FileURL, v.span), false
		}
	case "input_audio": // Chat
		if a := c.InputAudio; a != nil {
			return b.media("audio/"+a.Format, a.Data, true, "", v.span), false
		}
	}
	return opaque(v), false // reasoning, encrypted content, and any part it doesn't know
}

// imageURL reads an image_url that is a string (Responses) or {"url": …} (Chat).
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

// content reads a message's content: a string is one text part; a list is
// its parts; none is none. ok is false for anything else.
func (b *builder) content(v value) ([]Part, bool) {
	switch v.kind() {
	case 0, 'n':
		return nil, true
	case '"':
		s, _ := v.str()
		return []Part{b.text(s, v.span)}, true
	case '[':
		vs, _ := elements(v)
		parts := make([]Part, 0, len(vs))
		for _, e := range vs {
			if e.kind() != '{' {
				return nil, false
			}
			p, _ := b.contentPart(e)
			parts = append(parts, p)
		}
		return parts, true
	}
	return nil, false
}

func opaque(v value) Part {
	return Part{Kind: KindOpaque, Size: len(v.raw), Span: v.span}
}

// media is an image or file sent inline, as base64 (b64) or as text, or by
// reference (url: a URL or a file id).
func (b *builder) media(mediaType, data string, b64 bool, url string, span Span) Part {
	p := Part{Kind: KindMedia, MediaType: mediaType, URL: url, Span: span, content: data, b64: b64}
	if b64 {
		p.Size = base64Len(data)
	} else {
		p.Size = len(data)
	}
	return b.fill(p)
}

// dataURL is a media part from a data: URL, or by reference from any other URL.
func (b *builder) dataURL(u string, span Span) Part {
	rest, isData := strings.CutPrefix(u, "data:")
	if !isData {
		return b.media("", "", false, u, span)
	}
	meta, data, _ := strings.Cut(rest, ",")
	mediaType, _, _ := strings.Cut(meta, ";")
	return b.media(mediaType, data, strings.HasSuffix(meta, ";base64"), "", span)
}

// base64Len is the decoded size of a base64 string.
func base64Len(s string) int {
	return len(strings.TrimRight(s, "=")) * 3 / 4
}

// preview is the first 1 KiB of s, cut on a rune.
func preview(s string) string {
	if len(s) <= previewBytes {
		return s
	}
	i := previewBytes
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i]
}
