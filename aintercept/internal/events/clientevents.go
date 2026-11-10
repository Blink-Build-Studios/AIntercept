package events

import (
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"
)

// PhaseClientEvent marks a row carrying one event from a client's own
// analytics stream (see ClientEvent), fanned out from the request that
// delivered it.
const PhaseClientEvent Phase = "client_event"

// ClientEvent is one analytics event a client reports about itself, e.g.
// Claude Code's POST /api/event_logging/v2/batch: tool runs, shell commands,
// API calls with cost, hooks, subagents, permission and sandbox state.
//
// Kept in full while in experimentation mode: Data is the whole event_data
// object, with base64-encoded JSON fields (additional_metadata, process)
// decoded in place. Some fields can carry fragments of user input (e.g.
// Claude Code's fileExtension holds pieces of shell commands); see
// docs/PRODUCTION.md "Privacy considerations to revisit".
type ClientEvent struct {
	Source           string         `json:"source"` // "claude_code", "claude_web", "codex", "chatgpt_web", "otel"
	Type             string         `json:"type,omitempty"`
	Name             string         `json:"name,omitempty"`
	ID               string         `json:"id,omitempty"`
	Timestamp        string         `json:"timestamp,omitempty"` // client-reported
	SessionID        string         `json:"session_id,omitempty"`
	DeviceID         string         `json:"device_id,omitempty"`
	AccountUUID      string         `json:"account_uuid,omitempty"`
	OrganizationUUID string         `json:"organization_uuid,omitempty"`
	Model            string         `json:"model,omitempty"`
	AgentID          string         `json:"agent_id,omitempty"`
	AgentType        string         `json:"agent_type,omitempty"`
	UserID           string         `json:"user_id,omitempty"`
	Email            string         `json:"email,omitempty"`
	Data             map[string]any `json:"data,omitempty"`
	// NewEvent / NewFields flag what the client-event registry doesn't know
	// yet: an unregistered event name, or field paths new for this name.
	NewEvent  bool     `json:"new_event,omitempty"`
	NewFields []string `json:"new_fields,omitempty"`
}

// clientEventParser reads one client's analytics requests.
type clientEventParser struct {
	source string
	match  func(host, path string) bool
	parse  func(path string, raw []byte) []ClientEvent
}

// clientEventParsers lists the clients whose analytics we parse. Adding a
// client means a parser here and a registry file client_events/<source>.json.
var clientEventParsers = []clientEventParser{
	{
		// Claude web (claude.ai, and the desktop app that wraps it) posts the
		// same batch format as Claude Code, with its own events.
		source: "claude_web",
		match: func(h, p string) bool {
			return h == "claude.ai" && strings.HasPrefix(p, "/api/event_logging/")
		},
		parse: bodyOnly(parseClaudeCodeEvents),
	},
	{
		// claude.ai's Datadog RUM, proxied first-party: page views, user
		// actions, resource timings, errors, long tasks and vitals.
		source: "claude_web",
		match:  func(h, p string) bool { return h == "claude.ai" && p == "/api/v2/rum" },
		parse:  bodyOnly(parseRUMEvents),
	},
	{
		source: "claude_code",
		match:  func(_, p string) bool { return strings.HasPrefix(p, "/api/event_logging/") },
		parse:  bodyOnly(parseClaudeCodeEvents),
	},
	{
		source: "codex",
		match:  func(_, p string) bool { return strings.HasPrefix(p, "/backend-api/codex/analytics-events/") },
		parse:  bodyOnly(parseCodexEvents),
	},
	{
		// ChatGPT web's analytics: Statsig and structured product events
		// (/ces/v1/rgstr), turn logs (/ces/v1/telemetry/intake), and
		// counters/histograms (/ces/statsc/flush). See chatgptweb.go.
		source: "chatgpt_web",
		match:  func(h, p string) bool { return h == "chatgpt.com" && isChatGPTWebAnalyticsPath(p) },
		parse:  parseChatGPTWebAnalytics,
	},
	{
		// OpenTelemetry metrics (OTLP/HTTP JSON), e.g. Codex's
		// ab.chatgpt.com/otlp/v1/metrics; one row per metric per export.
		source: "otel",
		match:  func(_, p string) bool { return isOTLPMetricsPath(p) },
		parse:  bodyOnly(parseOTLPMetrics),
	},
}

// bodyOnly adapts a parser that needs only the request body.
func bodyOnly(f func(raw []byte) []ClientEvent) func(string, []byte) []ClientEvent {
	return func(_ string, raw []byte) []ClientEvent { return f(raw) }
}

// ClientEventsFromRequest parses the analytics events a client sent in a
// request body, or nil when the request isn't an analytics batch we know.
// encoding is the request's Content-Encoding. The first matching parser wins.
func ClientEventsFromRequest(host, path string, body []byte, encoding string) []ClientEvent {
	path = strings.SplitN(path, "?", 2)[0]
	for _, p := range clientEventParsers {
		if !p.match(host, path) {
			continue
		}
		raw, err := decodeBody(body, encoding)
		if err != nil {
			return nil
		}
		evs := p.parse(path, raw)
		for i := range evs {
			evs[i].Source = p.source
			evs[i].NewEvent, evs[i].NewFields = checkClientEvent(evs[i])
		}
		return evs
	}
	return nil
}

