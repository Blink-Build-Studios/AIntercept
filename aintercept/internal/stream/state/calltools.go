package state

import (
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// TableCallToolUses is each ended call's tools (process call_tool_uses), one row
// per call and tool; the query takes the distinct rows.
const TableCallToolUses = "call_tool_uses"

// CallToolUse is a call_tool_uses row, with the call it came from.
type CallToolUse struct {
	process.CallToolUse
	CallID string `parquet:"call_id"`
}

// callTools writes an ended call's tools: "mcp" with its server, else "builtin"
// (as the reference reads tool_uses_json), at the call's time. Only the call's
// own session key writes them (an ending copied under its start's key doesn't).
func (s *KeyState) callTools(r records.Record, d *Delta) {
	if r.Kind != records.KindCallEnd && r.Kind != records.KindWSConnection {
		return
	}
	c := r.Call
	sid := sessionOf(s.Key)
	if c == nil || c.Synthetic || c.SessionID == "" || c.SessionID != sid {
		return
	}
	ts := firstNZ(c.StartNS, c.EndNS)
	wrote := false
	for _, u := range process.ToolUses(c.ToolUsesJSON) {
		row := CallToolUse{CallToolUse: process.CallToolUse{SessionID: sid, TsNS: ts}, CallID: c.CallID}
		switch kind, server, tool := u[0], u[1], u[2]; {
		case kind == "mcp" && server != "" && tool != "":
			row.Kind, row.Server, row.Tool = "mcp", server, tool
		case tool != "":
			row.Kind, row.Tool = "builtin", tool
		default:
			continue
		}
		s.rows.put(d, TableCallToolUses, "tu:"+c.CallID+"\x00"+row.Kind+"\x00"+row.Server+"\x00"+row.Tool, row)
		wrote = true
	}
	if wrote {
		d.Cases = append(d.Cases, CaseCallToolUse)
	}
}
