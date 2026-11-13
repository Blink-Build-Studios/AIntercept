package state

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// The request drawer's tables (docs/STREAMING_PIPELINE.md §4.9): each call's
// own rows, so opening one call reads only those, never its session's.
const (
	TableDrawerLocator = "drawer_locator"
	TableDrawerCalls   = "drawer_calls"
	TableDrawerSteps   = "drawer_steps"
	TableDrawerFacts   = "drawer_facts"
	TableDrawerSandbox = "drawer_sandbox"
	TableDrawerReports = "drawer_reports"
	TableDrawerEvents  = "drawer_events"
)

// The roles of a call's facts.
const (
	RoleSuccess = "success" // its tengu_api_success
	RoleStall   = "stall"   // its streaming stall summary
	RoleTool    = "tool"    // a tool use it asked for
	RoleBash    = "bash"    // the Bash command of one of its tool uses
	RoleQuery   = "query"   // the tengu_api_query just before it
	RoleRetry   = "retry"   // a failed call's tengu_api_retry
)

// The drawer's cases.
const (
	CaseDrawerLocator   = "drawer.locator"
	CaseDrawerCallStart = "drawer.call_start"
	CaseDrawerCallEnd   = "drawer.call_end"
	CaseDrawerChain     = "drawer.chain"
	CaseDrawerEvent     = "drawer.event"
	CaseDrawerFact      = "drawer.fact"
	CaseDrawerSandbox   = "drawer.sandbox"
	CaseDrawerReport    = "drawer.report"
)

var drawerCases = []string{CaseDrawerLocator, CaseDrawerCallStart, CaseDrawerCallEnd, CaseDrawerChain,
	CaseDrawerEvent, CaseDrawerFact, CaseDrawerSandbox, CaseDrawerReport}

// DrawerLocator is where a call (or a reported call) is: the key its rows are under.
type DrawerLocator struct {
	Target string `parquet:"target"`
}

// DrawerCall is a call's request row, in flight until it ends.
type DrawerCall struct {
	canon.Request
	InFlight     bool   `parquet:"in_flight"`
	BatchSession string `parquet:"batch_session,optional"`
}

// DrawerStep is a visible model call's place in its session.
type DrawerStep struct {
	CallID        string `parquet:"call_id"` // connection~turn for a WebSocket turn
	PromptID      string `parquet:"prompt_id,optional"`
	RequestClass  string `parquet:"request_class,optional"`
	TS            int64  `parquet:"ts"`
	EndTS         *int64 `parquet:"end_ts,optional"`
	Status        *int64 `parquet:"status,optional"`
	RequestID     string `parquet:"request_id,optional"`
	Model         string `parquet:"model,optional"`
	StopReason    string `parquet:"stop_reason,optional"`
	MessageID     string `parquet:"message_id,optional"`
	PrevMessageID string `parquet:"prev_message_id,optional"`
	PrevCall      string `parquet:"prev_call,optional"` // by the message-id chain
	NextCall      string `parquet:"next_call,optional"`
}

// DrawerEvent is one Claude Code event as the drawer shows it.
type DrawerEvent struct {
	EventID  string `parquet:"event_id"` // <batch>:<n>
	BatchID  string `parquet:"batch_id"`
	TS       int64  `parquet:"ts"`
	RowTS    int64  `parquet:"row_ts"`
	Seq      int64  `parquet:"seq"`
	Name     string `parquet:"name,optional"`
	Session  string `parquet:"session_id,optional"`
	PromptID string `parquet:"prompt_id,optional"`
	DeviceID string `parquet:"device_id,optional"`
	HeadSHA  string `parquet:"head_sha,optional"`
	MD       string `parquet:"md,optional"`
	EventKey string `parquet:"event_key"`
	// The ids it names (its request_id and client_request_id columns).
	RequestID       string `parquet:"request_id,optional"`
	ClientRequestID string `parquet:"client_request_id,optional"`
}

// DrawerFact is one event about a call, by its role and what the drawer looks
// it up by: success and stall by the request id the event names (success by a
// client request id too), tool by the request id, bash by the tool use id,
// query and retry by the call (they carry no id: they're the call's by time).
// Keyed by id, an event is written once, however many calls share the id.
type DrawerFact struct {
	Lookup string `parquet:"lookup"`
	Role   string `parquet:"role"`
	DrawerEvent
}

