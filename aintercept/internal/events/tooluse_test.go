package events

import (
	"bytes"
	"sort"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
)

func toolUseSummary(uses []ToolCallCount) string {
	var parts []string
	for _, u := range uses {
		parts = append(parts, u.Kind+":"+u.Server+":"+u.Tool+"="+string(rune('0'+u.Count)))
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

func TestToolUses_AnthropicSSE(t *testing.T) {
	body := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"model":"claude-opus-5-5","usage":{"input_tokens":3}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"mcp__claude_ai_RCP__whoami","input":{}}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"purpose\":\"secret prompt text\"}"}}`,
		`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_2","name":"Bash","input":{}}}`,
		`data: {"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"toolu_3","name":"Bash","input":{}}}`,
		`data: {"type":"content_block_start","index":4,"content_block":{"type":"mcp_tool_use","id":"mcptoolu_1","name":"search","server_name":"docs"}}`,
		`data: {"type":"content_block_start","index":5,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search"}}`,
		`data: {"type":"message_delta","usage":{"output_tokens":40}}`,
	}, "\n")
	got := toolUseSummary(ToolUsesFromResponseBody([]byte(body), ""))
	want := "builtin::Bash=2 mcp:claude_ai_RCP:whoami=1 mcp:docs:search=1 server::web_search=1"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if strings.Contains(got, "secret") {
		t.Fatal("tool arguments must never be read")
	}
}

func TestToolUses_AnthropicJSONBrotli(t *testing.T) {
	var buf bytes.Buffer
	w := brotli.NewWriter(&buf)
	_, _ = w.Write([]byte(`{"type":"message","content":[{"type":"text","text":"hi"},{"type":"tool_use","name":"Read","input":{"file_path":"/x"}}]}`))
	_ = w.Close()
	if got := toolUseSummary(ToolUsesFromResponseBody(buf.Bytes(), "br")); got != "builtin::Read=1" {
		t.Fatalf("got %s", got)
	}
}

func TestToolUses_OpenAIChatStream(t *testing.T) {
	// The name arrives only on a call's first delta; later deltas stream args.
	body := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"get_weather","arguments":""}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"c2","function":{"name":"get_weather","arguments":""}}]}}]}`,
		`data: [DONE]`,
	}, "\n")
	if got := toolUseSummary(ToolUsesFromResponseBody([]byte(body), "")); got != "builtin::get_weather=2" {
		t.Fatalf("got %s", got)
	}
}

func TestToolUses_OpenAIResponsesStreamNotDoubleCounted(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"response.output_item.added","item":{"type":"function_call","name":"shell"}}`,
		`data: {"type":"response.output_item.added","item":{"type":"mcp_call","name":"list_issues","server_label":"github"}}`,
		`data: {"type":"response.output_item.done","item":{"type":"function_call","name":"shell"}}`,
		`data: {"type":"response.completed","response":{"output":[{"type":"function_call","name":"shell"},{"type":"mcp_call","name":"list_issues","server_label":"github"}],"usage":{"input_tokens":1}}}`,
	}, "\n")
	if got := toolUseSummary(ToolUsesFromResponseBody([]byte(body), "")); got != "builtin::shell=1 mcp:github:list_issues=1" {
		t.Fatalf("got %s", got)
	}
}

func TestToolUses_OpenAIResponsesJSONAndGemini(t *testing.T) {
	if got := toolUseSummary(ToolUsesFromResponseBody([]byte(`{"output":[{"type":"message"},{"type":"function_call","name":"shell"}]}`), "")); got != "builtin::shell=1" {
		t.Fatalf("responses json: got %s", got)
	}
	if got := toolUseSummary(ToolUsesFromResponseBody([]byte(`{"candidates":[{"content":{"parts":[{"text":"x"},{"functionCall":{"name":"lookup","args":{}}}]}}]}`), "")); got != "builtin::lookup=1" {
		t.Fatalf("gemini: got %s", got)
	}
}

func TestToolUses_NoneOrGarbage(t *testing.T) {
	for _, b := range []string{"", "not json", `{"type":"message","content":[{"type":"text","text":"hi"}]}`, "data: {\"broken"} {
		if got := ToolUsesFromResponseBody([]byte(b), ""); got != nil {
			t.Fatalf("%q: want nil, got %v", b, got)
		}
	}
}
