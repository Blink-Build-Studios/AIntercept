package process

import "strconv"

// The prompt rows' and marks' parsers and builders, for the streaming
// processor's per-key state (internal/stream/state): it keeps the same inputs
// these read and calls these to build each step and value, so the stream and the
// reference share one implementation.

// StepCall is a model call as the prompt rows read it: a step when Visible.
type StepCall struct{ c rowCall }

// StepCallOfRequest is an ended request's call.
func StepCallOfRequest(r Request) StepCall { return StepCall{callFromRequest(colsOf(r), interner{})} }

// StepCallOfPending is a call in flight.
func StepCallOfPending(p Pending) StepCall { return StepCall{callFromPending(p)} }

func (s StepCall) Visible() bool        { return s.c.ccCall() }    // a step (the API's cc_calls)
func (s StepCall) Agent() bool          { return s.c.agentCall() } // a coding agent's model call (hidden ones too)
func (s StepCall) Hidden() string       { return s.c.hidden }
func (s StepCall) ID() string           { return s.c.id }
func (s StepCall) TS() int64            { return s.c.ts }
func (s StepCall) End() int64           { return s.c.end }
func (s StepCall) Session() string      { return s.c.session }
func (s StepCall) Prompt() string       { return s.c.prompt }
func (s StepCall) Class() string        { return s.c.class }
func (s StepCall) ReqID() string        { return s.c.reqID }
func (s StepCall) ClientReqID() string  { return s.c.clientReqID }
func (s StepCall) Model() string        { return s.c.model }
func (s StepCall) Stop() string         { return s.c.stop }
func (s StepCall) IP() string           { return s.c.ip }
func (s StepCall) Product() string      { return s.c.product }
func (s StepCall) Output() int64        { return s.c.output }
func (s StepCall) Input() int64         { return s.c.input }
func (s StepCall) CacheRead() int64     { return s.c.cacheRead }
func (s StepCall) CacheWrite() int64    { return s.c.cacheWrite }
func (s StepCall) Context() *int64      { return s.c.context() }
func (s StepCall) HasDetail() bool      { return s.c.detail != nil }
func (s StepCall) DetailPrompt() string { return detailPrompt(s.c.detail) }

func detailPrompt(d *callDetail) string {
	if d == nil {
		return ""
	}
	return d.prompt
}

// DetailResults is the exit codes its recorded bodies give each tool call id, in
// the JSON's order.
func (s StepCall) DetailResults() []IDCodes {
	if s.c.detail == nil {
		return nil
	}
	out := make([]IDCodes, len(s.c.detail.results))
	for i, r := range s.c.detail.results {
		out[i] = IDCodes{r.id, r.codes}
	}
	return out
}

// DetailCallIDs is the tool call ids its recorded bodies name (whose exit codes
// its step shows).
func (s StepCall) DetailCallIDs() []string {
	if s.c.detail == nil {
		return nil
	}
	out := make([]string, len(s.c.detail.calls))
	for i, c := range s.c.detail.calls {
		out[i] = c.id
	}
	return out
}

// IDCodes is a tool call's exit codes.
type IDCodes struct {
	ID    string
	Codes []int64
}

// PromptEvent is one of a session's prompt-row events (promptEventNames).
type PromptEvent struct{ e *promptEvent }

// IsPromptEvent says whether the prompt rows read an event of this name.
func IsPromptEvent(name string) bool { return promptEventNames[name] }

// PromptEventOf is a prompt-row event; n is its number in its batch (an input
// with no prompt id is named by it).
func PromptEventOf(e Event, n int64) PromptEvent {
	pe := newPromptEvent(eventColsOf(e))
	pe.n = int(n)
	if pe.unnamedInput() {
		pe.id = pe.corr + ":" + itoa64(n)
	}
	pe.md = ""
	return PromptEvent{pe}
}

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }

// PromptEventFacts is what the prompt rows read of a prompt-row event.
type PromptEventFacts struct {
	Name, Prompt          string
	UnnamedID             string // <batch>:<n>, for an input with no prompt id
	ReqID, ClientReqID    string // session.py _str, "" for none
	ToolName              *string
	ToolKey               string // a tool or tick's identity: its use id, else its name (session.py _step)
	BashToolUseID         *string
	Destructive           bool
	PromptIndex, Length   *int64
	Negative, HadRecap    bool
	PastedBytes           *int64
	IsSuccess, IsTool     bool
	IsTick, IsBash, Paste bool
}

func (p PromptEvent) Facts() PromptEventFacts {
	e := p.e
	f := PromptEventFacts{Name: e.name, Prompt: e.prompt, UnnamedID: e.id, ReqID: deref(e.reqID),
		ClientReqID: deref(e.clientReqID), ToolName: e.toolName, BashToolUseID: e.bashToolUseID,
		Destructive: e.destructive != nil, PromptIndex: e.promptIndex, Length: e.promptLength,
		Negative: e.negative, HadRecap: e.hadRecap, PastedBytes: e.pastedBytes}
	switch e.name {
	case "tengu_api_success":
		f.IsSuccess = true
	case "tengu_tool_use_success", "tengu_tool_use_error":
		f.IsTool = true
	case tickEvent:
		f.IsTick = true
	case "tengu_bash_tool_command_executed":
		f.IsBash = true
	case "tengu_paste_text", "tengu_paste_image":
		f.Paste = true
	}
	switch {
	case deref(e.toolUseID) != "":
		f.ToolKey = "s" + *e.toolUseID
	case e.toolName != nil:
		f.ToolKey = "s" + *e.toolName
	default:
		f.ToolKey = "n"
	}
	return f
}

