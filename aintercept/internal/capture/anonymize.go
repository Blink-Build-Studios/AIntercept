// Package capture turns one real call from the raw files into a corpus case
// (docs/PROVIDERS.md, "The test rule"): its request and response with every
// identifier, credential and piece of content replaced, and its structure kept
// exactly, so it can be committed and replayed through the observer's code.
package capture

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/convo"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// Anonymizer replaces what can identify a person or a company, consistently
// within one case: the same identifier always becomes the same fake, so links
// between fields (a session id in a header and the body, a tool call and its
// result) still hold.
//
//   - Identifiers (UUIDs, provider ids like toolu_… and req_…, hex and numeric
//     ids, emails, IPs) become fakes of the same shape.
//   - Text (prompts, replies, tool inputs and results, system prompts, names,
//     paths, hostnames, anything not known to be structure) becomes
//     placeholder text of the same length and structure: letters x, digits 0,
//     punctuation, spaces and line breaks as they were. The harness's markers
//     the code reads (convo.HarnessNotes, patch headers, exit codes, exec
//     scripts' tools.<name>( calls) are kept.
//   - JSON inside a string stays valid JSON; base64 becomes the same length
//     of valid base64 (decoded and anonymized when it holds JSON).
//   - Field names, enum values, model names, versions and numbers are kept.
//
// When it can't tell, it replaces.
type Anonymizer struct {
	fakes map[string]string
	n     int
	// keys are fake names (flag_1, value_1, program_1) by prefix and real
	// name; nkeys counts each prefix's.
	keys  map[string]string
	nkeys map[string]int
	// models are the model names kept: the corpus's own model calls' (Models);
	// any other is a fake. modelCall: the case is a model call, whose own
	// model joins them.
	models    map[string]bool
	modelCall bool
	// Who is each real identifier that names a person, a device, a session or
	// an organization (accounts, orgs, emails, IPs, names), for the real-data
	// denylist.
	Who map[string]bool
}

// NewAnonymizer starts a case's anonymizer.
func NewAnonymizer() *Anonymizer {
	return &Anonymizer{fakes: map[string]string{}, Who: map[string]bool{}, keys: map[string]string{},
		nkeys: map[string]int{}, models: map[string]bool{}}
}

