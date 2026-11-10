package events

import (
	"encoding/json"
	"regexp"
	"strings"
)

// ToolUsesFromResponseBody returns the tools the model invoked in this one
// response, with counts. It reads the response, not the request, so it works
// however the request carries the conversation: in full (older clients), or
// only the newest turn with the history kept server-side (Claude Code with
// message threads), where request-history counting (ToolsFromRequestBody)
// sees nothing. It also counts each call once, where history counting
// re-counts every earlier call on every turn. Content and arguments are never
// read; only tool names and MCP server names.
//
// Understood shapes, streamed (SSE) or plain JSON:
//   - Anthropic: content blocks of type tool_use, server_tool_use, and
//     mcp_tool_use (whose server is server_name).
//   - OpenAI Chat Completions: tool_calls[].function.name.
//   - OpenAI Responses (incl. Codex): output items of type function_call,
//     custom_tool_call, and mcp_call (whose server is server_label); plus the
//     tools a Codex exec script calls (tools.<name>(…)), e.g. MCP tools.
//   - Gemini: parts[].functionCall.name.
func ToolUsesFromResponseBody(body []byte, encoding string) []ToolCallCount {
	raw, err := decodeBody(body, encoding)
	if err != nil || len(raw) == 0 {
		return nil
	}
	c := toolUseCounter{}
	text := string(raw)
	if strings.Contains(text, "data:") {
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			c.fromJSON([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))))
		}
	} else {
		c.fromJSON(raw)
	}
	return c.result()
}

type toolUseKey struct{ name, server string }

type toolUseCounter map[toolUseKey]int

// toolUseBlock is the name-bearing part of an Anthropic content block or an
// OpenAI Responses output item.
type toolUseBlock struct {
	Type string `json:"type"`
	Name string `json:"name"`
	// Input is a custom_tool_call script (a JSON string, Codex exec). An Anthropic
	// tool_use input is an object: raw, so it can't fail the decode.
	Input       json.RawMessage `json:"input"`
	ServerName  string          `json:"server_name"`  // Anthropic mcp_tool_use
	ServerLabel string          `json:"server_label"` // OpenAI Responses mcp_call
}

type openAIToolCall struct {
	Index    *int `json:"index"`
	Function struct {
		Name string `json:"name"`
	} `json:"function"`
}

type geminiCandidate struct {
	Content struct {
		Parts []struct {
			FunctionCall *struct {
				Name string `json:"name"`
			} `json:"functionCall"`
		} `json:"parts"`
	} `json:"content"`
}

// fromJSON folds one JSON object (an SSE data frame, or a whole response) in.
func (c toolUseCounter) fromJSON(b []byte) {
	var obj struct {
		Type         string         `json:"type"`
		ContentBlock *toolUseBlock  `json:"content_block"` // Anthropic SSE content_block_start
		Content      []toolUseBlock `json:"content"`       // Anthropic JSON
		Item         *toolUseBlock  `json:"item"`          // OpenAI Responses SSE output_item.added
		Output       []toolUseBlock `json:"output"`        // OpenAI Responses JSON
		Response     *struct {      // OpenAI Responses SSE response.completed
			Output []toolUseBlock `json:"output"`
		} `json:"response"`
		Choices []struct { // OpenAI Chat
			Delta *struct {
				ToolCalls []openAIToolCall `json:"tool_calls"`
			} `json:"delta"`
			Message *struct {
				ToolCalls []openAIToolCall `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Candidates []geminiCandidate `json:"candidates"`
	}
	if len(b) == 0 || b[0] != '{' || json.Unmarshal(b, &obj) != nil {
		return
	}
	if obj.ContentBlock != nil {
		c.addBlock(*obj.ContentBlock)
	}
	for _, blk := range obj.Content {
		c.addBlock(blk)
	}
	// Responses streams announce each item on output_item.added and repeat
	// them all in response.completed; count the announcements only, so a
	// stream isn't double-counted (a plain JSON response has only Output).
	if obj.Item != nil && obj.Type == "response.output_item.added" {
		c.addBlock(*obj.Item)
	}
	// The completed item carries a custom tool call's full input: count the
	// tools a Codex exec script calls (once, from .done, not .completed).
	if obj.Item != nil && obj.Type == "response.output_item.done" {
		c.addScriptTools(*obj.Item)
	}
	for _, it := range obj.Output {
		c.addBlock(it)
		c.addScriptTools(it)
	}
	for _, ch := range obj.Choices {
		// A streamed tool call carries its name only in its first delta.
		if ch.Delta != nil {
			for _, tc := range ch.Delta.ToolCalls {
				c.add(tc.Function.Name, "")
			}
		}
		if ch.Message != nil {
			for _, tc := range ch.Message.ToolCalls {
				c.add(tc.Function.Name, "")
			}
		}
	}
	for _, cand := range obj.Candidates {
		for _, p := range cand.Content.Parts {
			if p.FunctionCall != nil {
				c.add(p.FunctionCall.Name, "")
			}
		}
	}
}

func (c toolUseCounter) addBlock(b toolUseBlock) {
	switch b.Type {
	case "tool_use", "server_tool_use", "function_call", "custom_tool_call":
		c.add(b.Name, "")
	case "mcp_tool_use":
		c.add(b.Name, b.ServerName)
	case "mcp_call":
		c.add(b.Name, b.ServerLabel)
	}
}

// scriptToolCall matches a tool invocation in a Codex exec script,
// e.g. `tools.mcp__fs__read_text_file({...})` or `tools.exec_command({...})`.
var scriptToolCall = regexp.MustCompile(`\btools\.([A-Za-z_][A-Za-z0-9_]*)\s*\(`)

// addScriptTools counts the tools a custom tool call's script invokes (Codex's
// exec tool runs model-written JavaScript that calls MCP and built-in tools).
// Only tool names are read from the script, never arguments.
func (c toolUseCounter) addScriptTools(b toolUseBlock) {
	var script string
	if b.Type != "custom_tool_call" || json.Unmarshal(b.Input, &script) != nil || script == "" {
		return
	}
	for _, m := range scriptToolCall.FindAllStringSubmatch(script, -1) {
		c.add(m[1], "")
	}
}

func (c toolUseCounter) add(name, server string) {
	if name != "" {
		c[toolUseKey{name, server}]++
	}
}

func (c toolUseCounter) result() []ToolCallCount {
	var out []ToolCallCount
	for k, n := range c {
		s, tool, kind := ClassifyTool(k.name)
		if k.server != "" {
			// A provider-side MCP call names its server separately.
			s, tool, kind = k.server, k.name, "mcp"
		}
		out = append(out, ToolCallCount{Name: k.name, Server: s, Tool: tool, Kind: kind, Count: n})
	}
	return out
}
