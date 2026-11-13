package process

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// The prompt_rows and prompt_steps snapshots: every session's prompts (their
// steps inline) and between rows, as the query API showed them
// (api/src/aintercept_api/session.py items and typed_prompts, over the analytics
// joins in docs/ui/handoff/context/DATA_ANSWERS.md §1). Built with the request
// rows, from the same parts (requestrows.go), and held equal to the API's
// output by the same parity test (docs/architecture.md, Decision 4).
//
// What depends on the time of the request isn't stored: the inputs it needs are
// (PromptRow, PromptStep), and the API's SQL applies now():
//   - live: now - session_last_ns <= 5 min;
//   - a step's status: unfinished (no end) is streaming while live and started
//     within 10 min, else error; finished is error when `failed`, else done;
//   - a step's events are pending when live and events_pending_if_live;
//   - a step's tools include its running ones (tools_live_json) while live;
//   - a prompt is in flight when live and it is the open tail or one of its
//     steps is streaming (unfinished_max_ns within 10 min);
//   - a prompt's duration is its turn's (duration_turn_ms) unless it's in
//     flight, else Codex's report's (duration_report_ms).

// promptEventNames is the events the prompt rows read (the API's PROMPT_EVENTS).
var promptEventNames = map[string]bool{
	"tengu_input_prompt": true, "tengu_turn_end": true, "tengu_cancel": true,
	"tengu_bash_tool_command_executed": true, "tengu_tool_use_success": true, "tengu_tool_use_error": true,
	tickEvent: true, "tengu_api_success": true, "tengu_paste_text": true, "tengu_paste_image": true,
	"tengu_return_to_session":        true,
	"tengu_reactive_compact_attempt": true, "tengu_reactive_compact_succeeded": true, "tengu_idle_compact_fired": true,
}

const (
	tickEvent  = "tengu_tool_use_progress"
	idleLeadNS = 10 * 1_000_000_000 // tengu_idle_compact_fired precedes its compaction attempt by well under a second
)

// requestClasses is the request classes a step shows (others show none).
var requestClasses = map[string]bool{"main": true, "subagent": true, "auxiliary": true, "compaction": true}

// promptEvent is one of a session's prompt-row events, with the metadata the rows
// read, as the API reads it (json.loads, then session.py's _str/_int/_float/_true).
type promptEvent struct {
	ts, seq, rowNS int64
	corr, name     string
	prompt         string // the prompt_id column
	md             string // kept only until its batch is numbered
	n              int    // its number in its batch (the API's event id): orders events at the same time
	id             string // <batch>:<number in the batch>, for an input with no prompt id
	part           string // its events part, and its place among the part's Claude Code events
	pos            int

	reqID, clientReqID          *string
	toolUseID, toolName, server *string
	duration                    *float64
	resultBytes                 *int64
	bashToolUseID               *string // str(tool_use_id), when truthy
	argv0, destructive, scope   *string
	exitCode                    *int64
	promptIndex, promptLength   *int64
	negative, hadRecap          bool
	pastedBytes                 *int64
	cache5m, cache1h            *int64
	ttft, cost                  *float64
}

func newPromptEvent(e eventCols) *promptEvent {
	pe := &promptEvent{ts: e.NS, seq: e.Seq, rowNS: e.RowNS, corr: e.CorrelationID, name: e.Name, prompt: e.PromptID,
		md: e.MetadataJSON}
	md := pyObject(e.MetadataJSON)
	pe.reqID = pyStr(md["requestId"])
	switch e.Name {
	case "tengu_api_success":
		pe.clientReqID = pyStr(md["clientRequestId"])
		pe.cache5m, pe.cache1h = pyIntOf(md["cache_creation_5m_input_tokens"]), pyIntOf(md["cache_creation_1h_input_tokens"])
		pe.ttft, pe.cost = pyFloatOf(md["ttftMs"]), pyFloatOf(md["costUSD"])
	case "tengu_tool_use_success", "tengu_tool_use_error", tickEvent:
		pe.toolUseID, pe.toolName, pe.server = pyStr(md["toolUseID"]), pyStr(md["toolName"]), pyStr(md["mcpServerName"])
		pe.duration, pe.resultBytes = pyFloatOf(md["durationMs"]), pyIntOf(md["toolResultSizeBytes"])
	case "tengu_bash_tool_command_executed":
		if pyTruthy(md["tool_use_id"]) {
			pe.bashToolUseID = pyStr(md["tool_use_id"])
		}
		pe.argv0, pe.exitCode = pyStr(md["bash_argv0"]), pyIntOf(md["exit_code"])
		if c := pyStr(md["destructive_category"]); c != nil && *c != "none" && *c != "" {
			pe.destructive = c
		}
		pe.scope = pyStr(md["destructive_target_scope"])
	case "tengu_input_prompt":
		pe.promptIndex, pe.promptLength = pyIntOf(md["prompt_index"]), pyIntOf(md["prompt_length"])
		pe.negative = pyTrue(md["is_negative"])
	case "tengu_paste_text":
		pe.pastedBytes = pyIntOf(md["pastedTextBytes"])
	case "tengu_return_to_session":
		pe.hadRecap = pyTrue(md["hadRecap"])
	}
	return pe
}

