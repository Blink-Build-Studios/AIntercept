// Package events defines the observation event model, the pluggable sink
// interface, and redaction rules shared by the observer.
//
// Design notes (v0, but forward-looking per project constraints):
//   - Events are self-contained and keyed by a correlation ID so that any
//     observer instance can process any stream without sticky routing. No
//     cross-request or cross-instance state is implied by this model.
//   - A Sink is where processed events go. v0 ships StdoutSink; a future S3
//     (or other durable) sink implements the same interface with no change to
//     the hot path.
package events

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Phase is the lifecycle point an event describes.
type Phase string

const (
	PhaseRequestHeaders    Phase = "request_headers"
	PhaseRequestBodyChunk  Phase = "request_body_chunk"
	PhaseResponseHeaders   Phase = "response_headers"
	PhaseResponseBodyChunk Phase = "response_body_chunk"
	PhaseComplete          Phase = "complete"
	PhaseDisconnect        Phase = "disconnect"
	// PhaseWSTurnStart is a WebSocket model turn's start (its response.create),
	// so a turn still streaming can be shown; its complete row ends it.
	PhaseWSTurnStart Phase = "ws_turn_start"
	// PhaseVerdict records one verdict a customer's Interceptor returned
	// (applied in enforce mode, recorded only in observe mode).
	PhaseVerdict Phase = "verdict"
)

// Direction indicates which half of the transaction an event belongs to.
type Direction string

const (
	DirInbound  Direction = "inbound"  // client -> upstream (request)
	DirOutbound Direction = "outbound" // upstream -> client (response)
)

