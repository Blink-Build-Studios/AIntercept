package state

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// How a record reaches the prompt tables and marks. Each index below is what
// one kind of record needs to find the rows it changes: a record updates its
// own rows and, through these, its few neighbours (the steps its report or
// tools join, the prompt before or after a hidden call, the compactions around
// a main call), never the whole session.

// evOrd is a session event's place in the reference's order: time, batch, then
// its place in the batch (seq, row time, name, metadata), then key.
type evOrd struct {
	ts            int64
	corr          string
	seq, rowNS    int64
	name, md, key string
}

func lessEvOrd(a, b evOrd) bool {
	switch {
	case a.ts != b.ts:
		return a.ts < b.ts
	case a.corr != b.corr:
		return a.corr < b.corr
	case a.seq != b.seq:
		return a.seq < b.seq
	case a.rowNS != b.rowNS:
		return a.rowNS < b.rowNS
	case a.name != b.name:
		return a.name < b.name
	case a.md != b.md:
		return a.md < b.md
	}
	return a.key < b.key
}

func ordOf(e *canon.Event) evOrd {
	name := e.Name
	if name == "" {
		name = "(unnamed)"
	}
	return evOrd{e.NS, e.CorrelationID, e.Seq, e.RowNS, name, e.MetadataJSON, e.EventKey}
}

// pEv is a prompt-row event the state holds.
type pEv struct {
	ord evOrd
	pe  process.PromptEvent
	f   process.PromptEventFacts
}

// pCall is a visible step, or a hidden call.
type pCall struct {
	raw string // its call id
	key tsID   // (time, request id): its place among the session's steps
	sc  process.StepCall
	ctx *int64
}

// pPrompt is a prompt's running values.
type pPrompt struct {
	pid        string
	inputs     *omap[evOrd, *pEv]  // its input prompts: the first is its input
	calls      *omap[tsID, *pCall] // its steps
	said       *omap[tsID, string] // steps' recorded prompt text: the first is what it said
	unfinished *omap[tsID, bool]   // steps in flight
	hidden     *omap[tsID, string] // hidden calls given to it, kind
	tokIn      int64
	tokInN     int
	tokOut     int64
	tokOutN    int
	turnEnd    int64
	hasTurnEnd bool
	cancels    int
	destr      int64
	tools      map[string]int64
	report     *turnRep
	images     int64
	pasted     int64
	pastedN    int
	start      tsID // its key in starts, when it exists
	typedAt    tsID // its key in typed, when it has an input
	exists     bool
}

type turnRep struct {
	at        tsID
	hasStatus bool
	dur       *float64
	js        string
}

// comp is a compaction (process compactions).
type comp struct {
	ord      evOrd
	attempt  *int64
	reqID    string
	from, to *int64
	idle     bool
}

type pReport struct {
	ts        int64
	key       string
	rid, crid string
}

// promptsState is the key's indexes for the prompt tables and marks.
type promptsState struct {
	calls map[string]*pCall   // steps by call id
	byKey map[string]*pCall   // steps by request id (the call id as rows name it)
	hid   map[string]*pCall   // hidden calls by call id
	steps *omap[tsID, *pCall] // every step, in order

	prompts map[string]*pPrompt
	starts  *omap[tsID, *pPrompt] // existing prompts by (start, id)
	typed   *omap[tsID, *pPrompt] // prompts with an input, by (its time, id)
	hidden  *omap[tsID, string]   // hidden calls, kind
	hidAt   map[string]string     // hidden call (request id) → the prompt it's given to
	pastes  *omap[tsID, *pEv]     // pastes by (time, key)

	success    map[string]*omap[evOrd, *pEv] // reports by request id and by client request id
	tools      map[string]*omap[evOrd, *pEv] // finished tools by request id
	ticks      map[string]*omap[evOrd, *pEv] // progress ticks by request id
	bash       map[string]*omap[evOrd, *pEv] // Bash events by tool use id: the last
	byAnyID    map[string]map[string]bool    // a step's request id or client request id → its call ids
	byReq      map[string]map[string]bool    // a step's request id → its call ids
	byClient   map[string]map[string]bool    // a step's client request id → its call ids
	useReqs    map[string]map[string]bool    // a tool use id → request ids whose tools or ticks name it
	results    map[string]*omap[tsID, []int64]
	byDetailID map[string]map[string]bool // a recorded tool call id → call ids of steps showing it

	succ      *omap[evOrd, *comp]
	attempts  *omap[tsID, bool]
	fired     *omap[tsID, bool]
	mains     *omap[tsID, int64]  // main steps' context
	compCalls *omap[tsID, string] // compaction steps
	attached  map[string]int      // a step's request id → compactions it's the call of

	markIdx   map[string]*omap[evOrd, int64] // a prompt's numbered input prompts (the marks' number)
	reports   map[string]*pReport
	repByRID  map[string]map[string]bool // a report's request id → its keys
	repByCRID map[string]map[string]bool
	reported  map[string]int // "r:"+request id, "c:"+client request id → reports
	stepIDs   map[string]int // "r:"+request id, "c:"+client request id → steps

	lastPromptEv int64
	hasPromptEv  bool
}

func newPromptsState() *promptsState {
	return &promptsState{calls: map[string]*pCall{}, byKey: map[string]*pCall{}, hid: map[string]*pCall{},
		steps: newOmap[tsID, *pCall](lessTsID), prompts: map[string]*pPrompt{},
		starts: newOmap[tsID, *pPrompt](lessTsID), typed: newOmap[tsID, *pPrompt](lessTsID),
		hidden: newOmap[tsID, string](lessTsID), hidAt: map[string]string{}, pastes: newOmap[tsID, *pEv](lessTsID),
		success: map[string]*omap[evOrd, *pEv]{}, tools: map[string]*omap[evOrd, *pEv]{}, ticks: map[string]*omap[evOrd, *pEv]{},
		bash: map[string]*omap[evOrd, *pEv]{}, byAnyID: map[string]map[string]bool{}, byReq: map[string]map[string]bool{},
		byClient: map[string]map[string]bool{}, useReqs: map[string]map[string]bool{},
		results: map[string]*omap[tsID, []int64]{}, byDetailID: map[string]map[string]bool{},
		succ: newOmap[evOrd, *comp](lessEvOrd), attempts: newOmap[tsID, bool](lessTsID), fired: newOmap[tsID, bool](lessTsID),
		mains: newOmap[tsID, int64](lessTsID), compCalls: newOmap[tsID, string](lessTsID), attached: map[string]int{},
		markIdx: map[string]*omap[evOrd, int64]{}, reports: map[string]*pReport{}, repByRID: map[string]map[string]bool{},
		repByCRID: map[string]map[string]bool{}, reported: map[string]int{}, stepIDs: map[string]int{}}
}

