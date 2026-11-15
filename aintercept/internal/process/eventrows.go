package process

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

// An event's facts (EventFactsOf): what the flush stores of a Claude Code
// event at insert (docs/PIPELINE.md §3.2): its summary, marker and internal
// flag, and what the rollups count of it.

// Event row values, as session.py has them.
var (
	internalNames = map[string]bool{"declared_tool_set_held": true, "render_glyph_cardinality": true, "tree_sitter_parse_abort": true, "bash_ast_too_complex": true,
		"tool_use_progress": true} // tool-progress ticks (handoff §11Q.3)
	internalPrefixes = []string{"tether_", "wire_", "reminder_fold_", "spinner_"}
	summarySkip      = map[string]bool{
		"requestId": true, "clientRequestId": true, "cc_prompt_id": true, "toolUseID": true, "tool_use_id": true,
		"messageID": true, "message_id": true, "previousRequestId": true, "invokingRequestId": true,
		"queryChainId": true, "subscription_type": true, "rh": true,
	}
)

const (
	summaryPairs      = 6
	summaryValueRunes = 40
)

// listedEvent is one of a session's Claude Code events, with what its row and the
// state readout read of its metadata (json.loads, then session.py's _str, _int
// and _true; the latest counts as json_extract_string, then TRY_CAST). A string
// is "" for None: no value read here tells the two apart.
type listedEvent struct {
	ts, seq, rowNS int64
	corr, name     string // name: "(unnamed)" for none
	md             string // kept only until its batch is numbered
	part           string // the events part it came from, and its place among
	pos            int32  // the part's Claude Code events (to number a batch two parts hold)
	marker         uint8  // markerNone, markerDestructive, markerFrustrated

	device, prompt, head string
	summary              string
	reqID, tool          string // requestId; toolUseID, else tool_use_id
	toolName             string // toolName (the tool link's label)
	state                *eventState
	sums                 *eventSums // what the person summary reads (personmap.go)
	unnamed              bool
}

// eventState is what the state readout reads of its events.
type eventState struct {
	used     string // tengu_tool_use_success: str(toolName), when truthy
	server   string // its MCP server (session.py _mcp_server)
	isMCP    bool
	hook     string // tengu_run_hook: str(hookName), when truthy
	gitOp    string
	pr       *int64
	recapKey bool // tengu_return_to_session: hadRecap is present
	recap    bool
	count    *int64 // tengu_tool_search_mode_decision's mcpToolCount, tengu_skills_sync_success's total
}

const (
	markerNone uint8 = iota
	markerDestructive
	markerFrustrated
)

var markerNames = [...]string{"", "destructive", "frustrated"}

