package events

import (
	"strings"
	"testing"
)

func TestRequestMeta_AnthropicThreaded(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5-5","max_tokens":64000,"stream":true,
		"output_config":{"effort":"high"},"thinking":{"type":"adaptive","display":"summary"},
		"thread":{"type":"continue","previous_message_id":"msg_01ABC"},
		"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"secret output"}]}]}`)
	m := RequestMetaFromBody(body)
	if !m.Stream || m.MaxTokens != 64000 || m.Effort != "high" || m.ThinkingType != "adaptive" ||
		m.ThreadType != "continue" || m.PreviousMessageID != "msg_01ABC" {
		t.Fatalf("got %+v", m)
	}
}

func TestRequestMeta_OldShapeAndOpenAI(t *testing.T) {
	// The pre-thread format still parses (no thread fields, nothing breaks).
	if m := RequestMetaFromBody([]byte(`{"model":"m","max_tokens":10,"messages":[]}`)); m.MaxTokens != 10 || m.ThreadType != "" {
		t.Fatalf("old anthropic shape: %+v", m)
	}
	m := RequestMetaFromBody([]byte(`{"model":"gpt-5","max_output_tokens":99,"reasoning":{"effort":"low"},"previous_response_id":"resp_1","input":[]}`))
	if m.MaxTokens != 99 || m.Effort != "low" || m.PreviousMessageID != "resp_1" {
		t.Fatalf("openai responses: %+v", m)
	}
	if m := RequestMetaFromBody([]byte("not json")); m != (RequestMeta{}) {
		t.Fatalf("garbage: %+v", m)
	}
}

func TestResponseMeta_AnthropicSSE(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_01XYZ","model":"claude-opus-5-5","usage":{"input_tokens":2,"service_tier":"standard","inference_geo":"us","speed":"standard"}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_9","name":"Bash"}}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":12}}`,
	}, "\n")
	m := ResponseMetaFromBody([]byte(body), "")
	want := ResponseMeta{Model: "claude-opus-5-5", MessageID: "msg_01XYZ", StopReason: "tool_use", ServiceTier: "standard", InferenceGeo: "us", Speed: "standard"}
	if m != want {
		t.Fatalf("got %+v\nwant %+v", m, want)
	}
}

func TestResponseMeta_JSONAndOpenAI(t *testing.T) {
	if m := ResponseMetaFromBody([]byte(`{"id":"msg_1","type":"message","stop_reason":"end_turn","usage":{"service_tier":"priority"}}`), ""); m.MessageID != "msg_1" || m.StopReason != "end_turn" || m.ServiceTier != "priority" {
		t.Fatalf("anthropic json: %+v", m)
	}
	if m := ResponseMetaFromBody([]byte(`data: {"id":"chatcmpl-1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`), ""); m.MessageID != "chatcmpl-1" || m.StopReason != "tool_calls" {
		t.Fatalf("openai chat: %+v", m)
	}
	if m := ResponseMetaFromBody([]byte(`data: {"type":"response.created","response":{"id":"resp_7","status":"in_progress"}}`), ""); m.MessageID != "resp_7" {
		t.Fatalf("openai responses: %+v", m)
	}
}

func TestPickHeaders_NeverReturnsSecrets(t *testing.T) {
	hdrs := RedactHeaders(map[string]string{
		"User-Agent":               "claude-cli/2.1.308 (external, cli)",
		"x-claude-code-session-id": "sess-1",
		"authorization":            "Bearer sk-ant-secret",
		"x-api-key":                "sk-ant-secret",
		"cookie":                   "a=b",
		"x-unlisted":               "v",
	})
	got := pickHeaders(hdrs, append(clientHeaders, "authorization", "x-api-key", "cookie"))
	if got["user-agent"] != "claude-cli/2.1.308 (external, cli)" || got["x-claude-code-session-id"] != "sess-1" {
		t.Fatalf("missing allowlisted headers: %v", got)
	}
	for k, v := range got {
		if strings.Contains(v, "secret") || k == "authorization" || k == "x-api-key" || k == "cookie" || k == "x-unlisted" {
			t.Fatalf("leaked %s=%s", k, v)
		}
	}
}

func TestExtractDeferred_FillsMetaAndClient(t *testing.T) {
	e := Event{Phase: PhaseComplete}
	e.AttachBodies([]byte(`{"model":"claude-opus-5-5","thread":{"type":"continue","previous_message_id":"msg_prev"},"messages":[]}`),
		[]byte(`data: {"type":"message_start","message":{"id":"msg_next","usage":{"input_tokens":1}}}`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`),
		"", map[string]string{"anthropic-workspace-id": "wrkspc_1"})
	e.AttachRequestHeaders(map[string]string{"x-claude-code-agent-type": "Explore"})
	e.ExtractDeferred()
	if e.PreviousMessageID != "msg_prev" || e.MessageID != "msg_next" || e.StopReason != "end_turn" ||
		e.Client["x-claude-code-agent-type"] != "Explore" || e.Provider["anthropic-workspace-id"] != "wrkspc_1" {
		t.Fatalf("got %+v", e)
	}
}