// StepsAndCompactions is what the marks read of the prompt list: the session's
// steps in order and its compactions (each a between row and a mark, from the
// same comp), so the two lists can't disagree.
func (ps *promptsState) StepsAndCompactions() (*omap[tsID, *pCall], *omap[evOrd, *comp]) {
	return ps.steps, ps.succ
}

func lessEv(a, b evOrd) bool { return lessEvOrd(a, b) }

func addSet(m map[string]map[string]bool, k, v string) {
	if k == "" {
		return
	}
	if m[k] == nil {
		m[k] = map[string]bool{}
	}
	m[k][v] = true
}

func delSet(m map[string]map[string]bool, k, v string) {
	if s := m[k]; s != nil {
		delete(s, v)
		if len(s) == 0 {
			delete(m, k)
		}
	}
}

func evIndex(m map[string]*omap[evOrd, *pEv], k string, e *pEv) {
	if k == "" {
		return
	}
	if m[k] == nil {
		m[k] = newOmap[evOrd, *pEv](lessEv)
	}
	m[k].Set(e.ord, e)
}

func (ps *promptsState) prompt(pid string) *pPrompt {
	p := ps.prompts[pid]
	if p == nil {
		p = &pPrompt{pid: pid, inputs: newOmap[evOrd, *pEv](lessEv), calls: newOmap[tsID, *pCall](lessTsID),
			said: newOmap[tsID, string](lessTsID), unfinished: newOmap[tsID, bool](lessTsID),
			hidden: newOmap[tsID, string](lessTsID), tools: map[string]int64{}}
		ps.prompts[pid] = p
	}
	return p
}

// startPending is a call in flight as the processor holds it (the observer's
// call start: its request headers).
func startPending(c *canon.Request) canon.Pending {
	h := map[string]string{}
	for k, v := range map[string]string{
		"x-claude-code-session-id": c.SessionID, "x-claude-code-prompt-id": c.PromptID,
		"x-claude-code-request-class": c.RequestClass, "x-client-request-id": c.ClientRequestID,
		"x-claude-code-agent-type": c.AgentType,
	} {
		if v != "" {
			h[k] = v
		}
	}
	cj, _ := json.Marshal(h)
	return canon.Pending{CorrelationID: c.CallID, SeenNS: c.StartNS, StartNS: c.StartNS, Host: c.Host,
		Method: c.Method, Path: c.Path, Synthetic: c.Synthetic, KeyHash: c.KeyHash, RequestID: c.RequestID,
		ClientJSON: string(cj), ResponseStartNS: c.ResponseStartNS}
}

// prompts folds a record into the prompt tables and marks.
func (s *KeyState) prompts(r records.Record, d *Delta) {
	sid := sessionOf(s.Key)
	if sid == "" {
		return
	}
	switch {
	case r.Kind == records.KindClientEvent && r.Event != nil:
		e := r.Event
		switch {
		case e.Source == canon.ProductClaudeCode && e.SessionID == sid:
			s.promptEvent(r, d, sid)
		case e.Source == canon.ProductCodex && e.Name == "codex_turn_event" && e.SessionID == sid && e.PromptID != "":
			s.codexReport(e, d, sid)
		}
	case r.Kind == records.KindCallEnd || r.Kind == records.KindWSConnection:
		c := r.Call
		if c.SessionID != sid {
			s.dropCall(d, sid, c.CallID)
			return
		}
		s.setCall(d, sid, c.CallID, process.StepCallOfRequest(*c), true)
	case r.Kind == records.KindCallStart:
		c := r.Call
		if s.ses.ended[c.CallID] || strings.Contains(c.CallID, "#") || c.SessionID != sid {
			return
		}
		sc := process.StepCallOfPending(startPending(c))
		if !sc.Visible() {
			return
		}
		d.Cases = append(d.Cases, CaseStepPending)
		s.setCall(d, sid, c.CallID, sc, false)
	}
	s.writeSession(d, sid)
}

func (s *KeyState) writeSession(d *Delta, sid string) {
	ps := s.pr
	row := PromptSession{SessionID: sid, HasPromptEvent: ps.hasPromptEv, LastPromptEventNS: ps.lastPromptEv}
	if _, c, ok := ps.steps.First(); ok {
		row.Product = c.sc.Product()
	}
	if sp := s.ses.spanNow(); sp.ok {
		row.LastNS = sp.last
	}
	s.rows.put(d, TablePromptSession, "ps", row)
}

// --- events ----------------------------------------------------------------------------

func (s *KeyState) promptEvent(r records.Record, d *Delta, sid string) {
	ps := s.pr
	e := r.Event
	if process.IsPromptEvent(e.Name) {
		x := &pEv{ord: ordOf(e), pe: process.PromptEventOf(*e, r.BatchCCN)}
		x.f = x.pe.Facts()
		if !ps.hasPromptEv || e.NS > ps.lastPromptEv {
			ps.lastPromptEv, ps.hasPromptEv = e.NS, true
		}
		s.promptRowEvent(d, sid, x)
	}
	if process.IsMarkEvent(e.Name) {
		s.markEvent(d, sid, e)
	}
	if e.Name == "tengu_api_success" {
		s.reportEvent(d, sid, e)
	}
}

