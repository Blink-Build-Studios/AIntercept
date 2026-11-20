package capture

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// What a provider says about the account itself (its plan, limits and usage,
// entitlements, feature flags, early-access programs, the models it offers)
// is replaced wherever it is:
//
//   - a response header's value, unless it describes the exchange itself
//     (exchangeHeaders): an identifier becomes a fake, anything else a
//     placeholder (Opaque). Rate-limit state the code reads (providerHeaders)
//     is replaced too: the tests need its header, not the account's state;
//   - a body the code doesn't read whole (a control-plane call's: flags,
//     usage, catalogs, the account's profile) keeps only its structure:
//     nesting, value types and generic field names (genericKeys). Every other
//     field name is a fake (flag_1, key_1), every string a placeholder, every
//     number 0 and every boolean false. The fields an extractor reads in such
//     a body (readsOf) are anonymized as any value is;
//   - in a body read whole (a model call's, an analytics batch's), the plan
//     and tier fields (accountKeys), the in-band rate-limit events
//     (accountEvents) and in-band response headers;
//   - a model's name, unless one of the corpus's own model calls uses it
//     (Options.Models, and the case's own call): a fake of the same vendor;
//   - an early-access program's name (access_programs).

// exchangeHeaders are response headers that describe the exchange, not the
// account: kept as any header is.
var exchangeHeaders = set(":status", "content-type", "content-encoding", "content-length", "transfer-encoding",
	"connection", "upgrade", "sec-websocket-extensions", "set-cookie")

// ResponseHeaders anonymizes a response's headers: their names kept, their
// values replaced unless they describe the exchange.
func (a *Anonymizer) ResponseHeaders(h map[string]string) (map[string]string, error) {
	if err := CheckRedacted(h); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(h))
	for _, k := range sortedKeys(h) { // fakes are numbered in the order met
		out[k] = a.responseHeader(k, h[k])
	}
	return out, nil
}

func (a *Anonymizer) responseHeader(k, v string) string {
	lk := strings.ToLower(k)
	switch {
	case v == "" || v == redacted:
		return v
	case exchangeHeaders[lk]:
		h, _ := a.Headers(map[string]string{k: v})
		return h[k]
	case strings.HasSuffix(lk, "-id") || lk == "request-id" || lk == "openai-organization":
		return a.tokens(v, a.Text) // ids the code reads (request, workspace, organization) as fakes
	}
	return a.Opaque(v)
}

