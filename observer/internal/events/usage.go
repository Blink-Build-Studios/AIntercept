package events

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"strings"
)

// Usage holds the token counts extracted from a response. Content is never
// retained — only these counts and the model.
type Usage struct {
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
}

// ModelFromRequestBody parses the `model` field from a request body (plaintext
// JSON — Anthropic and OpenAI put it there). Returns "" if absent/unparseable.
func ModelFromRequestBody(body []byte) string {
	var m struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return ""
	}
	return m.Model
}

// ModelFromPath extracts the model from a request path for providers that carry
// it in the URL rather than the body. This matches ONLY the Gemini shape — the
// model is between "/models/" and a ":<action>" suffix:
//
//	/v1beta/models/gemini-2.5-pro:generateContent       -> "gemini-2.5-pro"
//	/v1beta/models/gemini-2.5-pro:streamGenerateContent -> "gemini-2.5-pro"
//
// Returns "" when the path lacks that ":<action>" suffix, so it does NOT misfire
// on OpenAI model-management paths that merely contain "/models/", e.g.
// GET /v1/models/gpt-4o or /v1/fine_tuning/models/ft:... (no ":action" → "").
func ModelFromPath(path string) string {
	i := strings.Index(path, "/models/")
	if i < 0 {
		return ""
	}
	rest := path[i+len("/models/"):]
	// Trim query string.
	if q := strings.IndexByte(rest, '?'); q >= 0 {
		rest = rest[:q]
	}
	// Gemini generate paths are "<model>:<action>" where <action> is a generate
	// method. Split on the LAST colon so model names that themselves contain a
	// colon (rare) aren't truncated, then require the action to be a known Gemini
	// method — this rejects OpenAI fine-tune ids like "ft:gpt-4o-mini:org" that
	// merely contain a colon.
	c := strings.LastIndexByte(rest, ':')
	if c < 0 {
		return ""
	}
	model, action := rest[:c], rest[c+1:]
	if !isGeminiAction(action) {
		return ""
	}
	// A model name never contains a slash.
	if strings.IndexByte(model, '/') >= 0 {
		return ""
	}
	return model
}

// isGeminiAction reports whether s is a Gemini generate-style method suffix.
func isGeminiAction(s string) bool {
	switch s {
	case "generateContent", "streamGenerateContent", "countTokens",
		"embedContent", "batchEmbedContents", "generateAnswer":
		return true
	}
	return false
}

// UsageFromResponseBody extracts token counts from a (possibly gzipped, possibly
// SSE) response body, discarding all content. It understands:
//   - Anthropic SSE: message_start carries input_tokens + cache fields;
//     message_delta carries the running output_tokens.
//   - OpenAI/Anthropic non-streamed JSON: a top-level usage/`usage` object.
//
// encoding is the response Content-Encoding ("gzip" or ""). Returns ok=false if
// no usage was found. It never returns or logs body content.
func UsageFromResponseBody(body []byte, encoding string) (Usage, bool) {
	raw, err := decodeBody(body, encoding)
	if err != nil {
		return Usage{}, false
	}
	text := string(raw)

	// SSE? Lines look like "event: ..." / "data: {json}". Parse each data line.
	if strings.Contains(text, "data:") {
		if u, ok := usageFromSSE(text); ok {
			return u, true
		}
	}
	// Fall back to a single JSON object with a usage block.
	return usageFromJSON(raw)
}

func decodeBody(body []byte, encoding string) ([]byte, error) {
	if strings.Contains(strings.ToLower(encoding), "gzip") {
		gr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		defer gr.Close()
		// A response that exceeded the capture cap is a TRUNCATED gzip stream, so
		// io.ReadAll returns ErrUnexpectedEOF along with everything decoded so far.
		// Keep that partial plaintext: it still contains the early message_start
		// frame (input/cache tokens), so we salvage usage rather than discarding
		// the whole response. Only a header-level error (gzip.NewReader above)
		// yields nothing.
		out, err := io.ReadAll(gr)
		if err != nil && len(out) == 0 {
			return nil, err
		}
		return out, nil
	}
	return body, nil
}

// usageFromSSE walks SSE data lines and accumulates usage. Anthropic emits
// input/cache tokens on message_start and the final output_tokens on
// message_delta; OpenAI streamed responses emit a usage object on the final
// chunk. We take the max output seen so partial deltas don't under-count.
func usageFromSSE(text string) (Usage, bool) {
	var u Usage
	found := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		if applyUsageJSON(payload, &u) {
			found = true
		}
	}
	return u, found
}