func (pe *promptEvent) unnamedInput() bool { return pe.name == "tengu_input_prompt" && pe.prompt == "" }

// batchKey is an event's place in its batch (the API numbers a batch's Claude Code
// events by seq, row time, time, name and metadata).
type batchKey struct {
	seq, rowNS, ns int64
	name, md       string
}

func (a batchKey) less(b batchKey) bool {
	switch {
	case a.seq != b.seq:
		return a.seq < b.seq
	case a.rowNS != b.rowNS:
		return a.rowNS < b.rowNS
	case a.ns != b.ns:
		return a.ns < b.ns
	case a.name != b.name:
		return a.name < b.name
	}
	return a.md < b.md
}

// numberUnnamed numbers each prompt-row and mark event in its batch (the API's event id,
// which orders a session's events at the same time and batch), gives each prompt
// with no prompt id its event id (its batch and that number), once a part is
// read, and drops the batches' keys.
func (d *eventAgg) numberUnnamed() {
	sorted := map[string]bool{}
	for _, evs := range d.promptEvs {
		for _, pe := range evs {
			keys := d.keys[pe.corr]
			if !sorted[pe.corr] {
				sort.Slice(keys, func(i, j int) bool { return keys[i].less(keys[j]) })
				sorted[pe.corr] = true
			}
			me := batchKey{pe.seq, pe.rowNS, pe.ts, firstStr(pe.name, "(unnamed)"), pe.md}
			pe.n = sort.Search(len(keys), func(i int) bool { return !keys[i].less(me) }) + 1
			if pe.unnamedInput() {
				pe.id = pe.corr + ":" + strconv.Itoa(pe.n)
			}
			pe.md = ""
		}
	}
	for _, evs := range d.markEvs {
		for _, me := range evs {
			keys := d.keys[me.corr]
			if !sorted[me.corr] {
				sort.Slice(keys, func(i, j int) bool { return keys[i].less(keys[j]) })
				sorted[me.corr] = true
			}
			k := me.key()
			me.n = sort.Search(len(keys), func(i int) bool { return !keys[i].less(k) }) + 1
			me.md = ""
		}
	}
	d.keys, d.unnamed = map[string][]batchKey{}, map[string]bool{}
}

// codexTurn is a codex_turn_event: Codex's report on a turn (its prompt).
type codexTurn struct {
	ts     int64
	prompt string
	report *turnReportJSON
}

// turnReportJSON is the API's TurnReport, in its field order.
type turnReportJSON struct {
	Status       *string       `json:"status"`
	DurationMS   *float64      `json:"durationMs"`
	SamplingMS   *float64      `json:"samplingMs"`
	Requests     *int64        `json:"requests"`
	Retries      *int64        `json:"retries"`
	ToolCalls    orderedCounts `json:"toolCalls"`
	Steers       *int64        `json:"steers"`
	CompactionMS *float64      `json:"compactionMs"`
	Trigger      *string       `json:"trigger"`
}

var turnToolKinds = [][2]string{
	{"shell_command_count", "shell"}, {"mcp_tool_call_count", "mcp"}, {"file_change_count", "fileChange"},
	{"web_search_count", "webSearch"}, {"subagent_tool_call_count", "subagent"}, {"dynamic_tool_call_count", "dynamic"},
}

// turnReport is agents.turn_report: numbers only from JSON numbers.
func turnReport(mdJSON string) *turnReportJSON {
	md := pyObject(mdJSON)
	num := func(k string) *float64 { return pyNum(md[k]) }
	integer := func(k string) *int64 {
		if f := pyNum(md[k]); f != nil {
			return truncInt(*f)
		}
		return nil
	}
	r := &turnReportJSON{
		Status: jsonStr(md["status"]), DurationMS: num("duration_ms"), SamplingMS: num("sampling_ms"),
		Requests: integer("sampling_request_count"), Retries: integer("sampling_retry_count"),
		Steers: integer("steer_count"), Trigger: jsonStr(md["turn_trigger"]), ToolCalls: orderedCounts{},
	}
	for _, k := range turnToolKinds {
		if n := integer(k[0]); n != nil && *n != 0 {
			r.ToolCalls = append(r.ToolCalls, countOf{k[1], *n})
		}
	}
	if c := num("compaction_ms"); c != nil && *c != 0 {
		r.CompactionMS = c
	}
	return r
}

// --- recorded bodies -----------------------------------------------------------------

// callDetail is what a step's detail_json gives its row.
type callDetail struct {
	prompt  string // the prompt's text (content: only in full interception mode)
	text    string
	calls   []detailCall
	results []detailResult // by call id, in the JSON's order
}

type detailCall struct {
	id       string
	commands []string
	patches  []events.PatchFile
}

type detailResult struct {
	id    string
	codes []int64
}