// DrawerSandbox is the session's sandbox setting from one Bash command on.
type DrawerSandbox struct {
	TS      int64  `parquet:"ts"`
	EventID string `parquet:"event_id"`
	Value   string `parquet:"value"`
}

// DrawerReport is a tengu_api_success, by the id its reported row opens with.
type DrawerReport struct {
	ReportID string `parquet:"report_id"`
	DrawerEvent
}

func drawerEventOf(id string, e *canon.Event) DrawerEvent {
	return DrawerEvent{EventID: id, BatchID: e.CorrelationID, TS: e.NS, RowTS: e.RowNS, Seq: e.Seq, Name: e.Name,
		Session: e.SessionID, PromptID: e.PromptID, DeviceID: e.DeviceID, HeadSHA: e.HeadSHA, MD: e.MetadataJSON,
		EventKey: e.EventKey, RequestID: e.RequestID, ClientRequestID: e.ClientRequestID}
}

// stepOf is a call's step when it's a visible model call (the API's cc_calls):
// an agent's model call, not synthetic, of a session, not hidden.
func stepOf(id string, c DrawerCall, sid string) (DrawerStep, bool) {
	r := c.Request
	product := r.Product
	if product == "" && r.Host == "api.anthropic.com" {
		product = canon.ProductClaudeCode
	}
	if r.Synthetic || r.SessionID == "" || r.HiddenKind != "" {
		return DrawerStep{}, false
	}
	ok := product == canon.ProductClaudeCode && r.Host == "api.anthropic.com" && strings.HasPrefix(r.Path, "/v1/messages") &&
		!strings.HasPrefix(r.Path, "/v1/messages/count_tokens") ||
		product == canon.ProductCodex && r.Host == "chatgpt.com" && r.Path == "/backend-api/codex/responses" &&
			(r.WSTurn != 0 || r.Method == "POST")
	if !ok {
		return DrawerStep{}, false
	}
	ts := r.StartNS
	if ts == 0 {
		ts = r.EndNS
	}
	st := DrawerStep{CallID: strings.ReplaceAll(id, "#", "~"), PromptID: r.PromptID, RequestClass: r.RequestClass, TS: ts,
		RequestID: r.RequestID, Model: r.Model, StopReason: r.StopReason, MessageID: r.MessageID, PrevMessageID: r.PreviousMessageID}
	if !c.InFlight {
		end := r.EndNS
		st.EndTS = &end
		if r.Status != 0 {
			status := r.Status
			st.Status = &status
		}
	}
	return st, true
}

func lessStep(a, b DrawerStep) bool { return a.TS < b.TS || a.TS == b.TS && a.CallID < b.CallID }

func callFailed(c DrawerCall) bool {
	if c.InFlight {
		return false
	}
	return c.Status == 0 && c.EndNS != 0 || c.Status >= 400
}

const secNS = int64(1_000_000_000)

// --- the per-key indexes ------------------------------------------------------------

type dOrd struct {
	ns   int64
	corr string
	n    int64
}

func lessDOrd(a, b dOrd) bool {
	if a.ns != b.ns {
		return a.ns < b.ns
	}
	if a.corr != b.corr {
		return a.corr < b.corr
	}
	return a.n < b.n
}

type dEvent struct {
	ord dOrd
	ev  DrawerEvent
	md  map[string]any
}

type dset map[string]bool

func daddTo(m map[string]dset, k, v string) {
	if k == "" {
		return
	}
	if m[k] == nil {
		m[k] = dset{}
	}
	m[k][v] = true
}

func ddelFrom(m map[string]dset, k, v string) {
	if k == "" || m[k] == nil {
		return
	}
	delete(m[k], v)
	if len(m[k]) == 0 {
		delete(m, k)
	}
}

type drawerState struct {
	calls map[string]DrawerCall // by call id
	steps map[string]DrawerStep // by call id
	// Calls by what their facts are found by.
	byRID, byCRID, byPrompt map[string]dset
	// Steps by message ids, for the chain.
	byMID, byPrev map[string]dset
	// Events by what finds them.
	events  map[string]*dEvent               // by event id
	success map[string]*omap[dOrd, string]   // id (requestId or clientRequestId) → success events
	stalls  map[string]*omap[dOrd, string]   // request_id → stall summaries
	tools   map[string]*omap[dOrd, string]   // requestId → tool uses
	bash    map[string]*omap[dOrd, string]   // tool_use_id → Bash commands
	queries map[string]*omap[dOrd, string]   // prompt → tengu_api_query
	retries *omap[dOrd, string]              // every tengu_api_retry
	toolRID map[string]dset                  // toolUseID → requestIds of tool uses naming it
	sandbox *omap[dOrd, string]              // Bash commands' sandbox_enabled, by event order
	reports map[string]*omap[dOrd, string]   // report id → success events
	facts   map[string]map[string]DrawerFact // call id → fact row id → fact
}

