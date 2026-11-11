package process

import (
	"encoding/json"
	"strings"

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

// productOf names the product a request came from, by its client headers.
func productOf(r *Request, originator string) string {
	return events.ProductOf(r.Host, r.UserAgent, originator, r.SessionID)
}

// clientOf is how the product was run (Codex: exec, tui; Claude Code: cli,
// sdk…) and its version, from the originator and user-agent.
func clientOf(r *Request, originator string) (client, version string) {
	ua := r.UserAgent
	name, rest, _ := strings.Cut(ua, "/")
	version, _, _ = strings.Cut(rest, " ")
	switch r.Product {
	case productCodex:
		o := firstStr(originator, name)
		client = strings.TrimPrefix(strings.TrimPrefix(o, "codex_"), "codex-")
		if client == "cli_rs" {
			client = "cli"
		}
	case productClaudeCode:
		// claude-cli/2.1.3 (external, cli): the entrypoint is the last part.
		if i := strings.LastIndex(ua, ", "); i >= 0 {
			client = strings.TrimSuffix(ua[i+2:], ")")
		}
	}
	return client, version
}

// isModelCall matches the model calls sessions are built from: for Claude Code
// a real /v1/messages call (not count_tokens) carrying a session; for Codex a
// response.create turn on a thread (a WebSocket turn, or the HTTP fallback).
func isModelCall(r *Request) bool {
	if r.Synthetic || r.SessionID == "" {
		return false
	}
	switch r.Product {
	case productClaudeCode:
		return r.Host == "api.anthropic.com" && strings.HasPrefix(r.Path, "/v1/messages") &&
			!strings.HasPrefix(r.Path, "/v1/messages/count_tokens")
	case productCodex:
		return r.Host == "chatgpt.com" && r.Path == "/backend-api/codex/responses" && (r.WSTurn > 0 || r.Method == "POST")
	}
	return false
}

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

// codexKeys are the client metadata fields a Codex request row keeps in ext_json.
var codexKeys = []string{
	"agent_name", "forked_from_ordinal_exclusive", "forked_from_thread_id", "history_initialization",
	"parent_thread_id", "parent_turn_id", "request_kind", "root_turn_id", "sandbox", "sandbox_mode",
	"subagent_kind", "thread_source", "turn_trigger", "window_id", "workspaces",
}

// applyCodex fills a Codex request row from its client metadata (Codex's
// x-codex-turn-metadata): the turn is the prompt, prewarm and title calls are
// hidden, and the rest goes in ext_json.
func applyCodex(r *Request, e *events.Event) {
	cm := e.ClientMetadata
	if cm == nil {
		return
	}
	r.SessionID = firstStr(r.SessionID, firstStr(str(cm["thread_id"]), str(cm["session_id"])))
	r.PromptID = str(cm["turn_id"])
	r.DeviceID = firstStr(r.DeviceID, str(cm["installation_id"]))
	switch {
	case e.Prewarm || str(cm["request_kind"]) == "prewarm":
		r.HiddenKind = "prewarm"
	case str(cm["thread_source"]) == "thread_title":
		r.HiddenKind = "title"
	}
	if r.HiddenKind != "" {
		r.PromptID = ""
	}
	ext := map[string]any{}
	for _, k := range codexKeys {
		if v, ok := cm[k]; ok && v != "" && v != nil {
			ext[k] = v
		}
	}
	if len(ext) > 0 {
		r.ExtJSON = marshal(ext)
	}
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