// readDetail reads detail_json (the processor's events.CallDetail); nil without one.
func readDetail(s string) *callDetail {
	if s == "" {
		return nil
	}
	var d events.CallDetail
	if json.Unmarshal([]byte(s), &d) != nil {
		return nil
	}
	out := &callDetail{prompt: d.Prompt, text: d.Text}
	for _, c := range d.Calls {
		out.calls = append(out.calls, detailCall{id: c.ID, commands: c.Commands, patches: c.Patches})
	}
	ids := make([]string, 0, len(d.Results))
	for id := range d.Results {
		ids = append(ids, id)
	}
	sort.Strings(ids) // as marshalled
	for _, id := range ids {
		r := d.Results[id]
		switch {
		case len(r.Exit) > 0:
			codes := make([]int64, len(r.Exit))
			for i, x := range r.Exit {
				codes[i] = int64(x)
			}
			out.results = append(out.results, detailResult{id, codes})
		case r.Error:
			out.results = append(out.results, detailResult{id, []int64{1}})
		}
	}
	return out
}

var (
	execTools  = map[string]bool{"exec": true, "exec_command": true, "shell": true, "local_shell": true, "Bash": true, "BashOutput": true}
	patchTools = map[string]bool{"apply_patch": true, "Edit": true, "Write": true, "MultiEdit": true, "NotebookEdit": true}
	spawnTools = map[string]bool{"spawn_agent": true, "Task": true, "Agent": true}
)

// stepKind is rows.step_kind: what a step did.
func stepKind(toolUsesJSON, stop string) string {
	if strings.Contains(stop, "interrupt") {
		return "interrupt"
	}
	var uses []any
	if toolUsesJSON != "" && json.Unmarshal([]byte(toolUsesJSON), &uses) != nil {
		uses = nil
	}
	names, mcp, n := map[string]bool{}, false, 0
	for _, u := range uses {
		m, ok := u.(map[string]any)
		if !ok {
			continue
		}
		n++
		name := "?"
		if s := pyTruthyStr(m["name"]); s != "" {
			name = s
		} else if s := pyTruthyStr(m["tool"]); s != "" {
			name = s
		}
		names[name] = true
		if pyTruthyStr(m["kind"]) == "mcp" {
			mcp = true
		}
	}
	has := func(set map[string]bool) bool {
		for k := range names {
			if set[k] {
				return true
			}
		}
		return false
	}
	switch {
	case has(patchTools):
		return "patch"
	case has(spawnTools):
		return "spawn"
	case mcp:
		return "mcp"
	case has(execTools):
		return "exec"
	case n > 0:
		return "tool"
	}
	return "text"
}

// --- the rows ------------------------------------------------------------------------

// promptSession is what one session's prompt rows are built from.
type promptSession struct {
	id      string
	steps   []*rowCall // its visible model calls, by time and id
	hidden  []hiddenCall
	evs     []*promptEvent
	turns   []codexTurn
	last    int64
	events  int64 // its Claude Code events
	product string
}

type hiddenCall struct {
	ts   int64
	kind string
}

// promptRows builds every session's prompt rows, steps and timeline marks: the sessions with a
// visible model call or a Claude Code event (the API's load_session).
func (d *rowData) promptRows() ([]PromptRow, []PromptStep, []SessionMark) {
	byID := map[string]*promptSession{}
	get := func(id string) *promptSession {
		s := byID[id]
		if s == nil {
			s = &promptSession{id: id}
			byID[id] = s
		}
		return s
	}
	var hidden []*rowCall
	for _, c := range d.calls {
		switch {
		case c.ccCall():
			s := get(c.session)
			s.steps = append(s.steps, c)
		case c.agentCall(): // a hidden call (prewarm, title)
			hidden = append(hidden, c)
		}
	}
	for id, n := range d.eventCount {
		get(id).events = n
	}
	for _, c := range hidden {
		if s := byID[c.session]; s != nil {
			s.hidden = append(s.hidden, hiddenCall{c.ts, c.hidden})
		}
	}
	var rows []PromptRow
	var steps []PromptStep
	var marks []SessionMark
	for _, id := range sortedSessionIDs(byID) {
		s := byID[id]
		sort.SliceStable(s.steps, func(i, j int) bool {
			a, b := s.steps[i], s.steps[j]
			return a.ts < b.ts || a.ts == b.ts && a.id < b.id
		})
		last, ok := d.lastEvent[id]
		for _, c := range s.steps {
			if !ok || c.ts > last {
				last, ok = c.ts, true
			}
			if s.product == "" {
				s.product = c.product
			}
		}
		s.last = last
		// Oldest first (the API's ts, batch, number in the batch).
		s.evs = d.promptEvs[id]
		sort.SliceStable(s.evs, func(i, j int) bool {
			a, b := s.evs[i], s.evs[j]
			switch {
			case a.ts != b.ts:
				return a.ts < b.ts
			case a.corr != b.corr:
				return a.corr < b.corr
			}
			return a.n < b.n
		})
		s.turns = d.turns[id]
		r, st := s.rows()
		rows, steps = append(rows, r...), append(steps, st...)
		marks = append(marks, s.marks(d, s.compactions())...)
	}
	return rows, steps, marks
}

func sortedSessionIDs(m map[string]*promptSession) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// joins is the session's events indexed for the steps (session.py _Joins).
type joins struct {
	success   map[string]*promptEvent
	tools     map[string][]*promptEvent
	ticks     map[string][]*promptEvent
	bash      map[string]*promptEvent
	byPrompt  map[string][]*promptEvent
	lastEvent *int64
	results   map[string][]int64
}

