package events

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// ── Unrecognized-shape sampling ──────────────────────────────────────────────
//
// Premise (no heuristics): if a transaction flowed through the proxy and our
// extractors did NOT fully recognize it — we failed to pull BOTH a model and any
// token usage — we retain a STRUCTURAL FINGERPRINT of it so we can understand the
// shape later and extend coverage. We make no guess about whether the traffic
// "looks like" inference; the only question is whether WE understood it.
//
// The contract — the one line that matters — is: KEYS ARE KEPT, VALUES ARE
// SCRUBBED. A JSON body collapses to a schema-like skeleton where every object
// key is preserved verbatim (keys are the structure we want to read) and every
// scalar VALUE is replaced by its type (+ a coarse length bucket for strings).
// No string or number value is ever copied. We deliberately do NOT try to detect
// or redact "sensitive-looking" keys: that would mean heuristically guessing at
// content, which is exactly what we refuse to do. The operator's contract is the
// mirror image — do not put secrets in KEY position; values are where payloads
// live and values are always scrubbed.
//
//   - JSON bodies → skeleton: keys verbatim, scalar values → type (+ length
//     bucket). No value ever copied.
//   - Non-JSON / unparseable bodies degrade to a byte classification (utf8 /
//     binary / gzip-magic / SSE-framed) + length. Never the bytes themselves.
//   - Headers are value-dropped except a small allowlist of non-secret SDK/
//     version markers; the existing sensitive set never appears at all.
//
// This runs on the drain goroutine (off the request path) and is fail-open: a
// panic in fingerprinting is recovered and the sample is skipped. It can never
// affect the proxied request.

// maxFingerprintNodes caps how many JSON nodes a single body skeleton may
// contain, so a pathologically large/deep body can't blow up memory or produce
// an enormous sample. Traversal stops (marking truncation) once hit.
const maxFingerprintNodes = 2000

// maxFingerprintDepth caps recursion depth for the same reason.
const maxFingerprintDepth = 40

// SchemaFingerprint is the content-free structural description of a transaction
// we did not fully recognize. It is attached to a dedicated schema-sample Event
// (never to normal telemetry) and is safe to persist and share: it contains no
// prompt/response content, no header secrets, and no scalar values.
type SchemaFingerprint struct {
	// Reason records why the transaction was sampled (which extractor(s) missed),
	// e.g. "no_model", "no_usage", "no_model,no_usage".
	Reason string `json:"reason"`

	// Request / Response are the structural views. Either may be nil (e.g. no body
	// was captured for that half).
	Request  *BodyShape `json:"request,omitempty"`
	Response *BodyShape `json:"response,omitempty"`

	// RequestHeaderNames / ResponseHeaderNames list the header NAMES present
	// (lowercased, sorted), so we can see which SDK/markers were on the wire
	// without retaining any value. Sensitive header names are listed too (their
	// presence is structure); their values are never captured anywhere.
	RequestHeaderNames  []string `json:"request_header_names,omitempty"`
	ResponseHeaderNames []string `json:"response_header_names,omitempty"`

	// SafeHeaders carries the VALUES of a small allowlist of non-secret markers
	// (content-type, user-agent, SDK/version headers). These identify the client
	// and schema family and are not sensitive. Everything else is name-only.
	SafeHeaders map[string]string `json:"safe_headers,omitempty"`
}

// BodyShape is the structural view of one body (request or response).
type BodyShape struct {
	// ByteLen is the captured body length in bytes.
	ByteLen int `json:"byte_len"`
	// ContentEncoding is the declared encoding ("gzip", "br", "" …) as seen on the
	// wire, so we know whether Skeleton reflects decoded or raw bytes.
	ContentEncoding string `json:"content_encoding,omitempty"`
	// Kind classifies the body: "json", "sse", "binary", "text", or "empty".
	Kind string `json:"kind"`
	// Skeleton is the JSON structure skeleton when Kind == "json" (or when SSE
	// data frames parsed as JSON). Scalars are type strings; objects/arrays
	// recurse. nil for non-JSON bodies.
	Skeleton json.RawMessage `json:"skeleton,omitempty"`
	// Truncated is true when traversal hit the node/depth cap and the skeleton is
	// partial.
	Truncated bool `json:"truncated,omitempty"`
}

// safeHeaderAllowlist are header names whose VALUES are non-secret and useful for
// identifying the client SDK / schema family. Lowercase. Values of any header NOT
// in this list are dropped (only the name is retained), and the sensitive set is
// never emitted regardless.
var safeHeaderAllowlist = map[string]struct{}{
	"content-type":                {},
	"accept":                      {},
	"user-agent":                  {},
	"anthropic-version":           {},
	"anthropic-beta":              {},
	"openai-version":              {},
	"openai-beta":                 {},
	"x-stainless-os":              {},
	"x-stainless-lang":            {},
	"x-stainless-package-version": {},
	"x-stainless-runtime":         {},
	"x-stainless-runtime-version": {},
	"x-stainless-arch":            {},
	"x-goog-api-client":           {},
	"content-encoding":            {},
	"transfer-encoding":           {},
}