func (s *KeyState) promptRowEvent(d *Delta, sid string, x *pEv) {
	ps := s.pr
	f := x.f
	switch {
	case f.Name == "tengu_input_prompt":
		pid := f.Prompt
		if pid == "" {
			pid = "input:" + f.UnnamedID
			d.Cases = append(d.Cases, CasePromptInputUnnamed)
		} else {
			d.Cases = append(d.Cases, CasePromptInput)
		}
		p := ps.prompt(pid)
		p.inputs.Set(x.ord, x)
		s.placePrompt(d, sid, p)
	case f.Paste:
		d.Cases = append(d.Cases, CasePromptPaste)
		ps.pastes.Set(tsID{x.ord.ts, x.ord.key}, x)
		s.refreshPastesFrom(d, sid, x.ord.ts)
	case f.Name == "tengu_return_to_session":
		if f.HadRecap {
			d.Cases = append(d.Cases, CaseBetweenRecap)
			s.rows.put(d, TablePromptItems, "r:"+x.ord.key, PromptItem{Row: process.PromptRow{SessionID: sid, TNS: x.ord.ts,
				Type: "between", BetweenKind: ptrS("recap"), Detail: ptrS("recap shown on return")}, Group: 2,
				OrdCorr: x.ord.corr, OrdSeq: x.ord.seq, OrdRowNS: x.ord.rowNS, OrdName: x.ord.name, OrdMD: x.ord.md, OrdKey: x.ord.key})
		}
	case f.Name == "tengu_reactive_compact_succeeded":
		d.Cases = append(d.Cases, CaseCompaction)
		ps.succ.Set(x.ord, &comp{ord: x.ord})
		s.recompact(d, sid, x.ord)
		ps.succ.Ascend(x.ord, func(k evOrd, _ *comp) bool {
			if k != x.ord {
				s.recompact(d, sid, k)
				return false
			}
			return true
		})
	case f.Name == "tengu_reactive_compact_attempt":
		d.Cases = append(d.Cases, CaseCompactionAttempt)
		ps.attempts.Set(tsID{x.ord.ts, x.ord.key}, true)
		if k, _, ok := ps.firstSuccAt(x.ord.ts); ok {
			s.recompact(d, sid, k)
		}
	case f.Name == "tengu_idle_compact_fired":
		d.Cases = append(d.Cases, CaseCompactionFired)
		ps.fired.Set(tsID{x.ord.ts, x.ord.key}, true)
		s.recompactFrom(d, sid, x.ord.ts, x.ord.ts+process.IdleLeadNS, true)
	}
	if f.Prompt != "" {
		var p *pPrompt
		switch {
		case f.Name == "tengu_turn_end":
			d.Cases = append(d.Cases, CasePromptTurnEnd)
			p = ps.prompt(f.Prompt)
			if !p.hasTurnEnd || x.ord.ts > p.turnEnd {
				p.turnEnd, p.hasTurnEnd = x.ord.ts, true
			}
		case f.Name == "tengu_cancel":
			d.Cases = append(d.Cases, CasePromptCancel)
			p = ps.prompt(f.Prompt)
			p.cancels++
		case f.IsBash && f.Destructive:
			d.Cases = append(d.Cases, CasePromptDestructive)
			p = ps.prompt(f.Prompt)
			p.destr++
		case f.IsTool:
			d.Cases = append(d.Cases, CasePromptTool)
			p = ps.prompt(f.Prompt)
			name := "—"
			if f.ToolName != nil && *f.ToolName != "" {
				name = *f.ToolName
			}
			p.tools[name]++
		}
		if p != nil {
			s.writePrompt(d, sid, p)
		}
	}
	// What the steps join.
	switch {
	case f.IsSuccess:
		touched := map[string]bool{}
		for _, k := range []string{f.ReqID, f.ClientReqID} {
			if k == "" {
				continue
			}
			evIndex(ps.success, k, x)
			// A step reads only its id's first report: a later one changes no step,
			// so a report costs the same however many steps share its id.
			if firstEv(ps.success[k]) != x {
				continue
			}
			for id := range ps.byAnyID[k] {
				touched[id] = true
			}
		}
		if len(touched) > 0 {
			d.Cases = append(d.Cases, CaseStepSuccess)
		}
		s.restep(d, sid, touched)
	case (f.IsTool || f.IsTick) && f.ReqID != "":
		if f.IsTool {
			evIndex(ps.tools, f.ReqID, x)
			d.Cases = append(d.Cases, CaseStepTool)
		} else {
			evIndex(ps.ticks, f.ReqID, x)
			d.Cases = append(d.Cases, CaseStepTick)
		}
		if tid := x.pe.ToolUseID(); tid != "" {
			addSet(ps.useReqs, tid, f.ReqID)
		}
		s.writeStepTools(d, sid, f.ReqID)
	case f.IsBash && f.BashToolUseID != nil:
		d.Cases = append(d.Cases, CaseStepBash)
		evIndex(ps.bash, *f.BashToolUseID, x)
		for rid := range ps.useReqs[*f.BashToolUseID] {
			s.writeStepTools(d, sid, rid)
		}
	}
}

func (s *KeyState) codexReport(e *canon.Event, d *Delta, sid string) {
	ps := s.pr
	p := ps.prompt(e.PromptID)
	at := tsID{e.NS, e.EventKey}
	if p.report != nil && !lessTsID(p.report.at, at) {
		return
	}
	d.Cases = append(d.Cases, CasePromptCodexReport)
	has, dur, js := process.TurnReport(e.MetadataJSON)
	p.report = &turnRep{at, has, dur, js}
	s.writePrompt(d, sid, p)
}

// --- calls -----------------------------------------------------------------------------

// setCall puts a call's newest version in: a step when visible, a hidden call
// when it's a coding agent's hidden model call.
func (s *KeyState) setCall(d *Delta, sid, raw string, sc process.StepCall, ended bool) {
	ps := s.pr
	wasStep := ps.calls[raw] != nil
	s.removeCall(d, sid, raw)
	switch {
	case sc.Visible() && sc.Session() == sid:
		c := &pCall{raw: raw, key: tsID{sc.TS(), sc.ID()}, sc: sc, ctx: sc.Context()}
		s.addStep(d, sid, c)
	case sc.Agent() && sc.Hidden() != "" && sc.Session() == sid:
		if wasStep {
			d.Cases = append(d.Cases, CaseStepGone)
		}
		c := &pCall{raw: raw, key: tsID{sc.TS(), sc.ID()}, sc: sc}
		ps.hid[raw] = c
		ps.hidden.Set(c.key, sc.Hidden())
		d.Cases = append(d.Cases, CasePromptHidden)
		s.hideOne(d, sid, c.key, sc.Hidden())
	default:
		if wasStep {
			d.Cases = append(d.Cases, CaseStepGone)
		}
	}
}