var (
	reNumber = regexp.MustCompile(`^-?\d+(\.\d+)?$`)
	reWordy  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.:-]*$`)
)

// Opaque is a value nothing reads, or account state: an identifier as a
// fake, a number 0, a boolean false, a model's name as a fake, one word as a
// fake word (value_1, the same everywhere in the case: its length would say
// which of a few it was), anything longer as placeholder text.
func (a *Anonymizer) Opaque(s string) string {
	switch {
	case s == "" || s == redacted:
		return s
	case reNumber.MatchString(s):
		return "0"
	case s == "true" || s == "false":
		return "false"
	case s == "True" || s == "False":
		return "False"
	case reModel.MatchString(s):
		return a.modelName(s)
	case isIP(s), reEmail.FindString(s) == s, reUUID.FindString(s) == s:
		return a.ID(s)
	case reWordy.MatchString(s):
		if longestRun(s) >= 12 && randomish(s) { // a random id (req_011CV…), not words (max_tier_20x)
			return a.ID(s)
		}
		return a.keyFake("value", s)
	}
	return a.tokens(s, a.Text)
}

// longestRun is the length of s's longest run of letters and digits.
func longestRun(s string) int {
	best, n := 0, 0
	for _, c := range s {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			n++
			best = max(best, n)
		} else {
			n = 0
		}
	}
	return best
}

// accountKeys are fields that name the account's plan or tier.
var accountKeys = set("plan_type", "chatgpt_plan_type", "planType", "subscription_type", "subscriptionType",
	"subscription_status", "rate_limit_tier", "rateLimitTier", "organization_rate_limit_tier", "user_rate_limit_tier",
	"seat_tier", "seatTier", "billing_type", "billingType", "organization_type", "plan_display_name",
	"plan_display_label", "account_tier", "usage_tier", "active_limit")

// accountKey is a field that names the account's plan or tier (not a
// request's service tier, which is the protocol's).
func accountKey(k string) bool {
	l := strings.ToLower(k)
	return accountKeys[k] || strings.Contains(l, "plan_type") || strings.Contains(l, "plantype") ||
		(strings.HasSuffix(l, "_tier") || strings.HasSuffix(k, "Tier")) && !strings.Contains(l, "service")
}

// accountEvents are stream events that carry the account's rate-limit state
// in band: every value in one but its type is replaced.
var accountEvents = set("codex.rate_limits")

// inBandHeaders are stream events that carry the response's headers in band.
var inBandHeaders = set("codex.response.metadata")

// modelKey is a field whose value is a model's name.
func modelKey(k string) bool {
	return k == "model" || k == "slug" || k == "default_model" || strings.HasSuffix(k, "_model") || strings.HasSuffix(k, "Model") ||
		k == "model_slug" || k == "requested_model"
}

// isModelSlug is a value that is a model's name with a version (gpt-5.1,
// claude-opus-4-5), not a product word (claude-code, codex-tui).
func isModelSlug(s string) bool {
	return reModel.MatchString(s) && strings.ContainsAny(s, "0123456789") && !strings.HasPrefix(s, "claude-code") &&
		!strings.HasPrefix(s, "claude-cli")
}

// modelName is a model's name: kept when one of the corpus's model calls
// uses it, else a fake of the same vendor (claude-model-1), the same
// everywhere in the case.
func (a *Anonymizer) modelName(s string) string {
	if a.models[s] {
		return s
	}
	vendor, _, _ := strings.Cut(s, "-")
	if len(vendor) == 2 && vendor[0] == 'o' { // o3, o4-mini
		vendor = "o"
	}
	return a.fake("model:"+s, func(n int) string { return fmt.Sprintf("%s-model-%d", vendor, n) })
}

// keyAt is an object key where it is: an early-access program's name and a
// model's name are fakes; anything else as key judges it.
func (a *Anonymizer) keyAt(n *node, k string) string {
	switch {
	case n.reduced():
		return a.reduceKey(n, k)
	case n.parent != nil && n.parent.key == "properties" && mcpSchema(n):
		return a.keyFake("param", k) // an MCP tool's parameter can name a company's systems, as its name can
	case n.parent != nil && n.parent.key == "access_programs":
		return a.keyFake("program", k)
	case reModel.MatchString(k):
		return a.modelName(k)
	}
	return a.key(k)
}

// mcpSchema is whether the value is inside an MCP tool's input schema.
func mcpSchema(n *node) bool {
	in, mcp := schemaOf(n)
	return in && mcp
}

// schemaOf is whether the value is inside a tool's input schema, and whether
// the tool is an MCP server's.
func schemaOf(n *node) (in, mcp bool) {
	for p := n.parent; p != nil; p = p.parent {
		if p.key == "input_schema" || p.key == "inputSchema" || p.key == "parameters" {
			return true, strings.HasPrefix(p.siblings["name"], "mcp__")
		}
	}
	return false, false
}

// scalarAt is a number, boolean or null where it is: replaced inside an
// in-band rate-limit event, kept elsewhere.
func (a *Anonymizer) scalarAt(n *node, raw string) string {
	if n.reduced() {
		return reduceScalar(raw)
	}
	if raw == "null" || !(accountKey(n.key) || n.inEvent(accountEvents) || n.parent != nil && n.parent.key == "access_programs") {
		return raw
	}
	if raw == "true" || raw == "false" {
		return "false"
	}
	return "0"
}

// keyFake is a stable fake for a name: prefix_1, prefix_2, ….
func (a *Anonymizer) keyFake(prefix, k string) string {
	if f, ok := a.keys[prefix+":"+k]; ok {
		return f
	}
	a.nkeys[prefix]++
	f := fmt.Sprintf("%s_%d", prefix, a.nkeys[prefix])
	a.keys[prefix+":"+k] = f
	return f
}

// ── Bodies the code doesn't read whole ───────────────────────────────────────

// modelAPI is a model call's endpoint.
func modelAPI(host, path string) bool {
	p, _, _ := strings.Cut(path, "?")
	switch APIOf(host, path) {
	case "anthropic_messages", "openai_responses", "openai_chat_completions", "gemini":
		return true
	case "chatgpt_backend":
		return p == "/backend-api/codex/responses"
	}
	return false
}

// readsOf is what the code reads of a body: all of it (a model call's, an
// analytics batch's), or only fields (paths, "[]" an array's elements, "*"
// any key). A new extractor of a control-plane body adds its fields here.
func readsOf(host, path string, status int, resp bool) (all bool, fields []string) {
	p, _, _ := strings.Cut(path, "?")
	switch {
	case modelAPI(host, path) && !(resp && strings.HasPrefix(p, "/v1/messages/count_tokens")):
		return true, nil
	case !resp && events.IsAnalyticsRequest(host, path):
		return true, nil
	}
	// Any call's body is read for its model and settings (events.RequestMeta,
	// events.ResponseMeta, events.ModelFromRequestBody).
	if resp {
		fields = append(fields, "id", "type", "model", "stop_reason", "service_tier", "inference_geo", "speed", "usage",
			"message.id", "message.model", "message.usage", "delta.stop_reason", "choices[].finish_reason", "response.id",
			"response.model", "response.status", "response.service_tier", "response.reasoning.effort",
			"response.incomplete_details.reason")
	} else {
		fields = append(fields, "model", "stream", "max_tokens", "max_output_tokens", "output_config.effort",
			"reasoning.effort", "thinking.type", "thread.type", "thread.previous_message_id", "previous_response_id",
			"metadata.user_id", "generate", "service_tier", "speed")
	}
	if resp && status >= 400 { // events.APIError
		fields = append(fields, "error.type", "error.message", "error.code", "detail", "message")
	}
	switch {
	case resp && strings.HasPrefix(p, "/v1/messages/count_tokens"): // the count of input tokens
		fields = append(fields, "input_tokens")
	case resp && events.IsAccountPath(p): // events.AccountFromResponse
		fields = append(fields, "account.uuid", "account.email", "account.full_name", "account.display_name",
			"organization.uuid", "organization.name", "oauth_account.account_uuid", "oauth_account.account_email",
			"oauth_account.display_name", "oauth_account.organization_name", "oauth_account.organization_uuid")
	case resp && p == "/backend-api/wham/accounts/check": // identity's ChatGPT lookup
		for _, f := range []string{"id", "account_id", "name", "is_deactivated"} {
			fields = append(fields, "accounts[]."+f, "accounts.*.account."+f)
		}
	case p == "/backend-api/ps/mcp": // events.MCPFromBodies
		fields = append(fields, "jsonrpc", "id", "method", "params.name", "result.tools[].name", "error.code")
	case strings.HasSuffix(p, "/alpha/search"): // events.WebSearchFromBodies
		fields = append(fields, "commands.search_query", "settings.external_web_access", "results[].domain")
	}
	return false, fields
}

// covers is whether path is one of fields, or above or inside one. A field's
// "*" is any key of an object (not an array's elements).
func covers(fields []string, path string) bool {
	ps := strings.Split(path, ".")
	for _, f := range fields {
		fs := strings.Split(f, ".")
		match := true
		for i := 0; i < min(len(fs), len(ps)); i++ {
			x, y := fs[i], ps[i]
			ok := x == y || x == "*" && !strings.HasSuffix(y, "[]") ||
				i == len(fs)-1 && x == strings.TrimSuffix(y, "[]") || // inside an array field
				i == len(ps)-1 && strings.TrimSuffix(x, "[]") == y // the array above a field's elements
			if !ok {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// genericKeys are field names that say nothing about an account: a body
// reduced to its structure keeps them, so it still reads as what it is.
var genericKeys = set("id", "type", "object", "name", "slug", "title", "description", "display_name", "label", "kind",
	"status", "state", "value", "values", "data", "items", "results", "result", "list", "models", "plugins",
	"accounts", "account", "features", "error", "message", "code", "detail", "created_at", "updated_at", "url",
	"version", "enabled", "default", "has_more", "next_page", "next_cursor", "cursor", "pagination", "limit",
	"offset", "total", "count", "page", "metadata", "config", "settings", "headers", "source", "experiment",
	"experimentResult", "jsonrpc", "method", "params", "tools", "commands", "search_query", "domain", "snippet",
	"content", "text", "role", "input_schema", "properties", "required", "client_data",
	// A feature-flag SDK's own schema.
	"defaultValue", "rules", "force", "condition", "variations", "weights", "coverage", "hashAttribute", "attributes")

// flagContainers hold feature flags or switches by name.
var flagContainers = set("features", "flags", "gates", "feature_gates", "dynamic_configs", "layer_configs",
	"experiments", "client_data")

// reducedContainers hold what a provider (or a client, for the provider's
// classifier) says about the account inside a body read whole: moderation
// verdicts, safety-classifier context with its experiment holdbacks. Reduced
// to their structure as an unread body is.
var reducedContainers = set("openai_chatgpt_moderation_metadata", "safeguard_results", "classifier_context")

// reduced is whether the value is inside a reduced container.
func (n *node) reduced() bool {
	for p := n.parent; p != nil; p = p.parent {
		if reducedContainers[p.key] {
			return true
		}
	}
	return false
}

// Unread anonymizes a body the code doesn't read whole: the fields it reads
// as any value is, the rest reduced to its structure.
func (a *Anonymizer) Unread(b []byte, fields []string) ([]byte, error) {
	return rewrite(b, rewriter{
		str: func(n *node, s string) string {
			if covers(fields, n.path()) {
				return a.str(n, s)
			}
			return a.reduceStr(s)
		},
		key: func(n *node, k string) string {
			if covers(fields, n.path()) {
				return a.keyAt(n, k)
			}
			return a.reduceKey(n, k)
		},
		scalar: func(n *node, raw string) string {
			if covers(fields, n.path()) {
				return raw
			}
			return reduceScalar(raw)
		},
	})
}

// reduceStr is a string of a body reduced to its structure: JSON inside it
// reduced too, anything else Opaque.
func (a *Anonymizer) reduceStr(s string) string {
	if t := strings.TrimSpace(s); len(t) > 1 && (t[0] == '{' || t[0] == '[') && json.Valid([]byte(t)) {
		if out, err := a.Unread([]byte(s), nil); err == nil {
			return string(out)
		}
	}
	return a.Opaque(s)
}

// reduceKey is a field name of a body reduced to its structure: a flag's a
// fake flag name, a model's a fake model, a generic one kept, any other a
// fake key.
func (a *Anonymizer) reduceKey(n *node, k string) string {
	switch {
	case n.parent != nil && flagContainers[n.parent.key]:
		return a.keyFake("flag", k)
	case reModel.MatchString(k):
		return a.modelName(k)
	case genericKeys[k]:
		return k
	}
	return a.keyFake("key", k)
}

// reduceScalar is a number or boolean of a body reduced to its structure: 0
// or false (null stays null).
func reduceScalar(raw string) string {
	switch raw {
	case "null":
		return raw
	case "true", "false":
		return "false"
	}
	return "0"
}

// BodyOf anonymizes a call's body (plain: decoded) as the code reads it: all
// of it, or only some fields (readsOf).
func (a *Anonymizer) BodyOf(plain []byte, contentType, host, path string, status int, resp bool) ([]byte, error) {
	all, fields := readsOf(host, path, status, resp)
	if all || len(plain) == 0 {
		return a.Body(plain, contentType)
	}
	if json.Valid(plain) {
		return a.Unread(plain, fields)
	}
	if !strings.Contains(contentType, "x-ndjson") && !strings.Contains(contentType, "jsonl") {
		return a.Body(plain, contentType)
	}
	var lines []string
	for _, l := range strings.Split(string(plain), "\n") {
		if json.Valid([]byte(l)) { // NDJSON
			b, err := a.Unread([]byte(l), fields)
			if err != nil {
				return nil, err
			}
			l = string(b)
		} else {
			l = a.tokens(l, a.Text)
		}
		lines = append(lines, l)
	}
	return []byte(strings.Join(lines, "\n")), nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
