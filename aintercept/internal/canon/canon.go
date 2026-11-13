// Package canon turns a transaction's raw observer rows into its request row,
// and its analytics events into event rows: the canonical, provider-mapped
// shape (docs/harness-parity.md) that both the observer's records and the
// processor use. One implementation, so the two can't drift.
package canon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// The products whose model calls roll up into sessions and prompts.
const (
	ProductClaudeCode = events.ProductClaudeCode
	ProductCodex      = events.ProductCodex
)

// PendingClientHeaders are the request header values a pending transaction keeps for its request row.
var PendingClientHeaders = []string{
	"x-claude-code-session-id", "x-claude-code-prompt-id", "x-claude-code-request-class",
	"x-client-request-id", "x-claude-code-agent-id", "x-claude-code-agent-type", "x-organization-uuid", "user-agent",
	"originator", "version",
}

// Request is one HTTP transaction, any host, or one model turn on a WebSocket.
// Fact table: rows are only ever added.
//
//	PK  call_id           correlation_id; correlation_id#turn for a WebSocket turn
//	FK  session_id        → sessions
//	FK  prompt_id         → prompts
//	FK  person_id         → people
//	FK  device_id         → person_devices (with person_id)
//	    request_id        the provider's id; analytics events reference it
type Request struct {
	CallID        string `parquet:"call_id"`
	CorrelationID string `parquet:"correlation_id"`
	// SourceFile is the raw file of the row that ended the transaction (its
	// complete, disconnect or WebSocket close), else of its first row.
	SourceFile string `parquet:"source_file"`
	// FirstFile is the raw file of its first row. A transaction's rows all come
	// from one pod, which uploads in order, so they lie in that pod's files
	// from FirstFile to SourceFile. Best effort: when a pod's file is found
	// late (the full listing), a call already written from its ending keeps
	// that file as its first.
	FirstFile       string `parquet:"first_file"`
	WSTurn          int64  `parquet:"ws_turn,optional"`
	Transport       string `parquet:"transport,optional"`
	StartNS         int64  `parquet:"start_ns,optional"`
	ResponseStartNS int64  `parquet:"response_start_ns,optional"`
	EndNS           int64  `parquet:"end_ns,optional"`
	Host            string `parquet:"host,optional"`
	Method          string `parquet:"method,optional"`
	Path            string `parquet:"path,optional"`
	Status          int64  `parquet:"status,optional"`
	Synthetic       bool   `parquet:"synthetic"`
	IP              string `parquet:"ip,optional"`
	KeyHash         string `parquet:"key_hash,optional"`
	KeyHint         string `parquet:"key_hint,optional"`
	Error           string `parquet:"error,optional"`

	SessionID       string `parquet:"session_id,optional"`
	PromptID        string `parquet:"prompt_id,optional"`
	PersonID        string `parquet:"person_id,optional"`
	DeviceID        string `parquet:"device_id,optional"`
	OrgID           string `parquet:"org_id,optional"`
	RequestClass    string `parquet:"request_class,optional"`
	ClientRequestID string `parquet:"client_request_id,optional"`
	RequestID       string `parquet:"request_id,optional"`
	AgentID         string `parquet:"agent_id,optional"`
	AgentType       string `parquet:"agent_type,optional"`
	UserAgent       string `parquet:"user_agent,optional"`

	Model             string `parquet:"model,optional"`
	StopReason        string `parquet:"stop_reason,optional"`
	MessageID         string `parquet:"message_id,optional"`
	PreviousMessageID string `parquet:"previous_message_id,optional"`
	ThreadType        string `parquet:"thread_type,optional"`
	Stream            bool   `parquet:"stream"`
	ServiceTier       string `parquet:"service_tier,optional"`
	InputTokens       int64  `parquet:"input_tokens,optional"`
	OutputTokens      int64  `parquet:"output_tokens,optional"`
	CacheReadTokens   int64  `parquet:"cache_read_tokens,optional"`
	CacheWriteTokens  int64  `parquet:"cache_write_tokens,optional"`
	ReasoningTokens   int64  `parquet:"reasoning_tokens,optional"`
	Effort            string `parquet:"effort,optional"` // the reasoning effort the call asked for
	// DetailJSON is what the call did, read from its recorded bodies (full interception
	// only; events.CallDetail): commands, patches, its first words, earlier calls' exit codes.
	DetailJSON      string `parquet:"detail_json,optional"`
	RequestBytes    int64  `parquet:"request_bytes,optional"`
	ResponseBytes   int64  `parquet:"response_bytes,optional"`
	ContentEncoding string `parquet:"content_encoding,optional"` // of the response
	// The rest of the allowlisted client and provider header values, and the
	// tools the model used, as JSON objects.
	ClientJSON   string `parquet:"client_json,optional"`
	ProviderJSON string `parquet:"provider_json,optional"`
	ToolUsesJSON string `parquet:"tool_uses_json,optional"`

	// Product is the coding harness (claude_code, codex), Client how it was run
	// (cli, exec, tui…) and ClientVersion its version. HiddenKind marks a call
	// the user never sees (Codex prewarm, title): kept, but out of the session's
	// calls and tokens. ExtJSON is what only that product reports, for this call.
	Product       string `parquet:"product,optional"`
	Client        string `parquet:"client,optional"`
	ClientVersion string `parquet:"client_version,optional"`
	HiddenKind    string `parquet:"hidden_kind,optional"`
	ExtJSON       string `parquet:"ext_json,optional"`
}