func newDrawerState() *drawerState {
	return &drawerState{calls: map[string]DrawerCall{}, steps: map[string]DrawerStep{},
		byRID: map[string]dset{}, byCRID: map[string]dset{}, byPrompt: map[string]dset{},
		byMID: map[string]dset{}, byPrev: map[string]dset{}, events: map[string]*dEvent{},
		success: map[string]*omap[dOrd, string]{}, stalls: map[string]*omap[dOrd, string]{},
		tools: map[string]*omap[dOrd, string]{}, bash: map[string]*omap[dOrd, string]{},
		queries: map[string]*omap[dOrd, string]{}, retries: newOmap[dOrd, string](lessDOrd),
		toolRID: map[string]dset{}, sandbox: newOmap[dOrd, string](lessDOrd),
		reports: map[string]*omap[dOrd, string]{}, facts: map[string]map[string]DrawerFact{}}
}

func dindex(m map[string]*omap[dOrd, string], k string, o dOrd, id string) {
	if k == "" {
		return
	}
	x := m[k]
	if x == nil {
		x = newOmap[dOrd, string](lessDOrd)
		m[k] = x
	}
	x.Set(o, id)
}

func dfirst(m map[string]*omap[dOrd, string], k string) (string, bool) {
	if k == "" || m[k] == nil {
		return "", false
	}
	_, id, ok := m[k].First()
	return id, ok
}

// --- applying ---------------------------------------------------------------------------

func (s *KeyState) drawer(r records.Record, d *Delta) {
	x := s.dr
	if r.Kind == records.KindLocator {
		t := records.LocatorTarget(&r)
		d.Cases = append(d.Cases, CaseDrawerLocator)
		s.rows.put(d, TableDrawerLocator, t, DrawerLocator{Target: t})
		return
	}
	if strings.HasPrefix(s.Key, "l:") {
		return
	}
	sid := sessionOf(s.Key)
	switch r.Kind {
	case records.KindCallEnd, records.KindWSConnection:
		if r.Call == nil {
			return
		}
		d.Cases = append(d.Cases, CaseDrawerCallEnd)
		s.dsetCall(d, callKey(r), DrawerCall{Request: *r.Call, BatchSession: r.BatchSession}, sid)
	case records.KindCallStart:
		if r.Call == nil {
			return
		}
		if c, ok := x.calls[callKey(r)]; ok && !c.InFlight {
			return
		}
		d.Cases = append(d.Cases, CaseDrawerCallStart)
		s.dsetCall(d, callKey(r), DrawerCall{Request: *r.Call, InFlight: true}, sid)
	case records.KindClientEvent:
		if r.Event == nil || r.Event.Source != canon.ProductClaudeCode {
			return
		}
		d.Cases = append(d.Cases, CaseDrawerEvent)
		s.daddEvent(d, r, sid)
	}
}

// callKey is a call record's row id: its call id (a record without one, its own id).
func callKey(r records.Record) string {
	if r.CallID != "" {
		return r.CallID
	}
	return "r:" + r.RecordID
}