func (s *KeyState) dropCall(d *Delta, sid, raw string) {
	if s.pr.calls[raw] != nil {
		d.Cases = append(d.Cases, CaseStepGone)
	}
	s.removeCall(d, sid, raw)
}

// removeCall takes a call's previous version out of every index and row.
func (s *KeyState) removeCall(d *Delta, sid, raw string) {
	ps := s.pr
	if h := ps.hid[raw]; h != nil {
		delete(ps.hid, raw)
		ps.hidden.Delete(h.key)
		if pid := ps.hidAt[h.key.id]; pid != "" {
			delete(ps.hidAt, h.key.id)
			p := ps.prompt(pid)
			p.hidden.Delete(h.key)
			s.writePrompt(d, sid, p)
		}
	}
	c := ps.calls[raw]
	if c == nil {
		return
	}
	delete(ps.calls, raw)
	delete(ps.byKey, c.key.id)
	ps.steps.Delete(c.key)
	id, sc := c.key.id, c.sc
	if pid := sc.Prompt(); pid != "" {
		p := ps.prompt(pid)
		p.calls.Delete(c.key)
		p.said.Delete(c.key)
		p.unfinished.Delete(c.key)
		if c.ctx != nil {
			p.tokIn -= *c.ctx
			p.tokInN--
		}
		if sc.Output() != 0 {
			p.tokOut -= sc.Output()
			p.tokOutN--
		}
		s.rows.drop(d, TablePromptSteps, "s:"+id)
		s.placePrompt(d, sid, p)
	} else {
		s.rows.drop(d, TablePromptItems, "b:"+id)
	}
	delSet(ps.byAnyID, sc.ReqID(), raw)
	delSet(ps.byAnyID, sc.ClientReqID(), raw)
	delSet(ps.byReq, sc.ReqID(), raw)
	delSet(ps.byClient, sc.ClientReqID(), raw)
	for _, x := range sc.DetailCallIDs() {
		delSet(ps.byDetailID, x, raw)
	}
	touched := map[string]bool{}
	for _, r := range sc.DetailResults() {
		if m := ps.results[r.ID]; m != nil {
			m.Delete(c.key)
			for o := range ps.byDetailID[r.ID] {
				touched[o] = true
			}
		}
	}
	s.restep(d, sid, touched)
	if sc.Class() == "main" && c.ctx != nil {
		ps.mains.Delete(c.key)
		s.recontext(d, sid, c.key.ts)
	}
	if sc.Class() == "compaction" {
		ps.compCalls.Delete(c.key)
		if k, _, ok := ps.firstSuccAt(c.key.ts); ok {
			s.recompact(d, sid, k)
		}
	}
	s.rows.drop(d, TableMarks, "c:"+id)
	s.stepIDs1(d, sid, sc, -1)
}

func (s *KeyState) addStep(d *Delta, sid string, c *pCall) {
	ps := s.pr
	sc := c.sc
	ps.calls[c.raw] = c
	ps.byKey[c.key.id] = c
	ps.steps.Set(c.key, c)
	addSet(ps.byAnyID, sc.ReqID(), c.raw)
	addSet(ps.byAnyID, sc.ClientReqID(), c.raw)
	addSet(ps.byReq, sc.ReqID(), c.raw)
	addSet(ps.byClient, sc.ClientReqID(), c.raw)
	for _, x := range sc.DetailCallIDs() {
		addSet(ps.byDetailID, x, c.raw)
	}
	touched := map[string]bool{}
	for _, r := range sc.DetailResults() {
		m := ps.results[r.ID]
		if m == nil {
			m = newOmap[tsID, []int64](lessTsID)
			ps.results[r.ID] = m
		}
		m.Set(c.key, r.Codes)
		for o := range ps.byDetailID[r.ID] {
			if o != c.raw {
				touched[o] = true
			}
		}
	}
	if len(touched) > 0 {
		d.Cases = append(d.Cases, CaseStepResults)
	}
	if pid := sc.Prompt(); pid != "" {
		if sc.End() != 0 {
			d.Cases = append(d.Cases, CaseStepCall)
		}
		p := ps.prompt(pid)
		p.calls.Set(c.key, c)
		if t := sc.DetailPrompt(); t != "" {
			p.said.Set(c.key, t)
		}
		if sc.End() == 0 {
			p.unfinished.Set(c.key, true)
		}
		if c.ctx != nil {
			p.tokIn += *c.ctx
			p.tokInN++
		}
		if sc.Output() != 0 {
			p.tokOut += sc.Output()
			p.tokOutN++
		}
		s.writeStep(d, sid, c)
		s.placePrompt(d, sid, p)
	} else {
		d.Cases = append(d.Cases, CaseBetweenCall)
		s.writeBetweenCall(d, sid, c)
	}
	s.restep(d, sid, touched)
	if sc.Class() == "main" && c.ctx != nil {
		ps.mains.Set(c.key, *c.ctx)
		d.Cases = append(d.Cases, CaseCompactionContext)
		s.recontext(d, sid, c.key.ts)
	}
	if sc.Class() == "compaction" {
		ps.compCalls.Set(c.key, c.key.id)
		if k, _, ok := ps.firstSuccAt(c.key.ts); ok {
			d.Cases = append(d.Cases, CaseCompactionCall)
			s.recompact(d, sid, k)
		}
	}
	d.Cases = append(d.Cases, CaseMarkCall)
	s.writeCallMark(d, sid, c)
	s.stepIDs1(d, sid, sc, 1)
}

// --- steps -----------------------------------------------------------------------------

func (s *KeyState) restep(d *Delta, sid string, ids map[string]bool) {
	for raw := range ids {
		if c := s.pr.calls[raw]; c != nil && c.sc.Prompt() != "" {
			s.writeStep(d, sid, c)
		}
	}
}