func isInternal(name string) bool {
	n := strings.TrimPrefix(name, "tengu_")
	if internalNames[n] {
		return true
	}
	for _, p := range internalPrefixes {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

func newListedEvent(e eventCols, part string, pos int, in interner) *listedEvent {
	ce := &listedEvent{ts: e.NS, seq: e.Seq, rowNS: e.RowNS, corr: e.CorrelationID, name: firstStr(e.Name, "(unnamed)"),
		md: e.MetadataJSON, part: part, pos: int32(pos), device: e.DeviceID, prompt: e.PromptID, head: in.of(e.HeadSHA)}
	items := pyItems(e.MetadataJSON)
	md := make(map[string]json.RawMessage, len(items))
	for _, kv := range items {
		md[kv.k] = kv.v
	}
	ce.summary = summaryOf(items)
	ce.unnamed = e.Name == ""
	ce.sums = newEventSums(e.Name, e.MetadataJSON)
	ce.reqID = deref(pyStr(md["requestId"]))
	ce.tool = deref(pyStr(md["toolUseID"]))
	if ce.tool == "" {
		ce.tool = deref(pyStr(md["tool_use_id"]))
	}
	ce.toolName = in.of(deref(pyStr(md["toolName"])))
	st := &eventState{}
	switch ce.name {
	case "tengu_bash_tool_command_executed":
		if c := deref(pyStr(md["destructive_category"])); c != "none" && c != "" {
			ce.marker = markerDestructive
		}
		return ce
	case "tengu_input_prompt":
		if pyTrue(md["is_negative"]) {
			ce.marker = markerFrustrated
		}
		return ce
	case "tengu_tool_use_success":
		if !pyTruthy(md["toolName"]) {
			return ce
		}
		st.used = in.of(pyStrAny(md["toolName"]))
		st.server = in.of(deref(pyStr(md["mcpServerName"])))
		if parts := strings.Split(ce.toolName, "__"); st.server == "" && strings.HasPrefix(ce.toolName, "mcp__") && len(parts) >= 3 {
			st.server = in.of(parts[1])
		}
		st.isMCP = pyTrue(md["isMcp"])
	case "tengu_run_hook":
		if !pyTruthy(md["hookName"]) {
			return ce
		}
		st.hook = in.of(pyStrAny(md["hookName"]))
	case "tengu_git_operation":
		st.gitOp = in.of(deref(pyStr(md["operation"])))
	case "tengu_session_linked_to_pr":
		st.pr = pyIntOf(md["prNumber"])
	case "tengu_return_to_session":
		_, st.recapKey = md["hadRecap"]
		st.recap = pyTrue(md["hadRecap"])
	case "tengu_tool_search_mode_decision":
		st.count = jsonInt(jsonFields(e.MetadataJSON, "mcpToolCount")["mcpToolCount"], math.MinInt64, math.MaxInt64)
	case "tengu_skills_sync_success":
		st.count = jsonInt(jsonFields(e.MetadataJSON, "total")["total"], math.MinInt64, math.MaxInt64)
	default:
		return ce
	}
	ce.state = st
	return ce
}

// summaryOf is session.py _summary: the first six of the metadata's scalar
// fields, ids aside, each value cut to 40 characters.
func summaryOf(items []pyItem) string {
	var pairs []string
	for _, kv := range items {
		if summarySkip[kv.k] {
			continue
		}
		v := bytes.TrimSpace(kv.v)
		if len(v) == 0 || v[0] == 'n' || v[0] == '{' || v[0] == '[' {
			continue
		}
		var text string
		switch v[0] {
		case 't':
			text = "true"
		case 'f':
			text = "false"
		default:
			text = pyStrAny(v)
		}
		if utf8.RuneCountInString(text) > summaryValueRunes {
			text = string([]rune(text)[:summaryValueRunes]) + "…"
		}
		pairs = append(pairs, kv.k+" "+text)
		if len(pairs) == summaryPairs {
			break
		}
	}
	return strings.Join(pairs, " · ")
}

type pyItem struct {
	k string
	v json.RawMessage
}

// pyItems is json.loads(s).items() for an object: keys in the order first seen,
// each with its last value; none for anything else.
func pyItems(s string) []pyItem {
	if s == "" {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(s))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil
	}
	var out []pyItem
	at := map[string]int{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil
		}
		var v json.RawMessage
		if dec.Decode(&v) != nil {
			return nil
		}
		k, _ := tok.(string)
		if i, ok := at[k]; ok {
			out[i].v = v
			continue
		}
		at[k] = len(out)
		out = append(out, pyItem{k, v})
	}
	return out
}

// pyStrAny is str(v) for a JSON value that isn't null: a string's value, True or
// False, an integer's digits, a float's repr (inf past the float range). An object
// or a list (Python would print its repr) is its JSON.
func pyStrAny(v json.RawMessage) string {
	v = bytes.TrimSpace(v)
	if len(v) == 0 {
		return ""
	}
	switch v[0] {
	case '{', '[':
		return string(v)
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		if !isIntLiteral(v) {
			if f := pyParseFloat(string(v)); f != nil {
				return pyRepr(*f)
			}
		}
	}
	return deref(pyStr(v))
}