func (s *promptSession) joins() *joins {
	j := &joins{success: map[string]*promptEvent{}, tools: map[string][]*promptEvent{}, ticks: map[string][]*promptEvent{},
		bash: map[string]*promptEvent{}, byPrompt: map[string][]*promptEvent{}}
	for _, e := range s.evs {
		if j.lastEvent == nil || e.ts > *j.lastEvent {
			j.lastEvent = ptr(e.ts)
		}
		rid := deref(e.reqID)
		switch {
		case e.name == "tengu_api_success":
			for _, k := range []string{rid, deref(e.clientReqID)} {
				if _, ok := j.success[k]; k != "" && !ok {
					j.success[k] = e
				}
			}
		case (e.name == "tengu_tool_use_success" || e.name == "tengu_tool_use_error") && rid != "":
			j.tools[rid] = append(j.tools[rid], e)
		case e.name == tickEvent && rid != "":
			j.ticks[rid] = append(j.ticks[rid], e)
		case e.name == "tengu_bash_tool_command_executed" && e.bashToolUseID != nil:
			j.bash[*e.bashToolUseID] = e
		}
		if e.prompt != "" {
			j.byPrompt[e.prompt] = append(j.byPrompt[e.prompt], e)
		}
	}
	j.results = map[string][]int64{}
	for _, c := range s.steps {
		if c.detail == nil {
			continue
		}
		for _, r := range c.detail.results {
			if _, ok := j.results[r.id]; !ok {
				j.results[r.id] = r.codes
			}
		}
	}
	return j
}

// toolJSON is the API's ToolCallData, in its field order.
type toolJSON struct {
	ToolUseID   *string          `json:"toolUseId"`
	Name        string           `json:"name"`
	MCPServer   *string          `json:"mcpServer"`
	Target      *string          `json:"target"`
	Status      string           `json:"status"`
	DurationMS  *float64         `json:"durationMs"`
	ExitCode    *int64           `json:"exitCode"`
	Command     *string          `json:"command"`
	ResultBytes *int64           `json:"resultBytes"`
	Destructive *destructiveJSON `json:"destructive"`
}

type destructiveJSON struct {
	Category string  `json:"category"`
	Scope    *string `json:"scope"`
}

func (j *joins) tool(e *promptEvent) toolJSON {
	name := firstStr(deref(e.toolName), "—")
	t := toolJSON{ToolUseID: e.toolUseID, Name: name, Status: "error", DurationMS: finite(e.duration), ResultBytes: e.resultBytes}
	if e.name == "tengu_tool_use_success" {
		t.Status = "ok"
	}
	if e.server != nil && *e.server != "" {
		t.MCPServer = e.server
	} else if n := deref(e.toolName); strings.HasPrefix(n, "mcp__") {
		if parts := strings.Split(n, "__"); len(parts) >= 3 {
			t.MCPServer = ptr(parts[1])
		}
	}
	var bash *promptEvent
	if tid := deref(e.toolUseID); tid != "" {
		bash = j.bash[tid]
	}
	if bash != nil {
		t.Command, t.ExitCode = bash.argv0, bash.exitCode
		if bash.destructive != nil {
			t.Destructive = &destructiveJSON{*bash.destructive, bash.scope}
		}
	}
	return t
}