func firstEv(m *omap[evOrd, *pEv]) *pEv {
	if m == nil {
		return nil
	}
	_, v, ok := m.First()
	if !ok {
		return nil
	}
	return v
}

func evList(m *omap[evOrd, *pEv]) []process.PromptEvent {
	if m == nil {
		return nil
	}
	out := make([]process.PromptEvent, 0, m.Len())
	m.All(func(_ evOrd, v *pEv) bool { out = append(out, v.pe); return true })
	return out
}

func (s *KeyState) writeStep(d *Delta, sid string, c *pCall) {
	ps := s.pr
	sc := c.sc
	j := process.StepJoins{
		Bash: func(tid string) *process.PromptEvent {
			m := ps.bash[tid]
			if m == nil {
				return nil
			}
			_, v, ok := m.Last()
			if !ok {
				return nil
			}
			return &v.pe
		},
		Results: func(id string) ([]int64, bool) {
			m := ps.results[id]
			if m == nil {
				return nil, false
			}
			_, v, ok := m.First()
			return v, ok
		},
	}
	byReq, byClient := firstEv(ps.success[sc.ReqID()]), firstEv(ps.success[sc.ClientReqID()])
	if sc.ReqID() == "" {
		byReq = nil
	}
	if sc.ClientReqID() == "" {
		byClient = nil
	}
	if byReq != nil {
		j.SuccessByReq = &byReq.pe
	}
	if byClient != nil {
		j.SuccessByClient = &byClient.pe
	}
	// Its tools are its request id's (prompt_step_tools), joined by the query.
	st := process.BuildStep(sc, sid, j)
	end := sc.TS()
	if sc.End() != 0 {
		end = sc.End()
	}
	st.EventsPendingIfLive = false
	s.rows.put(d, TablePromptSteps, "s:"+c.key.id, StepRow{Step: st, PromptID: sc.Prompt(),
		HasSuccess: byReq != nil || byClient != nil, PendEnd: end, ToolsRID: sc.ReqID(), DetailCalls: sc.HasDetailCalls()})
}

// writeStepTools writes a request id's tools: one row, whatever number of steps
// show them.
func (s *KeyState) writeStepTools(d *Delta, sid, rid string) {
	ps := s.pr
	bash := func(tid string) *process.PromptEvent {
		m := ps.bash[tid]
		if m == nil {
			return nil
		}
		_, v, ok := m.Last()
		if !ok {
			return nil
		}
		return &v.pe
	}
	t := process.BuildStepTools(rid, evList(ps.tools[rid]), evList(ps.ticks[rid]), bash)
	s.rows.put(d, TablePromptStepTools, "t:"+rid, StepToolsRow{SessionID: sid, RequestID: rid, Tools: t})
}

func (s *KeyState) writeBetweenCall(d *Delta, sid string, c *pCall) {
	sc := c.sc
	if s.pr.attached[c.key.id] > 0 {
		s.rows.drop(d, TablePromptItems, "b:"+c.key.id)
		return
	}
	s.rows.put(d, TablePromptItems, "b:"+c.key.id, PromptItem{Row: process.PromptRow{SessionID: sid, TNS: c.key.ts,
		Type: "between", BetweenKind: ptrS("call"), Detail: ptrS(process.CallLabel(sc.Class(), sc.Model(), sc.Stop())),
		RequestID: ptrS(c.key.id)}, Group: 3, OrdKey: c.key.id})
}

// --- prompts ---------------------------------------------------------------------------

// placePrompt puts a prompt where its start says, gives the hidden calls and
// pastes around a moved start to the right prompts again, and writes it.
func (s *KeyState) placePrompt(d *Delta, sid string, p *pPrompt) {
	ps := s.pr
	oldStart, wasExisting := p.start, p.exists
	oldTyped, wasTyped := p.typedAt, p.exists && p.inputs.Len() > 0
	p.exists = p.inputs.Len() > 0 || p.calls.Len() > 0
	if p.exists {
		if _, in, ok := p.inputs.First(); ok {
			p.start = tsID{in.ord.ts, p.pid}
		} else {
			k, _, _ := p.calls.First()
			p.start = tsID{k.ts, p.pid}
		}
	}
	if wasExisting && (!p.exists || p.start != oldStart) {
		ps.starts.Delete(oldStart)
	}
	if p.exists {
		ps.starts.Set(p.start, p)
	}
	isTyped := p.exists && p.inputs.Len() > 0
	if isTyped {
		_, in, _ := p.inputs.First()
		p.typedAt = tsID{in.ord.ts, p.pid}
	}
	if wasTyped && (!isTyped || p.typedAt != oldTyped) {
		ps.typed.Delete(oldTyped)
	}
	if isTyped {
		ps.typed.Set(p.typedAt, p)
	}
	if wasExisting != p.exists || oldStart != p.start {
		lo, hi := p.start.ts, p.start.ts
		if wasExisting {
			lo, hi = min(lo, oldStart.ts), max(hi, oldStart.ts)
		}
		s.rehide(d, sid, lo, hi)
	}
	if wasTyped != isTyped || oldTyped != p.typedAt {
		if isTyped {
			s.repaste(d, sid, p)
			s.refreshPastesAfter(d, sid, p.typedAt.ts)
		}
		if wasTyped {
			s.refreshPastesAfter(d, sid, oldTyped.ts)
			s.refreshPastesFrom(d, sid, oldTyped.ts)
		}
	}
	s.writePrompt(d, sid, p)
}

// rehide gives each hidden call whose nearest prompts may have changed (one
// between the prompts around [lo, hi]) to its prompt again.
func (s *KeyState) rehide(d *Delta, sid string, lo, hi int64) {
	ps := s.pr
	from := int64(-1 << 63)
	if k, _, ok := ps.starts.Lower(tsID{lo, ""}); ok {
		from = k.ts
	}
	to, bounded := int64(0), false
	ps.starts.Ascend(tsID{hi + 1, ""}, func(k tsID, _ *pPrompt) bool { to, bounded = k.ts, true; return false })
	var keys []tsID
	ps.hidden.Ascend(tsID{from, ""}, func(k tsID, _ string) bool {
		if bounded && k.ts > to {
			return false
		}
		keys = append(keys, k)
		return true
	})
	for _, k := range keys {
		_, kind, _ := ps.hidden.Floor(k)
		s.hideOne(d, sid, k, kind)
	}
}

