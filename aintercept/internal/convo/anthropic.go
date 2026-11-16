package convo

import (
	"encoding/json"
	"strings"
)

// anthropic reads an Anthropic Messages request's messages. system and tools
// aren't messages. One Anthropic message can hold several of ours: its text,
// each tool call, each tool result.
func (b *builder) anthropic(top map[string]value) bool {
	items, ok := elements(top["messages"])
	if !ok {
		return false
	}
	for _, it := range items {
		m, ok := members(it)
		if !ok {
			return false
		}
		role, _ := m["role"].str()
		if role != "user" && role != "assistant" {
			return false
		}
		b.item(role == "assistant")
		if !b.anthropicContent(Role(role), m["content"]) {
			return false
		}
	}
	return true
}

// anthropicContent splits a message's content into messages: each tool call
// and tool result is its own, and the blocks between them are one message of
// the item's role. Each message's span is its run of blocks (or the string).
func (b *builder) anthropicContent(role Role, content value) bool {
	if content.kind() == '"' {
		s, _ := content.str()
		b.add(role, content.span, b.text(s, content.span))
		return true
	}
	blocks, ok := elements(content)
	if !ok {
		return false
	}
	var run []Part
	var runSpan Span
	flush := func() {
		b.add(role, runSpan, run...)
		run = nil
	}
	for _, v := range blocks {
		blk, ok := members(v)
		if !ok {
			return false
		}
		typ, _ := blk["type"].str()
		switch {
		case typ == "tool_use", typ == "server_tool_use", typ == "mcp_tool_use":
			flush()
			var c struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			}
			if json.Unmarshal(v.raw, &c) != nil {
				return false
			}
			b.add(RoleAssistant, v.span, b.toolCall(c.ID, c.Name, string(blk["input"].raw), v.span))
		case strings.HasSuffix(typ, "tool_result"): // tool_result, and server tools' (web_search_tool_result, …)
			flush()
			var c struct {
				ToolUseID string `json:"tool_use_id"`
				IsError   bool   `json:"is_error"`
			}
			if json.Unmarshal(v.raw, &c) != nil {
				return false
			}
			if typ == "tool_result" {
				b.add(RoleTool, v.span, b.toolResult(c.ToolUseID, c.IsError, blk["content"], v.span)...)
				continue
			}
			raw := string(blk["content"].raw) // a server tool's result is structured: its JSON
			b.add(RoleTool, v.span, b.fill(Part{Kind: KindToolResult, CallID: c.ToolUseID, Size: len(raw), Span: v.span, content: raw}))
		default: // text, image, document; thinking and redacted_thinking are opaque
			p, _ := b.contentPart(v)
			if len(run) == 0 {
				runSpan.Start = v.span.Start
			}
			runSpan.End = v.span.End
			run = append(run, p)
		}
	}
	flush()
	return true
}