// rows is the session's prompt rows (with their steps) and between rows, in
// session.py items()'s order.
func (s *promptSession) rows() ([]PromptRow, []PromptStep) {
	j := s.joins()
	codex := s.product == productCodex
	reports := map[string]*turnReportJSON{}
	if codex {
		turns := append([]codexTurn(nil), s.turns...)
		sort.SliceStable(turns, func(a, b int) bool { return turns[a].ts < turns[b].ts })
		for _, t := range turns {
			reports[t.prompt] = t.report
		}
	}

	// Prompts: each tengu_input_prompt, plus prompt ids seen only on calls.
	inputs, starts := map[string]*promptEvent{}, map[string]int64{}
	for _, e := range s.evs {
		if e.name != "tengu_input_prompt" {
			continue
		}
		pid := e.prompt
		if pid == "" {
			pid = "input:" + e.id
		}
		if _, ok := inputs[pid]; !ok {
			inputs[pid] = e
		}
		if _, ok := starts[pid]; !ok {
			starts[pid] = e.ts
		}
	}
	byPrompt := map[string][]*rowCall{}
	for _, c := range s.steps {
		if c.prompt != "" {
			byPrompt[c.prompt] = append(byPrompt[c.prompt], c)
			if _, ok := starts[c.prompt]; !ok {
				starts[c.prompt] = c.ts
			}
		}
	}
	order := make([]string, 0, len(starts))
	for p := range starts {
		order = append(order, p)
	}
	sort.Slice(order, func(a, b int) bool {
		return starts[order[a]] < starts[order[b]] || starts[order[a]] == starts[order[b]] && order[a] < order[b]
	})
	hidden := s.hiddenByPrompt(starts, order)
	var pastes []*promptEvent
	for _, e := range s.evs {
		if e.name == "tengu_paste_text" || e.name == "tengu_paste_image" {
			pastes = append(pastes, e)
		}
	}
	typed := make([]int64, 0, len(inputs))
	for _, e := range inputs {
		typed = append(typed, e.ts)
	}
	sort.Slice(typed, func(a, b int) bool { return typed[a] < typed[b] })

	type item struct {
		row   PromptRow
		steps []PromptStep
	}
	var items []item
	for n, pid := range order {
		t, inp, calls := starts[pid], inputs[pid], byPrompt[pid]
		report := reports[pid]
		promptEvents := j.byPrompt[pid]
		row := PromptRow{SessionID: s.id, TNS: t, Type: "prompt", PromptID: ptr(pid), Steps: int64(len(calls)),
			Typed: inp != nil || codex}
		var stepRows []PromptStep
		var said string
		for i, c := range calls {
			stepRows = append(stepRows, s.step(c, j, int64(i+1)))
			if said == "" && c.detail != nil {
				said = c.detail.prompt
			}
			if c.end == 0 && (row.UnfinishedMaxNS == nil || c.ts > *row.UnfinishedMaxNS) {
				row.UnfinishedMaxNS = ptr(c.ts)
			}
		}
		var turnEnd *int64
		cancelled, destructive := false, int64(0)
		var toolCounts orderedCounts
		for _, e := range promptEvents {
			switch e.name {
			case "tengu_turn_end":
				if turnEnd == nil || e.ts > *turnEnd {
					turnEnd = ptr(e.ts)
				}
			case "tengu_cancel":
				cancelled = true
			case "tengu_bash_tool_command_executed":
				if e.destructive != nil {
					destructive++
				}
			case "tengu_tool_use_success", "tengu_tool_use_error":
				toolCounts = toolCounts.add(firstStr(deref(e.toolName), "—"))
			}
		}
		row.OpenTail = n == len(order)-1 && turnEnd == nil && !(report != nil && deref(report.Status) != "")
		if turnEnd != nil {
			row.DurationTurnMS = ptr(round1(float64(*turnEnd-t) / 1e6))
		}
		if report != nil {
			row.DurationReportMS = report.DurationMS
			row.TurnReportJSON = ptr(marshalJSON(report))
		}
		if inp != nil {
			prev := int64(-1)
			for _, x := range typed {
				if x < t && x > prev {
					prev = x
				}
			}
			var images int64
			for _, e := range pastes {
				if prev < e.ts && e.ts <= t {
					if e.name == "tengu_paste_text" {
						if e.pastedBytes != nil {
							row.PastedBytes = ptr(deref(row.PastedBytes) + *e.pastedBytes)
						}
					} else {
						images++
					}
				}
			}
			if s.events > 0 {
				row.Images = ptr(images)
			}
			row.PromptIndex = inp.promptIndex
			row.TextChars = inp.promptLength
			row.Frustrated = inp.negative
		} else if codex {
			row.PromptIndex = ptr(int64(n + 1))
		}
		if said != "" {
			row.TextRetained, row.Text = true, ptr(said)
			if inp == nil {
				row.TextChars = ptr(int64(utf8.RuneCountInString(said)))
			}
		}
		for _, c := range calls {
			if ctx := c.context(); ctx != nil {
				row.TokensIn = ptr(deref(row.TokensIn) + *ctx)
			}
			if c.output != 0 {
				row.TokensOut = ptr(deref(row.TokensOut) + c.output)
			}
		}
		toolCounts.sortByCount()
		row.ToolCountsJSON = marshalJSON(toolCounts)
		row.Interrupted, row.Destructive = cancelled, destructive
		row.HiddenJSON = marshalJSON(hidden[pid])
		items = append(items, item{row, stepRows})
	}

	comps := s.compactions()
	attached := map[string]bool{}
	for _, c := range comps {
		if c.requestID != "" {
			attached[c.requestID] = true
		}
	}
	between := func(t int64, kind, detail string, rid *string) *PromptRow {
		return &PromptRow{SessionID: s.id, TNS: t, Type: "between", BetweenKind: ptr(kind), Detail: ptr(detail), RequestID: rid}
	}
	for _, c := range comps {
		kind, label := "compaction", "compaction"
		if c.idle {
			kind, label = "idle-compaction", "idle compaction"
		}
		if c.from != nil && c.to != nil && *c.from != 0 && *c.to != 0 {
			label += " · context " + kTokens(*c.from) + " → " + kTokens(*c.to) + " tokens"
		}
		r := between(c.t, kind, label, nz(c.requestID))
		r.TokensFrom, r.TokensTo, r.WaitMS = c.from, c.to, c.wait
		items = append(items, item{row: *r})
	}
	for _, e := range s.evs {
		if e.name == "tengu_return_to_session" && e.hadRecap {
			items = append(items, item{row: *between(e.ts, "recap", "recap shown on return", nil)})
		}
	}
	for _, c := range s.steps {
		if c.prompt == "" && !attached[c.id] {
			detail := firstStr(c.class, "—") + " · " + modelFamily(c.model) + " · " + firstStr(c.stop, "—")
			items = append(items, item{row: *between(c.ts, "call", detail, ptr(c.id))})
		}
	}
	sort.SliceStable(items, func(a, b int) bool {
		x, y := items[a].row, items[b].row
		return x.TNS < y.TNS || x.TNS == y.TNS && x.Type < y.Type
	})
	var rows []PromptRow
	var steps []PromptStep
	for i, it := range items {
		it.row.Seq, it.row.SessionLastNS = int64(i+1), s.last
		rows = append(rows, it.row)
		for _, st := range it.steps {
			st.Seq = int64(i + 1)
			steps = append(steps, st)
		}
	}
	return rows, steps
}