// FullyRecognized reports whether our extractors understood the transaction: we
// pulled a model AND at least one token count. Anything short of that means the
// shape is (at least partly) unknown to us and should be sampled.
func (e Event) FullyRecognized() bool {
	return e.Model != "" && (e.InputTokens > 0 || e.OutputTokens > 0 || e.CacheReadTokens > 0 || e.CacheWriteTokens > 0)
}

// recognitionGap returns a comma-joined reason string naming which extractors
// missed, or "" if the transaction was fully recognized.
func (e Event) recognitionGap() string {
	var misses []string
	if e.Model == "" {
		misses = append(misses, "no_model")
	}
	if e.InputTokens == 0 && e.OutputTokens == 0 && e.CacheReadTokens == 0 && e.CacheWriteTokens == 0 {
		misses = append(misses, "no_usage")
	}
	return strings.Join(misses, ",")
}

// buildSchemaFingerprint builds the structural fingerprint from the raw buffers
// still attached to the event. It must be called BEFORE ExtractDeferred clears
// them. Returns nil if there is nothing to fingerprint. Fail-open: any panic is
// recovered and nil is returned.
func buildSchemaFingerprint(e *Event, reason string) (fp *SchemaFingerprint) {
	defer func() {
		if r := recover(); r != nil {
			fp = nil
		}
	}()
	fp = &SchemaFingerprint{
		Reason:              reason,
		RequestHeaderNames:  sortedHeaderNames(e.reqHeaders),
		SafeHeaders:         safeHeaderValues(e.reqHeaders),
		ResponseHeaderNames: sortedHeaderNames(e.respHeaders),
	}
	// Merge response safe headers into the same map (keyed by name; request and
	// response rarely share a marker, and when they do the response wins — it
	// carries the content-encoding that explains the response skeleton).
	for k, v := range safeHeaderValues(e.respHeaders) {
		if fp.SafeHeaders == nil {
			fp.SafeHeaders = map[string]string{}
		}
		fp.SafeHeaders[k] = v
	}
	if len(e.rawReq) > 0 {
		fp.Request = shapeBody(e.rawReq, "")
	}
	if len(e.rawResp) > 0 {
		fp.Response = shapeBody(e.rawResp, e.respEncoding)
	}
	return fp
}

// shapeBody classifies a body and, when it is JSON (or SSE carrying JSON data
// frames), builds a structure skeleton. encoding is the declared content-encoding
// of this body on the wire. We fingerprint the RAW bytes as captured: if the body
// was compressed we still classify it (kind=binary, encoding=gzip) rather than
// risk a decode here — the goal is to understand the shape we're NOT handling, and
// decoding is itself part of what may be unhandled.
func shapeBody(raw []byte, encoding string) *BodyShape {
	bs := &BodyShape{ByteLen: len(raw), ContentEncoding: encoding}
	if len(raw) == 0 {
		bs.Kind = "empty"
		return bs
	}
	// A compressed body is opaque bytes to the fingerprinter; classify and stop.
	if encoding != "" && !strings.EqualFold(encoding, "identity") {
		bs.Kind = "binary"
		return bs
	}
	trimmed := trimLeadingSpace(raw)
	// SSE: lines framed as "event:"/"data:". Pull the first JSON data frame's
	// skeleton so a streaming response we don't recognize still shows structure.
	if looksSSE(trimmed) {
		bs.Kind = "sse"
		if sk, trunc, ok := sseSkeleton(trimmed); ok {
			bs.Skeleton = sk
			bs.Truncated = trunc
		}
		return bs
	}
	// JSON object or array?
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		var v interface{}
		if err := json.Unmarshal(raw, &v); err == nil {
			n := 0
			sk := skeleton(v, 0, &n)
			bs.Kind = "json"
			if b, err := json.Marshal(sk); err == nil {
				bs.Skeleton = b
			}
			bs.Truncated = n >= maxFingerprintNodes
			return bs
		}
	}
	// Fallback: valid utf8 text vs. binary.
	if utf8.Valid(raw) {
		bs.Kind = "text"
	} else {
		bs.Kind = "binary"
	}
	return bs
}