func (s *KeyState) dsetCall(d *Delta, id string, c DrawerCall, sid string) {
	x := s.dr
	old, had := x.calls[id]
	if had {
		ddelFrom(x.byRID, old.RequestID, id)
		ddelFrom(x.byCRID, old.ClientRequestID, id)
		ddelFrom(x.byPrompt, old.PromptID, id)
	}
	x.calls[id] = c
	daddTo(x.byRID, c.RequestID, id)
	daddTo(x.byCRID, c.ClientRequestID, id)
	daddTo(x.byPrompt, c.PromptID, id)
	s.rows.put(d, TableDrawerCalls, id, c)
	// Its step, and the chain around it.
	affected := dset{}
	if st, ok := x.steps[id]; ok {
		ddelFrom(x.byMID, st.MessageID, st.CallID)
		ddelFrom(x.byPrev, st.PrevMessageID, st.CallID)
		for k := range x.byPrev[st.MessageID] {
			affected[k] = true
		}
		for k := range x.byMID[st.PrevMessageID] {
			affected[k] = true
		}
		delete(x.steps, id)
		s.rows.drop(d, TableDrawerSteps, st.CallID)
	}
	if st, ok := stepOf(id, c, sid); ok {
		x.steps[id] = st
		daddTo(x.byMID, st.MessageID, st.CallID)
		daddTo(x.byPrev, st.PrevMessageID, st.CallID)
		affected[st.CallID] = true
		for k := range x.byPrev[st.MessageID] {
			affected[k] = true
		}
		for k := range x.byMID[st.PrevMessageID] {
			affected[k] = true
		}
	}
	chained := false
	for cid := range affected {
		if s.drelink(d, cid) && cid != strings.ReplaceAll(id, "#", "~") {
			chained = true
		}
	}
	if st, ok := x.steps[id]; ok && (st.PrevCall != "" || st.NextCall != "") {
		chained = true
	}
	if chained {
		d.Cases = append(d.Cases, CaseDrawerChain)
	}
	if sid != "" {
		s.drefacts(d, id)
	}
}

// stepByCID is a step by its (drawer) call id.
func (x *drawerState) stepByCID(cid string) (string, DrawerStep, bool) {
	id := strings.Replace(cid, "~", "#", 1)
	st, ok := x.steps[id]
	return id, st, ok
}

// best is the greatest step (by time, then id) of a set: the chain's pick.
func (x *drawerState) best(set dset) string {
	var out DrawerStep
	found := false
	for cid := range set {
		_, st, ok := x.stepByCID(cid)
		if ok && (!found || lessStep(out, st)) {
			out, found = st, true
		}
	}
	return out.CallID
}

// drelink sets one step's chain neighbours; true when they changed.
func (s *KeyState) drelink(d *Delta, cid string) bool {
	x := s.dr
	id, st, ok := x.stepByCID(cid)
	if !ok {
		return false
	}
	prev, next := "", ""
	if st.PrevMessageID != "" {
		prev = x.best(x.byMID[st.PrevMessageID])
	}
	if st.MessageID != "" {
		next = x.best(x.byPrev[st.MessageID])
	}
	changed := prev != st.PrevCall || next != st.NextCall
	st.PrevCall, st.NextCall = prev, next
	x.steps[id] = st
	s.rows.put(d, TableDrawerSteps, st.CallID, st)
	return changed
}

func (s *KeyState) daddEvent(d *Delta, r records.Record, sid string) {
	x := s.dr
	e := r.Event
	id := r.BatchID + ":" + strconv.FormatInt(r.BatchCCN, 10)
	if _, ok := x.events[id]; ok {
		return
	}
	ev := &dEvent{ord: dOrd{e.NS, e.CorrelationID, r.BatchCCN}, ev: drawerEventOf(id, e)}
	ev.md = map[string]any{}
	_ = json.Unmarshal([]byte(e.MetadataJSON), &ev.md)
	s.dindexEvent(d, ev)
	s.rows.put(d, TableDrawerEvents, id, ev.ev)
	affected := s.deventCalls(ev)
	if sid != "" {
		for cid := range affected {
			s.drefacts(d, cid)
		}
	}
}

