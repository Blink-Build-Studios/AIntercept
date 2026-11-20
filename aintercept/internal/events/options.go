package events

import (
	"encoding/json"
	"strings"
)

// Options are the provider settings a call asks for, and the ones the
// provider reports serving it with (the Interceptor's RequestInfo.options and
// ResponseInfo.options, docs/INTERCEPTOR.md "Options"). Each API puts them in
// its own place in the body; they're read from there per API, and passed as
// the provider sends them. A setting the request doesn't make isn't listed.
const (
	OptionEffort      = "effort"       // how hard the model reasons
	OptionSpeed       = "speed"        // Anthropic's fast mode ("fast"; absent: standard)
	OptionServiceTier = "service_tier" // the priority tier
	OptionThinking    = "thinking"     // Anthropic's thinking mode (enabled, adaptive, disabled)
)

// Option is one setting.
type Option struct{ Key, Value string }

// optionMax bounds an option's value: a setting is a short word, so anything
// longer isn't one and is left out (no text reaches a content-free callback
// this way).
const optionMax = 64

// The APIs a call can be on (the Interceptor's Call.api): its wire format, by
// host and path.
const (
	APIAnthropicMessages = "anthropic_messages"      // api.anthropic.com /v1/messages
	APIClaudeAIWeb       = "claude_ai_web"           // claude.ai
	APIOpenAIResponses   = "openai_responses"        // api.openai.com /v1/responses
	APIOpenAIChat        = "openai_chat_completions" // api.openai.com /v1/chat/completions
	APIChatGPTBackend    = "chatgpt_backend"         // chatgpt.com /backend-api (Codex, ChatGPT web)
	APIGemini            = "gemini"                  // generativelanguage.googleapis.com
)

// The providers a call can go to (the Interceptor's Call.provider): the
// company that serves it, by host.
const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai"
	ProviderGoogle    = "google"
)

// bareHost is a host without a port, lowercased.
func bareHost(host string) string {
	host = strings.ToLower(host)
	if i := strings.LastIndexByte(host, ':'); i >= 0 && strings.Trim(host[i+1:], "0123456789") == "" && !strings.Contains(host[:i], ":") {
		host = host[:i]
	}
	return host
}

// APIOf is the API a call is on, by its host and path: one of the API
// constants, or "" for a call outside them (a control-plane endpoint).
func APIOf(host, path string) string {
	host = bareHost(host)
	p, _, _ := strings.Cut(path, "?")
	switch {
	case host == "api.anthropic.com" && strings.HasPrefix(p, "/v1/messages"):
		return APIAnthropicMessages
	case host == "claude.ai":
		return APIClaudeAIWeb
	case host == "api.openai.com" && strings.HasPrefix(p, "/v1/responses"):
		return APIOpenAIResponses
	case host == "api.openai.com" && strings.HasPrefix(p, "/v1/chat/completions"):
		return APIOpenAIChat
	case host == "chatgpt.com" && strings.HasPrefix(p, "/backend-api/"):
		return APIChatGPTBackend
	case host == "generativelanguage.googleapis.com":
		return APIGemini
	}
	return ""
}

// ProviderOf is the company a call goes to, by its host: one of the provider
// constants, or "" for a host that isn't one's.
func ProviderOf(host string) string {
	host = bareHost(host)
	under := func(domain string) bool { return host == domain || strings.HasSuffix(host, "."+domain) }
	switch {
	case under("anthropic.com"), under("claude.ai"):
		return ProviderAnthropic
	case under("openai.com"), under("chatgpt.com"):
		return ProviderOpenAI
	case under("googleapis.com"):
		return ProviderGoogle
	}
	return ""
}

// optionKeys is every option, in the order a list of them has.
var optionKeys = []string{OptionEffort, OptionSpeed, OptionServiceTier, OptionThinking}

// requestOptionPaths is where each API's request body sets each option.
var requestOptionPaths = map[string]map[string][]string{
	APIAnthropicMessages: {
		OptionEffort:      {"output_config", "effort"},
		OptionSpeed:       {"speed"},
		OptionServiceTier: {"service_tier"},
		OptionThinking:    {"thinking", "type"},
	},
	APIOpenAIResponses: {
		OptionEffort:      {"reasoning", "effort"},
		OptionServiceTier: {"service_tier"},
	},
	APIChatGPTBackend: { // Codex sends the Responses API's body, over HTTP or in a WebSocket's response.create
		OptionEffort:      {"reasoning", "effort"},
		OptionServiceTier: {"service_tier"},
	},
	APIOpenAIChat: {
		OptionEffort:      {"reasoning_effort"},
		OptionServiceTier: {"service_tier"},
	},
	APIGemini: {
		OptionEffort: {"generationConfig", "thinkingConfig", "thinkingLevel"},
	},
}

// OptionsFromRequest reads the options a request body on api asks for. body
// is plain JSON (a WebSocket's response.create too); anything else has none.
func OptionsFromRequest(api string, body []byte) []Option {
	paths := requestOptionPaths[api]
	if len(paths) == 0 || len(body) == 0 {
		return nil
	}
	var doc map[string]any
	if json.Unmarshal(body, &doc) != nil {
		return nil
	}
	var out []Option
	for _, k := range optionKeys {
		if p, ok := paths[k]; ok {
			if v := stringAt(doc, p); v != "" && len(v) <= optionMax {
				out = append(out, Option{k, v})
			}
		}
	}
	return out
}

// stringAt is the string at path in doc, or "".
func stringAt(doc map[string]any, path []string) string {
	var v any = doc
	for _, k := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return ""
		}
		v = m[k]
	}
	s, _ := v.(string)
	return s
}

// Options are the settings a response on api reports serving it with:
// Anthropic's usage.speed and usage.service_tier; the Responses API's
// service_tier and the reasoning effort it echoes; Chat Completions'
// service_tier.
func (m ResponseMeta) Options(api string) []Option {
	var have map[string]string
	switch api {
	case APIAnthropicMessages:
		have = map[string]string{OptionSpeed: m.Speed, OptionServiceTier: m.ServiceTier}
	case APIOpenAIResponses, APIChatGPTBackend:
		have = map[string]string{OptionEffort: m.Effort, OptionServiceTier: m.ServiceTier}
	case APIOpenAIChat:
		have = map[string]string{OptionServiceTier: m.ServiceTier}
	}
	var out []Option
	for _, k := range optionKeys {
		if v := have[k]; v != "" && len(v) <= optionMax {
			out = append(out, Option{k, v})
		}
	}
	return out
}