// Event is one observation. It is intentionally flat and JSON-serializable so a
// downstream sink (stdout now, S3 later) can persist it without reshaping.
type Event struct {
	// CorrelationID ties all events of a single HTTP transaction together. It is
	// stable for the life of one ext_proc stream.
	CorrelationID string `json:"correlation_id"`
	// Seq orders events within a transaction on a single observer instance.
	Seq         uint64    `json:"seq"`
	TimestampNS int64     `json:"timestamp_ns"`
	Phase       Phase     `json:"phase"`
	Direction   Direction `json:"direction,omitempty"`

	Host   string `json:"host,omitempty"`
	Method string `json:"method,omitempty"`
	Path   string `json:"path,omitempty"`
	Status int    `json:"status,omitempty"`

	// KeyHash is a salted, irreversible hash of the caller's API key
	// (Authorization / x-api-key), computed at capture time so usage can be
	// attributed to a stable pseudonymous identity WITHOUT ever storing the key.
	// Empty when no auth header was present. Only set on inbound request-header
	// events. See HashAPIKey.
	KeyHash string `json:"key_hash,omitempty"`
	// KeyHint is a non-secret, human-eyeball fragment of the key — first N and
	// last M characters with the middle masked, e.g. "sk-ant…a3f9" — so rows are
	// visually distinguishable in a UI without exposing the key. The masked
	// middle guarantees it's never enough to reconstruct the secret. Paired with
	// KeyHash (the stable join key). See KeyHint.
	KeyHint string `json:"key_hint,omitempty"`
	// IP is the caller's source IP: the downstream peer address Envoy observed
	// (the original client when the load balancer preserves it, e.g. via PROXY
	// protocol), or an operator-trusted header. See ClientIP. Set on inbound
	// request-header and complete events.
	IP string `json:"ip,omitempty"`
	// ProviderOrgID is the organization the provider says served the request,
	// from its response headers (see ProviderOrgID). Compared downstream with
	// the company's own org IDs to tell corporate from personal accounts. Set
	// on complete events.
	ProviderOrgID string `json:"provider_org_id,omitempty"`
	// Synthetic marks the observer's own health-probe traffic (see
	// probe.MarkerHeader). It is recorded like any request, never dropped, so
	// dashboards exclude it with a filter rather than it vanishing.
	Synthetic bool `json:"synthetic,omitempty"`

	// Headers are included redacted-by-default (see Redact). Sensitive headers
	// are replaced with a marker, never the value.
	Headers map[string]string `json:"headers,omitempty"`

	// On the complete row (HTTP): when the request and response headers arrived
	// (Unix ns, the timestamps of those header rows) and the body sizes in bytes
	// as sent on the wire, so one row describes the whole call.
	RequestStartNS  int64 `json:"request_start_ns,omitempty"`
	ResponseStartNS int64 `json:"response_start_ns,omitempty"`
	RequestBytes    int   `json:"request_bytes,omitempty"`
	ResponseBytes   int   `json:"response_bytes,omitempty"`

	// Bytes is the size of the body chunk this event represents.
	Bytes int `json:"bytes,omitempty"`
	// BodyPreview is populated ONLY when DEV_LOG_BODIES is enabled and then only
	// up to a strict byte cap. Empty by default. Safe-ish for stdout.
	BodyPreview string `json:"body_preview,omitempty"`
	// BodyB64 is the COMPLETE body chunk as base64 of the RAW wire bytes —
	// binary-safe, so gzip/brotli/zstd-compressed response bodies survive
	// losslessly (unlike a UTF-8 string, which mangles non-UTF-8 bytes). This is
	// the authoritative capture field. Populated only in full interception mode
	// (INTERCEPTION_MODE=full) and routed only to the full-interception sinks
	// (stripped from stdout and the content-free stream by ForStdout). The
	// extractor base64-decodes, then decompresses per the response's
	// content-encoding.
	BodyB64 string `json:"body_b64,omitempty"`

	// Model is the model name parsed from the request body (plaintext JSON).
	// Set on the complete event. Enables model-usage + cost views.
	Model string `json:"model,omitempty"`
	// ToolsAvailable is the declared tool catalog from the request (content-free:
	// names + server/tool/kind only). Powers "which MCP servers/tools are in
	// use". Set on the complete event.
	ToolsAvailable []ToolRef `json:"tools_available,omitempty"`
	// ToolCalls is the per-tool call-count rollup from the conversation history
	// (content-free). Powers tool-usage counts. Set on the complete event.
	ToolCalls []ToolCallCount `json:"tool_calls,omitempty"`
	// ToolUses are the tools the model invoked in THIS response, with counts,
	// read from the response itself (see ToolUsesFromResponseBody). Unlike
	// ToolCalls it needs no conversation history in the request, and each call
	// is counted once. Both are kept: ToolCalls for clients that send history,
	// ToolUses for every client.
	ToolUses []ToolCallCount `json:"tool_uses,omitempty"`

	// Content-free request/response metadata (see RequestMeta, ResponseMeta).
	// MessageID / PreviousMessageID chain the turns of a threaded conversation.
	MessageID         string `json:"message_id,omitempty"`
	PreviousMessageID string `json:"previous_message_id,omitempty"`
	ThreadType        string `json:"thread_type,omitempty"`
	Stream            bool   `json:"stream,omitempty"`
	MaxTokens         int    `json:"max_tokens,omitempty"`
	Effort            string `json:"effort,omitempty"`
	ThinkingType      string `json:"thinking_type,omitempty"`
	StopReason        string `json:"stop_reason,omitempty"`
	ServiceTier       string `json:"service_tier,omitempty"`
	InferenceGeo      string `json:"inference_geo,omitempty"`
	Speed             string `json:"speed,omitempty"`
	// Client holds allowlisted request header values identifying the client,
	// session and agent (clientHeaders); Provider holds allowlisted response
	// header values (providerHeaders). Set on complete events, so a row carries
	// them without joining the header events, which still record all headers.
	Client   map[string]string `json:"client,omitempty"`
	Provider map[string]string `json:"provider,omitempty"`

	// Identity the client reports in metadata.user_id (see ParseClientUserID):
	// the raw value, and its device / account / session parts when recognized.
	UserID      string `json:"user_id,omitempty"`
	DeviceID    string `json:"device_id,omitempty"`
	AccountUUID string `json:"account_uuid,omitempty"`
	SessionID   string `json:"session_id,omitempty"`

	// WebChat is a consumer chat app's turn or upload context (ChatGPT web,
	// claude.ai); see applyWebChat.
	WebChat *WebChat `json:"web_chat,omitempty"`

	// Auth is the caller's identity from a JWT bearer token's claims (email,
	// user/account ids, plan), when the request carried one. See
	// AuthIdentityFromHeaders.
	Auth *AuthIdentity `json:"auth,omitempty"`

	// Transport is "websocket" for rows from an upgraded WebSocket connection
	// (each model turn on it, and the connection summary); empty for HTTP.
	Transport string `json:"transport,omitempty"`
	// WSTurn numbers a model turn within its WebSocket connection (from 1);
	// correlation_id is the connection.
	WSTurn int `json:"ws_turn,omitempty"`
	// WebSocket summarizes a WebSocket connection (PhaseWSConnection rows).
	WebSocket *WSConnection `json:"websocket,omitempty"`

	// Prewarm marks a model request sent only to warm a connection or cache
	// (Codex response.create with generate:false); no output is produced.
	Prewarm bool `json:"prewarm,omitempty"`
	// Attachments counts images and files sent with a model request (inline
	// base64 or by reference); see AttachmentsFromRequestBody.
	Attachments *Attachments `json:"attachments,omitempty"`
	// WebSearch summarizes a web-search call (Codex /backend-api/codex/alpha/search).
	WebSearch *WebSearch `json:"web_search,omitempty"`

	// ClientMetadata is a client's own per-request metadata, decoded (Codex's
	// x-codex-turn-metadata: installation/session/thread/turn ids, sandbox
	// mode, workspaces, trigger, …). Kept in full in experimentation mode.
	ClientMetadata map[string]any `json:"client_metadata,omitempty"`
	// MCP is set when the transaction was an MCP-over-HTTP (JSON-RPC) call.
	MCP *MCPCall `json:"mcp,omitempty"`

	// ClientEvent is set on PhaseClientEvent rows: one event from a client's
	// own analytics batch (see ClientEventsFromRequest).
	ClientEvent *ClientEvent `json:"client_event,omitempty"`
	// Verdict is set on PhaseVerdict rows.
	Verdict *Verdict `json:"verdict,omitempty"`
	// InterceptMode is "enforce" when the call went through the enforcing
	// ext_proc filter (a customer Interceptor could act on it); "" otherwise.
	InterceptMode string `json:"intercept_mode,omitempty"`
	// clientEvents are parsed from an analytics request during deferred
	// extraction; the processor fans each out as its own row.
	clientEvents []ClientEvent
	// Token counts parsed from the response `usage` block, content discarded.
	// Set on the complete event when the response carried usage. This is how we
	// track tokens/cost WITHOUT storing prompt/response content.
	InputTokens      int `json:"input_tokens,omitempty"`
	OutputTokens     int `json:"output_tokens,omitempty"`
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
	ReasoningTokens  int `json:"reasoning_tokens,omitempty"`

	// SchemaFingerprint is populated ONLY on dedicated schema-sample events (see
	// SampleIfUnrecognized) — never on normal telemetry. It is a structure-only
	// description of a transaction our extractors did not fully recognize, and is
	// content-free by construction (no scalar values, no header secrets), so it
	// survives WithoutContent and is safe for any durable/shared sink.
	SchemaFingerprint *SchemaFingerprint `json:"schema_fingerprint,omitempty"`

	EndOfStream bool   `json:"end_of_stream,omitempty"`
	Error       string `json:"error,omitempty"`
	// ObserverInstance identifies which replica produced the event (useful once
	// many replicas fan into one S3 bucket).
	ObserverInstance string `json:"observer_instance,omitempty"`

	// ── Deferred-extraction carriers (unexported → NEVER serialized to any sink).
	// The ext_proc serving goroutine attaches the buffered request/response bodies
	// here on the complete event and returns immediately; the Processor's drain
	// goroutine calls ExtractDeferred to parse model/tokens/tools OFF the hot path,
	// then clears these. This keeps the ~tens-of-ms JSON parse off the proxied
	// request path. Content never leaves the process.
	rawReq       []byte
	rawResp      []byte
	respEncoding string
	reqHeaders   map[string]string
	respHeaders  map[string]string
	deferExtract bool
	// held is the bytes of buffered body this event carries, charged to the
	// observer's memory budget until the processor has extracted from them.
	held int64
}

