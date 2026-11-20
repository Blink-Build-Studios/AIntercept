package capture

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// A case is one call, as a directory (docs/PROVIDERS.md, "Adding a case"):
//
//	meta.json        what it is: provider, client and version, shape, when captured, notes
//	request.json     method, host, path, headers, the credential to stand in, the caller's IP
//	request-body.<ext>   the request body, decoded (request.json says how it was encoded on the wire)
//	response.json    status, headers, the body's chunk sizes on the wire
//	response-body.<ext>  the response body, decoded
//	websocket.jsonl  a WebSocket's messages instead of bodies, one per line, in order
//	expect.json      the fields the case asserts (written by hand, checked by the corpus test)

// Meta says what a case is.
type Meta struct {
	Provider string `json:"provider"` // anthropic, openai, gemini
	Client   string `json:"client"`   // claude-code, codex
	// API is the wire format, by host and path (APIOf): what an option or an
	// error's shape is read as. "" for a control-plane call outside one.
	API           string `json:"api,omitempty"`
	ClientVersion string `json:"client_version,omitempty"`
	Shape         string `json:"shape"`
	Captured      string `json:"captured"` // the day, UTC
	// Turns are the WebSocket turns kept, as numbered on the connection.
	Turns []int  `json:"turns,omitempty"`
	Notes string `json:"notes,omitempty"`
}

// Request is a case's request.
type Request struct {
	Method  string            `json:"method"`
	Host    string            `json:"host"`
	Path    string            `json:"path"`
	IP      string            `json:"ip,omitempty"`
	Headers map[string]string `json:"headers"`
	// Credential is what stands in for the redacted credential when the case
	// is replayed (the test builds it): its header and kind, and for a
	// ChatGPT token its claims.
	Credential *Credential `json:"credential,omitempty"`
	// Encoding is the body's content-encoding on the wire; the file holds it
	// decoded, and the replay encodes it again.
	Encoding string `json:"encoding,omitempty"`
	Body     string `json:"body,omitempty"` // the body's file
	// Chunks are the body's chunk sizes as sent (the replay sends the same cuts).
	Chunks []int `json:"chunks,omitempty"`
}

// Credential kinds.
const (
	CredAnthropicOAuth = "anthropic_oauth"   // sk-ant-oat01-…, Claude Code signed in with an account
	CredAnthropicKey   = "anthropic_api_key" // sk-ant-api03-…
	CredOpenAIKey      = "openai_api_key"    // sk-proj-…
	CredChatGPT        = "chatgpt_jwt"       // a ChatGPT sign-in's JWT (Codex), carrying Claims
	CredGeminiKey      = "gemini_api_key"    // AIza…
	CredOpaque         = "opaque"            // anything else: a placeholder token
)

// Credential stands in for a redacted credential.
type Credential struct {
	Header string `json:"header"`
	Scheme string `json:"scheme,omitempty"` // "Bearer"
	Kind   string `json:"kind"`
	// Claims are a ChatGPT token's claims (anonymized), as the JWT carries them.
	Claims json.RawMessage `json:"claims,omitempty"`
}

// Response is a case's response.
type Response struct {
	Status   int               `json:"status"`
	Headers  map[string]string `json:"headers"`
	Encoding string            `json:"encoding,omitempty"`
	Body     string            `json:"body,omitempty"`
	Chunks   []int             `json:"chunks,omitempty"`
	// Error is how the call ended without a response (a disconnect).
	Error string `json:"error,omitempty"`
}

// WSMessage is one WebSocket message: its direction ("up" client to server,
// "down" server to client) and either its text (JSON, as a value) or a close.
type WSMessage struct {
	Dir  string          `json:"dir"`
	Msg  json.RawMessage `json:"msg,omitempty"`
	Text string          `json:"text,omitempty"` // a text message that isn't JSON
	// Binary is a binary message's bytes, base64: zeros of its length (its
	// content is never kept).
	Binary string `json:"binary,omitempty"`
	Close  *int   `json:"close,omitempty"` // a close frame's code
}

// Case is one call, loaded.
type Case struct {
	Dir      string
	Meta     Meta
	Request  Request
	Response Response
	ReqBody  []byte
	RespBody []byte
	WS       []WSMessage
}

// Name is the case's path under the corpus root (provider/client/shape).
func (c *Case) Name(root string) string {
	rel, err := filepath.Rel(root, c.Dir)
	if err != nil {
		return c.Dir
	}
	return filepath.ToSlash(rel)
}

// Load reads a case directory.
func Load(dir string) (*Case, error) {
	c := &Case{Dir: dir}
	if err := readJSON(filepath.Join(dir, "meta.json"), &c.Meta); err != nil {
		return nil, err
	}
	if err := readJSON(filepath.Join(dir, "request.json"), &c.Request); err != nil {
		return nil, err
	}
	if err := readJSON(filepath.Join(dir, "response.json"), &c.Response); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	var err error
	if c.Request.Body != "" {
		if c.ReqBody, err = os.ReadFile(filepath.Join(dir, c.Request.Body)); err != nil {
			return nil, err
		}
	}
	if c.Response.Body != "" {
		if c.RespBody, err = os.ReadFile(filepath.Join(dir, c.Response.Body)); err != nil {
			return nil, err
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "websocket.jsonl")); err == nil {
		for i, l := range bytes.Split(bytes.TrimSpace(b), []byte("\n")) {
			var m WSMessage
			if err := json.Unmarshal(l, &m); err != nil {
				return nil, fmt.Errorf("%s: websocket.jsonl line %d: %w", dir, i+1, err)
			}
			c.WS = append(c.WS, m)
		}
	}
	return c, nil
}

// Write writes the case (not its expect.json) to dir.
func (c *Case) Write(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if len(c.ReqBody) > 0 {
		c.Request.Body = "request-body" + ext(c.ReqBody, c.Request.Headers["content-type"])
		if err := os.WriteFile(filepath.Join(dir, c.Request.Body), c.ReqBody, 0o644); err != nil {
			return err
		}
	}
	if len(c.RespBody) > 0 {
		c.Response.Body = "response-body" + ext(c.RespBody, c.Response.Headers["content-type"])
		if err := os.WriteFile(filepath.Join(dir, c.Response.Body), c.RespBody, 0o644); err != nil {
			return err
		}
	}
	if len(c.WS) > 0 {
		var b bytes.Buffer
		for _, m := range c.WS {
			line, err := json.Marshal(m)
			if err != nil {
				return err
			}
			b.Write(line)
			b.WriteByte('\n')
		}
		if err := os.WriteFile(filepath.Join(dir, "websocket.jsonl"), b.Bytes(), 0o644); err != nil {
			return err
		}
	}
	for name, v := range map[string]any{"meta.json": c.Meta, "request.json": c.Request, "response.json": c.Response} {
		if err := WriteJSON(filepath.Join(dir, name), v); err != nil {
			return err
		}
	}
	return nil
}

// ext names a body's file by what it holds.
func ext(body []byte, contentType string) string {
	t := bytes.TrimSpace(body)
	switch {
	case strings.Contains(contentType, "event-stream") || bytes.HasPrefix(t, []byte("event:")) || bytes.HasPrefix(t, []byte("data:")):
		return ".sse"
	case json.Valid(body):
		return ".json"
	}
	return ".txt"
}

// WriteJSON writes v as indented JSON, keys sorted (maps) or in field order.
func WriteJSON(path string, v any) error {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err
	}
	return os.WriteFile(path, b.Bytes(), 0o644)
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// Cases lists the case directories under root (each holding a meta.json), sorted.
func Cases(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == "meta.json" {
			out = append(out, filepath.Dir(p))
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}
