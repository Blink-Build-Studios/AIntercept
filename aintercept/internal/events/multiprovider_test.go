package events

import "testing"

// ── OpenAI ────────────────────────────────────────────────────────────────────

func TestUsage_OpenAI_ChatCompletions(t *testing.T) {
	// Non-streamed OpenAI response.
	body := `{"id":"x","model":"gpt-4o","usage":{"prompt_tokens":200,"completion_tokens":80,` +
		`"prompt_tokens_details":{"cached_tokens":64}}}`
	u, ok := UsageFromResponseBody([]byte(body), "")
	if !ok {
		t.Fatal("expected usage")
	}
	if u.InputTokens != 200 || u.OutputTokens != 80 || u.CacheReadTokens != 64 {
		t.Fatalf("bad openai usage: %+v", u)
	}
}

func TestTools_OpenAI_FunctionShape(t *testing.T) {
	// OpenAI: declared tools under tools[].function.name; calls under
	// messages[].tool_calls[].function.name.
	body := []byte(`{
		"model": "gpt-4o",
		"tools": [
			{"type":"function","function":{"name":"get_weather","description":"secret"}},
			{"type":"function","function":{"name":"mcp__github__create_issue"}}
		],
		"messages": [
			{"role":"assistant","tool_calls":[
				{"id":"c1","function":{"name":"get_weather","arguments":"{\"city\":\"secret\"}"}},
				{"id":"c2","function":{"name":"get_weather","arguments":"{}"}}
			]}
		]
	}`)
	avail, calls := ToolsFromRequestBody(body)
	if len(avail) != 2 {
		t.Fatalf("expected 2 declared tools, got %d (%+v)", len(avail), avail)
	}
	// The mcp__-prefixed one still decomposes to server=github.
	var sawGithub bool
	for _, a := range avail {
		if a.Server == "github" && a.Tool == "create_issue" {
			sawGithub = true
		}
	}
	if !sawGithub {
		t.Fatalf("mcp tool not decomposed: %+v", avail)
	}
	var weatherCalls int
	for _, c := range calls {
		if c.Name == "get_weather" {
			weatherCalls = c.Count
		}
	}
	if weatherCalls != 2 {
		t.Fatalf("expected 2 get_weather calls, got %d", weatherCalls)
	}
}

// ── Gemini ────────────────────────────────────────────────────────────────────

func TestUsage_Gemini_UsageMetadata(t *testing.T) {
	body := `{"candidates":[],"usageMetadata":{"promptTokenCount":512,` +
		`"candidatesTokenCount":128,"cachedContentTokenCount":256}}`
	u, ok := UsageFromResponseBody([]byte(body), "")
	if !ok {
		t.Fatal("expected gemini usage")
	}
	if u.InputTokens != 512 || u.OutputTokens != 128 || u.CacheReadTokens != 256 {
		t.Fatalf("bad gemini usage: %+v", u)
	}
}

func TestTools_Gemini_FunctionDeclarations(t *testing.T) {
	// Gemini: declared under tools[].functionDeclarations[].name; calls under
	// contents[].parts[].functionCall.name.
	body := []byte(`{
		"tools": [{"functionDeclarations":[
			{"name":"lookup_order"},{"name":"mcp__stripe__create_charge"}
		]}],
		"contents": [
			{"role":"model","parts":[{"functionCall":{"name":"lookup_order","args":{"id":"secret"}}}]},
			{"role":"model","parts":[{"functionCall":{"name":"lookup_order"}}]}
		]
	}`)
	avail, calls := ToolsFromRequestBody(body)
	if len(avail) != 2 {
		t.Fatalf("expected 2 declared tools, got %d (%+v)", len(avail), avail)
	}
	var lookups int
	for _, c := range calls {
		if c.Name == "lookup_order" {
			lookups = c.Count
		}
	}
	if lookups != 2 {
		t.Fatalf("expected 2 lookup_order calls, got %d", lookups)
	}
}

func TestModelFromPath_Gemini(t *testing.T) {
	cases := []struct{ path, want string }{
		{"/v1beta/models/gemini-2.5-pro:generateContent", "gemini-2.5-pro"},
		{"/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse", "gemini-2.5-flash"},
		{"/v1/messages", ""},
		{"/v1/chat/completions", ""},
	}
	for _, c := range cases {
		if got := ModelFromPath(c.path); got != c.want {
			t.Errorf("ModelFromPath(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

// Anthropic must still work unchanged after the multi-provider refactor.
func TestTools_Anthropic_StillWorks(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-8","tools":[{"name":"mcp__playwright__browser_navigate"}],` +
		`"messages":[{"role":"assistant","content":[{"type":"tool_use","name":"mcp__playwright__browser_navigate"}]}]}`)
	avail, calls := ToolsFromRequestBody(body)
	if len(avail) != 1 || avail[0].Server != "playwright" {
		t.Fatalf("anthropic avail regressed: %+v", avail)
	}
	if len(calls) != 1 || calls[0].Count != 1 {
		t.Fatalf("anthropic calls regressed: %+v", calls)
	}
}

// Real OpenAI Responses API shape (usage nested under `response` in the
// response.completed SSE event), captured live from the ChatGPT backend.
func TestUsage_OpenAI_ResponsesAPI(t *testing.T) {
	sse := "event: response.completed\n" +
		`data: {"type":"response.completed","response":{"model":"gpt-5.6-sol",` +
		`"usage":{"input_tokens":13,"input_tokens_details":{"cached_tokens":4},` +
		`"output_tokens":6,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":19}}}` + "\n\n"
	u, ok := UsageFromResponseBody([]byte(sse), "")
	if !ok {
		t.Fatal("expected usage from Responses API")
	}
	if u.InputTokens != 13 || u.OutputTokens != 6 || u.CacheReadTokens != 4 {
		t.Fatalf("bad Responses API usage: %+v", u)
	}
}

// ModelFromPath must NOT misfire on OpenAI model-management paths that merely
// contain "/models/" (review finding #2).
func TestModelFromPath_IgnoresNonGeminiModelsPaths(t *testing.T) {
	for _, p := range []string{
		"/v1/models/gpt-4o", // OpenAI retrieve
		"/v1/models",        // OpenAI list
		"/v1/fine_tuning/models/ft:gpt-4o-mini:org", // fine-tune
		"/v1beta/models/gemini-2.5-pro",             // Gemini list-ish, no :action
	} {
		if got := ModelFromPath(p); got != "" {
			t.Errorf("ModelFromPath(%q) = %q, want \"\" (not a Gemini generate path)", p, got)
		}
	}
	// The real Gemini generate shape still works.
	if got := ModelFromPath("/v1beta/models/gemini-2.5-pro:generateContent"); got != "gemini-2.5-pro" {
		t.Errorf("Gemini generate path broke: %q", got)
	}
}

// A hybrid/malformed tool entry carrying more than one provider's name shape is
// counted once, not multiple times (review finding #3).
func TestTools_NoDoubleCountHybridEntry(t *testing.T) {
	body := []byte(`{"tools":[{"name":"topname","function":{"name":"funcname"}}]}`)
	avail, _ := ToolsFromRequestBody(body)
	if len(avail) != 1 {
		t.Fatalf("expected 1 tool (no double-count), got %d: %+v", len(avail), avail)
	}
}