// Event is one analytics event a client reported. Fact table.
//
//	PK  event_key         correlation_id/client id, or correlation_id/h<hash of the row> when it has none
//	                      (a repeat of an identical row: …/h<hash>-2, -3, …)
//	FK  correlation_id    → requests.call_id: the analytics request that carried it
//	FK  session_id        → sessions
//	FK  prompt_id         → prompts
//	FK  person_id         → people
//	FK  request_id        → requests.request_id: the model call it reports on (null: none,
//	                        or a call that was never captured, which is a coverage gap)
type Event struct {
	EventKey        string `parquet:"event_key"`
	CorrelationID   string `parquet:"correlation_id"`
	SourceFile      string `parquet:"source_file"`
	RowNS           int64  `parquet:"row_ns"`
	NS              int64  `parquet:"ns"`  // the client's clock when it has one, else row_ns
	Seq             int64  `parquet:"seq"` // the raw row's sequence in its transaction
	Source          string `parquet:"source"`
	Name            string `parquet:"name,optional"`
	Type            string `parquet:"type,optional"`
	ClientEventID   string `parquet:"client_event_id,optional"`
	SessionID       string `parquet:"session_id,optional"`
	PromptID        string `parquet:"prompt_id,optional"`
	PersonID        string `parquet:"person_id,optional"`
	OrgID           string `parquet:"org_id,optional"`
	DeviceID        string `parquet:"device_id,optional"`
	AgentID         string `parquet:"agent_id,optional"`
	AgentType       string `parquet:"agent_type,optional"`
	Model           string `parquet:"model,optional"`
	RequestID       string `parquet:"request_id,optional"`
	ClientRequestID string `parquet:"client_request_id,optional"`
	ToolUseID       string `parquet:"tool_use_id,optional"`
	PromptIndex     int64  `parquet:"prompt_index,optional"`
	PermissionMode  string `parquet:"permission_mode,optional"`
	HeadSHA         string `parquet:"head_sha,optional"`
	MetadataJSON    string `parquet:"metadata_json,optional"` // data.additional_metadata
	EnvJSON         string `parquet:"env_json,optional"`      // data.env
}

