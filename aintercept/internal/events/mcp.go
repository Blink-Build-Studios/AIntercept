package events

import (
	"encoding/json"
	"strings"
)

// MCPCall is what a proxied MCP-over-HTTP exchange (JSON-RPC 2.0) tells us:
// e.g. Codex's hosted servers at chatgpt.com/backend-api/ps/mcp, or Claude's
// /api/artifacts/mcp. Arguments and results are never kept, only method and
// tool names.
type MCPCall struct {
	Method string `json:"method"`
	// Tool is the tool invoked by a tools/call.
	Tool string `json:"tool,omitempty"`
	// ToolsListed are the tool names a tools/list response returned.
	ToolsListed []string `json:"tools_listed,omitempty"`
	// ErrorCode is set when the response was a JSON-RPC error.
	ErrorCode int `json:"error_code,omitempty"`
}

// maxToolsListed bounds ToolsListed.
const maxToolsListed = 500

// MCPFromBodies parses a JSON-RPC MCP request (and its JSON or SSE response),
// or returns nil when the request isn't JSON-RPC.
func MCPFromBodies(req, resp []byte, respEncoding string) *MCPCall {
	var r struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	if json.Unmarshal(req, &r) != nil || r.JSONRPC == "" || r.Method == "" {
		return nil
	}
	m := &MCPCall{Method: r.Method}
	if r.Method == "tools/call" {
		m.Tool = r.Params.Name
	}
	raw, err := decodeBody(resp, respEncoding)
	if err != nil || len(raw) == 0 {
		return m
	}
	frames := [][]byte{raw}
	if text := string(raw); strings.Contains(text, "data:") { // streamable HTTP (SSE)
		frames = nil
		for _, line := range strings.Split(text, "\n") {
			if line = strings.TrimSpace(line); strings.HasPrefix(line, "data:") {
				frames = append(frames, []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))))
			}
		}
	}
	for _, f := range frames {
		var out struct {
			Result *struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			} `json:"result"`
			Error *struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(f, &out) != nil {
			continue
		}
		if out.Error != nil {
			m.ErrorCode = out.Error.Code
		}
		if out.Result != nil {
			for _, t := range out.Result.Tools {
				if len(m.ToolsListed) < maxToolsListed && t.Name != "" {
					m.ToolsListed = append(m.ToolsListed, t.Name)
				}
			}
		}
	}
	return m
}

// CodexTurnMetadata is codexTurnMetadata, for a turn's start: from the
// x-codex-turn-metadata header or a response.create body's client_metadata.
func CodexTurnMetadata(header string, reqBody []byte) map[string]any {
	return codexTurnMetadata(header, reqBody)
}

// codexTurnMetadata returns Codex's per-turn metadata (installation, session,
// thread and turn ids, sandbox mode, workspaces, trigger, model, …), from the
// x-codex-turn-metadata header or, failing that, the request body's
// client_metadata. nil when absent.
func codexTurnMetadata(header string, reqBody []byte) map[string]any {
	parse := func(s string) map[string]any {
		var m map[string]any
		if strings.HasPrefix(strings.TrimSpace(s), "{") && json.Unmarshal([]byte(s), &m) == nil {
			return m
		}
		return nil
	}
	if m := parse(header); m != nil {
		return m
	}
	// Values are decoded loosely: one non-string field must not drop the rest.
	var body struct {
		ClientMetadata map[string]any `json:"client_metadata"`
	}
	if json.Unmarshal(reqBody, &body) != nil || body.ClientMetadata == nil {
		return nil
	}
	turnMD, _ := body.ClientMetadata["x-codex-turn-metadata"].(string)
	m := parse(turnMD)
	if id, _ := body.ClientMetadata["x-codex-installation-id"].(string); id != "" {
		if m == nil {
			m = map[string]any{}
		}
		if _, ok := m["installation_id"]; !ok {
			m["installation_id"] = id
		}
	}
	return m
}
