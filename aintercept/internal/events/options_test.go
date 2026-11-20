package events

import (
	"fmt"
	"strings"
	"testing"
)

// The capture corpus proves each option on each API real traffic carries
// (internal/extproc TestCorpus); KNOWN_GAPS lists the ones no capture has.
// These pin where each API's body puts each option, so a mapping no capture
// proves yet still reads the documented field (docs/INTERCEPTOR.md, Options).
func TestOptionsFromRequest(t *testing.T) {
	for _, tc := range []struct {
		api, body, want string
	}{
		{APIAnthropicMessages, `{"speed":"fast","output_config":{"effort":"high"},"thinking":{"type":"adaptive"},"service_tier":"auto"}`,
			"[{effort high} {speed fast} {service_tier auto} {thinking adaptive}]"},
		{APIAnthropicMessages, `{"output_config":{"effort":"high"}}`, "[{effort high}]"}, // standard speed: no speed
		{APIOpenAIResponses, `{"reasoning":{"effort":"low"},"service_tier":"priority"}`, "[{effort low} {service_tier priority}]"},
		{APIChatGPTBackend, `{"type":"response.create","reasoning":{"effort":"xhigh"},"service_tier":"flex"}`, "[{effort xhigh} {service_tier flex}]"},
		{APIOpenAIChat, `{"reasoning_effort":"medium","service_tier":"default"}`, "[{effort medium} {service_tier default}]"},
		{APIGemini, `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"low"}}}`, "[{effort low}]"},
		{APIOpenAIChat, `{"reasoning":{"effort":"low"},"speed":"fast"}`, "[]"}, // not where Chat Completions puts them
		{"", `{"speed":"fast"}`, "[]"},                                              // a control-plane call has none
		{APIAnthropicMessages, `{"speed":{"mode":"fast"}}`, "[]"},                   // not a string
		{APIAnthropicMessages, `{"speed":"` + strings.Repeat("f", 65) + `"}`, "[]"}, // too long to be a setting
	} {
		got := fmt.Sprint(OptionsFromRequest(tc.api, []byte(tc.body)))
		if tc.want == "[]" && got == "[]" || got == tc.want {
			continue
		}
		t.Errorf("%s %s: %s, want %s", tc.api, tc.body, got, tc.want)
	}
}

func TestResponseOptions(t *testing.T) {
	m := ResponseMeta{Speed: "fast", ServiceTier: "priority", Effort: "high"}
	for api, want := range map[string]string{
		APIAnthropicMessages: "[{speed fast} {service_tier priority}]",
		APIOpenAIResponses:   "[{effort high} {service_tier priority}]",
		APIChatGPTBackend:    "[{effort high} {service_tier priority}]",
		APIOpenAIChat:        "[{service_tier priority}]",
		APIGemini:            "[]",
	} {
		if got := fmt.Sprint(m.Options(api)); got != want {
			t.Errorf("%s: %s, want %s", api, got, want)
		}
	}
}

func TestAPIAndProvider(t *testing.T) {
	for _, tc := range []struct{ host, path, api, provider string }{
		{"api.anthropic.com", "/v1/messages?beta=true", APIAnthropicMessages, ProviderAnthropic},
		{"api.anthropic.com:443", "/v1/messages/count_tokens", APIAnthropicMessages, ProviderAnthropic},
		{"api.anthropic.com", "/api/oauth/profile", "", ProviderAnthropic},
		{"claude.ai", "/api/organizations", APIClaudeAIWeb, ProviderAnthropic},
		{"api.openai.com", "/v1/responses", APIOpenAIResponses, ProviderOpenAI},
		{"api.openai.com", "/v1/chat/completions", APIOpenAIChat, ProviderOpenAI},
		{"chatgpt.com", "/backend-api/codex/responses", APIChatGPTBackend, ProviderOpenAI},
		{"ab.chatgpt.com", "/otlp/v1/metrics", "", ProviderOpenAI},
		{"generativelanguage.googleapis.com", "/v1beta/models/g:generateContent", APIGemini, ProviderGoogle},
		{"example.com", "/v1/messages", "", ""},
		{"notanthropic.com", "/", "", ""},
	} {
		if api, p := APIOf(tc.host, tc.path), ProviderOf(tc.host); api != tc.api || p != tc.provider {
			t.Errorf("%s%s: %q %q, want %q %q", tc.host, tc.path, api, p, tc.api, tc.provider)
		}
	}
}

// An option of another JSON type reads as unset, and doesn't cost the rest of
// the request's metadata.
func TestOptionOfAnotherTypeIsUnset(t *testing.T) {
	m := RequestMetaFromBody([]byte(`{"speed":{"mode":"fast"},"service_tier":7,"output_config":{"effort":"high"},"stream":true,"max_tokens":9}`))
	if got := fmt.Sprint(m.Options(APIAnthropicMessages)); got != "[{effort high}]" || !m.Stream || m.MaxTokens != 9 {
		t.Fatalf("%s, stream %v, max_tokens %d", got, m.Stream, m.MaxTokens)
	}
}