// hideOne gives one hidden call to its prompt (session.py _hidden_by_prompt).
func (s *KeyState) hideOne(d *Delta, sid string, k tsID, kind string) {
	ps := s.pr
	var after, before *pPrompt
	ps.starts.Ascend(tsID{k.ts, ""}, func(_ tsID, p *pPrompt) bool { after = p; return false })
	if _, p, ok := ps.starts.Lower(tsID{k.ts, ""}); ok {
		before = p
	}
	to := before
	if to == nil {
		to = after
	}
	if kind == "prewarm" {
		to = after
		if to == nil {
			to = before
		}
	}
	pid := ""
	if to != nil {
		pid = to.pid
	}
	old := ps.hidAt[k.id]
	if old == pid {
		return
	}
	if old != "" {
		op := ps.prompt(old)
		op.hidden.Delete(k)
		s.writePrompt(d, sid, op)
	}
	if pid == "" {
		delete(ps.hidAt, k.id)
		return
	}
	ps.hidAt[k.id] = pid
	to.hidden.Set(k, kind)
	if old != "" {
		d.Cases = append(d.Cases, CasePromptHidden)
	}
	s.writePrompt(d, sid, to)
}

// repaste counts the pastes of a typed prompt: those after the typed prompt
// before it, up to its own time.
func (s *KeyState) repaste(d *Delta, sid string, p *pPrompt) {
	ps := s.pr
	t := p.typedAt.ts
	prev := int64(-1)
	if k, _, ok := ps.typed.Lower(tsID{t, ""}); ok {
		prev = k.ts
	}
	p.images, p.pasted, p.pastedN = 0, 0, 0
	ps.pastes.Ascend(tsID{prev + 1, ""}, func(k tsID, x *pEv) bool {
		if k.ts > t {
			return false
		}
		if x.f.Name == "tengu_paste_text" {
			if x.f.PastedBytes != nil {
				p.pasted += *x.f.PastedBytes
				p.pastedN++
			}
		} else {
			p.images++
		}
		return true
	})
}

// refreshPastesFrom counts again the pastes of the typed prompts at the first
// typed time at or after ts (whose window holds ts).
func (s *KeyState) refreshPastesFrom(d *Delta, sid string, ts int64) {
	ps := s.pr
	first, found := int64(0), false
	ps.typed.Ascend(tsID{ts, ""}, func(k tsID, p *pPrompt) bool {
		if found && k.ts != first {
			return false
		}
		first, found = k.ts, true
		s.repaste(d, sid, p)
		s.writePrompt(d, sid, p)
		return true
	})
}

// refreshPastesAfter is refreshPastesFrom for the first typed time after ts.
func (s *KeyState) refreshPastesAfter(d *Delta, sid string, ts int64) {
	s.refreshPastesFrom(d, sid, ts+1)
}

func (s *KeyState) writePrompt(d *Delta, sid string, p *pPrompt) {
	if !p.exists {
		s.rows.drop(d, TablePromptItems, "p:"+p.pid)
		return
	}
	pid := p.pid
	row := process.PromptRow{SessionID: sid, TNS: p.start.ts, Type: "prompt", PromptID: ptrS(pid), Steps: int64(p.calls.Len())}
	if k, _, ok := p.unfinished.Last(); ok {
		row.UnfinishedMaxNS = ptrI(k.ts)
	}
	if p.hasTurnEnd {
		v := process.Round1(float64(p.turnEnd-p.start.ts) / 1e6)
		row.DurationTurnMS = &v
	}
	if p.report != nil {
		row.DurationReportMS = p.report.dur
		row.TurnReportJSON = ptrS(p.report.js)
	}
	_, in, hasInput := p.inputs.First()
	if hasInput {
		if p.pastedN > 0 {
			row.PastedBytes = ptrI(p.pasted)
		}
		row.Images = ptrI(p.images)
		row.PromptIndex, row.TextChars, row.Frustrated = in.f.PromptIndex, in.f.Length, in.f.Negative
	}
	if _, said, ok := p.said.First(); ok {
		row.TextRetained, row.Text = true, ptrS(said)
		if !hasInput {
			row.TextChars = ptrI(int64(utf8.RuneCountInString(said)))
		}
	}
	if p.tokInN > 0 {
		row.TokensIn = ptrI(p.tokIn)
	}
	if p.tokOutN > 0 {
		row.TokensOut = ptrI(p.tokOut)
	}
	keys := make([]string, 0, len(p.tools))
	for k, n := range p.tools {
		if n > 0 {
			keys = append(keys, k)
		}
	}
	row.ToolCountsJSON = process.CountsJSON(keys, p.tools, true)
	row.Interrupted, row.Destructive = p.cancels > 0, p.destr
	var kinds []string
	counts := map[string]int64{}
	p.hidden.All(func(_ tsID, kind string) bool {
		if counts[kind] == 0 {
			kinds = append(kinds, kind)
		}
		counts[kind]++
		return true
	})
	row.HiddenJSON = process.CountsJSON(kinds, counts, false)
	s.rows.put(d, TablePromptItems, "p:"+pid, PromptItem{Row: row, Group: 0, OrdKey: pid, HasInput: hasInput,
		HasTurnEnd: p.hasTurnEnd, ReportStatus: p.report != nil && p.report.hasStatus})
}

// --- compactions -----------------------------------------------------------------------

// firstSuccAt is the first compaction at or after ts: the one whose window
// (after the previous one, up to its own time) holds ts.
func (ps *promptsState) firstSuccAt(ts int64) (evOrd, *comp, bool) {
	var k evOrd
	var c *comp
	ok := false
	ps.succ.Ascend(evOrd{ts: ts}, func(kk evOrd, cc *comp) bool { k, c, ok = kk, cc, true; return false })
	return k, c, ok
}

