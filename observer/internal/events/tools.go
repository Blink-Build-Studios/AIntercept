package events

import (
	"encoding/json"
	"strings"
)

// ToolRef is the structured, content-free fact we keep about one tool: which
// MCP server it belongs to, the bare tool name, and its kind. The raw arguments
// (which contain prompt/content) are never retained.
type ToolRef struct {
	Name   string `json:"name"`             // fully-qualified, e.g. mcp__playwright__browser_navigate
	Server string `json:"server,omitempty"` // MCP server, or "" for builtins/provider tools
	Tool   string `json:"tool"`             // bare tool name
	Kind   string `json:"kind"`             // mcp | builtin | server | unknown
}

// ToolCallCount pairs a tool with how many times it was called in the
// conversation history (content-free usage signal).
type ToolCallCount struct {
	Name   string `json:"name"`
	Server string `json:"server,omitempty"`
	Tool   string `json:"tool"`
	Kind   string `json:"kind"`
	Count  int    `json:"count"`
}

// ClassifyTool splits a tool name into (server, tool, kind). A name like
// mcp__playwright__browser_navigate -> ("playwright", "browser_navigate", "mcp").
// Anthropic server-side tools (web_search, web_fetch, bash_*) -> ("", name,
// "server"). Everything else is a builtin (Bash, Edit, Read, …).
func ClassifyTool(name string) (server, tool, kind string) {
	if name == "" {
		return "", "", "unknown"
	}
	if strings.HasPrefix(name, "mcp__") {
		parts := strings.SplitN(name, "__", 3)
		if len(parts) == 3 {
			return parts[1], parts[2], "mcp"
		}
		return "", name, "mcp"
	}
	// Anthropic provider-hosted tools.
	if strings.HasPrefix(name, "web_search") ||
		strings.HasPrefix(name, "web_fetch") ||
		strings.HasPrefix(name, "bash_") ||
		strings.HasPrefix(name, "code_execution") ||
		strings.HasPrefix(name, "computer") {
		return "", name, "server"
	}
	return "", name, "builtin"
}

// providerRequest is a superset of the three providers' request shapes for tool
// visibility. Each provider populates a different subset; we detect which by
// which fields are present. Everything else (prompt content, system, etc.) is
// ignored and never retained.
type providerRequest struct {
	// tools[] — Anthropic uses {name}; OpenAI uses {function:{name}}; both unmarshal here.
	Tools []struct {
		Name     string `json:"name"` // Anthropic
		Function *struct {
			Name string `json:"name"`
		} `json:"function"` // OpenAI
		// Gemini: tools[].functionDeclarations[].name
		FunctionDeclarations []struct {
			Name string `json:"name"`
		} `json:"functionDeclarations"`
	} `json:"tools"`

	// Anthropic/OpenAI conversation history.
	Messages []struct {
		// Anthropic: content is an array of blocks (tool_use).
		Content json.RawMessage `json:"content"`
		// OpenAI: assistant turns carry tool_calls[].function.name.
		ToolCalls []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tool_calls"`
	} `json:"messages"`

	// Gemini conversation history: contents[].parts[].functionCall.name.
	Contents []struct {
		Parts []struct {
			FunctionCall *struct {
				Name string `json:"name"`
			} `json:"functionCall"`
		} `json:"parts"`
	} `json:"contents"`
}

type contentBlock struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

// ToolsFromRequestBody parses an LLM request body into the declared tool catalog
// (tools_available) and a per-tool call-count rollup from the conversation
// history. Handles Anthropic (/v1/messages), OpenAI (/v1/chat/completions,
// /v1/responses), and Gemini (:generateContent) shapes. Returns (nil, nil) for a
// body that isn't a recognized request. Content is dropped.
func ToolsFromRequestBody(body []byte) (available []ToolRef, calls []ToolCallCount) {
	var req providerRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, nil
	}

	addAvail := func(name string) {
		if name == "" {
			return
		}
		s, tool, kind := ClassifyTool(name)
		available = append(available, ToolRef{Name: name, Server: s, Tool: tool, Kind: kind})
	}
	for _, t := range req.Tools {
		// One tool entry yields one tool, by provider shape (no double-count when
		// a hybrid/malformed entry carries more than one of these).
		switch {
		case t.Name != "": // Anthropic
			addAvail(t.Name)
		case t.Function != nil: // OpenAI
			addAvail(t.Function.Name)
		default: // Gemini: tools[].functionDeclarations[]
			for _, fd := range t.FunctionDeclarations {
				addAvail(fd.Name)
			}
		}
	}

	// Count tool invocations across the conversation history, keyed by tool name.
	counts := map[string]int{}
	for _, msg := range req.Messages {
		// Anthropic: tool_use content blocks.
		var blocks []contentBlock
		if err := json.Unmarshal(msg.Content, &blocks); err == nil {
			for _, b := range blocks {
				if b.Type == "tool_use" && b.Name != "" {
					counts[b.Name]++
				}
			}
		}
		// OpenAI: assistant tool_calls.
		for _, tc := range msg.ToolCalls {
			if tc.Function.Name != "" {
				counts[tc.Function.Name]++
			}
		}
	}
	// Gemini: functionCall parts.
	for _, c := range req.Contents {
		for _, p := range c.Parts {
			if p.FunctionCall != nil && p.FunctionCall.Name != "" {
				counts[p.FunctionCall.Name]++
			}
		}
	}

	for name, n := range counts {
		s, tool, kind := ClassifyTool(name)
		calls = append(calls, ToolCallCount{Name: name, Server: s, Tool: tool, Kind: kind, Count: n})
	}
	return available, calls
}