// parseClaudeCodeEvents reads Claude Code's POST /api/event_logging/v2/batch:
// {"events":[{"event_type", "event_data":{event_name, session_id, …}}]}.
func parseClaudeCodeEvents(raw []byte) []ClientEvent {
	var batch struct {
		Events []struct {
			EventType string         `json:"event_type"`
			EventData map[string]any `json:"event_data"`
		} `json:"events"`
	}
	if json.Unmarshal(raw, &batch) != nil {
		return nil
	}
	out := make([]ClientEvent, 0, len(batch.Events))
	for _, ev := range batch.Events {
		d := ev.EventData
		decodeEmbeddedJSON(d)
		str := func(k string) string { s, _ := d[k].(string); return s }
		name := str("event_name")
		if name == "" { // e.g. claude.ai's GrowthbookExperimentEvent
			name = ev.EventType
		}
		ce := ClientEvent{
			Type:      ev.EventType,
			Name:      name,
			ID:        str("event_id"),
			Timestamp: str("client_timestamp"),
			SessionID: str("session_id"),
			DeviceID:  str("device_id"),
			Model:     str("model"),
			AgentID:   str("agent_id"),
			AgentType: str("agent_type"),
			Data:      d,
		}
		if auth, ok := d["auth"].(map[string]any); ok {
			ce.AccountUUID, _ = auth["account_uuid"].(string)
			ce.OrganizationUUID, _ = auth["organization_uuid"].(string)
		}
		out = append(out, ce)
	}
	return out
}

// parseCodexEvents reads Codex's POST /backend-api/codex/analytics-events/events:
// {"events":[{"event_type":"codex_thread_initialized", "event_params":{…}}]}.
// The event type is the event's name; its params are its data.
func parseCodexEvents(raw []byte) []ClientEvent {
	var batch struct {
		Events []struct {
			EventType   string         `json:"event_type"`
			EventParams map[string]any `json:"event_params"`
		} `json:"events"`
	}
	if json.Unmarshal(raw, &batch) != nil {
		return nil
	}
	out := make([]ClientEvent, 0, len(batch.Events))
	for _, ev := range batch.Events {
		d := ev.EventParams
		if d == nil {
			d = map[string]any{}
		}
		decodeEmbeddedJSON(d)
		str := func(k string) string { s, _ := d[k].(string); return s }
		out = append(out, ClientEvent{
			Type:      ev.EventType,
			Name:      ev.EventType,
			SessionID: str("session_id"),
			Model:     str("model"),
			AgentType: str("subagent_source"),
			Data:      d,
		})
	}
	return out
}

// decodeEmbeddedJSON replaces string fields holding base64-encoded JSON
// objects (they start "eyJ", the encoding of `{"`) with the decoded object, so
// rows are queryable without a second decode. Anything else is left as sent.
func decodeEmbeddedJSON(m map[string]any) {
	for k, v := range m {
		s, ok := v.(string)
		if !ok || !strings.HasPrefix(s, "eyJ") {
			continue
		}
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			b, err = base64.RawStdEncoding.DecodeString(s)
		}
		if err != nil {
			continue
		}
		var obj map[string]any
		if json.Unmarshal(b, &obj) == nil {
			m[k] = obj
		}
	}
}

// ClientUserID is the identity a client puts in a model request's
// metadata.user_id.
type ClientUserID struct {
	Raw         string
	DeviceID    string
	AccountUUID string
	SessionID   string
}

// legacyUserID matches Claude Code's older plain-string user_id format:
// user_<device hash>_account_<uuid>_session_<uuid>.
var legacyUserID = regexp.MustCompile(`^user_([0-9a-fA-F]+)_account_([0-9a-fA-F-]*)_session_([0-9a-fA-F-]+)$`)

// ParseClientUserID reads metadata.user_id in either Claude Code format: the
// current JSON object string, or the older user_…_account_…_session_… string.
// Unrecognized values are kept as Raw.
func ParseClientUserID(s string) ClientUserID {
	u := ClientUserID{Raw: s}
	var obj struct {
		DeviceID    string `json:"device_id"`
		AccountUUID string `json:"account_uuid"`
		SessionID   string `json:"session_id"`
	}
	if strings.HasPrefix(strings.TrimSpace(s), "{") && json.Unmarshal([]byte(s), &obj) == nil {
		u.DeviceID, u.AccountUUID, u.SessionID = obj.DeviceID, obj.AccountUUID, obj.SessionID
	} else if m := legacyUserID.FindStringSubmatch(s); m != nil {
		u.DeviceID, u.AccountUUID, u.SessionID = m[1], m[2], m[3]
	}
	return u
}