// dindexEvent files one event where its facts are found, and writes its report
// and sandbox rows.
func (s *KeyState) dindexEvent(d *Delta, ev *dEvent) {
	x := s.dr
	x.events[ev.ev.EventID] = ev
	id, md := ev.ev.EventID, ev.md
	switch ev.ev.Name {
	case "tengu_api_success":
		dindex(x.success, dmd(md, "requestId"), ev.ord, id)
		if c := dmd(md, "clientRequestId"); c != dmd(md, "requestId") {
			dindex(x.success, c, ev.ord, id)
		}
		rid := dmd(md, "requestId")
		if rid == "" {
			rid = dmd(md, "clientRequestId")
		}
		if rid == "" {
			rid = strconv.FormatInt(ev.ev.TS, 10)
		}
		dindex(x.reports, rid, ev.ord, id)
		if first, _ := dfirst(x.reports, rid); first == id && d != nil {
			d.Cases = append(d.Cases, CaseDrawerReport)
			s.rows.put(d, TableDrawerReports, rid, DrawerReport{ReportID: rid, DrawerEvent: ev.ev})
		}
		for _, k := range []string{dmd(md, "requestId"), dmd(md, "clientRequestId")} {
			s.firstFact(d, RoleSuccess, k, x.success)
		}
	case "tengu_streaming_stall_summary":
		rid := dmd(md, "request_id")
		dindex(x.stalls, rid, ev.ord, id)
		s.firstFact(d, RoleStall, rid, x.stalls)
	case "tengu_tool_use_success", "tengu_tool_use_error":
		rid := dmd(md, "requestId")
		dindex(x.tools, rid, ev.ord, id)
		daddTo(x.toolRID, dmd(md, "toolUseID"), rid)
		s.putFact(d, RoleTool, rid, ev)
	case "tengu_bash_tool_command_executed":
		tid := dmd(md, "tool_use_id")
		dindex(x.bash, tid, ev.ord, id)
		s.putFact(d, RoleBash, tid, ev)
		if v := dmd(md, "sandbox_enabled"); v != "" {
			x.sandbox.Set(ev.ord, id)
			if d != nil {
				s.dresandbox(d, ev.ord)
			}
		}
	case "tengu_api_query":
		dindex(x.queries, ev.ev.PromptID, ev.ord, id)
	case "tengu_api_retry":
		x.retries.Set(ev.ord, id)
	}
}

// firstFact writes the first event under k (success, stall) as its fact.
func (s *KeyState) firstFact(d *Delta, role, k string, m map[string]*omap[dOrd, string]) {
	if d == nil || k == "" || sessionOf(s.Key) == "" {
		return
	}
	if id, ok := dfirst(m, k); ok {
		row := DrawerFact{Lookup: k, Role: role, DrawerEvent: s.dr.events[id].ev}
		if cur, ok := s.rows[TableDrawerFacts][role+"/"+k]; !ok || cur != any(row) {
			d.Cases = append(d.Cases, CaseDrawerFact)
		}
		s.rows.put(d, TableDrawerFacts, role+"/"+k, row)
	}
}

// putFact writes one event as a fact under k (tool, bash).
func (s *KeyState) putFact(d *Delta, role, k string, ev *dEvent) {
	if d == nil || k == "" || sessionOf(s.Key) == "" {
		return
	}
	d.Cases = append(d.Cases, CaseDrawerFact)
	s.rows.put(d, TableDrawerFacts, role+"/"+k+"/"+ev.ev.EventID, DrawerFact{Lookup: k, Role: role, DrawerEvent: ev.ev})
}

// dresandbox redoes the change-point rows of the Bash command at o and the one after it.
func (s *KeyState) dresandbox(d *Delta, o dOrd) {
	x := s.dr
	var prev string
	if _, id, ok := x.sandbox.Lower(o); ok {
		prev = dmd(x.events[id].md, "sandbox_enabled")
	}
	// o itself, and the command after it.
	var ids []string
	x.sandbox.Ascend(o, func(_ dOrd, id string) bool {
		ids = append(ids, id)
		return len(ids) < 2
	})
	for _, id := range ids {
		ev := x.events[id]
		v := dmd(ev.md, "sandbox_enabled")
		if v != prev {
			d.Cases = append(d.Cases, CaseDrawerSandbox)
			s.rows.put(d, TableDrawerSandbox, id, DrawerSandbox{TS: ev.ev.TS, EventID: id, Value: v})
		} else {
			s.rows.drop(d, TableDrawerSandbox, id)
		}
		prev = v
	}
}

// deventCalls is the calls an event can be a fact of.
func (s *KeyState) deventCalls(ev *dEvent) dset {
	x := s.dr
	out := dset{}
	add := func(set dset) {
		for k := range set {
			out[k] = true
		}
	}
	switch ev.ev.Name {
	case "tengu_api_query":
		add(x.byPrompt[ev.ev.PromptID])
	case "tengu_api_retry":
		add(x.byPrompt[ev.ev.PromptID])
		add(x.byRID[ev.ev.RequestID])
		add(x.byCRID[ev.ev.ClientRequestID])
	}
	return out
}

func dmd(m map[string]any, k string) string {
	v, ok := m[k]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case map[string]any, []any:
		return ""
	default:
		b, _ := json.Marshal(t)
		return strings.Trim(string(b), `"`)
	}
}