// context is what a call sent: input + cache read + cache write (nil when none is known).
func (c *rowCall) context() *int64 {
	if c.input == 0 && c.cacheRead == 0 && c.cacheWrite == 0 {
		return nil
	}
	return ptr(c.input + c.cacheRead + c.cacheWrite)
}

// step is one of a prompt's steps (session.py _step), with what depends on now
// left as its inputs.
func (s *promptSession) step(x *rowCall, j *joins, n int64) PromptStep {
	succ := j.success[x.reqID]
	if succ == nil {
		succ = j.success[x.clientReqID]
	}
	tools, live := j.toolsOf(x.reqID)
	st := PromptStep{
		SessionID: s.id, Step: n, RequestID: x.id, TNS: x.ts, Kind: "model",
		Model: nz(x.model), StopReason: nz(x.stop), AgentType: nz(x.agentType),
		TokensInput: nzi(x.input), TokensCacheRead: nzi(x.cacheRead), TokensCacheWrite: nzi(x.cacheWrite),
		TokensOutput: nzi(x.output), TokensAccuracy: "missing",
		Failed: x.status == 0 || x.status >= 400, Does: x.does, UsageMissing: usageMissing(x),
		ToolsJSON: marshalJSON(tools),
	}
	if x.end != 0 {
		st.EndNS = ptr(x.end)
		st.DurationMS = ptr(round1(float64(x.end-x.ts) / 1e6))
	}
	if len(live) > len(tools) {
		st.ToolsLiveJSON = ptr(marshalJSON(live))
	}
	if x.class == "subagent" {
		st.Kind = "subagent"
	}
	if requestClasses[x.class] {
		st.RequestClass = ptr(x.class)
	}
	if x.output != 0 {
		st.TokensAccuracy = "exact"
	}
	if succ != nil {
		st.TokensCacheWrite5m, st.TokensCacheWrite1h = succ.cache5m, succ.cache1h
		st.TTFTMS, st.CostUSD = finite(succ.ttft), finite(succ.cost)
	}
	end := x.ts
	if x.end != 0 {
		end = x.end
	}
	st.EventsPendingIfLive = succ == nil && (j.lastEvent == nil || *j.lastEvent <= end)

	// Its commands, patches, first words and exit codes, from its recorded bodies;
	// without them, what Claude Code's Bash events report of its tools (running
	// ones too, while live).
	var calls []detailCall
	if x.detail != nil {
		calls = x.detail.calls
		st.Text = nz(x.detail.text)
	}
	patches := []events.PatchFile{}
	if len(calls) == 0 {
		st.Commands, st.ExitCodes, st.ContentLive, st.CommandsLive, st.ExitCodesLive = toolContent(tools, live)
	} else {
		st.Commands, st.ExitCodes = []string{}, []int64{}
		for _, c := range calls {
			st.Commands = append(st.Commands, c.commands...)
			for _, p := range c.patches {
				if p.File != "" {
					patches = append(patches, p)
				}
			}
			st.ExitCodes = append(st.ExitCodes, j.results[c.id]...)
		}
	}
	if !st.ContentLive {
		st.CommandsLive, st.ExitCodesLive = []string{}, []int64{}
	}
	st.PatchesJSON = marshalJSON(patches)
	return st
}

// toolsOf is a request id's finished tools, and those plus the tools its
// progress ticks show still running (shown while live).
func (j *joins) toolsOf(reqID string) (tools, live []toolJSON) {
	finished := j.tools[reqID]
	tools = make([]toolJSON, 0, len(finished))
	for _, e := range finished {
		tools = append(tools, j.tool(e))
	}
	live = append([]toolJSON(nil), tools...)
	key := func(e *promptEvent) string { // _str(toolUseID) or _str(toolName)
		if v := deref(e.toolUseID); v != "" {
			return "s" + v
		}
		if e.toolName != nil {
			return "s" + *e.toolName
		}
		return "n" // None
	}
	done := map[string]bool{}
	for _, e := range finished {
		done[key(e)] = true
	}
	for _, e := range j.ticks[reqID] {
		if k := key(e); !done[k] {
			done[k] = true
			t := j.tool(e)
			t.Status = "running"
			live = append(live, t)
		}
	}
	return tools, live
}

// toolContent is what Claude Code's Bash events report of a step's tools: their
// commands and exit codes, and, when the running ones add to them, the live
// version (a step with no recorded bodies shows these).
func toolContent(tools, live []toolJSON) (cmds []string, codes []int64, contentLive bool, cmdsLive []string, codesLive []int64) {
	cmds, codes = bashContent(tools)
	if len(live) > len(tools) {
		c, x := bashContent(live)
		if len(c) != len(cmds) || len(x) != len(codes) {
			return cmds, codes, true, c, x
		}
	}
	return cmds, codes, false, nil, nil
}

// bashContent is the commands and exit codes of the tools Claude Code's Bash events name.
func bashContent(tools []toolJSON) ([]string, []int64) {
	cmds, codes := []string{}, []int64{}
	for _, t := range tools {
		if deref(t.Command) != "" {
			cmds = append(cmds, *t.Command)
			if t.ExitCode != nil {
				codes = append(codes, *t.ExitCode)
			}
		}
	}
	return cmds, codes
}