// recompactFrom works out again the compactions from the first at or after lo
// whose window may start by hi.
func (s *KeyState) recompactFrom(d *Delta, sid string, lo, hi int64, bounded bool) {
	ps := s.pr
	var keys []evOrd
	prev := int64(-1)
	if k, _, ok := ps.succ.Lower(evOrd{ts: lo}); ok {
		prev = k.ts
	}
	ps.succ.Ascend(evOrd{ts: lo}, func(k evOrd, _ *comp) bool {
		if bounded && prev > hi {
			return false
		}
		keys = append(keys, k)
		prev = k.ts
		return true
	})
	for _, k := range keys {
		s.recompact(d, sid, k)
	}
}

// recontext works out again the compactions a main call's context at t may be
// the before or after of: those from the main call before t to the one after.
func (s *KeyState) recontext(d *Delta, sid string, t int64) {
	ps := s.pr
	lo := int64(-1 << 63)
	if k, _, ok := ps.mains.Lower(tsID{t, ""}); ok {
		lo = k.ts
	}
	hi, bounded := int64(0), false
	ps.mains.Ascend(tsID{t + 1, ""}, func(k tsID, _ int64) bool { hi, bounded = k.ts, true; return false })
	s.recompactFrom(d, sid, lo, hi, bounded)
}

// recompact works out one compaction (process compactions) and writes its
// between row and mark.
func (s *KeyState) recompact(d *Delta, sid string, k evOrd) {
	ps := s.pr
	_, c, ok := ps.succ.Floor(k)
	if !ok || c.ord != k {
		return
	}
	prev := int64(-1)
	if pk, _, ok := ps.succ.Lower(k); ok {
		prev = pk.ts
	}
	c.attempt = nil
	if ak, _, ok := ps.attempts.Lower(tsID{k.ts + 1, ""}); ok && ak.ts > prev {
		c.attempt = ptrI(ak.ts)
	}
	start := k.ts
	if c.attempt != nil {
		start = *c.attempt
	}
	old := c.reqID
	c.reqID = ""
	ps.compCalls.Ascend(tsID{start, ""}, func(ck tsID, id string) bool {
		if ck.ts <= k.ts {
			c.reqID = id
		}
		return false
	})
	if old != c.reqID {
		if old != "" {
			ps.attached[old]--
			if ps.attached[old] == 0 {
				delete(ps.attached, old)
			}
			s.rebetween(d, sid, old)
		}
		if c.reqID != "" {
			ps.attached[c.reqID]++
			s.rebetween(d, sid, c.reqID)
		}
	}
	c.from, c.to = nil, nil
	if _, v, ok := ps.mains.Lower(tsID{start, ""}); ok {
		c.from = ptrI(v)
	}
	ps.mains.Ascend(tsID{k.ts + 1, ""}, func(_ tsID, v int64) bool { c.to = ptrI(v); return false })
	c.idle = false
	ps.fired.Ascend(tsID{start - process.IdleLeadNS, ""}, func(fk tsID, _ bool) bool {
		c.idle = fk.ts <= start
		return false
	})
	var wait *float64
	if c.attempt != nil {
		w := process.Round1(float64(k.ts-*c.attempt) / 1e6)
		wait = &w
	}
	kind, label := process.CompactionLabel(c.idle, c.from, c.to)
	var rid *string
	if c.reqID != "" {
		rid = ptrS(c.reqID)
	}
	s.rows.put(d, TablePromptItems, "c:"+k.key, PromptItem{Row: process.PromptRow{SessionID: sid, TNS: k.ts,
		Type: "between", BetweenKind: ptrS(kind), Detail: ptrS(label), RequestID: rid, TokensFrom: c.from, TokensTo: c.to,
		WaitMS: wait}, Group: 1, OrdCorr: k.corr, OrdSeq: k.seq, OrdRowNS: k.rowNS, OrdName: k.name, OrdMD: k.md, OrdKey: k.key})
	s.rows.put(d, TableMarks, "cm:"+k.key, MarkRow{Mark: process.SessionMark{SessionID: sid, Kind: "compaction",
		TNS: k.ts, TokensFrom: c.from, TokensTo: c.to, Idle: c.idle, WaitMS: wait, RequestID: rid}, Group: 3,
		OrdCorr: k.corr, OrdSeq: k.seq, OrdRowNS: k.rowNS, OrdName: k.name, OrdMD: k.md, OrdKey: k.key})
}

// rebetween writes a call's between row again (it shows only while no
// compaction is attached to it).
func (s *KeyState) rebetween(d *Delta, sid, id string) {
	if c := s.pr.byKey[id]; c != nil && c.sc.Prompt() == "" {
		s.writeBetweenCall(d, sid, c)
	}
}

// --- marks -----------------------------------------------------------------------------

func (s *KeyState) writeCallMark(d *Delta, sid string, c *pCall) {
	ps := s.pr
	sc := c.sc
	m := process.SessionMark{SessionID: sid, Kind: "call", TNS: c.key.ts, RequestID: ptrS(c.key.id), RequestClass: nzS(sc.Class()),
		Model: nzS(sc.Model()), Context: c.ctx, Output: nzI(sc.Output()), Input: nzI(sc.Input()), CacheRead: nzI(sc.CacheRead()),
		CacheWrite: nzI(sc.CacheWrite()), PromptID: nzS(sc.Prompt()),
		Unreported: !(sc.ReqID() != "" && ps.reported["r:"+sc.ReqID()] > 0) && !(sc.ClientReqID() != "" && ps.reported["c:"+sc.ClientReqID()] > 0)}
	s.rows.put(d, TableMarks, "c:"+c.key.id, MarkRow{Mark: m, Group: 0, OrdKey: c.key.id, PromptRef: sc.Prompt(), PointIP: sc.IP()})
}

