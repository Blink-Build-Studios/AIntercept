package convo

import (
	"encoding/json"
	"strings"
)

// anthropic reads an Anthropic Messages request's messages. The system prompt
// and tools aren't messages; system messages in the conversation (Claude
// Code's notes: tools added, a file changed) are the harness's (RoleSystem).
// One Anthropic message can hold several of ours: its text, each tool call,
// each tool result, and each of the harness's notes in a user's turn.
// Anything it doesn't know is an opaque part.
func (b *builder) anthropic(top map[string]value) bool {
	items, ok := elements(top["messages"])
	if !ok {
		return false
	}
	for _, it := range items {
		m, ok := members(it)
		if !ok {
			b.item(false)
			b.add("", it.span, opaque(it))
			continue
		}
		role, _ := m["role"].str()
		switch role {
		case "user", "assistant":
			b.item(role == "assistant")
			b.anthropicContent(Role(role), m["content"])
		case "system":
			b.item(false)
			b.anthropicContent(RoleSystem, m["content"])
		default:
			b.item(false)
			b.add(Role(role), it.span, opaque(it))
		}
	}
	return true
}

// anthropicContent splits a message's content into messages: each tool call
// and tool result is its own, and the blocks between them are one message of
// the item's role (in a user's turn, each of the harness's notes is its own
// RoleSystem message). Each message's span is its run of blocks (or the
// string).
func (b *builder) anthropicContent(role Role, content value) {
	if content.kind() == '"' {
		s, _ := content.str()
		p, r := b.text(s, content.span), role
		if role == RoleUser {
			p, r = b.userText(s, content.span)
		}
		b.add(r, content.span, p)
		return
	}
	blocks, ok := elements(content)
	if !ok {
		if content.kind() != 0 && content.kind() != 'n' {
			b.add(role, content.span, opaque(content))
		}
		return
	}
	var run []Part
	var runSpan Span
	flush := func() {
		b.add(role, runSpan, run...)
		run = nil
	}
	appendRun := func(p Part, span Span) {
		if len(run) == 0 {
			runSpan.Start = span.Start
		}
		runSpan.End = span.End
		run = append(run, p)
	}
	for _, v := range blocks {
		blk, ok := members(v)
		if !ok {
			appendRun(opaque(v), v.span)
			continue
		}
		typ, _ := blk["type"].str()
		switch {
		case typ == "tool_use", typ == "server_tool_use", typ == "mcp_tool_use":
			var c struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			}
			if json.Unmarshal(v.raw, &c) != nil {
				appendRun(opaque(v), v.span)
				continue
			}
			flush()
			b.add(RoleAssistant, v.span, b.toolCall(c.ID, c.Name, string(blk["input"].raw), v.span))
		case strings.HasSuffix(typ, "tool_result"): // tool_result, and server tools' (web_search_tool_result, …)
			var c struct {
				ToolUseID string `json:"tool_use_id"`
				IsError   bool   `json:"is_error"`
			}
			if json.Unmarshal(v.raw, &c) != nil {
				appendRun(opaque(v), v.span)
				continue
			}
			flush()
			if typ == "tool_result" {
				b.add(RoleTool, v.span, b.toolResult(c.ToolUseID, c.IsError, blk["content"], v.span)...)
				continue
			}
			raw := string(blk["content"].raw) // a server tool's result is structured: its JSON
			b.add(RoleTool, v.span, b.fill(Part{Kind: KindToolResult, CallID: c.ToolUseID, Size: len(raw), Span: v.span, content: raw}))
		case typ == "tool_addition":
			appendRun(b.toolAdded(v, blk), v.span)
		default: // text, image, document; thinking, redacted_thinking and any block it doesn't know are opaque
			p, isText := b.contentPart(v)
			if isText && role == RoleUser {
				if q, r := b.userText(p.content, v.span); r == RoleSystem {
					flush()
					b.add(RoleSystem, v.span, q)
					continue
				} else {
					p = q
				}
			}
			appendRun(p, v.span)
		}
	}
	flush()
}

// toolAdded reads Claude Code's tool_addition block: a tool added to the
// catalog mid-session. One it can't read is opaque.
func (b *builder) toolAdded(v value, blk map[string]value) Part {
	var t struct {
		Type       string `json:"type"`
		Definition struct {
			Name string `json:"name"`
		} `json:"definition"`
	}
	if json.Unmarshal(blk["tool"].raw, &t) != nil || t.Type != "tool_definition" || t.Definition.Name == "" {
		return opaque(v)
	}
	return Part{Kind: KindToolAdded, Name: t.Definition.Name, Size: len(v.raw), Span: v.span}
}
