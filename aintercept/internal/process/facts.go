// Package process is what the workers' flush reads of a record beyond its
// columns (docs/PIPELINE.md §4): a call's request kind and Codex lineage and
// plan, an event's facts (eventrows.go), and the object store the workers list
// and read raw files from (store.go).
package process

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
)

// Request is one HTTP transaction's row: see canon.Request.
type Request = canon.Request

// Event is one analytics event's row: see canon.Event.
type Event = canon.Event

var envKeys = [3]string{"platform", "terminal", "shell"}

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

// CodexLineage is the session a Codex call's thread came from (ext_json) and
// how: "subagent", "review" or "fork" ("" and "": none), as addLineage reads it.
func CodexLineage(ext string) (kind, parent string) {
	switch l := lineageOf(ext); {
	case l.ParentThreadID != "":
		if l.SubagentKind == "review" {
			return "review", l.ParentThreadID
		}
		return "subagent", l.ParentThreadID
	case l.ForkedFrom != "":
		return "fork", l.ForkedFrom
	}
	return "", ""
}

// eventColsOf is an event row's columns as the builders read them.
func eventColsOf(e Event) eventCols {
	return eventCols{
		CorrelationID: e.CorrelationID, RowNS: e.RowNS, NS: e.NS, Seq: e.Seq, Source: e.Source, Name: e.Name,
		SessionID: e.SessionID, PromptID: e.PromptID, PersonID: e.PersonID, MetadataJSON: e.MetadataJSON,
		DeviceID: e.DeviceID, EnvJSON: e.EnvJSON, HeadSHA: e.HeadSHA, OrgID: e.OrgID, EventKey: e.EventKey,
	}
}

// SubscriptionType is an event's subscription_type, as account_facts reads it.
func SubscriptionType(metadataJSON string) (string, bool) {
	if !strings.Contains(metadataJSON, "subscription_type") {
		return "", false
	}
	return jsonString(jsonFields(metadataJSON, "subscription_type")["subscription_type"])
}

// EnvFields is an event's env platform, terminal and shell, as the device rows read them.
func EnvFields(envJSON string) (v [3]string, ok [3]bool) {
	if envJSON == "" {
		return
	}
	md := jsonFields(envJSON, envKeys[:]...)
	for i, k := range envKeys {
		v[i], ok[i] = jsonString(md[k])
	}
	return
}

// CodexPlanOf is a Codex call's x-codex-plan-type, as account_facts reads it.
func CodexPlanOf(providerJSON string) (string, bool) {
	if providerJSON == "" {
		return "", false
	}
	return jsonString(jsonFields(providerJSON, "x-codex-plan-type")["x-codex-plan-type"])
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return ""
}

func num(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return 0
}

func firstStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