// Hold records that the event now owns n bytes charged to the memory budget;
// the processor releases them once it's done with the event.
func (e *Event) Hold(n int64) { e.held += n }

// Held is the bytes the event owns in the memory budget.
func (e *Event) Held() int64 { return e.held }

// AttachBodies stores the buffered request/response bodies (and the response
// headers, redacted) on the event for deferred (off-hot-path) extraction of
// model/tokens/tools and for unrecognized-shape fingerprinting by the drain
// goroutine. All are held in unexported fields and are never serialized onto a
// normal telemetry event — the fingerprinter copies only structure out of them.
func (e *Event) AttachBodies(req, resp []byte, respEncoding string, respHeaders map[string]string) {
	e.rawReq = req
	e.rawResp = resp
	e.respEncoding = respEncoding
	e.respHeaders = respHeaders
	e.deferExtract = true
}

// AttachRequestHeaders stores the redacted request headers on the unexported
// carrier for unrecognized-shape fingerprinting. They are never serialized onto
// the normal telemetry event — only the fingerprinter reads them.
func (e *Event) AttachRequestHeaders(reqHeaders map[string]string) {
	e.reqHeaders = reqHeaders
}

// ExtractDeferred performs the model/usage/tool parse from the attached bodies
// (if any), stamps the results onto the event, and clears the raw buffers. It is
// meant to run on the processor's drain goroutine, not the request path. A
// no-op when no bodies were attached.
func (e *Event) ExtractDeferred() {
	if !e.deferExtract {
		return
	}
	// Decode a compressed request body (Codex sends zstd) once, so every
	// request-side extractor and the shape fingerprinter read plain JSON.
	if enc := headerValue(e.reqHeaders, "content-encoding"); enc != "" && len(e.rawReq) > 0 {
		if plain, err := decodeBody(e.rawReq, enc); err == nil {
			e.rawReq = plain
		}
	}
	// Decode the response once for every extractor below (each would
	// otherwise decompress it again). The raw body and its encoding stay for
	// the shape fingerprinter.
	resp, respEnc := e.rawResp, e.respEncoding
	if respEnc != "" && len(resp) > 0 && decodable(respEnc) {
		if plain, err := decodeBody(resp, respEnc); err == nil {
			resp, respEnc = plain, ""
		}
	}
	if len(e.rawReq) > 0 {
		if e.Model == "" {
			e.Model = ModelFromRequestBody(e.rawReq)
		}
		if avail, calls := ToolsFromRequestBody(e.rawReq); avail != nil || calls != nil {
			e.ToolsAvailable = avail
			e.ToolCalls = calls
		}
		rm := RequestMetaFromBody(e.rawReq)
		e.Prewarm = rm.Prewarm
		e.Attachments = AttachmentsFromRequestBody(e.rawReq)
		e.WebSearch = WebSearchFromBodies(e.Path, e.rawReq, resp, respEnc)
		e.Stream, e.MaxTokens, e.Effort, e.ThinkingType = rm.Stream, rm.MaxTokens, rm.Effort, rm.ThinkingType
		e.ThreadType, e.PreviousMessageID = rm.ThreadType, rm.PreviousMessageID
		e.UserID, e.DeviceID, e.AccountUUID, e.SessionID = rm.UserID.Raw, rm.UserID.DeviceID, rm.UserID.AccountUUID, rm.UserID.SessionID
		e.clientEvents = ClientEventsFromRequest(e.Host, e.Path, e.rawReq, "")
		e.MCP = MCPFromBodies(e.rawReq, resp, respEnc)
	}
	// Codex identity: per-turn metadata (installation → device, session) and
	// the ChatGPT account header.
	if md := codexTurnMetadata(headerValue(e.reqHeaders, "x-codex-turn-metadata"), e.rawReq); md != nil {
		e.ClientMetadata = md
		str := func(k string) string { s, _ := md[k].(string); return s }
		if e.SessionID == "" {
			e.SessionID = str("session_id")
		}
		if e.DeviceID == "" {
			e.DeviceID = str("installation_id")
		}
	}
	if e.AccountUUID == "" {
		e.AccountUUID = headerValue(e.reqHeaders, "chatgpt-account-id")
	}
	e.Client = pickHeaders(e.reqHeaders, clientHeaders)
	e.Provider = pickHeaders(e.respHeaders, providerHeaders)
	// Over a WebSocket, what HTTP sends as response headers arrives in-band
	// (Codex: codex.rate_limits, codex.response.metadata).
	if e.Transport == TransportWebSocket && len(resp) > 0 {
		for k, v := range inBandProviderMeta(resp) {
			if e.Provider == nil {
				e.Provider = map[string]string{}
			}
			if _, set := e.Provider[k]; !set {
				e.Provider[k] = v
			}
		}
	}
	// Gemini carries the model in the URL path, not the body; fall back to it.
	if e.Model == "" {
		e.Model = ModelFromPath(e.Path)
	}
	// Claude Code's account endpoints name the signed-in person (the only traffic that does).
	if id := AccountFromResponse(e.Path, resp, respEnc); id != nil {
		if e.Auth == nil {
			e.Auth = id
		} else {
			e.Auth.AccountID = firstNonEmpty(e.Auth.AccountID, id.AccountID)
			e.Auth.Email = firstNonEmpty(e.Auth.Email, id.Email)
			e.Auth.Name = firstNonEmpty(e.Auth.Name, id.Name)
			e.Auth.Org = firstNonEmpty(e.Auth.Org, id.Org)
		}
	}
	if len(resp) > 0 {
		e.ToolUses = ToolUsesFromResponseBody(resp, respEnc)
		pm := ResponseMetaFromBody(resp, respEnc)
		// The response names the model and effort when the request didn't
		// yield them.
		if e.Model == "" {
			e.Model = pm.Model
		}
		if e.Effort == "" {
			e.Effort = pm.Effort
		}
		e.MessageID, e.StopReason = pm.MessageID, pm.StopReason
		e.ServiceTier, e.InferenceGeo, e.Speed = pm.ServiceTier, pm.InferenceGeo, pm.Speed
		if u, ok := UsageFromResponseBody(resp, respEnc); ok {
			e.InputTokens = u.InputTokens
			e.OutputTokens = u.OutputTokens
			e.CacheReadTokens = u.CacheReadTokens
			e.CacheWriteTokens = u.CacheWriteTokens
			e.ReasoningTokens = u.ReasoningTokens
		}
	}
	// Consumer chat apps: runs last so the model that served a ChatGPT turn
	// replaces the "auto" its request named.
	e.applyWebChat(e.Path, e.rawReq, resp, respEnc)
}