// ToolUseID is a tool event's toolUseID ("" for none): the Bash event it joins.
func (p PromptEvent) ToolUseID() string { return deref(p.e.toolUseID) }

// StepJoins is what a step's row joins: its report (tengu_api_success, by its
// request id, else its client request id), its finished tools and its progress
// ticks (by request id, in the session's event order), the Bash events by tool
// use id (the last in order), and the exit codes by tool call id (the first
// step's, in step order).
type StepJoins struct {
	SuccessByReq, SuccessByClient *PromptEvent
	Tools, Ticks                  []PromptEvent
	Bash                          func(toolUseID string) *PromptEvent
	Results                       func(callID string) ([]int64, bool)
}

// BuildStep is a step's row (session.py _step), its number and prompt's seq left
// zero (the query numbers them) and events_pending_if_live as if no event
// followed it (the query applies the session's last prompt-row event).
func BuildStep(c StepCall, sessionID string, j StepJoins) PromptStep {
	jj := &joins{success: map[string]*promptEvent{}, tools: map[string][]*promptEvent{}, ticks: map[string][]*promptEvent{},
		bash: map[string]*promptEvent{}, results: map[string][]int64{}}
	if j.SuccessByReq != nil && c.c.reqID != "" {
		jj.success[c.c.reqID] = j.SuccessByReq.e
	}
	if j.SuccessByClient != nil && c.c.clientReqID != "" {
		if _, ok := jj.success[c.c.clientReqID]; !ok {
			jj.success[c.c.clientReqID] = j.SuccessByClient.e
		}
	}
	for _, e := range j.Tools {
		jj.tools[c.c.reqID] = append(jj.tools[c.c.reqID], e.e)
		addBash(jj, e, j.Bash)
	}
	for _, e := range j.Ticks {
		jj.ticks[c.c.reqID] = append(jj.ticks[c.c.reqID], e.e)
		addBash(jj, e, j.Bash)
	}
	if c.c.detail != nil && j.Results != nil {
		for _, dc := range c.c.detail.calls {
			if codes, ok := j.Results(dc.id); ok {
				jj.results[dc.id] = codes
			}
		}
	}
	s := &promptSession{id: sessionID}
	return s.step(&c.c, jj, 0)
}

func addBash(jj *joins, e PromptEvent, bash func(string) *PromptEvent) {
	if tid := deref(e.e.toolUseID); tid != "" && bash != nil {
		if b := bash(tid); b != nil {
			jj.bash[tid] = b.e
		}
	}
}

// TurnReport is a codex_turn_event's report: its status (non-empty), duration
// and JSON (the prompt row's turn_report_json).
func TurnReport(metadataJSON string) (hasStatus bool, durationMS *float64, js string) {
	r := turnReport(metadataJSON)
	return deref(r.Status) != "", r.DurationMS, marshalJSON(r)
}

// CountsJSON is an ordered count dict as JSON: keys in the order given, or by
// count (most first, then key) when byCount.
func CountsJSON(keys []string, counts map[string]int64, byCount bool) string {
	o := make(orderedCounts, 0, len(keys))
	for _, k := range keys {
		o = append(o, countOf{k, counts[k]})
	}
	if byCount {
		o.sortByCount()
	}
	return marshalJSON(o)
}

// CompactionLabel is a compaction between row's kind and detail.
func CompactionLabel(idle bool, from, to *int64) (kind, detail string) {
	kind, detail = "compaction", "compaction"
	if idle {
		kind, detail = "idle-compaction", "idle compaction"
	}
	if from != nil && to != nil && *from != 0 && *to != 0 {
		detail += " · context " + kTokens(*from) + " → " + kTokens(*to) + " tokens"
	}
	return kind, detail
}

// Round1 is Python's round(x, 1).
func Round1(x float64) float64 { return round1(x) }

// IdleLeadNS is how long before its compaction attempt a tengu_idle_compact_fired
// makes it an idle compaction.
const IdleLeadNS = idleLeadNS

// MarkEventFacts is what the session marks read of a Claude Code event.
type MarkEventFacts struct {
	PromptID           string // $.cc_prompt_id
	Index              *int64 // $.prompt_index
	Negative           bool
	Destructive, Scope string
	Mode               *string // tengu_api_query's permissionMode
}

// IsMarkEvent says whether the marks read an event of this name.
func IsMarkEvent(name string) bool { return markEventNames[name] }

func MarkEventOf(e Event) MarkEventFacts {
	me := newMarkEvent(eventColsOf(e))
	return MarkEventFacts{PromptID: me.promptID, Index: me.index, Negative: me.negative, Destructive: me.destructive,
		Scope: me.scope, Mode: me.mode}
}

// ReportIDsOf is a tengu_api_success's request and client request ids as the
// marks read them.
func ReportIDsOf(e Event) (rid, crid string) {
	md := jsonFields(e.MetadataJSON, "requestId", "clientRequestId")
	rid, _ = jsonString(md["requestId"])
	crid, _ = jsonString(md["clientRequestId"])
	return rid, crid
}
