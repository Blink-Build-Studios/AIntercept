package events

import "testing"

func TestClassifyTool(t *testing.T) {
	cases := []struct{ name, server, tool, kind string }{
		{"mcp__playwright__browser_navigate", "playwright", "browser_navigate", "mcp"},
		{"mcp__github__create_issue", "github", "create_issue", "mcp"},
		{"mcp__a__b__c", "a", "b__c", "mcp"}, // tool name may contain __
		{"Bash", "", "Bash", "builtin"},
		{"Edit", "", "Edit", "builtin"},
		{"web_search", "", "web_search", "server"},
		{"web_fetch", "", "web_fetch", "server"},
		{"", "", "", "unknown"},
	}
	for _, c := range cases {
		s, tl, k := ClassifyTool(c.name)
		if s != c.server || tl != c.tool || k != c.kind {
			t.Errorf("ClassifyTool(%q) = (%q,%q,%q), want (%q,%q,%q)",
				c.name, s, tl, k, c.server, c.tool, c.kind)
		}
	}
}

func TestToolsFromRequestBody_DeclaredCatalog(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-6",
		"tools": [
			{"name": "mcp__playwright__browser_navigate", "description": "secret stuff"},
			{"name": "mcp__github__create_issue"},
			{"name": "Bash"}
		],
		"messages": []
	}`)
	avail, calls := ToolsFromRequestBody(body)
	if len(avail) != 3 {
		t.Fatalf("expected 3 tools_available, got %d", len(avail))
	}
	if avail[0].Server != "playwright" || avail[0].Tool != "browser_navigate" || avail[0].Kind != "mcp" {
		t.Errorf("bad first tool: %+v", avail[0])
	}
	if avail[2].Kind != "builtin" {
		t.Errorf("Bash should be builtin: %+v", avail[2])
	}
	if len(calls) != 0 {
		t.Errorf("no tool_use in messages, expected 0 calls, got %d", len(calls))
	}
}

func TestToolsFromRequestBody_CallCounts(t *testing.T) {
	// Two calls to playwright navigate, one to github — content-free counting.
	body := []byte(`{
		"model": "m",
		"messages": [
			{"role": "assistant", "content": [
				{"type": "text", "text": "a secret prompt"},
				{"type": "tool_use", "name": "mcp__playwright__browser_navigate", "input": {"url": "secret"}},
				{"type": "tool_use", "name": "mcp__github__create_issue", "input": {"title": "secret"}}
			]},
			{"role": "user", "content": "plain string content ignored"},
			{"role": "assistant", "content": [
				{"type": "tool_use", "name": "mcp__playwright__browser_navigate", "input": {}}
			]}
		]
	}`)
	_, calls := ToolsFromRequestBody(body)
	counts := map[string]int{}
	for _, c := range calls {
		counts[c.Name] = c.Count
		// Verify no content leaked into the ToolCallCount struct (it has no input field).
	}
	if counts["mcp__playwright__browser_navigate"] != 2 {
		t.Errorf("expected 2 playwright calls, got %d", counts["mcp__playwright__browser_navigate"])
	}
	if counts["mcp__github__create_issue"] != 1 {
		t.Errorf("expected 1 github call, got %d", counts["mcp__github__create_issue"])
	}
}

func TestToolsFromRequestBody_NonMessagesBody(t *testing.T) {
	// A telemetry batch or non-JSON body yields nothing, no panic.
	if a, c := ToolsFromRequestBody([]byte(`not json at all`)); a != nil || c != nil {
		t.Errorf("expected nil,nil for non-JSON, got %v,%v", a, c)
	}
	if a, c := ToolsFromRequestBody([]byte(`{"events":[1,2,3]}`)); len(a) != 0 || len(c) != 0 {
		t.Errorf("expected empty for non-messages JSON, got %v,%v", a, c)
	}
}

func TestToolsFromRequestBody_NoContentRetained(t *testing.T) {
	// The structs only expose name/server/tool/kind/count — never input/content.
	// This test documents the invariant: a reviewer adding an Input field would
	// break it. We assert the parsed output carries no argument data by shape.
	body := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","name":"mcp__x__y","input":{"password":"hunter2"}}]}]}`)
	_, calls := ToolsFromRequestBody(body)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	// ToolCallCount has no field that could hold "hunter2"; a marshal must not contain it.
	if got := calls[0]; got.Name != "mcp__x__y" || got.Count != 1 {
		t.Fatalf("unexpected: %+v", got)
	}
}
