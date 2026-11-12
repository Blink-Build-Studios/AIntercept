package process

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The event_rows and session_tools snapshots: every session's Claude Code
// analytics events as its Events tab lists them (session.py event_rows: the
// row's id, summary, marker, link and prompt number), with the columns the state
// readout counts (session.py state), and the tools each session's calls and
// events used. Built from the same event parts as the other derived tables, and
// held equal to the API's rows by the same parity test (docs/architecture.md,
// Decision 4).
//
// What depends on the request stays SQL: the range, `q` (against the lowered
// name and summary, lowered as Python lowers), hiding internals and folding
// consecutive tool-progress ticks, which depend on what the filters left.

// Event row values, as session.py has them.
var (
	internalNames    = map[string]bool{"declared_tool_set_held": true, "render_glyph_cardinality": true, "tree_sitter_parse_abort": true, "bash_ast_too_complex": true}
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
	n              int32  // its number in its batch (the API's event id)
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

// numberEvents numbers each event of a part's batches as the API's event id does:
// row_number() over the batch's Claude Code events by seq, row time, time, name and
// metadata (identical events in table order, the API's being arbitrary among them).
func (d *eventAgg) numberEvents() {
	for _, keys := range d.keys {
		sort.Slice(keys, func(i, j int) bool { return keys[i].less(keys[j]) })
	}
	taken := map[string]map[int]int{}
	for _, ce := range d.ccAll {
		keys := d.keys[ce.corr]
		me := batchKey{ce.seq, ce.rowNS, ce.ts, ce.name, ce.md}
		lb := sort.Search(len(keys), func(i int) bool { return !keys[i].less(me) })
		if taken[ce.corr] == nil {
			taken[ce.corr] = map[int]int{}
		}
		ce.n = int32(lb + 1 + taken[ce.corr][lb])
		taken[ce.corr][lb]++
		ce.md = ""
	}
	d.ccAll = nil
}

// renumberSpanning numbers again the events of the batches that more than one of
// the parts hold (a part numbers only its own share): it reads those parts'
// events of those batches and numbers each batch over all of them.
func (rp *rowParts) renumberSpanning(ctx context.Context, keys []string, d *rowData) error {
	parts := map[string][]string{} // batch -> the parts holding its Claude Code events
	for _, k := range keys {
		for corr := range rp.cache[k].events.ccBatches {
			parts[corr] = append(parts[corr], k)
		}
	}
	spanning := map[string]bool{}
	read := map[string]bool{}
	for corr, ks := range parts {
		if len(ks) > 1 {
			spanning[corr] = true
			for _, k := range ks {
				read[k] = true
			}
		}
	}
	if len(spanning) == 0 {
		return nil
	}
	type at struct {
		part string
		pos  int32
	}
	type keyed struct {
		key batchKey
		at
		order int
	}
	byBatch := map[string][]keyed{}
	order := 0
	for _, k := range keys {
		if !read[k] {
			continue
		}
		pos := 0
		err := eachParquet(ctx, rp.out, k, func(e eventCols) {
			if e.Source != productClaudeCode {
				return
			}
			if spanning[e.CorrelationID] {
				byBatch[e.CorrelationID] = append(byBatch[e.CorrelationID],
					keyed{batchKey{e.Seq, e.RowNS, e.NS, firstStr(e.Name, "(unnamed)"), e.MetadataJSON}, at{k, int32(pos)}, order})
				order++
			}
			pos++
		})
		if err != nil {
			return fmt.Errorf("derived tables: read %s: %w", k, err)
		}
	}
	number := map[at]int32{}
	for _, evs := range byBatch {
		sort.SliceStable(evs, func(i, j int) bool { return evs[i].key.less(evs[j].key) })
		for i, e := range evs {
			number[e.at] = int32(i + 1)
		}
	}
	for _, evs := range d.ccEvs {
		for _, ce := range evs {
			if spanning[ce.corr] {
				ce.n = number[at{ce.part, ce.pos}]
			}
		}
	}
	for _, evs := range d.promptEvs {
		for _, pe := range evs {
			if spanning[pe.corr] {
				pe.n = int(number[at{pe.part, int32(pe.pos)}])
				if pe.unnamedInput() {
					pe.id = pe.corr + ":" + strconv.Itoa(pe.n)
				}
			}
		}
	}
	for _, evs := range d.markEvs {
		for _, me := range evs {
			if spanning[me.corr] {
				me.n = int(number[at{me.part, int32(me.pos)}])
			}
		}
	}
	return nil
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

// SessionTool is a session_tools row: a tool a session's calls (their responses'
// tool_use blocks) or Claude Code events (tengu_tool_use_success) used, first at
// first_ns. kind is "mcp" (with its server) or "builtin" (server "").
type SessionTool struct {
	SessionID string `parquet:"session_id"`
	Kind      string `parquet:"kind"`
	Server    string `parquet:"server"`
	Tool      string `parquet:"tool"`
	FirstNS   int64  `parquet:"first_ns"`
}

// toolUse is one entry of a call's tool_uses_json, as from_json reads it.
type toolUse struct{ server, tool, kind string }

func readToolUses(s string, in interner) []toolUse {
	if s == "" {
		return nil
	}
	var raw []map[string]json.RawMessage
	if json.Unmarshal([]byte(s), &raw) != nil {
		return nil
	}
	out := make([]toolUse, 0, len(raw))
	for _, u := range raw {
		server, _ := jsonString(u["server"])
		tool, _ := jsonString(u["tool"])
		kind, _ := jsonString(u["kind"])
		out = append(out, toolUse{in.of(server), in.of(tool), in.of(kind)})
	}
	return out
}

// eventRows builds every session's event rows and tools.
func (d *rowData) eventRows() ([]EventRow, []SessionTool) {
	steps := map[string]map[string]*rowCall{} // session -> request id -> its newest-ordered visible call
	var cc []*rowCall
	for _, c := range d.calls {
		if c.ccCall() {
			cc = append(cc, c)
		}
	}
	sort.SliceStable(cc, func(i, j int) bool { return cc[i].ts < cc[j].ts || cc[i].ts == cc[j].ts && cc[i].id < cc[j].id })
	for _, c := range cc {
		if c.reqID == "" {
			continue
		}
		if steps[c.session] == nil {
			steps[c.session] = map[string]*rowCall{}
		}
		steps[c.session][c.reqID] = c
	}

	tools := map[SessionTool]bool{}
	first := map[SessionTool]int64{}
	use := func(s, kind, server, tool string, ts int64) {
		k := SessionTool{SessionID: s, Kind: kind, Server: server, Tool: tool}
		if cur, ok := first[k]; !ok || ts < cur {
			first[k] = ts
		}
		tools[k] = true
	}
	for _, c := range d.calls {
		if c.ended && c.session != "" {
			for _, u := range c.toolUses {
				switch {
				case u.kind == "mcp" && u.server != "" && u.tool != "":
					use(c.session, "mcp", u.server, u.tool, c.ts)
				case u.tool != "":
					use(c.session, "builtin", "", u.tool, c.ts)
				}
			}
		}
	}

	total := 0
	for _, evs := range d.ccEvs {
		total += len(evs)
	}
	rows := make([]EventRow, 0, total)
	for _, s := range sortedKeyList(d.ccEvs) {
		evs := d.ccEvs[s]
		sort.SliceStable(evs, func(i, j int) bool {
			a, b := evs[i], evs[j]
			switch {
			case a.ts != b.ts:
				return a.ts < b.ts
			case a.corr != b.corr:
				return a.corr < b.corr
			}
			return a.n < b.n
		})
		index := d.promptIndex(s)
		for i, ce := range evs {
			r := EventRow{
				SessionID: s, Seq: int64(i), EventID: ce.corr + ":" + strconv.Itoa(int(ce.n)), TsNS: ce.ts,
				Name: ce.name, NameLower: pyLower(ce.name), Internal: isInternal(ce.name),
				Summary: ce.summary, SummaryLower: pyLower(ce.summary), Marker: markerNames[ce.marker],
				HeadSHA: ce.head,
			}
			if st := ce.state; st != nil {
				r.Hook, r.GitOp, r.PR, r.RecapKey, r.Recap, r.Count = st.hook, st.gitOp, st.pr, st.recapKey, st.recap, st.count
				if st.used != "" {
					switch {
					case st.server != "":
						use(s, "mcp", st.server, st.used, ce.ts)
					case !st.isMCP:
						use(s, "builtin", "", st.used, ce.ts)
					}
				}
			}
			r.Unnamed = ce.unnamed
			if x := ce.sums; x != nil {
				r.ToolName, r.IsMCP, r.HookMS = x.tool, x.mcp, x.hookMS
				r.Argv0, r.Category, r.OutsideCwd = x.argv0, x.category, x.outside
				r.HookName, r.Operation, r.PRNumber = x.hook, x.gitOp, x.pr
				r.Cache5m, r.Cache1h, r.CostUSD = x.cache5m, x.cache1h, x.costUSD
			}
			if ce.prompt != "" {
				if n, ok := index[ce.prompt]; ok {
					r.PromptIndex = ptr(n)
				}
			}
			r.LinkKind, r.LinkID, r.LinkLabel = d.link(ce, s, steps[s], index)
			rows = append(rows, r)
		}
	}
	out := make([]SessionTool, 0, len(tools))
	for k := range tools {
		k.FirstNS = first[k]
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.SessionID != b.SessionID {
			return a.SessionID < b.SessionID
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Server != b.Server {
			return a.Server < b.Server
		}
		return a.Tool < b.Tool
	})
	return rows, out
}

// link is session.py _link: what an event row points to.
func (d *rowData) link(ce *listedEvent, s string, steps map[string]*rowCall, index map[string]int64) (string, string, string) {
	switch rid := ce.reqID; {
	case ce.name == "tengu_event_loop_stall" && ce.device != "":
		return "device", ce.device, "device"
	case ce.tool != "":
		return "tool", ce.tool, firstStr(ce.toolName, "tool")
	case rid != "":
		if c := steps[rid]; c != nil {
			return "request", c.id, firstStr(c.class, "—") + " · " + modelFamily(c.model) + " · " + firstStr(c.stop, "—")
		}
		return "request", "reported:" + rid, "not captured"
	case ce.prompt != "":
		if n, ok := index[ce.prompt]; ok {
			return "prompt", ce.prompt, "#" + strconv.FormatInt(n, 10)
		}
		return "prompt", ce.prompt, "prompt"
	}
	return "session", s, "session"
}

// promptIndex is person.py prompt_indexes for one session: each prompt's number,
// Claude Code's from tengu_input_prompt, a Codex turn's from its place in the
// thread (the later wins, as the API's dict does).
func (d *rowData) promptIndex(s string) map[string]int64 {
	index := map[string]int64{}
	for _, e := range d.prompts[s] {
		if e.promptID != "" && e.index != nil {
			index[e.promptID] = *e.index
		}
	}
	for pid, n := range d.codexPrompt[s] {
		index[pid] = n
	}
	return index
}

func sortedKeyList[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
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

// CallLabel is a request link's label: the call's class, model family and stop
// reason (session.py _link).
func CallLabel(class, model, stop string) string {
	return firstStr(class, "—") + " · " + modelFamily(model) + " · " + firstStr(stop, "—")
}

// ToolUses is a call's tool_uses_json as the session tools read it: kind,
// server and tool of each.
func ToolUses(toolUsesJSON string) [][3]string {
	var out [][3]string
	for _, u := range readToolUses(toolUsesJSON, interner{}) {
		out = append(out, [3]string{u.kind, u.server, u.tool})
	}
	return out
}

// PromptIndexOf is a tengu_input_prompt's prompt id and number as the prompt
// index reads them (cc_prompt_id, prompt_index), and whether it has both.
func PromptIndexOf(e Event) (string, int64, bool) {
	if e.Name != "tengu_input_prompt" {
		return "", 0, false
	}
	md := jsonFields(e.MetadataJSON, "cc_prompt_id", "prompt_index")
	pid, _ := jsonString(md["cc_prompt_id"])
	n := jsonInt(md["prompt_index"], math.MinInt32, math.MaxInt32)
	if pid == "" || n == nil {
		return "", 0, false
	}
	return pid, *n, true
}