// clearBuffers drops the raw body/header buffers after deferred extraction and
// any shape fingerprinting are done. Content never leaves the process.
func (e *Event) clearBuffers() {
	e.rawReq = nil
	e.rawResp = nil
	e.reqHeaders = nil
	e.respHeaders = nil
	e.clientEvents = nil
	e.deferExtract = false
}

// SampleIfUnrecognized runs AFTER ExtractDeferred, while the raw buffers are
// still attached. If our extractors did not fully recognize the transaction (we
// failed to pull a model AND any usage), it returns a dedicated schema-sample
// Event carrying a structure-only fingerprint of the bodies/headers, plus ok=
// true. It returns ok=false (and a zero Event) when the transaction was fully
// recognized or nothing was captured. It never copies prompt/response content.
//
// The returned event is a self-contained record (not the original transaction):
// it carries the correlation id, routing metadata, and the fingerprint, and is
// meant for a separate schema-sample sink.
func (e *Event) SampleIfUnrecognized() (Event, bool) {
	if !e.deferExtract || e.Phase != PhaseComplete {
		return Event{}, false
	}
	reason := e.recognitionGap()
	if reason == "" {
		return Event{}, false
	}
	fp := buildSchemaFingerprint(e, reason)
	if fp == nil {
		return Event{}, false
	}
	return Event{
		CorrelationID:     e.CorrelationID,
		Seq:               e.Seq,
		TimestampNS:       e.TimestampNS,
		Phase:             PhaseComplete,
		Host:              e.Host,
		Method:            e.Method,
		Path:              e.Path,
		Status:            e.Status,
		KeyHash:           e.KeyHash,
		KeyHint:           e.KeyHint,
		Model:             e.Model, // may be set even when usage missed, and vice-versa
		ObserverInstance:  e.ObserverInstance,
		SchemaFingerprint: fp,
	}, true
}