// stepIDs1 counts a step's ids in or out, and writes the missed marks of the
// reports they decide.
func (s *KeyState) stepIDs1(d *Delta, sid string, sc process.StepCall, n int) {
	ps := s.pr
	keys := map[string]bool{}
	// A report's missed mark turns only when its id's step count goes to or from 0.
	if r := sc.ReqID(); r != "" {
		was := ps.stepIDs["r:"+r]
		ps.stepIDs["r:"+r] += n
		if (was == 0) != (ps.stepIDs["r:"+r] == 0) {
			for k := range ps.repByRID[r] {
				keys[k] = true
			}
		}
	}
	if c := sc.ClientReqID(); c != "" {
		was := ps.stepIDs["c:"+c]
		ps.stepIDs["c:"+c] += n
		if (was == 0) != (ps.stepIDs["c:"+c] == 0) {
			for k := range ps.repByCRID[c] {
				keys[k] = true
			}
		}
	}
	for k := range keys {
		if s.writeMissed(d, sid, ps.reports[k]) {
			d.Cases = append(d.Cases, CaseMarkMissed)
		}
	}
}

// writeMissed writes a report's missed mark, or takes it back; it says whether
// that changed.
func (s *KeyState) writeMissed(d *Delta, sid string, r *pReport) bool {
	ps := s.pr
	missed := !(r.rid != "" && ps.stepIDs["r:"+r.rid] > 0) && !(r.crid != "" && ps.stepIDs["c:"+r.crid] > 0)
	_, had := s.rows[TableMarks]["mis:"+r.key]
	if missed {
		s.rows.put(d, TableMarks, "mis:"+r.key, MarkRow{Mark: process.SessionMark{SessionID: sid, Kind: "missed", TNS: r.ts},
			Group: 1, Sub: 1, OrdKey: r.key})
	} else {
		s.rows.drop(d, TableMarks, "mis:"+r.key)
	}
	return had != missed
}

func (s *KeyState) reportEvent(d *Delta, sid string, e *canon.Event) {
	ps := s.pr
	rid, crid := process.ReportIDsOf(*e)
	r := &pReport{ts: e.NS, key: e.EventKey, rid: rid, crid: crid}
	ps.reports[r.key] = r
	d.Cases = append(d.Cases, CaseMarkReported)
	s.rows.put(d, TableMarks, "rep:"+r.key, MarkRow{Mark: process.SessionMark{SessionID: sid, Kind: "reported", TNS: r.ts},
		Group: 1, Sub: 0, OrdKey: r.key})
	s.writeMissed(d, sid, r)
	touched := map[string]bool{}
	// A call mark's unreported flag turns only on its id's first report.
	if rid != "" {
		addSet(ps.repByRID, rid, r.key)
		if ps.reported["r:"+rid]++; ps.reported["r:"+rid] == 1 {
			for id := range ps.byReq[rid] {
				touched[id] = true
			}
		}
	}
	if crid != "" {
		addSet(ps.repByCRID, crid, r.key)
		if ps.reported["c:"+crid]++; ps.reported["c:"+crid] == 1 {
			for id := range ps.byClient[crid] {
				touched[id] = true
			}
		}
	}
	for raw := range touched {
		c := ps.calls[raw]
		before := s.rows[TableMarks]["c:"+c.key.id].(MarkRow).Mark.Unreported
		s.writeCallMark(d, sid, c)
		if before != s.rows[TableMarks]["c:"+c.key.id].(MarkRow).Mark.Unreported {
			d.Cases = append(d.Cases, CaseMarkUnreported)
		}
	}
}

func (s *KeyState) markEvent(d *Delta, sid string, e *canon.Event) {
	ps := s.pr
	mf := process.MarkEventOf(*e)
	o := ordOf(e)
	base := MarkRow{Group: 2, OrdCorr: o.corr, OrdSeq: o.seq, OrdRowNS: o.rowNS, OrdName: o.name, OrdMD: o.md, OrdKey: o.key}
	switch e.Name {
	case "tengu_input_prompt":
		if mf.PromptID != "" && mf.Index != nil {
			m := ps.markIdx[mf.PromptID]
			if m == nil {
				m = newOmap[evOrd, int64](lessEv)
				ps.markIdx[mf.PromptID] = m
			}
			m.Set(o, *mf.Index)
			if lk, v, _ := m.Last(); lk == o {
				d.Cases = append(d.Cases, CaseMarkPromptIndex)
				s.rows.put(d, TableMarkPromptIndex, "mi:"+mf.PromptID, MarkPromptIndex{SessionID: sid, PromptID: mf.PromptID, Index: v})
			}
		}
		if mf.Negative {
			d.Cases = append(d.Cases, CaseMarkFriction)
			base.Mark = process.SessionMark{SessionID: sid, Kind: "friction", TNS: e.NS, FrictionKind: ptrS("frustrated"), PromptIndex: mf.Index}
			s.rows.put(d, TableMarks, "f:"+o.key, base)
		}
	case "tengu_cancel":
		d.Cases = append(d.Cases, CaseMarkFriction)
		base.Mark = process.SessionMark{SessionID: sid, Kind: "friction", TNS: e.NS, FrictionKind: ptrS("interrupted")}
		base.PromptRef = mf.PromptID
		s.rows.put(d, TableMarks, "f:"+o.key, base)
	case "tengu_bash_tool_command_executed":
		if mf.Destructive != "" {
			d.Cases = append(d.Cases, CaseMarkFriction)
			var parts []string
			for _, p := range []string{mf.Destructive, mf.Scope} {
				if p != "" {
					parts = append(parts, p)
				}
			}
			base.Mark = process.SessionMark{SessionID: sid, Kind: "friction", TNS: e.NS, FrictionKind: ptrS("destructive"),
				Detail: ptrS(strings.Join(parts, " · "))}
			base.PromptRef = mf.PromptID
			s.rows.put(d, TableMarks, "f:"+o.key, base)
		}
	case "tengu_api_query":
		if mf.Mode != nil && *mf.Mode != "" {
			d.Cases = append(d.Cases, CaseMarkPermission)
			base.Group = 4
			base.Mark = process.SessionMark{SessionID: sid, Kind: "permission", TNS: e.NS, Mode: mf.Mode}
			s.rows.put(d, TableMarks, "pm:"+o.key, base)
		}
	}
}

// --- helpers ---------------------------------------------------------------------------

func ptrS(s string) *string { return &s }
func ptrI(n int64) *int64   { return &n }

func nzS(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nzI(n int64) *int64 {
	if n == 0 {
		return nil
	}
	return &n
}