// Pending is a transaction whose headers were seen but whose ending (complete,
// disconnect, WebSocket close) hasn't been yet, or, with Done, one that has
// ended recently: a marker so its rows arriving late (another file, another
// run) are recognized and not written twice. Processor state, committed with
// each run so a restart resumes it.
type Pending struct {
	CorrelationID string `parquet:"correlation_id"`
	SourceFile    string `parquet:"source_file,optional"`
	SeenNS        int64  `parquet:"seen_ns"` // its first row, or for Done its ending
	Done          bool   `parquet:"done"`
	// Expired marks a Done transaction that was written without its ending
	// (it waited past pendingTTL): its marker is kept for expiredTTL.
	Expired bool `parquet:"expired,optional"`
	// A disconnect that arrived before the request's headers: the row is
	// written, with this ending, once they arrive.
	EndNS           int64  `parquet:"end_ns,optional"`
	Error           string `parquet:"error,optional"`
	EndFile         string `parquet:"end_file,optional"`
	StartNS         int64  `parquet:"start_ns,optional"`
	ResponseStartNS int64  `parquet:"response_start_ns,optional"`
	Host            string `parquet:"host,optional"`
	Method          string `parquet:"method,optional"`
	Path            string `parquet:"path,optional"`
	Status          int64  `parquet:"status,optional"`
	Synthetic       bool   `parquet:"synthetic"`
	IP              string `parquet:"ip,optional"`
	KeyHash         string `parquet:"key_hash,optional"`
	KeyHint         string `parquet:"key_hint,optional"`
	RequestBytes    int64  `parquet:"request_bytes,optional"`
	ResponseBytes   int64  `parquet:"response_bytes,optional"`
	WebSocket       bool   `parquet:"websocket"`
	// Header values the request row needs when its ending doesn't carry them
	// (rows from before the observer put them on `complete`).
	ClientJSON      string `parquet:"client_json,optional"`
	RequestID       string `parquet:"request_id,optional"`
	ContentEncoding string `parquet:"content_encoding,optional"`
	ProviderJSON    string `parquet:"provider_json,optional"` // allowlisted response header values
}

// RequestFrom builds a request row from a transaction's ending row, filling
// what the ending doesn't carry (rows from older observers) from its headers.
func RequestFrom(e *events.Event, p *Pending, file string) Request {
	r := Request{CallID: e.CorrelationID}
	if p != nil {
		r = RequestFromPending(p)
	}
	r.CorrelationID, r.SourceFile, r.EndNS = e.CorrelationID, file, e.TimestampNS
	if r.FirstFile == "" {
		r.FirstFile = file
	}
	r.WSTurn, r.Transport = int64(e.WSTurn), e.Transport
	r.StartNS = firstNZ(e.RequestStartNS, r.StartNS)
	r.ResponseStartNS = firstNZ(e.ResponseStartNS, r.ResponseStartNS)
	r.RequestBytes = firstNZ(int64(e.RequestBytes), r.RequestBytes)
	r.ResponseBytes = firstNZ(int64(e.ResponseBytes), r.ResponseBytes)
	r.Host, r.Method, r.Path = firstStr(e.Host, r.Host), firstStr(e.Method, r.Method), firstStr(e.Path, r.Path)
	r.Status = firstNZ(int64(e.Status), r.Status)
	r.Synthetic = e.Synthetic || r.Synthetic
	r.IP, r.KeyHash, r.KeyHint = firstStr(e.IP, r.IP), firstStr(e.KeyHash, r.KeyHash), firstStr(e.KeyHint, r.KeyHint)
	r.Error = e.Error
	if len(e.Client) > 0 {
		SetClient(&r, e.Client)
	}
	r.SessionID = firstStr(r.SessionID, e.SessionID)
	r.PersonID, r.DeviceID = e.AccountUUID, e.DeviceID
	if r.Product == ProductCodex {
		ApplyCodex(&r, e)
	}
	r.RequestID = firstStr(e.Provider["request-id"], r.RequestID)
	r.Model, r.StopReason, r.MessageID, r.PreviousMessageID = e.Model, e.StopReason, e.MessageID, e.PreviousMessageID
	r.ThreadType, r.Stream, r.ServiceTier = e.ThreadType, e.Stream, e.ServiceTier
	r.InputTokens, r.OutputTokens = int64(e.InputTokens), int64(e.OutputTokens)
	r.CacheReadTokens, r.CacheWriteTokens = int64(e.CacheReadTokens), int64(e.CacheWriteTokens)
	r.ReasoningTokens = int64(e.ReasoningTokens)
	r.Effort = e.Effort
	// Provider values from the response headers, overridden by the complete row's own.
	provider := map[string]string{}
	if p != nil && p.ProviderJSON != "" {
		json.Unmarshal([]byte(p.ProviderJSON), &provider)
	}
	for k, v := range e.Provider {
		provider[k] = v
	}
	r.ProviderJSON = Marshal(provider)
	if len(e.ToolUses) > 0 {
		r.ToolUsesJSON = Marshal(e.ToolUses)
	}
	return r
}

