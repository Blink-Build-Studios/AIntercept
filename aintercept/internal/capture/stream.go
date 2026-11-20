package capture

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"
)

// Events anonymizes a reply's events (an event stream's data, or a
// WebSocket's messages), each a JSON object. Streamed pieces of one text,
// tool input or argument string are put together, anonymized as the whole,
// and cut back into pieces of the same lengths, so they still add up to the
// whole that later events repeat (Responses' .done events, the completed
// response), and a tool input's pieces still make valid JSON.
func (a *Anonymizer) Events(evs [][]byte) ([][]byte, error) {
	type piece struct {
		event int
		text  string
	}
	type run struct {
		json   bool
		pieces []piece
	}
	runs := map[string]*run{}
	var order []string
	for i, ev := range evs {
		k, field, isJSON, text := deltaOf(ev)
		if field == "" {
			continue
		}
		r := runs[k]
		if r == nil {
			r = &run{json: isJSON}
			runs[k] = r
			order = append(order, k)
		}
		r.pieces = append(r.pieces, piece{i, text})
	}
	override := map[int]string{}
	for _, k := range order {
		r := runs[k]
		var whole strings.Builder
		for _, p := range r.pieces {
			whole.WriteString(p.text)
		}
		var anon string
		if r.json {
			anon = a.JSONText(whole.String())
		} else {
			anon = a.TextOrJSON(whole.String())
		}
		rest := anon
		for j, p := range r.pieces {
			if j == len(r.pieces)-1 {
				override[p.event] = rest
				break
			}
			n := utf8.RuneCountInString(p.text)
			cut := 0
			for c := 0; c < n && cut < len(rest); c++ {
				_, size := utf8.DecodeRuneInString(rest[cut:])
				cut += size
			}
			override[p.event], rest = rest[:cut], rest[cut:]
		}
	}
	out := make([][]byte, len(evs))
	for i, ev := range evs {
		o, has := override[i]
		_, field, _, _ := deltaOf(ev)
		str := a.str
		if has {
			str = func(n *node, s string) string {
				if n.key == field && (n.parent == nil || n.parent.key == "" || n.parent.key == "delta") {
					return o
				}
				return a.str(n, s)
			}
		}
		b, err := rewrite(ev, rewriter{str: str, key: a.keyAt, scalar: a.scalarAt})
		if err != nil {
			return nil, fmt.Errorf("event %d: %w", i, err)
		}
		out[i] = b
	}
	return out, nil
}

// deltaOf says whether an event carries a piece of a streamed string: its
// run's key, the field holding the piece, whether the whole is JSON, and the piece.
func deltaOf(ev []byte) (key, field string, isJSON bool, text string) {
	var e struct {
		Type         string          `json:"type"`
		Index        *int            `json:"index"`
		ItemID       string          `json:"item_id"`
		OutputIndex  *int            `json:"output_index"`
		ContentIndex *int            `json:"content_index"`
		SummaryIndex *int            `json:"summary_index"`
		Delta        json.RawMessage `json:"delta"`
	}
	if json.Unmarshal(ev, &e) != nil || len(e.Delta) == 0 {
		return "", "", false, ""
	}
	idx := func(p *int) string {
		if p == nil {
			return ""
		}
		return fmt.Sprint(*p)
	}
	if e.Type == "content_block_delta" { // Anthropic
		var d map[string]any
		if json.Unmarshal(e.Delta, &d) != nil {
			return "", "", false, ""
		}
		for _, f := range []string{"text", "thinking", "partial_json"} {
			if s, ok := d[f].(string); ok {
				return "a/" + idx(e.Index) + "/" + f, f, f == "partial_json", s
			}
		}
		return "", "", false, ""
	}
	var s string
	if !strings.HasSuffix(e.Type, ".delta") || json.Unmarshal(e.Delta, &s) != nil { // Responses: a string delta
		return "", "", false, ""
	}
	base := strings.TrimSuffix(e.Type, ".delta")
	return "r/" + base + "/" + e.ItemID + "/" + idx(e.OutputIndex) + "/" + idx(e.ContentIndex) + "/" + idx(e.SummaryIndex),
		"delta", base == "response.function_call_arguments", s
}

