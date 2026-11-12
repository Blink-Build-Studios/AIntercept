package canon

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// Atomic cases for the canonical mapping: each input in each situation it
// arrives in, and exactly what it becomes.

func TestIsModelCall(t *testing.T) {
	cc := func(path string) *Request {
		return &Request{Product: ProductClaudeCode, SessionID: "s", Host: "api.anthropic.com", Path: path}
	}
	cases := []struct {
		name string
		r    *Request
		want bool
	}{
		{"claude code messages", cc("/v1/messages?beta=true"), true},
		{"count_tokens isn't one", cc("/v1/messages/count_tokens"), false},
		{"another path", cc("/api/oauth/profile"), false},
		{"synthetic", &Request{Synthetic: true, Product: ProductClaudeCode, SessionID: "s", Host: "api.anthropic.com", Path: "/v1/messages"}, false},
		{"no session", &Request{Product: ProductClaudeCode, Host: "api.anthropic.com", Path: "/v1/messages"}, false},
		{"codex websocket turn", &Request{Product: ProductCodex, SessionID: "t", Host: "chatgpt.com", Path: "/backend-api/codex/responses", WSTurn: 1}, true},
		{"codex http POST", &Request{Product: ProductCodex, SessionID: "t", Host: "chatgpt.com", Path: "/backend-api/codex/responses", Method: "POST"}, true},
		{"codex GET (the upgrade)", &Request{Product: ProductCodex, SessionID: "t", Host: "chatgpt.com", Path: "/backend-api/codex/responses", Method: "GET"}, false},
		{"another product", &Request{Product: "web", SessionID: "x", Host: "claude.ai"}, false},
	}
	for _, c := range cases {
		if got := IsModelCall(c.r); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
}

func TestApplyCodex(t *testing.T) {
	cases := []struct {
		name string
		e    events.Event
		want Request
	}{
		{"no metadata: nothing", events.Event{}, Request{}},
		{"thread and turn", events.Event{ClientMetadata: map[string]any{"thread_id": "t1", "session_id": "s1", "turn_id": "u1", "installation_id": "i1"}},
			Request{SessionID: "t1", PromptID: "u1", DeviceID: "i1"}},
		{"session when no thread", events.Event{ClientMetadata: map[string]any{"session_id": "s1"}}, Request{SessionID: "s1"}},
		{"prewarm (flag)", events.Event{Prewarm: true, ClientMetadata: map[string]any{"thread_id": "t1", "turn_id": "u1"}},
			Request{SessionID: "t1", HiddenKind: "prewarm"}},
		{"prewarm (request kind)", events.Event{ClientMetadata: map[string]any{"thread_id": "t1", "turn_id": "u1", "request_kind": "prewarm"}},
			Request{SessionID: "t1", HiddenKind: "prewarm", ExtJSON: `{"request_kind":"prewarm"}`}},
		{"title", events.Event{ClientMetadata: map[string]any{"thread_id": "t1", "turn_id": "u1", "thread_source": "thread_title"}},
			Request{SessionID: "t1", HiddenKind: "title", ExtJSON: `{"thread_source":"thread_title"}`}},
		{"ext keeps the known keys, set ones only", events.Event{ClientMetadata: map[string]any{"thread_id": "t1", "sandbox": "x",
			"agent_name": "", "parent_thread_id": nil, "unknown_key": "y"}}, Request{SessionID: "t1", ExtJSON: `{"sandbox":"x"}`}},
	}
	for _, c := range cases {
		var r Request
		ApplyCodex(&r, &c.e)
		if r != c.want {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, r, c.want)
		}
	}
}

func TestEventFrom(t *testing.T) {
	row := func(ce events.ClientEvent) (*events.Event, []byte) {
		e := &events.Event{CorrelationID: "a1", TimestampNS: 100, Seq: 4, ClientEvent: &ce, AccountUUID: "row-acct"}
		b, _ := json.Marshal(e)
		return e, b
	}
	e, l := row(events.ClientEvent{Source: "claude_code", Name: "n", ID: "e1", Timestamp: "2026-11-12T10:00:00Z",
		Data: map[string]any{"additional_metadata": map[string]any{"b": 1.0, "a": "x", "cc_prompt_id": "p", "prompt_index": "3",
			"requestId": "r", "toolUseID": "tu", "permissionMode": "default"}, "env": map[string]any{"platform": "darwin"}, "head_sha": "abc"}})
	ev := EventFrom(e, l, "f")
	if ev.EventKey != "a1/e1" || ev.NS == ev.RowNS || ev.PromptID != "p" || ev.PromptIndex != 3 || ev.RequestID != "r" ||
		ev.ToolUseID != "tu" || ev.PermissionMode != "default" || ev.HeadSHA != "abc" || ev.SourceFile != "f" ||
		!strings.Contains(ev.MetadataJSON, `"cc_prompt_id"`) || ev.EnvJSON != `{"platform":"darwin"}` || ev.PersonID != "" {
		t.Fatalf("claude code event: %+v", ev)
	}

	e, l = row(events.ClientEvent{Source: "claude_code", Name: "n", Timestamp: "not a time", Data: map[string]any{"additional_metadata": nil}})
	ev = EventFrom(e, l, "f")
	if !strings.HasPrefix(ev.EventKey, "a1/h") || ev.NS != 100 || ev.MetadataJSON != "" || ev.EnvJSON != "" {
		t.Fatalf("unnumbered, no clock, null metadata: %+v", ev)
	}

	e, l = row(events.ClientEvent{Source: "codex", Name: "codex_turn_event", ID: "c1",
		Data: map[string]any{"turn_id": "u1", "status": "completed", "tool_use_id": "tu2"}})
	ev = EventFrom(e, l, "f")
	if ev.PromptID != "u1" || ev.MetadataJSON != `{"status":"completed","tool_use_id":"tu2","turn_id":"u1"}` || ev.PersonID != "row-acct" {
		t.Fatalf("codex event: %+v", ev)
	}
}

func TestRequestFromKeepsToolUsesAndMergesProvider(t *testing.T) {
	e := &events.Event{CorrelationID: "c", Phase: events.PhaseComplete, ToolUses: []events.ToolCallCount{{Name: "Bash", Count: 2}},
		Provider: map[string]string{"b": "complete"}}
	r := RequestFrom(e, &Pending{CorrelationID: "c", ProviderJSON: `{"a":"headers","b":"headers"}`}, "f")
	if r.ToolUsesJSON == "" || r.ProviderJSON != `{"a":"headers","b":"complete"}` {
		t.Fatalf("tool uses %q provider %q", r.ToolUsesJSON, r.ProviderJSON)
	}
}

func TestValueConversions(t *testing.T) {
	for v, want := range map[any]string{"s": "s", true: "true", 1.5: "1.5", 3: ""} {
		if got := str(v); got != want {
			t.Errorf("str(%v) = %q", v, got)
		}
	}
	for v, want := range map[any]int64{2.9: 2, "7": 7, "x": 0, true: 0} {
		if got := num(v); got != want {
			t.Errorf("num(%v) = %d", v, got)
		}
	}
	if Marshal(map[string]string{}) != "" || Marshal(make(chan int)) != "" || Marshal([]int{1}) != "[1]" {
		t.Fatal("Marshal: empty map and unencodable values are empty")
	}
}