func usageFromJSON(raw []byte) (Usage, bool) {
	var u Usage
	if applyUsageJSON(string(raw), &u) {
		return u, true
	}
	return Usage{}, false
}

// applyUsageJSON parses one JSON object and folds any usage fields into u.
// Handles all three providers across their request/response shapes:
//   - Anthropic: top-level `usage` (or `message.usage` on message_start) with
//     input_tokens/output_tokens/cache_*.
//   - OpenAI chat/completions: `usage` with prompt_tokens/completion_tokens
//     (+ cached prompt tokens under prompt_tokens_details).
//   - OpenAI Responses API: `usage` with input_tokens/output_tokens, nested
//     under a `response` object in the response.completed SSE event; cached
//     tokens under input_tokens_details.cached_tokens.
//   - Gemini: top-level `usageMetadata` with promptTokenCount /
//     candidatesTokenCount / cachedContentTokenCount.
//
// Returns true if any usage field was seen.
func applyUsageJSON(s string, u *Usage) bool {
	var obj struct {
		Usage   *rawUsage `json:"usage"`
		Message *struct {
			Usage *rawUsage `json:"usage"`
		} `json:"message"`
		// OpenAI Responses API nests usage under `response`.
		Response *struct {
			Usage *rawUsage `json:"usage"`
		} `json:"response"`
		UsageMetadata *geminiUsage `json:"usageMetadata"`
	}
	if err := json.Unmarshal([]byte(s), &obj); err != nil {
		return false
	}

	// Gemini shape.
	if gm := obj.UsageMetadata; gm != nil {
		if gm.PromptTokenCount > 0 {
			u.InputTokens = gm.PromptTokenCount
		}
		if gm.CandidatesTokenCount > u.OutputTokens {
			u.OutputTokens = gm.CandidatesTokenCount
		}
		if gm.CachedContentTokenCount > 0 {
			u.CacheReadTokens = gm.CachedContentTokenCount
		}
		return true
	}

	ru := obj.Usage
	if ru == nil && obj.Message != nil {
		ru = obj.Message.Usage
	}
	if ru == nil && obj.Response != nil {
		ru = obj.Response.Usage
	}
	if ru == nil {
		return false
	}
	// Anthropic: input_tokens/output_tokens (+ cache_*). OpenAI:
	// prompt_tokens/completion_tokens. Take whichever is present; for streamed
	// output_tokens, keep the largest seen (deltas are cumulative-ish).
	if ru.InputTokens > 0 {
		u.InputTokens = ru.InputTokens
	}
	if ru.PromptTokens > 0 {
		u.InputTokens = ru.PromptTokens
	}
	if ru.OutputTokens > u.OutputTokens {
		u.OutputTokens = ru.OutputTokens
	}
	if ru.CompletionTokens > u.OutputTokens {
		u.OutputTokens = ru.CompletionTokens
	}
	if ru.CacheReadInputTokens > 0 {
		u.CacheReadTokens = ru.CacheReadInputTokens
	}
	// OpenAI chat/completions reports cached prompt tokens under
	// prompt_tokens_details; the Responses API uses input_tokens_details.
	if ru.PromptTokensDetails != nil && ru.PromptTokensDetails.CachedTokens > 0 {
		u.CacheReadTokens = ru.PromptTokensDetails.CachedTokens
	}
	if ru.InputTokensDetails != nil && ru.InputTokensDetails.CachedTokens > 0 {
		u.CacheReadTokens = ru.InputTokensDetails.CachedTokens
	}
	if ru.CacheCreationInputTokens > 0 {
		u.CacheWriteTokens = ru.CacheCreationInputTokens
	}
	return true
}

type rawUsage struct {
	// Anthropic + OpenAI Responses API
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	// OpenAI chat/completions
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	// OpenAI Responses API cached tokens.
	InputTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

// geminiUsage is Gemini's usageMetadata shape.
type geminiUsage struct {
	PromptTokenCount        int `json:"promptTokenCount"`
	CandidatesTokenCount    int `json:"candidatesTokenCount"`
	CachedContentTokenCount int `json:"cachedContentTokenCount"`
}