// skeleton recursively replaces a decoded JSON value with its structural type.
// Scalars become type strings; a string additionally carries a coarse length
// bucket (e.g. "str(<=64)"). No scalar value is ever copied. Object keys are
// preserved (keys are structure). n counts visited nodes against the global cap.
func skeleton(v interface{}, depth int, n *int) interface{} {
	*n++
	if *n >= maxFingerprintNodes || depth >= maxFingerprintDepth {
		return "…truncated…"
	}
	switch t := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		// Keys are kept verbatim (they ARE the schema we want to read); only the
		// VALUE under each key is reduced to its type. We do not inspect or redact
		// keys — see the package contract: values are scrubbed, keys are not.
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out[k] = skeleton(t[k], depth+1, n)
		}
		return out
	case []interface{}:
		// Represent an array as [len] plus the skeleton of a FEW sample elements
		// (not all — a 10k-element array needn't cost 10k nodes). We fold distinct
		// element shapes so a homogeneous array collapses to one shape.
		shapes := []interface{}{}
		seen := map[string]struct{}{}
		for _, el := range t {
			s := skeleton(el, depth+1, n)
			key, _ := json.Marshal(s)
			if _, dup := seen[string(key)]; dup {
				continue
			}
			seen[string(key)] = struct{}{}
			shapes = append(shapes, s)
			if len(shapes) >= 5 || *n >= maxFingerprintNodes {
				break
			}
		}
		return map[string]interface{}{
			fmt.Sprintf("array[%d]", len(t)): shapes,
		}
	case string:
		return fmt.Sprintf("str(%s)", lenBucket(len(t)))
	case float64:
		// encoding/json decodes all numbers to float64; distinguish integers.
		if t == float64(int64(t)) {
			return "int"
		}
		return "float"
	case bool:
		return "bool"
	case nil:
		return "null"
	default:
		return "unknown"
	}
}

// lenBucket maps a string length to a coarse bucket, so we see rough field sizes
// (a 3-char "role" vs a 4000-char prompt) without any content and without a
// precise length that could, in aggregate, fingerprint a specific value.
func lenBucket(n int) string {
	switch {
	case n == 0:
		return "0"
	case n <= 8:
		return "<=8"
	case n <= 64:
		return "<=64"
	case n <= 512:
		return "<=512"
	case n <= 4096:
		return "<=4k"
	default:
		return ">4k"
	}
}

// looksSSE reports whether the body is server-sent-events framed (starts with an
// "event:" or "data:" line). Prefix-only; never scans content.
func looksSSE(b []byte) bool {
	return hasBytePrefix(b, "event:") || hasBytePrefix(b, "data:")
}

// sseSkeleton parses the FIRST data frame that is JSON and returns its skeleton,
// so a streaming shape we don't recognize still reveals structure. Returns ok=
// false when no data frame parses as JSON.
func sseSkeleton(b []byte) (sk json.RawMessage, truncated bool, ok bool) {
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" || (payload[0] != '{' && payload[0] != '[') {
			continue
		}
		var v interface{}
		if err := json.Unmarshal([]byte(payload), &v); err != nil {
			continue
		}
		n := 0
		s := skeleton(v, 0, &n)
		if b, err := json.Marshal(s); err == nil {
			return b, n >= maxFingerprintNodes, true
		}
	}
	return nil, false, false
}

// sortedHeaderNames returns the lowercased, sorted header names present. Names
// are structure (which headers were on the wire); no values are included.
func sortedHeaderNames(hdrs map[string]string) []string {
	if len(hdrs) == 0 {
		return nil
	}
	out := make([]string, 0, len(hdrs))
	for k := range hdrs {
		out = append(out, strings.ToLower(k))
	}
	sort.Strings(out)
	return out
}

// safeHeaderValues returns the values of allowlisted non-secret marker headers
// only. A header in the sensitive set is never returned even if (mistakenly)
// added to the allowlist.
func safeHeaderValues(hdrs map[string]string) map[string]string {
	if len(hdrs) == 0 {
		return nil
	}
	var out map[string]string
	for k, v := range hdrs {
		lk := strings.ToLower(k)
		if _, ok := safeHeaderAllowlist[lk]; !ok {
			continue
		}
		if IsSensitiveHeader(lk) {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[lk] = v
	}
	return out
}

// trimLeadingSpace returns b without leading ASCII whitespace, without allocating
// when there is none.
func trimLeadingSpace(b []byte) []byte {
	i := 0
	for i < len(b) {
		switch b[i] {
		case ' ', '\t', '\r', '\n':
			i++
		default:
			return b[i:]
		}
	}
	return b[i:]
}

// hasBytePrefix reports whether b starts with prefix (after no allocation).
func hasBytePrefix(b []byte, prefix string) bool {
	if len(b) < len(prefix) {
		return false
	}
	return string(b[:len(prefix)]) == prefix
}