func RequestFromPending(p *Pending) Request {
	r := Request{
		CallID: p.CorrelationID, CorrelationID: p.CorrelationID, SourceFile: p.SourceFile, FirstFile: p.SourceFile,
		ContentEncoding: p.ContentEncoding, ProviderJSON: p.ProviderJSON,
		StartNS: p.StartNS, ResponseStartNS: p.ResponseStartNS,
		Host: p.Host, Method: p.Method, Path: p.Path, Status: p.Status, Synthetic: p.Synthetic,
		IP: p.IP, KeyHash: p.KeyHash, KeyHint: p.KeyHint,
		RequestBytes: p.RequestBytes, ResponseBytes: p.ResponseBytes, RequestID: p.RequestID,
		EndNS: p.EndNS, Error: p.Error,
	}
	if p.EndFile != "" {
		r.SourceFile = p.EndFile
	}
	if p.WebSocket {
		r.Transport = events.TransportWebSocket
	}
	if p.ClientJSON != "" {
		var c map[string]string
		if json.Unmarshal([]byte(p.ClientJSON), &c) == nil {
			SetClient(&r, c)
		}
	}
	return r
}

func SetClient(r *Request, c map[string]string) {
	r.SessionID = c["x-claude-code-session-id"]
	r.PromptID = c["x-claude-code-prompt-id"]
	r.RequestClass = c["x-claude-code-request-class"]
	r.ClientRequestID = c["x-client-request-id"]
	r.AgentID, r.AgentType = c["x-claude-code-agent-id"], c["x-claude-code-agent-type"]
	r.OrgID = c["x-organization-uuid"]
	r.UserAgent = c["user-agent"]
	r.ClientJSON = Marshal(c)
	r.Product = ProductOf(r, c["originator"])
	r.Client, r.ClientVersion = ClientOf(r, c["originator"])
}

func EventFrom(e *events.Event, line []byte, file string) Event {
	ce := e.ClientEvent
	md, _ := ce.Data["additional_metadata"].(map[string]any)
	key := e.CorrelationID + "/" + ce.ID
	if ce.ID == "" {
		h := sha256.Sum256(line)
		key = e.CorrelationID + "/h" + hex.EncodeToString(h[:8])
	}
	ev := Event{
		EventKey: key, CorrelationID: e.CorrelationID, SourceFile: file,
		RowNS: e.TimestampNS, NS: e.TimestampNS, Seq: int64(e.Seq),
		Source: ce.Source, Name: ce.Name, Type: ce.Type, ClientEventID: ce.ID,
		SessionID: ce.SessionID, PersonID: ce.AccountUUID, OrgID: ce.OrganizationUUID, DeviceID: ce.DeviceID,
		AgentID: ce.AgentID, AgentType: ce.AgentType, Model: ce.Model,
		PromptID: str(md["cc_prompt_id"]), RequestID: str(md["requestId"]), ClientRequestID: str(md["clientRequestId"]),
		ToolUseID:      firstStr(str(md["toolUseID"]), str(md["tool_use_id"])),
		PromptIndex:    num(md["prompt_index"]),
		PermissionMode: str(md["permissionMode"]),
		HeadSHA:        str(ce.Data["head_sha"]),
	}
	if t, err := time.Parse(time.RFC3339Nano, ce.Timestamp); err == nil {
		ev.NS = t.UnixNano()
	}
	if ce.Source == "codex" { // Codex sends no additional_metadata: its params are the event
		ev.PromptID = str(ce.Data["turn_id"])
		ev.MetadataJSON = Marshal(ce.Data)
		ev.PersonID = firstStr(ev.PersonID, e.AccountUUID)
		return ev
	}
	// The metadata and env exactly as the client sent them: key order is the
	// client's (summaries list fields in it), which a decoded map loses.
	var raw struct {
		ClientEvent struct {
			Data struct {
				Metadata json.RawMessage `json:"additional_metadata"`
				Env      json.RawMessage `json:"env"`
			} `json:"data"`
		} `json:"client_event"`
	}
	if json.Unmarshal(line, &raw) == nil {
		d := raw.ClientEvent.Data
		if len(d.Metadata) > 0 && string(d.Metadata) != "null" {
			ev.MetadataJSON = string(d.Metadata)
		}
		if len(d.Env) > 0 && string(d.Env) != "null" {
			ev.EnvJSON = string(d.Env)
		}
	}
	return ev
}

// ProductOf names the product a request came from, by its client headers.
func ProductOf(r *Request, originator string) string {
	return events.ProductOf(r.Host, r.UserAgent, originator, r.SessionID)
}

