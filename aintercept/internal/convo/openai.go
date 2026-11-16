package convo

import (
	"encoding/json"
	"strings"
)

// responses reads an OpenAI Responses request's input (Codex over HTTP or its
// WebSocket, the SDKs). instructions, developer and system messages, and tool
// catalogs (additional_tools) aren't messages. Each input item is one message,
// spanning the item.
func (b *builder) responses(top map[string]value) bool {
	input := top["input"]
	if input.kind() == '"' { // a plain prompt
		s, _ := input.str()
		b.add(RoleUser, input.span, b.text(s, input.span))
		return true
	}
	items, ok := elements(input)
	if !ok {
		return false
	}
	for _, v := range items {
		m, ok := members(v)
		if !ok {
			return false
		}
		var it struct {
			Type      string `json:"type"`
			Role      string `json:"role"`
			ID        string `json:"id"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"` // function_call, mcp_call: JSON text
		}
		if json.Unmarshal(v.raw, &it) != nil {
			return false
		}
		switch {
		case it.Type == "" || it.Type == "message":
			switch it.Role {
			case "developer", "system":
				b.item(false)
				continue
			case "user", "assistant":
			default:
				return false
			}
			b.item(it.Role == "assistant")
			parts, ok := b.content(m["content"])
			if !ok {
				return false
			}
			b.add(Role(it.Role), v.span, parts...)
		case it.Type == "reasoning":
			b.item(true)
			b.add(RoleAssistant, v.span, opaque(v))
		case it.Type == "function_call", it.Type == "mcp_call":
			b.item(true)
			b.add(RoleAssistant, v.span, b.toolCall(firstOf(it.CallID, it.ID), it.Name, it.Arguments, v.span))
		case it.Type == "custom_tool_call": // free-form input: its arguments are that input as a JSON string
			b.item(true)
			b.add(RoleAssistant, v.span, b.toolCall(it.CallID, it.Name, string(m["input"].raw), v.span))
		case strings.HasSuffix(it.Type, "_call_output"):
			b.item(false)
			b.add(RoleTool, v.span, b.toolResult(it.CallID, false, m["output"], v.span)...)
		case strings.HasSuffix(it.Type, "_call"): // a built-in tool (local_shell_call, web_search_call, …): the item is its arguments
			b.item(true)
			b.add(RoleAssistant, v.span, b.toolCall(firstOf(it.CallID, it.ID), firstOf(it.Name, strings.TrimSuffix(it.Type, "_call")), string(v.raw), v.span))
		case it.Type == "additional_tools":
			b.item(false)
		default: // an item it doesn't know (agent_message, item_reference, …): present, not inspectable
			b.item(it.Role == "assistant")
			role := RoleUser
			if it.Role == "assistant" {
				role = RoleAssistant
			}
			b.add(role, v.span, opaque(v))
		}
	}
	return true
}

// chat reads an OpenAI Chat Completions request's messages. System and
// developer messages aren't ours. An assistant message's text and each of its
// tool calls are separate messages: the text spans the message's content,
// each call its tool_calls element. A user message spans its content; a tool
// result, the whole message.
func (b *builder) chat(top map[string]value) bool {
	items, ok := elements(top["messages"])
	if !ok {
		return false
	}
	for _, v := range items {
		m, ok := members(v)
		if !ok {
			return false
		}
		var it struct {
			Role       string `json:"role"`
			ToolCallID string `json:"tool_call_id"`
			Name       string `json:"name"`
		}
		if json.Unmarshal(v.raw, &it) != nil {
			return false
		}
		switch it.Role {
		case "system", "developer":
			b.item(false)
		case "user", "assistant":
			b.item(it.Role == "assistant")
			if c := m["content"]; c.kind() != 0 && c.kind() != 'n' {
				parts, ok := b.content(c)
				if !ok {
					return false
				}
				b.add(Role(it.Role), c.span, parts...)
			}
			if it.Role == "assistant" && !b.chatToolCalls(m["tool_calls"]) {
				return false
			}
		case "tool", "function": // function: the deprecated form, named by the function
			b.item(false)
			c := m["content"]
			if k := c.kind(); k != '"' && k != '[' && k != 'n' && k != 0 {
				return false
			}
			b.add(RoleTool, v.span, b.toolResult(firstOf(it.ToolCallID, it.Name), false, c, v.span)...)
		default:
			return false
		}
	}
	return true
}

func (b *builder) chatToolCalls(v value) bool {
	if v.kind() == 0 || v.kind() == 'n' {
		return true
	}
	calls, ok := elements(v)
	if !ok {
		return false
	}
	for _, c := range calls {
		var tc struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Function *struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		}
		if c.kind() != '{' || json.Unmarshal(c.raw, &tc) != nil {
			return false
		}
		if tc.Function != nil {
			b.add(RoleAssistant, c.span, b.toolCall(tc.ID, tc.Function.Name, tc.Function.Arguments, c.span))
		} else { // a custom tool call: the element is its arguments
			b.add(RoleAssistant, c.span, b.toolCall(tc.ID, tc.Type, string(c.raw), c.span))
		}
	}
	return true
}

func firstOf(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