// hiddenByPrompt gives each hidden call to a prompt: a prewarm to the next one to
// start, a title to the last one started before it (session.py _hidden_by_prompt).
func (s *promptSession) hiddenByPrompt(starts map[string]int64, order []string) map[string]orderedCounts {
	calls := append([]hiddenCall(nil), s.hidden...)
	sort.SliceStable(calls, func(a, b int) bool { return calls[a].ts < calls[b].ts })
	out := map[string]orderedCounts{}
	for _, h := range calls {
		after, before := "", ""
		for _, p := range order {
			if starts[p] >= h.ts {
				after = p
				break
			}
		}
		for i := len(order) - 1; i >= 0; i-- {
			if starts[order[i]] < h.ts {
				before = order[i]
				break
			}
		}
		pid := firstStr(before, after)
		if h.kind == "prewarm" {
			pid = firstStr(after, before)
		}
		if pid != "" {
			out[pid] = out[pid].add(h.kind)
		}
	}
	return out
}

type compaction struct {
	t         int64
	from, to  *int64
	idle      bool
	wait      *float64
	requestID string
}

// compactions is session.py compactions: each tengu_reactive_compact_succeeded,
// with its attempt, call and the context before and after.
func (s *promptSession) compactions() []compaction {
	type point struct{ t, ctx int64 }
	var main []point
	for _, x := range s.steps {
		if x.class == "main" {
			if c := x.context(); c != nil {
				main = append(main, point{x.ts, *c})
			}
		}
	}
	var attempts, fired []int64
	for _, e := range s.evs {
		switch e.name {
		case "tengu_reactive_compact_attempt":
			attempts = append(attempts, e.ts)
		case "tengu_idle_compact_fired":
			fired = append(fired, e.ts)
		}
	}
	var out []compaction
	prev := int64(-1)
	for _, e := range s.evs {
		if e.name != "tengu_reactive_compact_succeeded" {
			continue
		}
		var attempt *int64
		for _, a := range attempts {
			if prev < a && a <= e.ts && (attempt == nil || a > *attempt) {
				attempt = ptr(a)
			}
		}
		start := e.ts
		if attempt != nil {
			start = *attempt
		}
		c := compaction{t: e.ts}
		for _, x := range s.steps {
			if x.class == "compaction" && start <= x.ts && x.ts <= e.ts {
				c.requestID = x.id
				break
			}
		}
		for i := len(main) - 1; i >= 0; i-- {
			if main[i].t < start {
				c.from = ptr(main[i].ctx)
				break
			}
		}
		for _, m := range main {
			if m.t > e.ts {
				c.to = ptr(m.ctx)
				break
			}
		}
		for _, f := range fired {
			if start-idleLeadNS <= f && f <= start {
				c.idle = true
			}
		}
		if attempt != nil {
			c.wait = ptr(round1(float64(e.ts-*attempt) / 1e6))
		}
		out = append(out, c)
		prev = e.ts
	}
	return out
}

// kTokens is session.py _k: 1234 -> "1k" (Python's round: half to even).
func kTokens(n int64) string {
	if n >= 1000 {
		return strconv.FormatInt(int64(math.RoundToEven(float64(n)/1000)), 10) + "k"
	}
	return strconv.FormatInt(n, 10)
}

// --- ordered counts --------------------------------------------------------------------

type countOf struct {
	key string
	n   int64
}

// orderedCounts is a dict[str, int] that keeps Python's order when written as JSON.
type orderedCounts []countOf

func (o orderedCounts) add(k string) orderedCounts {
	for i := range o {
		if o[i].key == k {
			o[i].n++
			return o
		}
	}
	return append(o, countOf{k, 1})
}

// sortByCount orders by count, most first, then by key.
func (o orderedCounts) sortByCount() {
	sort.SliceStable(o, func(a, b int) bool { return o[a].n > o[b].n || o[a].n == o[b].n && o[a].key < o[b].key })
}

func (o orderedCounts) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, c := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(c.key)
		b.Write(k)
		b.WriteByte(':')
		b.WriteString(strconv.FormatInt(c.n, 10))
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func marshalJSON(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic(err) // our own types; only a NaN can fail, and finite() keeps those out
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// --- metadata values as Python reads them ------------------------------------------------

// pyObject is json.loads of a metadata object: its top-level fields, the last of a
// repeated key; empty unless it's an object.
func pyObject(s string) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	if s == "" {
		return out
	}
	dec := json.NewDecoder(strings.NewReader(s))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return out
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return map[string]json.RawMessage{}
		}
		var v json.RawMessage
		if dec.Decode(&v) != nil {
			return map[string]json.RawMessage{}
		}
		if k, ok := tok.(string); ok {
			out[k] = v
		}
	}
	return out
}