// pyLower is Python's str.lower: the simple lowercase mapping, but İ is i̇ and a
// final Σ is ς.
func pyLower(s string) string {
	if isASCII(s) {
		return strings.ToLower(s)
	}
	rs := []rune(s)
	var b strings.Builder
	for i, r := range rs {
		switch {
		case r == 'İ':
			b.WriteString("i̇")
		case r == 'Σ' && finalSigma(rs, i):
			b.WriteRune('ς')
		default:
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// finalSigma is Unicode's Final_Sigma: a cased letter before (case-ignorable
// characters skipped), none after.
func finalSigma(rs []rune, i int) bool {
	cased := func(r rune) bool { return unicode.IsUpper(r) || unicode.IsLower(r) || unicode.IsTitle(r) }
	ignorable := func(r rune) bool {
		return unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf, unicode.Lm, unicode.Sk) || r == '\'' || r == '.' || r == ':' || r == '·' || r == '’'
	}
	before := false
	for j := i - 1; j >= 0; j-- {
		if !ignorable(rs[j]) {
			before = cased(rs[j])
			break
		}
	}
	if !before {
		return false
	}
	for j := i + 1; j < len(rs); j++ {
		if !ignorable(rs[j]) {
			return !cased(rs[j])
		}
	}
	return true
}

// EventRow is an event_rows row: one of a session's Claude Code analytics events
// as its Events tab lists it (session.py event_rows, whose rows only the page's
// filters change), with the values the state readout counts (session.py state).
// seq orders a session's events: by time, batch, then place in the batch.
type EventRow struct {
	SessionID    string `parquet:"session_id"`
	Seq          int64  `parquet:"seq"`
	EventID      string `parquet:"event_id"` // <batch>:<number in the batch>
	TsNS         int64  `parquet:"ts_ns"`
	Name         string `parquet:"name"` // "(unnamed)" for none
	NameLower    string `parquet:"name_lower"`
	Internal     bool   `parquet:"internal"`
	Summary      string `parquet:"summary"`
	SummaryLower string `parquet:"summary_lower"`
	Marker       string `parquet:"marker,optional"` // destructive, frustrated; null for none
	LinkKind     string `parquet:"link_kind"`
	LinkID       string `parquet:"link_id"`
	LinkLabel    string `parquet:"link_label"`
	PromptIndex  *int64 `parquet:"prompt_index,optional"`
	HeadSHA      string `parquet:"head_sha,optional"`

	Hook     string `parquet:"hook,optional"`   // tengu_run_hook's hookName
	GitOp    string `parquet:"git_op,optional"` // tengu_git_operation's operation
	PR       *int64 `parquet:"pr,optional"`     // tengu_session_linked_to_pr's prNumber
	RecapKey bool   `parquet:"recap_key"`       // tengu_return_to_session: hadRecap is present
	Recap    bool   `parquet:"recap"`           // … and true
	Count    *int64 `parquet:"count,optional"`  // the latest-count events' (mcpToolCount, total)

	// What the person summary counts (person.py event_rollup), read as its SQL
	// read them: json_extract_string, then TRY_CAST; null where absent.
	Unnamed    bool     `parquet:"unnamed"`
	ToolName   *string  `parquet:"tool_name,optional"` // tengu_tool_use_success: toolName,
	IsMCP      bool     `parquet:"is_mcp"`             // isMcp is 'true',
	HookMS     *float64 `parquet:"hook_ms,optional"`   // preToolHookDurationMs
	Argv0      *string  `parquet:"argv0,optional"`     // tengu_bash_tool_command_executed: bash_argv0,
	Category   *string  `parquet:"category,optional"`  // destructive_category,
	OutsideCwd bool     `parquet:"outside_cwd"`        // destructive_target_scope is 'outside_cwd'
	HookName   *string  `parquet:"hook_name,optional"` // tengu_run_hook: hookName
	Operation  *string  `parquet:"operation,optional"` // tengu_git_operation: operation
	PRNumber   *string  `parquet:"pr_number,optional"` // tengu_session_linked_to_pr: prNumber
	Cache5m    *int64   `parquet:"cache_5m,optional"`  // tengu_api_success: cache_creation_5m_input_tokens,
	Cache1h    *int64   `parquet:"cache_1h,optional"`  // cache_creation_1h_input_tokens,
	CostUSD    *float64 `parquet:"cost_usd,optional"`  // costUSD
}

// EventFacts is what one Claude Code event's row reads of the event itself:
// everything but its place, its prompt's number and what its link resolves to.
// The streaming processor builds its event rows from it (one parsing, so the
// two can't drift). Row's Seq, PromptIndex, LinkKind, LinkID and LinkLabel are
// unset; the link's inputs are ReqID, Tool, ToolName, Device and Prompt.
type EventFacts struct {
	Row                                   EventRow
	ReqID, Tool, ToolName, Device, Prompt string
	// UsedTool is the tool a tengu_tool_use_success names for the session's
	// tools ("" for none), with its MCP server, or IsMCP with none.
	UsedTool, UsedServer string
	UsedIsMCP            bool
}

// EventFactsOf is an event's facts, as eventRows reads them.
func EventFactsOf(e Event) EventFacts {
	ce := newListedEvent(eventColsOf(e), "", 0, interner{})
	r := EventRow{
		SessionID: e.SessionID, TsNS: ce.ts, Name: ce.name, NameLower: pyLower(ce.name), Internal: isInternal(ce.name),
		Summary: ce.summary, SummaryLower: pyLower(ce.summary), Marker: markerNames[ce.marker], HeadSHA: ce.head,
		Unnamed: ce.unnamed,
	}
	f := EventFacts{ReqID: ce.reqID, Tool: ce.tool, ToolName: ce.toolName, Device: ce.device, Prompt: ce.prompt}
	if st := ce.state; st != nil {
		r.Hook, r.GitOp, r.PR, r.RecapKey, r.Recap, r.Count = st.hook, st.gitOp, st.pr, st.recapKey, st.recap, st.count
		f.UsedTool, f.UsedServer, f.UsedIsMCP = st.used, st.server, st.isMCP
	}
	if x := ce.sums; x != nil {
		r.ToolName, r.IsMCP, r.HookMS = x.tool, x.mcp, x.hookMS
		r.Argv0, r.Category, r.OutsideCwd = x.argv0, x.category, x.outside
		r.HookName, r.Operation, r.PRNumber = x.hook, x.gitOp, x.pr
		r.Cache5m, r.Cache1h, r.CostUSD = x.cache5m, x.cache1h, x.costUSD
	}
	f.Row = r
	return f
}

// eventSums is what the person summary sums or counts of an event, read as the
// API's SQL read it (json_extract_string, then TRY_CAST): nil for the events it
// reads nothing of.
type eventSums struct {
	tool     *string // tengu_tool_use_success: toolName
	mcp      bool    // … isMcp is 'true'
	hookMS   *float64
	argv0    *string // tengu_bash_tool_command_executed: bash_argv0,
	category *string // destructive_category,
	outside  bool    // destructive_target_scope is 'outside_cwd'
	hook     *string // tengu_run_hook: hookName
	gitOp    *string // tengu_git_operation: operation
	pr       *string // tengu_session_linked_to_pr: prNumber
	cache5m  *int64  // tengu_api_success: cache_creation_5m_input_tokens,
	cache1h  *int64  // cache_creation_1h_input_tokens,
	costUSD  *float64
	sandbox  *string // tengu_bash_tool_command_executed: sandbox_enabled
	gzipCA   bool    // tengu_api_success: gzipSkipReason is 'custom_ca'
}

// jsonText is json_extract_string: nil for null or absent.
func jsonText(v json.RawMessage) *string {
	if s, ok := jsonString(v); ok {
		return &s
	}
	return nil
}

func newEventSums(name, md string) *eventSums {
	switch name {
	case "tengu_tool_use_success":
		f := jsonFields(md, "toolName", "isMcp", "preToolHookDurationMs")
		mcp, _ := jsonString(f["isMcp"])
		return &eventSums{tool: jsonText(f["toolName"]), mcp: mcp == "true", hookMS: jsonDouble(f["preToolHookDurationMs"])}
	case "tengu_bash_tool_command_executed":
		f := jsonFields(md, "bash_argv0", "destructive_category", "destructive_target_scope", "sandbox_enabled")
		scope, _ := jsonString(f["destructive_target_scope"])
		return &eventSums{argv0: jsonText(f["bash_argv0"]), category: jsonText(f["destructive_category"]),
			outside: scope == "outside_cwd", sandbox: jsonText(f["sandbox_enabled"])}
	case "tengu_run_hook":
		return &eventSums{hook: jsonText(jsonFields(md, "hookName")["hookName"])}
	case "tengu_git_operation":
		return &eventSums{gitOp: jsonText(jsonFields(md, "operation")["operation"])}
	case "tengu_session_linked_to_pr":
		return &eventSums{pr: jsonText(jsonFields(md, "prNumber")["prNumber"])}
	case "tengu_api_success":
		f := jsonFields(md, "cache_creation_5m_input_tokens", "cache_creation_1h_input_tokens", "costUSD", "gzipSkipReason")
		gz, _ := jsonString(f["gzipSkipReason"])
		return &eventSums{
			cache5m: jsonInt(f["cache_creation_5m_input_tokens"], math.MinInt64, math.MaxInt64),
			cache1h: jsonInt(f["cache_creation_1h_input_tokens"], math.MinInt64, math.MaxInt64),
			costUSD: jsonDouble(f["costUSD"]), gzipCA: gz == "custom_ca"}
	}
	return nil
}