// SSE anonymizes an event stream: each data line's JSON (as Events), every
// other line kept.
func (a *Anonymizer) SSE(body []byte) ([]byte, error) {
	lines := strings.SplitAfter(string(body), "\n")
	var at []int
	var evs [][]byte
	for i, l := range lines {
		d, ok := strings.CutPrefix(l, "data:")
		if !ok {
			lines[i] = a.sseLine(l)
			continue
		}
		d = strings.TrimSpace(d)
		if strings.HasPrefix(d, "{") {
			at = append(at, i)
			evs = append(evs, []byte(d))
		} else if d != "[DONE]" && d != "" {
			lines[i] = "data: " + a.Text(d) + strings.Repeat("\n", strings.Count(l, "\n"))
		}
	}
	out, err := a.Events(evs)
	if err != nil {
		return nil, err
	}
	for j, i := range at {
		l := lines[i]
		lead := "data:"
		if strings.HasPrefix(l, "data: ") {
			lead = "data: "
		}
		trail := l[len(strings.TrimRight(l, "\r\n")):]
		lines[i] = lead + string(out[j]) + trail
	}
	return []byte(strings.Join(lines, "")), nil
}

// sseLine is an event stream's line other than data: an event name kept
// when it reads as one, comments, ids and anything else replaced.
func (a *Anonymizer) sseLine(l string) string {
	body := strings.TrimRight(l, "\r\n")
	trail := l[len(body):]
	switch {
	case body == "":
		return l
	case strings.HasPrefix(body, "event:"):
		if name := strings.TrimSpace(strings.TrimPrefix(body, "event:")); !enumLike(name) {
			return "event: " + a.Text(name) + trail
		}
		return l
	case strings.HasPrefix(body, "retry:"):
		return l
	case strings.HasPrefix(body, "id:"):
		return "id: " + a.ID(strings.TrimSpace(strings.TrimPrefix(body, "id:"))) + trail
	case json.Valid([]byte(body)):
		if b, err := a.JSON([]byte(body)); err == nil {
			return string(b) + trail
		}
	}
	return a.Text(body) + trail
}

// Body anonymizes a decoded body by its content type: an event stream, JSON,
// a form, or text.
func (a *Anonymizer) Body(body []byte, contentType string) ([]byte, error) {
	if len(body) == 0 {
		return body, nil
	}
	t := strings.TrimSpace(string(body))
	switch {
	case strings.Contains(contentType, "event-stream") || strings.HasPrefix(t, "data:") || strings.HasPrefix(t, "event:"):
		return a.SSE(body)
	case json.Valid(body):
		return a.JSON(body)
	case strings.Contains(contentType, "x-ndjson") || strings.Contains(contentType, "jsonl"):
		var out []string
		for _, l := range strings.Split(string(body), "\n") {
			if json.Valid([]byte(l)) {
				b, err := a.JSON([]byte(l))
				if err != nil {
					return nil, err
				}
				l = string(b)
			} else {
				l = a.Default(l)
			}
			out = append(out, l)
		}
		return []byte(strings.Join(out, "\n")), nil
	case strings.Contains(contentType, "x-www-form-urlencoded"):
		q, err := url.ParseQuery(t)
		if err != nil {
			return []byte(a.Text(string(body))), nil
		}
		for k, vs := range q {
			for i := range vs {
				vs[i] = a.Default(vs[i])
			}
			q[k] = vs
		}
		return []byte(q.Encode()), nil
	}
	return []byte(a.Text(string(body))), nil
}

// ── Headers and paths ────────────────────────────────────────────────────────

// keepHeaders are request headers (and a response's exchange headers) that
// hold structure: formats, versions, the client's own settings. A
// response's other headers are ResponseHeaders'.
var keepHeaders = set(":authority", ":method", ":scheme", ":status", "accept", "accept-encoding", "accept-language",
	"content-type", "content-encoding", "content-length", "connection", "x-forwarded-proto",
	"x-envoy-expected-rq-timeout-ms", "upgrade", "sec-websocket-version",
	"sec-websocket-extensions", "anthropic-version", "anthropic-beta", "anthropic-dangerous-direct-browser-access",
	"x-app", "x-stainless-os", "x-stainless-arch", "x-stainless-lang", "x-stainless-package-version",
	"x-stainless-runtime", "x-stainless-runtime-version", "x-stainless-retry-count", "x-stainless-timeout",
	"x-stainless-helper-method", "x-claude-code-request-class", "originator", "version", "openai-beta",
	"x-codex-beta-features", "oai-product-sku", "x-openai-product-sku", "transfer-encoding", "cache-control",
	"pragma", "priority", "te", "sec-fetch-mode", "sec-fetch-site", "sec-fetch-dest", "x-goog-api-client",
	"x-envoy-attempt-count")