// orgIDHeaders are response headers in which providers report the
// organization a request was billed to, in order of preference.
var orgIDHeaders = []string{
	"anthropic-organization-id", // Anthropic API
	"openai-organization",       // OpenAI API
}

// ProviderOrgID returns the provider-reported organization ID from response
// headers, or "" when none is present. The ID is an identifier, not a secret.
func ProviderOrgID(respHdrs map[string]string) string {
	for _, name := range orgIDHeaders {
		for k, v := range respHdrs {
			if strings.EqualFold(k, name) && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}

// sensitiveHeaders are never logged in the clear, even with DEV_LOG_BODIES.
var sensitiveHeaders = map[string]struct{}{
	"authorization":       {},
	"proxy-authorization": {},
	"x-api-key":           {},
	"api-key":             {},
	"cookie":              {},
	"set-cookie":          {},
	"x-auth-token":        {},
	// Gemini (generativelanguage.googleapis.com) API key header.
	"x-goog-api-key": {},
	// ChatGPT web's per-turn anti-abuse and routing tokens: short-lived, but
	// replayable bearer-like values.
	"openai-sentinel-chat-requirements-token": {},
	"openai-sentinel-proof-token":             {},
	"openai-sentinel-turnstile-token":         {},
	"x-conduit-token":                         {},
}

const redactedMarker = "<redacted>"

// RedactHeaders returns a copy of hdrs with sensitive values replaced by a
// marker. The header name is preserved so its presence is still observable.
func RedactHeaders(hdrs map[string]string) map[string]string {
	if hdrs == nil {
		return nil
	}
	out := make(map[string]string, len(hdrs))
	for k, v := range hdrs {
		if _, bad := sensitiveHeaders[strings.ToLower(k)]; bad {
			out[k] = redactedMarker
			continue
		}
		// The request path can carry a credential in its query string
		// (Gemini's `?key=`), so it gets the same treatment as a header.
		if k == ":path" {
			out[k] = RedactPath(v)
			continue
		}
		out[k] = v
	}
	return out
}

// sensitiveQueryParams name query-string parameters that carry credentials.
// Matching is case-insensitive. `key` is the Gemini API's documented form
// (`...:generateContent?key=<API_KEY>`); the rest are common equivalents.
var sensitiveQueryParams = map[string]struct{}{
	"key":          {},
	"api_key":      {},
	"apikey":       {},
	"access_token": {},
}

// RedactPath returns p with the values of credential-bearing query parameters
// replaced by the redaction marker. Everything else, including parameter order
// and encoding, is left exactly as received, so the path stays useful for
// endpoint and model extraction. A path without a query is returned unchanged.
func RedactPath(p string) string {
	q := strings.IndexByte(p, '?')
	if q < 0 {
		return p
	}
	base, query := p[:q], p[q+1:]
	frag := ""
	if h := strings.IndexByte(query, '#'); h >= 0 {
		query, frag = query[:h], query[h:]
	}
	parts := strings.Split(query, "&")
	changed := false
	for i, part := range parts {
		name, _, hasValue := strings.Cut(part, "=")
		if !hasValue {
			continue
		}
		if decoded, err := url.QueryUnescape(name); err == nil {
			name = decoded
		}
		if _, bad := sensitiveQueryParams[strings.ToLower(name)]; bad {
			parts[i] = part[:strings.IndexByte(part, '=')+1] + redactedMarker
			changed = true
		}
	}
	if !changed {
		return p
	}
	return base + "?" + strings.Join(parts, "&") + frag
}

// queryAPIKey returns the value of the first credential-bearing query
// parameter in p, or "".
func queryAPIKey(p string) string {
	q := strings.IndexByte(p, '?')
	if q < 0 {
		return ""
	}
	query := p[q+1:]
	if h := strings.IndexByte(query, '#'); h >= 0 {
		query = query[:h]
	}
	for _, part := range strings.Split(query, "&") {
		name, value, hasValue := strings.Cut(part, "=")
		if !hasValue || value == "" {
			continue
		}
		if decoded, err := url.QueryUnescape(name); err == nil {
			name = decoded
		}
		if _, bad := sensitiveQueryParams[strings.ToLower(name)]; bad {
			if decoded, err := url.QueryUnescape(value); err == nil {
				value = decoded
			}
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// IsSensitiveHeader reports whether a header name must never be logged in clear.
func IsSensitiveHeader(name string) bool {
	_, bad := sensitiveHeaders[strings.ToLower(name)]
	return bad
}

// HashAPIKey returns a salted, irreversible identifier for an API key extracted
// from request headers, or "" if no recognizable key is present. The real key is
// NEVER returned or stored — only sha256(salt || key), hex-truncated to 16 bytes
// (32 hex chars), which is plenty to avoid collisions while staying compact.
//
// The salt is a per-deployment server secret: it makes the hash stable within a
// deployment (so the same key always maps to the same identity) while preventing
// cross-deployment correlation and offline dictionary attacks against known keys.
// An empty salt still produces a usable (if un-salted) hash.
//
// Recognized sources, in order: `authorization: Bearer <key>` (OpenAI/Anthropic
// SDKs), `x-api-key: <key>` (Anthropic), `api-key: <key>` (Azure),
// `x-goog-api-key: <key>` and `?key=<key>` on the path (Gemini).
func HashAPIKey(hdrs map[string]string, salt string) string {
	key := extractAPIKey(hdrs)
	if key == "" {
		return ""
	}
	sum := sha256.Sum256(append([]byte(salt), []byte(key)...))
	return hex.EncodeToString(sum[:16])
}

// KeyHint returns a non-secret, display-only fragment of the API key: the first
// `prefix` and last `suffix` characters with the middle masked by an ellipsis,
// e.g. KeyHint(hdrs, 6, 4) → "sk-ant…a3f9". Returns "" when no key is present.
//
// Safety: the masked middle means the fragment is never enough to reconstruct
// the key. To avoid leaking too much of a short key, if the key is not longer
// than prefix+suffix+1 the whole middle is masked to a fixed width instead of
// revealing the entire key.
func KeyHint(hdrs map[string]string, prefix, suffix int) string {
	key := extractAPIKey(hdrs)
	if key == "" {
		return ""
	}
	if prefix < 0 {
		prefix = 0
	}
	if suffix < 0 {
		suffix = 0
	}
	// Short key: don't reveal most of it — mask to a fixed stub.
	if len(key) <= prefix+suffix+1 {
		head := key
		if len(head) > 3 {
			head = head[:3]
		}
		return head + "…"
	}
	return key[:prefix] + "…" + key[len(key)-suffix:]
}

// extractAPIKey pulls the raw key from the recognized auth headers. Header lookup
// is case-insensitive because upstream header maps are not normalized.
func extractAPIKey(hdrs map[string]string) string {
	get := func(name string) string {
		for k, v := range hdrs {
			if strings.EqualFold(k, name) {
				return v
			}
		}
		return ""
	}
	if v := get("authorization"); v != "" {
		// Strip a "Bearer " / "Basic " scheme prefix if present.
		if i := strings.IndexByte(v, ' '); i >= 0 {
			return strings.TrimSpace(v[i+1:])
		}
		return strings.TrimSpace(v)
	}
	if v := get("x-api-key"); v != "" {
		return strings.TrimSpace(v)
	}
	if v := get("api-key"); v != "" {
		return strings.TrimSpace(v)
	}
	if v := get("x-goog-api-key"); v != "" {
		return strings.TrimSpace(v)
	}
	// Gemini also accepts the key as `?key=` on the request path.
	if v := queryAPIKey(get(":path")); v != "" {
		return v
	}
	// Browser chat apps authenticate with a session cookie instead; hashing it
	// gives every request of a signed-in session one stable key_hash.
	return sessionCookie(get("cookie"))
}

// sessionCookies are the cookies that carry a chat app's signed-in session.
var sessionCookies = []string{
	"sessionKey",                       // claude.ai
	"__Secure-next-auth.session-token", // chatgpt.com (when no bearer token is sent)
}

// sessionCookie returns the first session cookie's value in a Cookie header.
// A cookie too large for one header value may be split into chunks named
// "<name>.0", "<name>.1", … (NextAuth does this); those are joined in order.
func sessionCookie(header string) string {
	if header == "" {
		return ""
	}
	vals := map[string]string{}
	for _, part := range strings.Split(header, ";") {
		if k, v, ok := strings.Cut(strings.TrimSpace(part), "="); ok {
			vals[k] = v
		}
	}
	for _, name := range sessionCookies {
		if v := vals[name]; v != "" {
			return v
		}
		var joined strings.Builder
		for i := 0; ; i++ {
			v, ok := vals[name+"."+strconv.Itoa(i)]
			if !ok {
				break
			}
			joined.WriteString(v)
		}
		if joined.Len() > 0 {
			return joined.String()
		}
	}
	return ""
}

// ClientIP returns the caller's source IP. Each header in trustedHeaders is
// checked in order and the first one present wins (for a comma-separated list
// such as x-forwarded-for, its first entry, the original client). Otherwise
// it falls back to peer, the downstream address Envoy observed on the TCP
// connection ("ip:port" or a bare IP). Headers are only consulted when an
// operator names them: a client can set any header it likes, so trusting one
// by default would let a caller forge its own attribution.
func ClientIP(hdrs map[string]string, trustedHeaders []string, peer string) string {
	for _, name := range trustedHeaders {
		for k, v := range hdrs {
			if !strings.EqualFold(k, name) || v == "" {
				continue
			}
			if i := strings.IndexByte(v, ','); i >= 0 {
				v = v[:i]
			}
			if v = strings.TrimSpace(v); v != "" {
				return v
			}
		}
	}
	return hostOnly(peer)
}

// hostOnly strips the port from an "ip:port" / "[v6]:port" address, returning
// bare IPs unchanged.
func hostOnly(addr string) string {
	addr = strings.TrimSpace(addr)
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// ForStdout returns a copy of the event safe to print to the terminal: the
// full-body capture field is removed so complete prompt/response content never
// reaches stdout or the Docker log buffer. The durable file sink receives the
// full event.
func (e Event) ForStdout() Event {
	e.BodyB64 = ""
	return e
}

// WithoutContent returns a copy with EVERY content-bearing field cleared — the
// full-body capture and the bounded preview. This is the view that must be used
// for any durable/network sink (S3/Spaces): decrypted prompt or response content
// must never leave the process through object storage, even a 256-byte preview.
func (e Event) WithoutContent() Event {
	e.BodyB64 = ""
	e.BodyPreview = ""
	return e
}

// headerValue returns a header's value by case-insensitive name, or "".
func headerValue(hdrs map[string]string, name string) string {
	for k, v := range hdrs {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

// ClientEventRows returns one PhaseClientEvent row per analytics event parsed
// from this transaction, carrying the transaction's identity (correlation id,
// caller ip, key hash, client headers) so each row stands alone.
func (e *Event) ClientEventRows() []Event {
	rows := make([]Event, 0, len(e.clientEvents))
	for i := range e.clientEvents {
		ce := e.clientEvents[i]
		ce.Index = i + 1
		rows = append(rows, Event{
			CorrelationID:    e.CorrelationID,
			TimestampNS:      e.TimestampNS,
			Phase:            PhaseClientEvent,
			Host:             e.Host,
			Method:           e.Method,
			Path:             e.Path,
			Status:           e.Status,
			KeyHash:          e.KeyHash,
			KeyHint:          e.KeyHint,
			IP:               e.IP,
			ProviderOrgID:    e.ProviderOrgID,
			Synthetic:        e.Synthetic,
			Client:           e.Client,
			Auth:             e.Auth,
			ObserverInstance: e.ObserverInstance,
			ClientEvent:      &ce,
		})
	}
	return rows
}

// TransportWebSocket marks rows from an upgraded WebSocket connection.
const TransportWebSocket = "websocket"

// PhaseWSConnection is the one row summarizing a WebSocket connection, emitted
// when it closes.
const PhaseWSConnection Phase = "ws_connection"

// WSConnection summarizes a WebSocket connection.
type WSConnection struct {
	DurationMS   int64 `json:"duration_ms"`
	BytesUp      int   `json:"bytes_up"`   // client→server, on the wire
	BytesDown    int   `json:"bytes_down"` // server→client, on the wire
	MessagesUp   int   `json:"messages_up"`
	MessagesDown int   `json:"messages_down"`
	Turns        int   `json:"turns"`
	CloseCode    int   `json:"close_code,omitempty"`
	// TruncatedMessages counts messages over the size cap: skipped, or kept
	// only up to the cap (see wsframe.Message.Truncated).
	TruncatedMessages int  `json:"truncated_messages,omitempty"`
	Deflate           bool `json:"deflate,omitempty"`
	// Undecodable: a direction hit malformed input or a size cap; decoding
	// stopped there (traffic is never affected).
	Undecodable bool `json:"undecodable,omitempty"`
	// MessageTypes counts messages by "<direction>:<type>" (JSON "type").
	MessageTypes map[string]int `json:"message_types,omitempty"`
	Closed       bool           `json:"-"`
}

// Verdict is one decision a customer's Interceptor returned for a call.
type Verdict struct {
	// Callback is "call_start" or "message".
	Callback string `json:"callback"`
	// Action: allow, replace, drop, deny, or error (the Interceptor failed or
	// missed its deadline; Applied then says whether the call was refused).
	Action      string            `json:"action"`
	Applied     bool              `json:"applied"`
	Mode        string            `json:"mode"`
	Reason      string            `json:"reason,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	// LatencyMS is how long the Interceptor took to answer.
	LatencyMS int64 `json:"latency_ms,omitempty"`
}
