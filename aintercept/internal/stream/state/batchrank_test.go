package state

import (
	"fmt"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// mixedBatch is one analytics batch whose Claude Code events sit between
// Codex ones: BatchN numbers every event, BatchCCN only the Claude Code ones,
// and event ids (and unnamed inputs) number by the latter.
func mixedBatch(s string, t int64) []records.Record {
	b := "mixed-" + s
	var out []records.Record
	n, cc := int64(0), int64(0)
	add := func(src, name, md string) {
		recSeq++
		n++
		e := &canon.Event{EventKey: fmt.Sprintf("mx-%06d", recSeq), CorrelationID: b, RowNS: (t + n) * ms, NS: (t + n) * ms,
			Seq: n, Source: src, Name: name, SessionID: s, MetadataJSON: md}
		r := records.Record{Kind: records.KindClientEvent, EventKey: e.EventKey, BatchID: b, BatchN: n, Event: e}
		if src == canon.ProductClaudeCode {
			cc++
			r.BatchCCN = cc
		}
		out = append(out, rec(r))
	}
	add(canon.ProductCodex, "codex_turn_event", `{}`)
	add(canon.ProductClaudeCode, "tengu_tool_use_success", `{"toolName":"Read"}`)
	add(canon.ProductCodex, "codex_turn_event", `{}`)
	add(canon.ProductClaudeCode, "tengu_input_prompt", `{"prompt_index":1}`)
	add(canon.ProductClaudeCode, "tengu_cancel", `{}`)
	return out
}

func TestEventIDsRankClaudeCodeEventsOnly(t *testing.T) {
	batchN = map[string]int64{}
	rs := mixedBatch("mx", 1000)
	permute(len(rs), func(order []int) {
		in := make([]records.Record, len(order))
		for i, j := range order {
			in[i] = rs[j]
		}
		o := stream(in)
		checkEvents(t, fmt.Sprint(order), rs, o)
		if !t.Failed() {
			return
		}
	})
}
