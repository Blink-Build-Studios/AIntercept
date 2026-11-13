package state

import "github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"

// TableCallToolUses is each ended call's tools (process call_tool_uses), one row
// per call and tool; the query takes the distinct rows.
const TableCallToolUses = "call_tool_uses"

// CallToolUse is a call_tool_uses row, with the call it came from.
type CallToolUse struct {
	process.CallToolUse
	CallID string `parquet:"call_id"`
}