// pyStr is session.py _str: None for null, an object or a list; else str(v).
func pyStr(v json.RawMessage) *string {
	v = bytes.TrimSpace(v)
	if len(v) == 0 {
		return nil
	}
	switch v[0] {
	case 'n', '{', '[':
		return nil
	case '"':
		var s string
		if json.Unmarshal(v, &s) != nil {
			return nil
		}
		return &s
	case 't':
		return ptr("True")
	case 'f':
		return ptr("False")
	}
	if isIntLiteral(v) {
		n, ok := new(big.Int).SetString(string(v), 10)
		if ok {
			return ptr(n.String())
		}
	}
	f, err := strconv.ParseFloat(string(v), 64)
	if err != nil {
		return nil
	}
	return ptr(pyRepr(f))
}

// pyTruthy is Python's bool(v) for a JSON value.
func pyTruthy(v json.RawMessage) bool {
	v = bytes.TrimSpace(v)
	switch {
	case len(v) == 0, string(v) == "null", string(v) == "false", string(v) == `""`, string(v) == "[]", string(v) == "{}":
		return false
	case v[0] == '{' || v[0] == '[':
		var x any
		if json.Unmarshal(v, &x) == nil {
			switch t := x.(type) {
			case map[string]any:
				return len(t) > 0
			case []any:
				return len(t) > 0
			}
		}
		return true
	case v[0] == '"' || v[0] == 't':
		return true
	}
	f, err := strconv.ParseFloat(string(v), 64)
	return err != nil || f != 0
}

// pyTruthyStr is str(v) when v is truthy (as rows.tool_calls reads a name), else "".
func pyTruthyStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "True"
		}
	case float64:
		if t != 0 {
			return pyRepr(t)
		}
	}
	return ""
}

// pyIntOf is session.py _int: int(float(v)); None for a bool, null or anything
// float() refuses.
func pyIntOf(v json.RawMessage) *int64 {
	f := pyFloatOf(v)
	if f == nil || math.IsNaN(*f) || math.IsInf(*f, 0) {
		return nil
	}
	return truncInt(*f)
}

// pyFloatOf is session.py _float: float(v) for a number or a numeric string.
func pyFloatOf(v json.RawMessage) *float64 {
	v = bytes.TrimSpace(v)
	if len(v) == 0 {
		return nil
	}
	switch v[0] {
	case 'n', 't', 'f', '{', '[':
		return nil
	case '"':
		var s string
		if json.Unmarshal(v, &s) != nil {
			return nil
		}
		return pyParseFloat(s)
	}
	f, err := strconv.ParseFloat(string(v), 64)
	if err != nil {
		return nil
	}
	return &f
}

// pyNum is agents._num: a JSON number only.
func pyNum(v json.RawMessage) *float64 {
	v = bytes.TrimSpace(v)
	if len(v) == 0 || !(v[0] == '-' || v[0] >= '0' && v[0] <= '9') {
		return nil
	}
	f, err := strconv.ParseFloat(string(v), 64)
	if err != nil {
		return nil
	}
	return &f
}

// jsonStr is a JSON string's value (isinstance(v, str)); nil for anything else.
func jsonStr(v json.RawMessage) *string {
	v = bytes.TrimSpace(v)
	if len(v) == 0 || v[0] != '"' {
		return nil
	}
	var s string
	if json.Unmarshal(v, &s) != nil {
		return nil
	}
	return &s
}

// pyTrue is session.py _true: v is True or v == "true".
func pyTrue(v json.RawMessage) bool {
	v = bytes.TrimSpace(v)
	return string(v) == "true" || string(v) == `"true"`
}

// pyParseFloat is Python's float(str): surrounding whitespace, underscores between
// digits, inf and nan; no hex.
func pyParseFloat(s string) *float64 {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, "xXpP") {
		return nil
	}
	if strings.Contains(s, "_") {
		for i := range len(s) {
			if s[i] == '_' && (i == 0 || i == len(s)-1 || !isDigit(s[i-1]) || !isDigit(s[i+1])) {
				return nil
			}
		}
		s = strings.ReplaceAll(s, "_", "")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		var ne *strconv.NumError
		if errors.As(err, &ne) && ne.Err == strconv.ErrRange {
			return &f // Python: overflow is inf, underflow 0
		}
		return nil
	}
	return &f
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

func isIntLiteral(v []byte) bool {
	for i, b := range v {
		if !(isDigit(b) || i == 0 && b == '-') {
			return false
		}
	}
	return true
}

// truncInt is int(f): toward zero.
func truncInt(f float64) *int64 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil
	}
	return ptr(int64(math.Trunc(f)))
}

// pyRepr is repr(float): the shortest round-trip digits, exponent form below 1e-4
// and from 1e16.
func pyRepr(f float64) string {
	switch {
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	case math.IsNaN(f):
		return "nan"
	}
	e := strconv.FormatFloat(f, 'e', -1, 64) // d.ddde±XX
	mant, exps, _ := strings.Cut(e, "e")
	exp, _ := strconv.Atoi(exps)
	if exp < -4 || exp >= 16 {
		sign := "+"
		if exp < 0 {
			sign, exp = "-", -exp
		}
		return mant + "e" + sign + fmt.Sprintf("%02d", exp)
	}
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.ContainsAny(s, ".") {
		s += ".0"
	}
	return s
}

// finite drops NaN and infinities (JSON can't hold them).
func finite(f *float64) *float64 {
	if f == nil || math.IsNaN(*f) || math.IsInf(*f, 0) {
		return nil
	}
	return f
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}