// Replaced is every real value replaced with a fake, for checking that none
// survives.
func (a *Anonymizer) Replaced() []string {
	out := make([]string, 0, len(a.fakes))
	for k, v := range a.fakes {
		if k != v {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// ── Identifiers ──────────────────────────────────────────────────────────────

var (
	reUUID     = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	reEmail    = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	reIPv4     = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	reIPv6     = regexp.MustCompile(`(?i)\b(?:[0-9a-f]{1,4}:){3,7}[0-9a-f]{1,4}\b`)
	rePrefixed = regexp.MustCompile(`\b[a-z][a-z0-9]{0,15}[_-][A-Za-z0-9]{8,}(?:[_-][A-Za-z0-9]{4,})*\b`)
	reHex      = regexp.MustCompile(`(?i)\b[0-9a-f]{16,}\b`)
	reDigits   = regexp.MustCompile(`\b\d{6,}\b`)
	reJWT      = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*`)
	reToken    = regexp.MustCompile(`\b[A-Za-z0-9]*[0-9][A-Za-z0-9]*[A-Za-z][A-Za-z0-9]*\b|\b[A-Za-z0-9]*[A-Za-z][A-Za-z0-9]*[0-9][A-Za-z0-9]*\b`)
	reVersion  = regexp.MustCompile(`^v?\d+(\.\d+){1,3}([-+][0-9A-Za-z.]+)?$`)
	reTime     = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}([T ]\d{2}:\d{2}(:\d{2}(\.\d+)?)?(Z|[+-]\d{2}:?\d{2})?)?$`)
	reModel    = regexp.MustCompile(`^(claude-[a-z0-9.-]+|gpt-[A-Za-z0-9.-]+|o\d(-[a-z0-9.-]+)?|gemini-[a-z0-9.-]+|codex-[a-z0-9.-]+)$`)
	reB64      = regexp.MustCompile(`^[A-Za-z0-9+/_-]{40,}={0,2}$`)
)

// fake is the stable fake for one identifier of a kind.
func (a *Anonymizer) fake(orig string, make func(n int) string) string {
	if f, ok := a.fakes[orig]; ok {
		return f
	}
	a.n++
	f := make(a.n)
	a.fakes[orig] = f
	a.fakes[f] = f // a fake is never replaced again
	return f
}

// fill is a fake of the same length and character classes as orig: letters
// x or X, digits 0 (hex: 0), others as they were, with the counter n in the
// last letter or digit positions so fakes stay distinct.
func fill(orig string, n int, hex bool) string {
	r := []rune(orig)
	digits := []rune(fmt.Sprintf("%d", n))
	if hex {
		digits = []rune(fmt.Sprintf("%x", n))
	}
	out := make([]rune, len(r))
	k := len(digits) - 1
	for i := len(r) - 1; i >= 0; i-- {
		c := r[i]
		alnum := unicode.IsDigit(c) || unicode.IsLetter(c)
		switch {
		case alnum && k >= 0 && c < utf8.RuneSelf:
			out[i] = digits[k]
			k--
		case unicode.IsDigit(c) || hex && isHex(c):
			out[i] = '0'
		case unicode.IsUpper(c):
			out[i] = 'X'
		case unicode.IsLetter(c):
			out[i] = 'x'
		default:
			out[i] = c
		}
	}
	return string(out)
}

func isHex(c rune) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }

// ID is a whole value that is one identifier, as a fake of the same shape.
func (a *Anonymizer) ID(s string) string {
	if s == "" || s == redacted || strings.Trim(s, "0-.:") == "" {
		return s // all zeros: an unset trace id
	}
	if isModelSlug(s) {
		return a.modelName(s)
	}
	switch {
	case reUUID.MatchString(s) && len(s) == 36:
		a.Who[s] = true
		return a.fake(s, func(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012x", n) })
	case reEmail.MatchString(s) && reEmail.FindString(s) == s:
		return a.email(s)
	case isIP(s):
		return a.ip(s)
	}
	if reUUID.MatchString(s) || reEmail.MatchString(s) { // ids inside a longer value (<uuid>:5)
		return a.tokens(s, a.glue)
	}
	if p, rest, ok := strings.Cut(s, "_"); ok && rest != "" && isWord(p) {
		return a.fake(s, func(n int) string { return p + "_" + fill(rest, n, false) })
	}
	if p, rest, ok := strings.Cut(s, "-"); ok && rest != "" && isWord(p) && !strings.Contains(rest, "-") {
		if p == "user" || p == "org" || p == "acct" {
			a.Who[s] = true // OpenAI's user-…, org-…
		}
		return a.fake(s, func(n int) string { return p + "-" + fill(rest, n, false) })
	}
	hex := true
	for _, c := range s {
		if !isHex(c) && c != '-' {
			hex = false
		}
	}
	if hex && len(s) >= 16 {
		a.Who[s] = true // device ids
	}
	return a.fake(s, func(n int) string { return fill(s, n, hex) })
}

// randomish is an identifier made of random characters (a digit or an
// uppercase letter in it), not a name made of words (prompt_suggestion).
func randomish(s string) bool {
	return strings.ContainsAny(s, "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ")
}

func isWord(s string) bool {
	if s == "" || len(s) > 16 {
		return false
	}
	for _, c := range s {
		if c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}

func isIP(s string) bool {
	return reIPv4.FindString(s) == s || strings.Count(s, ":") >= 2 && reIPv6.FindString(s) == s
}

func (a *Anonymizer) email(s string) string {
	a.Who[strings.ToLower(s)] = true
	return a.fake(strings.ToLower(s), func(n int) string { return fmt.Sprintf("person%d@example.com", n) })
}

func (a *Anonymizer) ip(s string) string {
	a.Who[s] = true
	if strings.Contains(s, ":") {
		return a.fake(s, func(n int) string { return fmt.Sprintf("2001:db8::%x", n) })
	}
	return a.fake(s, func(n int) string { return fmt.Sprintf("192.0.2.%d", n%250+1) })
}

// tokens replaces each identifier inside s (emails, UUIDs, IPs, JWTs,
// prefixed, hex and numeric ids) and hands the rest to rest.
func (a *Anonymizer) tokens(s string, rest func(string) string) string {
	type span struct {
		i, j int
		f    func(string) string
	}
	var spans []span
	taken := func(i, j int) bool {
		for _, sp := range spans {
			if i < sp.j && j > sp.i {
				return true
			}
		}
		return false
	}
	add := func(re *regexp.Regexp, f func(string) string) {
		for _, m := range re.FindAllStringIndex(s, -1) {
			if !taken(m[0], m[1]) {
				spans = append(spans, span{m[0], m[1], f})
			}
		}
	}
	add(reJWT, func(t string) string { return fakeJWT(a, t) })
	add(reEmail, a.email)
	add(reUUID, a.ID)
	add(reIPv4, func(t string) string {
		if isIP(t) {
			return a.ip(t)
		}
		return rest(t)
	})
	add(reIPv6, func(t string) string { return a.ip(t) })
	add(rePrefixed, func(t string) string {
		if randomish(t) {
			return a.ID(t)
		}
		return rest(t)
	})
	add(reHex, a.ID)
	add(reDigits, a.ID)
	add(reToken, func(t string) string {
		if len(t) >= 12 {
			return a.ID(t)
		}
		return rest(t)
	})
	sort.Slice(spans, func(x, y int) bool { return spans[x].i < spans[y].i })
	var b strings.Builder
	at := 0
	for _, sp := range spans {
		b.WriteString(rest(s[at:sp.i]))
		b.WriteString(sp.f(s[sp.i:sp.j]))
		at = sp.j
	}
	b.WriteString(rest(s[at:]))
	return b.String()
}

// ── Text ─────────────────────────────────────────────────────────────────────

// markers are the harness's words the code reads in text; they're kept.
var markers = func() []string {
	m := append([]string(nil), convo.HarnessNotes...)
	for _, n := range convo.HarnessNotes {
		if strings.HasPrefix(n, "<") && !strings.Contains(n, " ") {
			m = append(m, "</"+strings.Trim(n, "<>")+">", strings.TrimSuffix(n, ">")+">")
		}
	}
	m = append(m, convo.ImageNote, "</system-reminder>", "<bash-input>", "</bash-input>", "<pasted_content>", "</pasted_content>",
		"<command-name>", "</command-name>", "<command-message>", "</command-message>", "<command-args>", "</command-args>",
		"The user sent a new message while you were working:", "This is how Claude Code surfaces messages the user sends mid-turn",
		" sent a message while you were working:",
		"*** Begin Patch", "*** End Patch", "*** End of File", "*** Add File: ", "*** Update File: ", "*** Delete File: ", "*** Move to: ")
	return m
}()

// markerRes are patterned markers: exit codes, an exec script's tool calls and commands.
var markerRes = []*regexp.Regexp{
	regexp.MustCompile(`\\[nrtbfv0"'\\/` + "`" + `]`), // an escape inside a script or JSON text (\n): what it stands for stays

	regexp.MustCompile(`(?i)\\*"?exit_code\\*"?\s*[:=]\s*-?\d+|(?:exited with code|exit code:?)\s+-?\d+`),
	regexp.MustCompile(`\btools\.[A-Za-z_][A-Za-z0-9_]*\s*\(`),
	regexp.MustCompile(`\bcmd\s*:`),
}

// Text is s as placeholder text: the same length in bytes and runes, the same
// line breaks, spaces and punctuation, letters x or X, digits 0; the
// harness's markers kept.
func (a *Anonymizer) Text(s string) string {
	if s == "" {
		return s
	}
	keep := make([]bool, len(s))
	for _, m := range markers {
		for i := 0; ; {
			j := strings.Index(s[i:], m)
			if j < 0 {
				break
			}
			for k := i + j; k < i+j+len(m); k++ {
				keep[k] = true
			}
			i += j + len(m)
		}
	}
	for _, re := range markerRes {
		for _, m := range re.FindAllStringIndex(s, -1) {
			for k := m[0]; k < m[1]; k++ {
				keep[k] = true
			}
		}
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if keep[i] {
			b.WriteString(s[i : i+size])
		} else {
			b.WriteString(placeholder(r, size))
		}
		i += size
	}
	// An exec script's tool names are kept, but an MCP tool's is replaced as
	// everywhere (it can name a company's own systems).
	return reScriptMCP.ReplaceAllStringFunc(b.String(), func(m string) string { return "tools." + a.ToolName(m[len("tools."):]) })
}

var reScriptMCP = regexp.MustCompile(`\btools\.mcp__[A-Za-z0-9_]+`)

// programs are words of a shell command kept as they are: common programs
// and their subcommands name what ran, never who ran it, and the pipeline
// reads them (events.CallDetail's commands).
var programs = set("cd", "ls", "cat", "git", "npm", "pnpm", "yarn", "npx", "bun", "go", "cargo", "make", "uv", "docker",
	"kubectl", "gh", "pip", "helm", "terraform", "python", "python3", "node", "bash", "sh", "zsh", "sed", "awk", "grep",
	"rg", "find", "head", "tail", "wc", "sort", "uniq", "echo", "export", "source", "sudo", "env", "time", "test", "build",
	"run", "install", "commit", "push", "pull", "status", "diff", "log", "add", "checkout", "rm", "mv", "cp", "mkdir",
	"touch", "curl", "jq", "tee", "xargs", "true", "vet", "fmt", "lint", "pytest", "ruff", "tsc", "eslint", "apply_patch")

var reWord = regexp.MustCompile(`[A-Za-z0-9_][A-Za-z0-9_.+-]*`)

// Command is a shell command line as placeholder text, its programs kept.
func (a *Anonymizer) Command(s string) string {
	out := []byte(a.Text(s))
	for _, m := range reWord.FindAllStringIndex(s, -1) {
		if programs[s[m[0]:m[1]]] {
			copy(out[m[0]:m[1]], s[m[0]:m[1]])
		}
	}
	return string(out)
}

// placeholder is the rune standing in for r: the same class and UTF-8 length.
func placeholder(r rune, size int) string {
	switch {
	case r == utf8.RuneError && size == 1:
		return "x"
	case unicode.IsDigit(r):
		return [...]string{"", "0", "٠", "०", "𝟎"}[size]
	case unicode.IsUpper(r):
		return [...]string{"", "X", "Ä", "Ａ", "𝐗"}[size]
	case unicode.IsLetter(r):
		return [...]string{"", "x", "é", "あ", "𝑥"}[size]
	}
	return string(r) // spaces, punctuation, symbols, emoji
}

// ── Values by where they are ─────────────────────────────────────────────────

const redacted = "<redacted>"

// keepKeys are fields whose string values are structure (enums, model names,
// formats), kept as sent.
var keepKeys = set("type", "role", "model", "stop_reason", "status", "object", "media_type", "service_tier",
	"inference_geo", "speed", "effort", "event", "event_type", "event_name", "phase", "code", "tool_choice",
	"include", "ttl", "verbosity", "finish_reason", "reason", "originator", "request_kind", "thread_source",
	"history_initialization", "sandbox", "sandbox_mode", "turn_trigger", "subagent_kind", "approval_policy",
	"personality", "collaboration_mode", "reasoning_effort", "model_provider",
	"initialization_mode", "unit", "auth_mode", "method", "jsonrpc", "protocolVersion", "level", "kind",
	"source", "summary", "format", "encoding_format", "service", "detail", "object_type",
	"model_slug", "requested_model", "transport", "service.name", "session_source", "stop", "name_source")

// textKeys are fields of reply and prompt text that may arrive in pieces
// (deltas): placeholder only, so the pieces still add up to the whole.
var textKeys = set("text", "thinking", "delta", "content", "system", "instructions", "input", "output",
	"message", "description", "title", "prompt", "query", "summary_text", "transcript", "refusal")

// b64Keys hold base64 blobs: images, files, signatures, encrypted reasoning.
var b64Keys = set("data", "signature", "encrypted_content", "encrypted_output", "file_data", "image_url", "redacted_thinking")

// toolMarkers are keys beside a "name" that make it a tool's, function's or
// metric's name (structure) rather than a person's or an organization's.
var toolMarkers = set("input_schema", "parameters", "arguments", "input", "unit", "sum", "gauge",
	"histogram", "dataPoints", "server_name", "call_id")

// toolTypes are the "type" values of a tool, a call of one, or a namespace of
// them: the "name" beside one is the tool's. (A directory's or a person's list
// of connectors also has types and names: those are replaced.)
var toolTypes = set("tool_use", "server_tool_use", "mcp_tool_use", "function_call", "custom_tool_call", "mcp_call",
	"function", "custom", "namespace", "tool_reference", "tool", "web_search_call", "local_shell_call", "json_schema")

// reServerTool is a provider's versioned server tool type (advisor_20260301).
var reServerTool = regexp.MustCompile(`^[a-z_]+_\d{8}$`)

// toolName is whether a "name" sits in a tool's object.
func toolName(n *node) bool {
	if hasAny(n.siblings, toolMarkers) {
		return true
	}
	t := n.siblings["type"]
	return toolTypes[t] || reServerTool.MatchString(t) || strings.HasPrefix(t, "web_search_") || strings.HasPrefix(t, "web_fetch_") ||
		strings.HasPrefix(t, "code_execution_") || strings.HasPrefix(t, "bash_") || strings.HasPrefix(t, "text_editor_") ||
		strings.HasPrefix(t, "computer_") || strings.HasPrefix(t, "memory_") || strings.HasPrefix(t, "tool_search_")
}

func set(ks ...string) map[string]bool {
	m := map[string]bool{}
	for _, k := range ks {
		m[k] = true
	}
	return m
}

// str is a JSON string value, judged by where it is.
func (a *Anonymizer) str(n *node, s string) string {
	if s == "" {
		return s
	}
	k := n.key
	switch {
	case n.reduced():
		return a.reduceStr(s)
	case k == "required" && mcpSchema(n):
		return a.keyFake("param", s)
	case k == "required" && enumLike(s):
		if in, _ := schemaOf(n); in { // a built-in tool's parameter names, as its schema's keys
			return s
		}
		return a.Default(s)
	case k == "user_id" && n.under("metadata"):
		return a.userID(s)
	case k == "name" && toolName(n):
		return a.ToolName(s)
	case k == "server_name" || k == "server_label":
		return a.mcpPart(s)
	case k != "type" && n.inEvent(accountEvents):
		return a.Opaque(s)
	case n.parent != nil && n.parent.key == "headers" && n.inEvent(inBandHeaders):
		return a.responseHeader(k, s) // a WebSocket turn's response headers, in band
	case accountKey(k) || n.parent != nil && n.parent.key == "access_programs":
		return a.Opaque(s)
	case modelKey(k) && reModel.MatchString(s):
		if a.modelCall && (n.path() == "model" || n.path() == "response.model" || n.path() == "message.model") {
			a.models[s] = true // the call's own model
			return s
		}
		return a.modelName(s)
	case k == "agent_name": // Codex: "/root", or a subagent's name under it
		if rest, ok := strings.CutPrefix(s, "/root"); ok {
			return "/root" + a.Text(rest)
		}
		return a.Text(s)
	case k == "command" || k == "cmd":
		return a.Command(s)
	case k == "partial_json" || k == "arguments":
		return a.JSONText(s)
	case b64Keys[k]:
		return a.B64(s)
	case k == "image_url" || strings.HasPrefix(s, "data:") && strings.Contains(s, ";base64,"):
		return a.B64(s)
	case keepKeys[k] && enumLike(s):
		return s
	case textKeys[k]:
		return a.TextOrJSON(s)
	case k == "id" || strings.HasSuffix(k, "_id") || strings.HasSuffix(k, "Id") || strings.HasSuffix(k, "_uuid") ||
		k == "uuid" || strings.HasSuffix(k, "_key") && !strings.Contains(s, " "):
		if strings.ContainsAny(s, " /\n") || !randomish(s) {
			return a.Default(s)
		}
		return a.ID(s)
	case strings.Contains(k, "email"):
		return a.tokens(s, a.Text)
	case strings.Contains(k, "name"):
		if hasAny(n.siblings, personKeys) || k == "full_name" || k == "given_name" || k == "family_name" {
			a.Who[strings.ToLower(s)] = true // a person's or an organization's name
		}
		return a.Default(s)
	}
	return a.Default(s)
}

// key is an object key: kept unless it is itself an identifier or a path.
func (a *Anonymizer) key(s string) string {
	switch {
	case strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~") || strings.Contains(s, `:\`) || strings.Contains(s, "@") ||
		strings.Contains(s, "://") || reDomain.MatchString(s):
		return a.Default(s)
	case reUUID.FindString(s) == s || reHex.FindString(s) == s && len(s) >= 16 || reDigits.FindString(s) == s ||
		rePrefixed.FindString(s) == s && randomish(s):
		return a.ID(s)
	case strings.HasPrefix(s, "mcp__"):
		return a.ToolName(s)
	case reUUID.MatchString(s) || reEmail.MatchString(s) || reHex.MatchString(s) || reIPv4.MatchString(s):
		return a.tokens(s, a.glue) // ids inside a key (<uuid>:<tool>)
	}
	return s
}

// personKeys are keys beside a name that make it a person's or an
// organization's (an account or a profile, not a tool or a model).
var personKeys = set("email", "email_address", "full_name", "organization_type", "billing_type")

func hasAny(m map[string]string, keys map[string]bool) bool {
	for k := range m {
		if keys[k] {
			return true
		}
	}
	return false
}

// Default is a value of unknown meaning: identifiers inside it replaced,
// everything else placeholder, except whole values that are plainly
// structure (versions, timestamps, model names, booleans, short numbers).
func (a *Anonymizer) Default(s string) string {
	switch {
	case s == "true" || s == "false" || s == "null" || s == redacted:
		return s
	case isIP(s), reEmail.FindString(s) == s, reUUID.FindString(s) == s:
		return a.ID(s)
	case isModelSlug(s):
		return a.modelName(s)
	case reVersion.MatchString(s), reTime.MatchString(s):
		return s
	case len(s) <= 4 && strings.Trim(s, "0123456789.-") == "":
		return s
	}
	if strings.HasPrefix(s, "mcp__") {
		return a.ToolName(s)
	}
	if j, ok := a.jsonInString(s); ok {
		return j
	}
	if reHex.FindString(s) == s {
		return a.ID(s)
	}
	if reB64.MatchString(s) && !strings.ContainsAny(s, "_-") || strings.HasPrefix(s, "data:") {
		return a.B64(s)
	}
	return a.tokens(s, a.Text)
}

// jsonInString anonymizes a string that holds a JSON object or array.
func (a *Anonymizer) jsonInString(s string) (string, bool) {
	t := strings.TrimSpace(s)
	if len(t) < 2 || !(t[0] == '{' && t[len(t)-1] == '}' || t[0] == '[' && t[len(t)-1] == ']') || !json.Valid([]byte(t)) {
		return "", false
	}
	out, err := a.JSON([]byte(s))
	if err != nil {
		return "", false
	}
	return string(out), true
}

// TextOrJSON is text, or JSON when the whole of it is JSON (a reply that
// answers in JSON): what a reply's text and the pieces it streamed in become.
func (a *Anonymizer) TextOrJSON(s string) string {
	if j, ok := a.jsonInString(s); ok {
		return j
	}
	return a.Text(s)
}

// JSONText is a string that is (or may be a piece of) JSON text, like a tool
// call's arguments: anonymized as JSON when it parses, else as text.
func (a *Anonymizer) JSONText(s string) string {
	if json.Valid([]byte(s)) {
		if out, err := a.JSON([]byte(s)); err == nil {
			return string(out)
		}
	}
	return a.Text(s)
}

// glue is what's between the identifiers of a value made of them: its words
// kept (user_, _account_), random-looking runs replaced.
func (a *Anonymizer) glue(s string) string {
	return reWord.ReplaceAllStringFunc(s, func(w string) string {
		if len(w) >= 5 && randomish(w) && strings.ToLower(w) != w {
			return a.ID(w)
		}
		return w
	})
}

// userID is Claude Code's metadata.user_id: a JSON object of ids, or the
// older user_<hex>_account_<uuid>_session_<uuid>.
func (a *Anonymizer) userID(s string) string {
	if j, ok := a.jsonInString(s); ok {
		return j
	}
	return a.tokens(s, a.glue)
}

// ToolName keeps a built-in tool's name; an MCP tool's server and tool
// (mcp__<server>__<tool>) can name a company's systems, so they're
// placeholders, the same everywhere in the case.
func (a *Anonymizer) ToolName(s string) string {
	rest, ok := strings.CutPrefix(s, "mcp__")
	if !ok {
		if looksSecret(s) || strings.ContainsAny(s, " /@") || reDomain.MatchString(s) {
			return a.Default(s)
		}
		return s
	}
	parts := strings.Split(rest, "__")
	for i, p := range parts {
		parts[i] = a.mcpPart(p)
	}
	return "mcp__" + strings.Join(parts, "__")
}

// mcpPart is an MCP server's or tool's name as a distinct placeholder.
func (a *Anonymizer) mcpPart(s string) string {
	if s == "" {
		return s
	}
	return a.fake("mcp:"+s, func(n int) string { return fill(s, n, false) })
}

// enumLike is a value that reads as structure: one short token, not a path,
// an address or a credential.
func enumLike(s string) bool {
	return len(s) <= 64 && !strings.ContainsAny(s, " \t\n@\\~") && !looksSecret(s) && !reDomain.MatchString(s) &&
		!strings.HasPrefix(s, "/Users/") && !strings.HasPrefix(s, "/home/") && strings.Count(s, "/") <= 2
}

// reDomain is a value that names a host (example.com, mcp.example.io).
var reDomain = regexp.MustCompile(`(?i)\b[a-z0-9-]+\.(com|net|org|io|ai|dev|app|co|so|sh|us|uk|de|fr|jp|cn|me|tv|info|edu|gov|xyz|cloud|gg|site|tech|ly|to|cc|in|eu)\b`)

// B64 is a base64 blob (or a data: URL) as the same length of valid base64:
// JSON inside is anonymized and re-encoded, anything else is zeros.
func (a *Anonymizer) B64(s string) string {
	prefix := ""
	if strings.HasPrefix(s, "data:") {
		i := strings.Index(s, ";base64,")
		if i < 0 { // a data: URL of text (data:text/plain,hello): its media type kept, its data placeholder text
			if head, data, ok := strings.Cut(s, ","); ok {
				return head + "," + a.Text(data)
			}
			return a.Text(s)
		}
		prefix, s = s[:i+len(";base64,")], s[i+len(";base64,"):]
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		b, err := enc.DecodeString(s)
		if err != nil {
			continue
		}
		if t := bytes.TrimSpace(b); len(t) > 1 && (t[0] == '{' || t[0] == '[') && json.Valid(t) {
			if out, err := a.JSON(b); err == nil {
				return prefix + enc.EncodeToString(out)
			}
		}
		return prefix + enc.EncodeToString(make([]byte, len(b)))
	}
	if !reB64.MatchString(s) {
		return prefix + a.Default(s)
	}
	return prefix + strings.Repeat("A", len(s))
}

// reHost is a host name with at least three labels (build7.corp.example.com).
var reHost = regexp.MustCompile(`(?i)\b[a-z0-9-]+(\.[a-z0-9-]+)+\.[a-z][a-z-]+\b`)

// UserAgent keeps a user agent's product tokens and platform (the client
// and version are read from it) and replaces anything in it that names a
// machine or a person: addresses, host names, paths, ids.
func (a *Anonymizer) UserAgent(s string) string {
	words := strings.Split(s, " ")
	for i, w := range words {
		core := strings.Trim(w, "();,")
		if core == "" || i == 0 {
			continue
		}
		if isIP(core) || reEmail.MatchString(core) || reUUID.MatchString(core) || strings.ContainsAny(core, "@\\~") ||
			strings.HasPrefix(core, "/") || reHost.MatchString(core) && !reVersion.MatchString(core) ||
			reHex.MatchString(core) || len(core) > 40 {
			words[i] = strings.Replace(w, core, a.Default(core), 1)
		}
	}
	return strings.Join(words, " ")
}

// looksSecret is a value shaped like a credential.
func looksSecret(s string) bool {
	return strings.HasPrefix(s, "sk-") || strings.HasPrefix(s, "eyJ") && strings.Count(s, ".") == 2 ||
		strings.HasPrefix(s, "AIza") || strings.HasPrefix(s, "Bearer ")
}

// JSON anonymizes a JSON document, keeping its bytes but for the strings it replaces.
func (a *Anonymizer) JSON(b []byte) ([]byte, error) {
	return rewrite(b, rewriter{str: a.str, key: a.keyAt, scalar: a.scalarAt})
}

// ── Credentials ──────────────────────────────────────────────────────────────

// fakeJWT replaces a JWT found in content with a fake whose claims are the
// original's, anonymized.
func fakeJWT(a *Anonymizer, t string) string {
	parts := strings.Split(t, ".")
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return a.fake(t, func(n int) string { return fill(t, n, false) })
	}
	out, err := a.JSON(payload)
	if err != nil {
		return a.fake(t, func(n int) string { return fill(t, n, false) })
	}
	return JWT(out)
}

// JWT is an unsigned token carrying claims (a JSON object), shaped like the
// bearer tokens ChatGPT signs in with.
func JWT(claims []byte) string {
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + enc.EncodeToString(claims) + ".fake-signature"
}

// CheckRedacted fails when a credential-bearing header holds anything but
// the redaction marker: raw rows never carry one, so a value means something
// went wrong upstream and the case must not be written.
func CheckRedacted(h map[string]string) error {
	for k, v := range h {
		if events.IsSensitiveHeader(k) && v != "" && v != redacted {
			return fmt.Errorf("header %s holds a credential; raw rows must have it redacted", k)
		}
		if k == ":path" && events.RedactPath(v) != v {
			return fmt.Errorf("the path holds a credential in its query; raw rows must have it redacted")
		}
	}
	return nil
}

// Check fails when any real value this anonymizer replaced, or a credential,
// appears in out.
func (a *Anonymizer) Check(name string, out []byte) error {
	for _, r := range a.Replaced() {
		// A bare number replaced in one place may be a plain number elsewhere
		// (a max-age, a size): only identifiers count.
		if strings.Trim(r, "0123456789") == "" && !a.Who[r] {
			continue
		}
		if len(r) >= 6 && bytes.Contains(out, []byte(r)) {
			return fmt.Errorf("%s: a real value survived (%s…)", name, r[:3])
		}
	}
	for _, re := range reCredentials {
		if re.Match(out) {
			return fmt.Errorf("%s: holds a credential", name)
		}
	}
	return nil
}

// reCredentials are the credentials Check refuses: Anthropic's and OpenAI's
// API keys, and Google's.
var reCredentials = []*regexp.Regexp{regexp.MustCompile(`sk-ant-[a-z]{3}\d\d-[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{30,}`), regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`)}
