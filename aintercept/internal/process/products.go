package process

import (
	"encoding/json"
	"strings"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// The products whose model calls roll up into sessions and prompts
// (docs/harness-parity.md). Each one finds the common concepts (session,
// prompt, client, hidden calls) in its own fields; what only it has goes in
// the row's ext_json.
const (
	productClaudeCode = events.ProductClaudeCode
	productCodex      = events.ProductCodex
)

// isModelCall matches the model calls sessions are built from (canon.IsModelCall).
func isModelCall(r *Request) bool { return canon.IsModelCall(r) }

// codexLineage is what a Codex call says about its thread: where it came from
// (a subagent, a review, a fork) and how its history began.
type codexLineage struct {
	ParentThreadID string
	ParentTurnID   string
	SubagentKind   string // thread_spawn, review
	ForkedFrom     string
	ForkOrdinal    int64
	Init           string // history_initialization: new, warm_fork, cold_fork, cold_resume
	AgentName      string
}

// lineageOf reads a Codex call's lineage back from its ext_json.
func lineageOf(ext string) codexLineage {
	var cm map[string]any
	if ext == "" || json.Unmarshal([]byte(ext), &cm) != nil {
		return codexLineage{}
	}
	return codexLineage{
		ParentThreadID: str(cm["parent_thread_id"]), ParentTurnID: str(cm["parent_turn_id"]),
		SubagentKind: str(cm["subagent_kind"]), ForkedFrom: str(cm["forked_from_thread_id"]),
		ForkOrdinal: num(cm["forked_from_ordinal_exclusive"]), Init: str(cm["history_initialization"]),
		AgentName: str(cm["agent_name"]),
	}
}

// addLineage folds a call's lineage into its session. Every field is a min or
// a max, so the order calls arrive in doesn't matter.
func (ss *Session) addLineage(l codexLineage) {
	switch {
	case l.ParentThreadID != "":
		kind := "subagent"
		if l.SubagentKind == "review" {
			kind = "review"
		}
		ss.OriginKind, ss.OriginSessionID = minStr(ss.OriginKind, kind), minStr(ss.OriginSessionID, l.ParentThreadID)
		ss.OriginTurnID = minStr(ss.OriginTurnID, l.ParentTurnID)
	case l.ForkedFrom != "":
		ss.OriginKind, ss.OriginSessionID = minStr(ss.OriginKind, "fork"), minStr(ss.OriginSessionID, l.ForkedFrom)
		ss.OriginTurn = max(ss.OriginTurn, l.ForkOrdinal)
	}
	if l.Init != "" && l.Init != "new" {
		ss.Init = minStr(ss.Init, l.Init)
	}
	if l.AgentName != "" && l.AgentName != "/root" {
		ss.AgentName = minStr(ss.AgentName, l.AgentName)
	}
}

// newerVersion is the later of two dotted versions ("2.1.10" after "2.1.9"),
// comparing their leading numbers part by part; empty loses to anything.
func newerVersion(a, b string) string {
	if a == "" || b == "" {
		return a + b
	}
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		na, nb := leadingInt(pa[i]), leadingInt(pb[i])
		if na != nb {
			if na > nb {
				return a
			}
			return b
		}
	}
	if len(pb) > len(pa) {
		return b
	}
	return a
}

func leadingInt(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}