// Headers anonymizes a header map (its credentials already redacted).
func (a *Anonymizer) Headers(h map[string]string) (map[string]string, error) {
	if err := CheckRedacted(h); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(h))
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys) // fakes are numbered in the order met: the same call, the same case
	for _, k := range keys {
		v := h[k]
		lk := strings.ToLower(k)
		switch {
		case v == redacted || v == "":
		case lk == ":path":
			v = a.Path(v)
		case lk == "user-agent":
			v = a.UserAgent(v)
		case keepHeaders[lk]:
		case strings.Contains(lk, "usage-limit") || strings.Contains(lk, "ratelimit") || strings.Contains(lk, "plan-type") ||
			strings.Contains(lk, "tier") || strings.Contains(lk, "credits"):
			v = a.Opaque(v) // the account's state, as a client relays it
		case lk == "x-forwarded-for" || lk == "x-real-ip" || lk == "x-envoy-external-address" || lk == "true-client-ip" ||
			lk == "cf-connecting-ip":
			parts := strings.Split(v, ",")
			for i, p := range parts {
				lead := p[:len(p)-len(strings.TrimLeft(p, " "))]
				parts[i] = lead + a.Default(strings.TrimSpace(p))
			}
			v = strings.Join(parts, ",")
		case strings.HasPrefix(strings.TrimSpace(v), "{") && json.Valid([]byte(v)):
			b, err := a.JSON([]byte(v))
			if err != nil {
				return nil, err
			}
			v = string(b)
		case strings.HasSuffix(lk, "-id") || strings.HasSuffix(lk, "_id") || strings.HasSuffix(lk, "-uuid") ||
			lk == "traceparent" || lk == "x-request-id" || lk == "request-id":
			v = a.tokens(v, a.Text)
		default:
			v = a.Default(v)
		}
		out[k] = v
	}
	return out, nil
}

// Path anonymizes a request path: each segment that is an identifier
// replaced, the endpoint's own words kept; query values anonymized.
// queryKeys are query parameters that name nothing of the account's: kept.
var queryKeys = set("beta", "model", "entrypoint", "slug", "limit", "offset", "page", "cursor", "after", "before",
	"pageToken", "page_token", "client_version", "version", "scope", "enabled_only", "compact", "alt", "key", "stream",
	"comment_hint", "vn")

func (a *Anonymizer) Path(p string) string {
	path, query, hasQuery := strings.Cut(p, "?")
	segs := strings.Split(path, "/")
	for i, s := range segs {
		if s == "" || pathWord(s) {
			continue
		}
		segs[i] = a.tokens(s, a.Text)
	}
	out := strings.Join(segs, "/")
	if !hasQuery {
		return out
	}
	parts := strings.Split(query, "&")
	for i, part := range parts {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		if !queryKeys[k] { // a parameter of the provider's own (a feature's codename, say)
			k = a.keyFake("param", k)
		}
		if dv, err := url.QueryUnescape(v); err == nil && !enumLike(dv) || len(v) > 24 {
			v = url.QueryEscape(a.Default(dv))
		}
		parts[i] = k + "=" + v
	}
	return out + "?" + strings.Join(parts, "&")
}

// pathWord is a path segment that names the endpoint: lowercase words, no digits run.
func pathWord(s string) bool {
	if len(s) > 32 || reToken.FindString(s) == s && len(s) >= 8 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c == '-' || c == '_' || c == '.' || c == ':' || c >= '0' && c <= '9') {
			return false
		}
	}
	return !reUUID.MatchString(s) && !reHex.MatchString(s) && !reDigits.MatchString(s)
}
