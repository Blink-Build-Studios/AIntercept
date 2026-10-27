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
	// IP is the caller's source IP (from x-forwarded-for, falling back to the
	// proxy-observed peer). Only set on inbound request-header events.
	IP string `json:"ip,omitempty"`

	// Headers are included redacted-by-default (see Redact). Sensitive headers
	// are replaced with a marker, never the value.
	Headers map[string]string `json:"headers,omitempty"`

	// Bytes is the size of the body chunk this event represents.
	Bytes int `json:"bytes,omitempty"`
	// BodyPreview is populated ONLY when DEV_LOG_BODIES is enabled and then only
	// up to a strict byte cap. Empty by default. Safe-ish for stdout.
	BodyPreview string `json:"body_preview,omitempty"`
	// BodyB64 is the COMPLETE body chunk as base64 of the RAW wire bytes —
	// binary-safe, so gzip/brotli/zstd-compressed response bodies survive
	// losslessly (unlike a UTF-8 string, which mangles non-UTF-8 bytes). This is
	// the authoritative capture field. Populated ONLY when DEV_CAPTURE_FULL_BODIES
	// is enabled, and routed ONLY to the durable file sink (stripped from stdout
	// by ForStdout). The extractor base64-decodes and then decompresses per the
	// response's content-encoding.
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
	// Token counts parsed from the response `usage` block, content discarded.
	// Set on the complete event when the response carried usage. This is how we
	// track tokens/cost WITHOUT storing prompt/response content.
	InputTokens      int `json:"input_tokens,omitempty"`
	OutputTokens     int `json:"output_tokens,omitempty"`
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`

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
}

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
	if len(e.rawReq) > 0 {
		if e.Model == "" {
			e.Model = ModelFromRequestBody(e.rawReq)
		}
		if avail, calls := ToolsFromRequestBody(e.rawReq); avail != nil || calls != nil {
			e.ToolsAvailable = avail
			e.ToolCalls = calls
		}
	}
	// Gemini carries the model in the URL path, not the body; fall back to it.
	if e.Model == "" {
		e.Model = ModelFromPath(e.Path)
	}
	if len(e.rawResp) > 0 {
		if u, ok := UsageFromResponseBody(e.rawResp, e.respEncoding); ok {
			e.InputTokens = u.InputTokens
			e.OutputTokens = u.OutputTokens
			e.CacheReadTokens = u.CacheReadTokens
			e.CacheWriteTokens = u.CacheWriteTokens
		}
	}
}

// clearBuffers drops the raw body/header buffers after deferred extraction and
// any shape fingerprinting are done. Content never leaves the process.
func (e *Event) clearBuffers() {
	e.rawReq = nil
	e.rawResp = nil
	e.reqHeaders = nil
	e.respHeaders = nil
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
	if !e.deferExtract {
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

// sensitiveHeaders are never logged in the clear, even with DEV_LOG_BODIES.
var sensitiveHeaders = map[string]struct{}{
	"authorization":       {},
	"proxy-authorization": {},
	"x-api-key":           {},
	"api-key":             {},
	"cookie":              {},
	"set-cookie":          {},
	"x-auth-token":        {},
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
		out[k] = v
	}
	return out
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
// SDKs), `x-api-key: <key>` (Anthropic), `api-key: <key>` (Azure).
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
	return ""
}

// ClientIP returns the caller's source IP from request headers: the first entry
// of x-forwarded-for (the original client through proxies), falling back to
// x-real-ip. Returns "" when neither is present.
func ClientIP(hdrs map[string]string) string {
	get := func(name string) string {
		for k, v := range hdrs {
			if strings.EqualFold(k, name) {
				return v
			}
		}
		return ""
	}
	if xff := get("x-forwarded-for"); xff != "" {
		// XFF is a comma-separated list; the first is the original client.
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	return strings.TrimSpace(get("x-real-ip"))
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