// drefacts recomputes one call's facts and writes what changed.
func (s *KeyState) drefacts(d *Delta, id string) {
	x := s.dr
	c, ok := x.calls[id]
	if !ok {
		return
	}
	want := map[string]DrawerFact{}
	cid := strings.ReplaceAll(id, "#", "~")
	add := func(role, eid string) {
		ev := x.events[eid]
		want[role+"/"+cid] = DrawerFact{Lookup: cid, Role: role, DrawerEvent: ev.ev}
	}
	rid, crid, prompt := c.RequestID, c.ClientRequestID, c.PromptID
	ts := c.StartNS
	if ts == 0 {
		ts = c.EndNS
	}
	if q := x.queries[prompt]; q != nil && prompt != "" {
		var best string
		q.Ascend(dOrd{ns: ts - 60*secNS}, func(o dOrd, eid string) bool {
			if o.ns > ts+2*secNS {
				return false
			}
			best = eid
			return true
		})
		if best != "" {
			add(RoleQuery, best)
		}
	}
	if callFailed(c) {
		stop := c.EndNS
		if stop == 0 {
			stop = ts
		}
		var best string
		var bestD, bestAt int64
		x.retries.Ascend(dOrd{ns: ts}, func(o dOrd, eid string) bool {
			if o.ns > stop+30*secNS {
				return false
			}
			ev := x.events[eid]
			p := ev.ev.PromptID
			if !(prompt != "" && p == prompt || rid != "" && ev.ev.RequestID == rid || crid != "" && ev.ev.ClientRequestID == crid) {
				return true
			}
			if prompt != "" && p != "" && p != prompt {
				return true
			}
			dd := o.ns - stop
			if dd < 0 {
				dd = -dd
			}
			if best == "" || dd < bestD || dd == bestD && o.ns < bestAt {
				best, bestD, bestAt = eid, dd, o.ns
			}
			return true
		})
		if best != "" {
			add(RoleRetry, best)
		}
	}
	have := x.facts[id]
	for k := range have {
		if _, ok := want[k]; !ok {
			s.rows.drop(d, TableDrawerFacts, k)
		}
	}
	for k, f := range want {
		if old, ok := have[k]; !ok || old != f {
			d.Cases = append(d.Cases, CaseDrawerFact)
		}
		s.rows.put(d, TableDrawerFacts, k, f)
	}
	if len(want) == 0 {
		delete(x.facts, id)
	} else {
		x.facts[id] = want
	}
}

// rebuildDrawer is the indexes again from the stored rows (Decode).
func (s *KeyState) rebuildDrawer() {
	sid := sessionOf(s.Key)
	rows := s.rows
	for id, row := range rows[TableDrawerCalls] {
		c := row.(DrawerCall)
		x := s.dr
		x.calls[id] = c
		daddTo(x.byRID, c.RequestID, id)
		daddTo(x.byCRID, c.ClientRequestID, id)
		daddTo(x.byPrompt, c.PromptID, id)
		if st, ok := stepOf(id, c, sid); ok {
			if old, ok := rows[TableDrawerSteps][st.CallID]; ok {
				st = old.(DrawerStep)
			}
			x.steps[id] = st
			daddTo(x.byMID, st.MessageID, st.CallID)
			daddTo(x.byPrev, st.PrevMessageID, st.CallID)
		}
	}
	for k, row := range rows[TableDrawerFacts] {
		f := row.(DrawerFact)
		if f.Role != RoleQuery && f.Role != RoleRetry {
			continue
		}
		id := strings.Replace(f.Lookup, "~", "#", 1)
		if s.dr.facts[id] == nil {
			s.dr.facts[id] = map[string]DrawerFact{}
		}
		s.dr.facts[id][k] = f
	}
	for _, row := range rows[TableDrawerEvents] {
		e := row.(DrawerEvent)
		ev := &dEvent{ord: dOrd{e.TS, e.BatchID, ccnOf(e.EventID)}, ev: e, md: map[string]any{}}
		_ = json.Unmarshal([]byte(e.MD), &ev.md)
		s.dindexEvent(nil, ev)
	}
}

func ccnOf(eventID string) int64 {
	i := strings.LastIndex(eventID, ":")
	n, _ := strconv.ParseInt(eventID[i+1:], 10, 64)
	return n
}