// ClientOf is how the product was run (Codex: exec, tui; Claude Code: cli,
// sdk…) and its version, from the originator and user-agent.
func ClientOf(r *Request, originator string) (client, version string) {
	ua := r.UserAgent
	name, rest, _ := strings.Cut(ua, "/")
	version, _, _ = strings.Cut(rest, " ")
	switch r.Product {
	case ProductCodex:
		o := firstStr(originator, name)
		client = strings.TrimPrefix(strings.TrimPrefix(o, "codex_"), "codex-")
		if client == "cli_rs" {
			client = "cli"
		}
	case ProductClaudeCode:
		// claude-cli/2.1.3 (external, cli): the entrypoint is the last part.
		if i := strings.LastIndex(ua, ", "); i >= 0 {
			client = strings.TrimSuffix(ua[i+2:], ")")
		}
	}
	return client, version
}

// IsModelCall matches the model calls sessions are built from: for Claude Code
// a real /v1/messages call (not count_tokens) carrying a session; for Codex a
// response.create turn on a thread (a WebSocket turn, or the HTTP fallback).
func IsModelCall(r *Request) bool {
	if r.Synthetic || r.SessionID == "" {
		return false
	}
	switch r.Product {
	case ProductClaudeCode:
		return r.Host == "api.anthropic.com" && strings.HasPrefix(r.Path, "/v1/messages") &&
			!strings.HasPrefix(r.Path, "/v1/messages/count_tokens")
	case ProductCodex:
		return r.Host == "chatgpt.com" && r.Path == "/backend-api/codex/responses" && (r.WSTurn > 0 || r.Method == "POST")
	}
	return false
}

// codexKeys are the client metadata fields a Codex request row keeps in ext_json.
var codexKeys = []string{
	"agent_name", "forked_from_ordinal_exclusive", "forked_from_thread_id", "history_initialization",
	"parent_thread_id", "parent_turn_id", "request_kind", "root_turn_id", "sandbox", "sandbox_mode",
	"subagent_kind", "thread_source", "turn_trigger", "window_id", "workspaces",
}

// ApplyCodex fills a Codex request row from its client metadata (Codex's
// x-codex-turn-metadata): the turn is the prompt, prewarm and title calls are
// hidden, and the rest goes in ext_json.
func ApplyCodex(r *Request, e *events.Event) {
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
		r.ExtJSON = Marshal(ext)
	}
}

// Pick is the named values of h that are set.
func Pick(h map[string]string, names []string) map[string]string {
	out := map[string]string{}
	for _, n := range names {
		if v := h[n]; v != "" {
			out[n] = v
		}
	}
	return out
}

// Marshal is JSON with sorted keys (encoding/json sorts maps), so output is stable.
func Marshal(v any) string {
	if m, ok := v.(map[string]string); ok && len(m) == 0 {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
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

func firstNZ(a, b int64) int64 {
	if a != 0 {
		return a
	}
	return b
}

func firstStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// PendingOfStart is a call's start record as the processor held the call in
// flight (RequestFromPending's inverse): its own fields, every client header,
// what it had of the response.
func PendingOfStart(c *Request) Pending {
	cj := c.ClientJSON
	if cj == "" {
		h := map[string]string{}
		for k, v := range map[string]string{
			"x-claude-code-session-id": c.SessionID, "x-claude-code-prompt-id": c.PromptID,
			"x-claude-code-request-class": c.RequestClass, "x-client-request-id": c.ClientRequestID,
			"x-claude-code-agent-type": c.AgentType,
		} {
			if v != "" {
				h[k] = v
			}
		}
		b, _ := json.Marshal(h)
		cj = string(b)
	}
	return Pending{CorrelationID: c.CallID, SeenNS: c.StartNS, StartNS: c.StartNS, ResponseStartNS: c.ResponseStartNS,
		Host: c.Host, Method: c.Method, Path: c.Path, Status: c.Status, Synthetic: c.Synthetic,
		KeyHash: c.KeyHash, KeyHint: c.KeyHint, IP: c.IP, RequestID: c.RequestID,
		RequestBytes: c.RequestBytes, ResponseBytes: c.ResponseBytes, ContentEncoding: c.ContentEncoding,
		ProviderJSON: c.ProviderJSON, ClientJSON: cj}
}
