package drawer

import (
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream/state"
)

// The drawer's scope: the facts today's drawer loads for a call (its key's
// requests and analytics rows), and the views request_detail.py reads over
// them (store.VIEWS: calls, cc_calls, cc_events). A table's optional column is
// NULL at its zero value, as the API loads it.

func optS(s string) Value {
	if s == "" {
		return None
	}
	return StrV(s)
}

func optI(n int64) Value {
	if n == 0 {
		return None
	}
	return IntV(n)
}

// callRow is a calls view row.
type callRow struct {
	c       *canon.Request
	id      string // agent_calls' correlation_id: the call id, a turn's # as ~
	ts      Value  // coalesce(start_ns, end_ns)
	product string
	st      *step // its step, when it's one of its session's
}

func newCallRow(c *canon.Request) *callRow {
	r := &callRow{c: c, id: strings.ReplaceAll(c.CallID, "#", "~"), ts: optI(c.StartNS), product: c.Product}
	if r.ts.IsNone() {
		r.ts = optI(c.EndNS)
	}
	if r.product == "" && c.Host == "api.anthropic.com" {
		r.product = canon.ProductClaudeCode
	}
	return r
}

// ccCall is whether the call is in cc_calls (agent_calls, no hidden kind).
func (r *callRow) ccCall() bool {
	c := r.c
	if c.Synthetic || c.SessionID == "" || c.HiddenKind != "" {
		return false
	}
	switch r.product {
	case canon.ProductClaudeCode:
		return c.Host == "api.anthropic.com" && strings.HasPrefix(c.Path, "/v1/messages") &&
			!strings.HasPrefix(c.Path, "/v1/messages/count_tokens")
	case canon.ProductCodex:
		return c.Host == "chatgpt.com" && c.Path == "/backend-api/codex/responses" && (c.WSTurn != 0 || c.Method == "POST")
	}
	return false
}

// step is session.Step: one of a session's cc_calls.
type step struct {
	id         string
	callID     string // its request row's id
	ts         Value
	end        Value
	status     Value
	prompt     Value
	class      Value
	model      string
	stop       string
	requestID  Value
	messageID  Value
	previousID Value
}

// ev is session.Event, with the columns the drawer's queries filter on.
type ev struct {
	id      string
	ts      int64
	name    string
	device  Value
	mdJSON  string
	md      *Value
	prompt  Value // the prompt_id column
	session string
	rid     string
	crid    string
	tool    string
	corr    string
	n       int64
	seq     int64
	rowNS   int64
	key     string
	fx      *mdFields
	rowKey  string // its fact_events row id
	rank    int64  // its stored rank in the batch (0: none, numbered by row)
}

// mdFields is what renders have read of an event's metadata: kept across
// renders (Memo) while the metadata is the same, so an event about many rows
// is parsed once, not once per render.
type mdFields struct {
	json string
	vals map[string]Value   // md.get(k)
	strs map[string]*string // _str(md.get(k))
	ext  map[string]string  // json_extract_string(md, '$.k')
}

func (e *ev) fields() *mdFields {
	if e.fx == nil || e.fx.json != e.mdJSON {
		e.fx = &mdFields{json: e.mdJSON, vals: map[string]Value{}, strs: map[string]*string{}, ext: map[string]string{}}
	}
	return e.fx
}

func (e *ev) MD() Value {
	if e.md == nil {
		v := Value{K: Object}
		if e.mdJSON != "" {
			if p, ok := Loads([]byte(e.mdJSON)); ok && p.K == Object {
				v = p
			}
		}
		e.md = &v
	}
	return *e.md
}

// get is e.md.get(k), cached.
func (e *ev) get(k string) Value {
	f := e.fields()
	if v, ok := f.vals[k]; ok {
		return v
	}
	v := e.MD().Get(k)
	f.vals[k] = v
	return v
}

// mdStr is _str(e.md.get(k)), cached: the fields every call's facts read of
// every event about it.
func (e *ev) mdStr(k string) *string {
	f := e.fields()
	if v, ok := f.strs[k]; ok {
		return v
	}
	v := pyStr(e.get(k))
	f.strs[k] = v
	return v
}

// extract is extractString(e.md, k), cached.
func (e *ev) extract(k string) string {
	f := e.fields()
	if v, ok := f.ext[k]; ok {
		return v
	}
	v := extractString(e.mdJSON, k)
	f.ext[k] = v
	return v
}

func lessEv(a, b *ev) bool {
	if a.ts != b.ts {
		return a.ts < b.ts
	}
	if a.corr != b.corr {
		return a.corr < b.corr
	}
	return a.n < b.n
}

// evOrder is a session's events' order: lessEv, then the key's (fact_events
// id) order they were stably sorted from.
func evOrder(a, b *ev) bool {
	if a.ts != b.ts || a.corr != b.corr || a.n != b.n {
		return lessEv(a, b)
	}
	return a.rowKey < b.rowKey
}

func byRowKey(a, b *ev) bool      { return a.rowKey < b.rowKey }
func byCallID(a, b *callRow) bool { return a.c.CallID < b.c.CallID }

// lessStep is a session's steps' order: their time (none last), id, call id.
func lessStep(a, b *step) bool {
	if !a.ts.Eq(b.ts) {
		switch {
		case a.ts.IsNone():
			return false
		case b.ts.IsNone():
			return true
		}
		x, _ := a.ts.Int64()
		y, _ := b.ts.Int64()
		return x < y
	}
	if a.id != b.id {
		return a.id < b.id
	}
	return a.callID < b.callID
}

// scope is a key's facts as the drawer reads them. A render builds it whole
// (newScope); the next keeps it (Memo) and applies the run's changed rows to
// it (warm.go), so what it holds is always what a whole build of the state's
// rows would hold.
type scope struct {
	key       string
	calls     map[string]*callRow   // fact_requests id (the call id) → its row
	drawer    map[string][]*callRow // drawer id → its rows, by call id
	byCorr    map[string][]*callRow // correlation id → its rows, by call id
	callsOf   map[string][]*callRow // session id → its rows, by call id
	evs       map[string]*ev        // fact_events id → its Claude Code event
	evsOf     map[string][]*ev      // session id → its events, by fact_events id
	batches   map[string]*batch     // correlation id → its rows
	evByID    map[string][]*ev      // drawer event id → its events, by fact_events id
	reports   map[string][]*ev      // find_report's key → its successes, by fact_events id
	stepOwner map[string]string     // step id → the session whose step it is
	rowCorr   map[string]string     // fact_events id → its batch
	sessions  map[string]*sess
	part      func(batch string) (BatchParts, bool)
	rows      map[string]process.EventRow // the session's rendered event rows, by event id
	rec       *rowDeps                    // the lookups of the row being derived (incremental.go); nil: none recorded
	ch        *changes                    // what a run's updates moved (warm.go); nil: none tracked
}

// batch is an analytics batch's rows in the key.
type batch struct {
	evs      []*ev                   // its Claude Code events, by fact_events id
	analytic map[string]*canon.Event // every analytics row of it, by fact_events id
	session  *canon.Event            // its earliest row naming a session
}

func newScope(key string, rows state.Rows, part func(string) (BatchParts, bool), eventRows []process.EventRow) *scope {
	s := &scope{key: key, calls: map[string]*callRow{}, drawer: map[string][]*callRow{}, byCorr: map[string][]*callRow{},
		callsOf: map[string][]*callRow{}, evs: map[string]*ev{}, evsOf: map[string][]*ev{}, batches: map[string]*batch{},
		evByID: map[string][]*ev{}, reports: map[string][]*ev{}, stepOwner: map[string]string{}, rowCorr: map[string]string{},
		sessions: map[string]*sess{}, part: part, rows: map[string]process.EventRow{}}
	ids := make([]string, 0, len(rows[state.TableFactRequests]))
	for id := range rows[state.TableFactRequests] {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if c, ok := rows[state.TableFactRequests][id].(canon.Request); ok {
			r := newCallRow(&c)
			s.calls[c.CallID] = r
			s.drawer[r.id] = append(s.drawer[r.id], r)
			s.byCorr[c.CorrelationID] = append(s.byCorr[c.CorrelationID], r)
			s.callsOf[c.SessionID] = append(s.callsOf[c.SessionID], r)
		}
	}
	keys := make([]string, 0, len(rows[state.TableFactEvents]))
	for k := range rows[state.TableFactEvents] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		e, ok := rows[state.TableFactEvents][k].(canon.Event)
		if !ok {
			continue
		}
		b := s.batch(e.CorrelationID)
		b.analytic[k] = &e
		s.rowCorr[k] = e.CorrelationID
		if x := newEv(&e, k); x != nil {
			s.evs[k] = x
			s.evsOf[x.session] = append(s.evsOf[x.session], x)
			b.evs = append(b.evs, x)
		}
	}
	for _, b := range s.batches {
		for e, k := range s.ids(b) {
			e.n, e.id = k.n, k.id
		}
		b.session = earliestSession(b)
	}
	for _, k := range keys {
		if x := s.evs[k]; x != nil {
			s.evByID[x.id] = append(s.evByID[x.id], x)
			for _, rk := range reportKeys(x) {
				s.reports[rk] = append(s.reports[rk], x)
			}
		}
	}
	for _, r := range eventRows {
		s.rows[r.EventID] = r
	}
	for sid := range s.callsOf {
		s.build(sid)
	}
	for sid := range s.evsOf {
		s.build(sid)
	}
	return s
}

func (s *scope) batch(c string) *batch {
	b := s.batches[c]
	if b == nil {
		b = &batch{analytic: map[string]*canon.Event{}}
		s.batches[c] = b
	}
	return b
}

// newEv is a Claude Code analytics row's event (nil for another source's).
func newEv(e *canon.Event, k string) *ev {
	if e.Source != "claude_code" {
		return nil
	}
	name := e.Name
	if name == "" {
		name = "(unnamed)"
	}
	return &ev{ts: e.NS, name: name, device: optS(e.DeviceID), mdJSON: e.MetadataJSON,
		prompt: optS(e.PromptID), session: e.SessionID, rid: e.RequestID, crid: e.ClientRequestID, tool: e.ToolUseID,
		corr: e.CorrelationID, rank: e.BatchN, n: e.BatchN, seq: e.Seq, rowNS: e.RowNS, key: e.EventKey, rowKey: k}
}

// earliestSession is _BATCH_SESSION's row of the key: the batch's earliest
// (time, session) that names a session.
func earliestSession(b *batch) *canon.Event {
	var best *canon.Event
	for _, e := range b.analytic {
		if e.SessionID == "" {
			continue
		}
		if best == nil || e.NS < best.NS || e.NS == best.NS && e.SessionID < best.SessionID {
			best = e
		}
	}
	return best
}

// reportKeys are the keys find_report finds a success by: its requestId,
// clientRequestId (json_extract_string's) and time.
func reportKeys(e *ev) []string {
	if e.name != "tengu_api_success" {
		return nil
	}
	out := []string{e.extract("requestId")}
	for _, k := range []string{e.extract("clientRequestId"), itoa(e.ts)} {
		if !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}

// rankedID is an event's rank in its batch and its drawer id.
type rankedID struct {
	n  int64
	id string
}

// ids is a batch's events' ids, <batch>:<n>: their stored rank, else their
// row number in the batch by (seq, row time, time, name, metadata, event key).
func (s *scope) ids(b *batch) map[*ev]rankedID {
	unranked := false
	for _, e := range b.evs {
		if e.rank == 0 {
			unranked = true
			break
		}
	}
	rowN := map[*ev]int64{}
	if unranked {
		es := append([]*ev(nil), b.evs...)
		sort.SliceStable(es, func(i, j int) bool {
			a, b := es[i], es[j]
			switch {
			case a.seq != b.seq:
				return a.seq < b.seq
			case a.rowNS != b.rowNS:
				return a.rowNS < b.rowNS
			case a.ts != b.ts:
				return a.ts < b.ts
			case a.name != b.name:
				return nullsLast(a.name, b.name)
			case a.mdJSON != b.mdJSON:
				return nullsLast(a.mdJSON, b.mdJSON)
			}
			return a.key < b.key
		})
		for i, e := range es {
			if e.rank == 0 {
				rowN[e] = int64(i + 1)
			}
		}
	}
	out := make(map[*ev]rankedID, len(b.evs))
	for _, e := range b.evs {
		k := e.rank
		if k == 0 {
			k = rowN[e]
		}
		out[e] = rankedID{k, e.corr + ":" + strconv.FormatInt(k, 10)}
	}
	return out
}

func nullsLast(a, b string) bool {
	switch {
	case a == "":
		return false
	case b == "":
		return true
	}
	return a < b
}

// sess is what the drawer reads of one session in the scope.
type sess struct {
	s        *scope
	id       string
	exists   bool // cc.load_session isn't None: a call or an event carries it
	steps    []*step
	byPrompt map[string][]*step
	noPrompt []*step        // its steps with no prompt, in order (a call with none's retry reads them)
	stepIDs  map[string]int // step id → how many of its steps have it
	events   []*ev          // its cc_events, oldest first (evOrder)
	evPrompt map[string][]*ev
	evRID    map[string][]*ev
	evCRID   map[string][]*ev
	evTool   map[string][]*ev
	stalls   []*ev
	bash     []*ev
	success  []*ev
	prompts  map[string][]*ev // its typed prompts by their cc_prompt_id, in event order
	index    map[string]int64 // cc.prompt_index
	chain    map[string][2]Value
	midCids  map[string]map[string]bool // message id → the chain keys with it (byMid's is the last)
	prevCids map[string]map[string]bool
	byMid    map[string]string // message id → the step whose message it is
	byPrev   map[string]string // previous message id → the step that follows it
	sandbox  []sandboxAt       // its bash events' sandbox_enabled, by time
}

// sandboxAt is a bash event's time and the latest non-empty sandbox_enabled up
// to it (in event order).
type sandboxAt struct {
	ts   int64
	last *string
}

// sandboxUpTo is the sandbox setting of the session's bash events at or before limit.
func (x *sess) sandboxUpTo(limit int64) *string {
	x.s.useSandbox(x.id, limit)
	i := sort.Search(len(x.sandbox), func(i int) bool { return x.sandbox[i].ts > limit })
	if i == 0 {
		return nil
	}
	return x.sandbox[i-1].last
}

// session is sid's view (an empty one for a session the key has no rows of).
func (s *scope) session(sid string) *sess {
	s.use(kExists, sid, "")
	if x := s.sessions[sid]; x != nil {
		return x
	}
	return s.build(sid)
}

func newSess(s *scope, sid string) *sess {
	return &sess{s: s, id: sid, byPrompt: map[string][]*step{}, stepIDs: map[string]int{}, evPrompt: map[string][]*ev{},
		evRID: map[string][]*ev{}, evCRID: map[string][]*ev{}, evTool: map[string][]*ev{}, prompts: map[string][]*ev{},
		index: map[string]int64{}, chain: map[string][2]Value{}, midCids: map[string]map[string]bool{},
		prevCids: map[string]map[string]bool{}, byMid: map[string]string{}, byPrev: map[string]string{}}
}

func newStep(r *callRow) *step {
	c := r.c
	return &step{id: r.id, callID: c.CallID, ts: r.ts, end: optI(c.EndNS), status: optI(c.Status), prompt: optS(c.PromptID),
		class: optS(c.RequestClass), model: c.Model, stop: c.StopReason, requestID: optS(c.RequestID)}
}

// build is sid's view from the scope's rows, whole.
func (s *scope) build(sid string) *sess {
	x := newSess(s, sid)
	s.sessions[sid] = x
	for _, r := range s.callsOf[sid] {
		if r.ccCall() {
			st := newStep(r)
			r.st = st
			x.steps = append(x.steps, st)
		}
	}
	sort.SliceStable(x.steps, func(i, j int) bool { return lessStep(x.steps[i], x.steps[j]) })
	for _, st := range x.steps {
		if !st.prompt.IsNone() {
			x.byPrompt[st.prompt.S] = append(x.byPrompt[st.prompt.S], st)
		} else {
			x.noPrompt = append(x.noPrompt, st)
		}
		x.stepIDs[st.id]++
		s.stepOwner[st.id] = sid
	}
	for k := range x.stepIDs {
		x.chainKey(k)
	}
	x.events = append([]*ev(nil), s.evsOf[sid]...)
	sort.SliceStable(x.events, func(i, j int) bool { return evOrder(x.events[i], x.events[j]) })
	for _, e := range x.events {
		x.lists(e, func(l []*ev, e *ev) []*ev { return append(l, e) })
	}
	x.resandbox(0)
	for pid := range x.prompts {
		x.reindex(pid)
	}
	x.exists = len(x.steps) > 0 || len(x.events) > 0
	return x
}

// lists applies op (an append or a sorted insert or removal) to each of the
// session's event lists e belongs in.
func (x *sess) lists(e *ev, op func([]*ev, *ev) []*ev) {
	if e.prompt.K == Str {
		x.evPrompt[e.prompt.S] = op(x.evPrompt[e.prompt.S], e)
	}
	if e.rid != "" {
		x.evRID[e.rid] = op(x.evRID[e.rid], e)
	}
	if e.crid != "" {
		x.evCRID[e.crid] = op(x.evCRID[e.crid], e)
	}
	if e.tool != "" {
		x.evTool[e.tool] = op(x.evTool[e.tool], e)
	}
	switch e.name {
	case "tengu_streaming_stall_summary":
		x.stalls = op(x.stalls, e)
	case "tengu_bash_tool_command_executed":
		x.bash = op(x.bash, e)
	case "tengu_api_success":
		x.success = op(x.success, e)
	case "tengu_input_prompt":
		if pid, _, ok := promptIndexOf(e); ok {
			x.prompts[pid] = op(x.prompts[pid], e)
		}
	}
}

func promptIndexOf(e *ev) (string, int64, bool) {
	return process.PromptIndexOf(canon.Event{Name: e.name, MetadataJSON: e.mdJSON})
}

// resandbox recomputes the sandbox settings from the i-th bash event on.
func (x *sess) resandbox(i int) {
	x.sandbox = x.sandbox[:min(i, len(x.sandbox))]
	var last *string
	if i > 0 && i <= len(x.sandbox) {
		last = x.sandbox[i-1].last
	}
	for _, e := range x.bash[len(x.sandbox):] {
		if v := e.mdStr("sandbox_enabled"); v != nil && *v != "" {
			last = v
		}
		x.sandbox = append(x.sandbox, sandboxAt{e.ts, last})
	}
}

// reindex is cc.prompt_index's value for pid: its last typed prompt's.
func (x *sess) reindex(pid string) {
	es := x.prompts[pid]
	if len(es) == 0 {
		delete(x.index, pid)
		delete(x.prompts, pid)
		return
	}
	_, n, _ := promptIndexOf(es[len(es)-1])
	x.index[pid] = n
}

// chainKey sets the chain entry of step id k: the last call (by call id) whose
// correlation id is k, and the message ids it keys.
func (x *sess) chainKey(k string) {
	old, had := x.chain[k]
	var cur [2]Value
	has := false
	if x.stepIDs[k] > 0 {
		if rs := x.s.byCorr[k]; len(rs) > 0 {
			r := rs[len(rs)-1]
			cur, has = [2]Value{optS(r.c.MessageID), optS(r.c.PreviousMessageID)}, true
		}
	}
	if had == has && old[0].Eq(cur[0]) && old[1].Eq(cur[1]) && old[0].K == cur[0].K && old[1].K == cur[1].K {
		return
	}
	if c := x.s.ch; c != nil {
		c.chain = append(c.chain, k)
		for _, v := range []Value{old[0], cur[0]} {
			if v.Truthy() {
				c.mids = append(c.mids, v.S)
			}
		}
		for _, v := range []Value{old[1], cur[1]} {
			if v.Truthy() {
				c.prevs = append(c.prevs, v.S)
			}
		}
	}
	if had {
		x.unkey(x.midCids, x.byMid, old[0], k)
		x.unkey(x.prevCids, x.byPrev, old[1], k)
		delete(x.chain, k)
	}
	if has {
		x.chain[k] = cur
		x.rekey(x.midCids, x.byMid, cur[0], k)
		x.rekey(x.prevCids, x.byPrev, cur[1], k)
	}
}

// rekey and unkey keep byMid (byPrev) the last chain key, in key order, with
// each message id.
func (x *sess) rekey(set map[string]map[string]bool, by map[string]string, m Value, k string) {
	if !m.Truthy() {
		return
	}
	if set[m.S] == nil {
		set[m.S] = map[string]bool{}
	}
	set[m.S][k] = true
	if k > by[m.S] {
		by[m.S] = k
	}
}

func (x *sess) unkey(set map[string]map[string]bool, by map[string]string, m Value, k string) {
	if !m.Truthy() {
		return
	}
	delete(set[m.S], k)
	if by[m.S] != k {
		return
	}
	best := ""
	for c := range set[m.S] {
		if c > best {
			best = c
		}
	}
	if best == "" {
		delete(by, m.S)
		delete(set, m.S)
	} else {
		by[m.S] = best
	}
}

// about is session.events(…, about=(prompt, ids, tools)): the session's events
// about one call, oldest first.
//
// Which events it returns doesn't make a row's dependencies: the facts record
// what they pick from them, by role (factsOf), and an event is filed under
// every role it can play (incremental.go).
func (x *sess) about(prompt Value, ids, tools []string) []*ev {
	seen := map[*ev]bool{}
	var out []*ev
	add := func(es []*ev) {
		for _, e := range es {
			if !seen[e] {
				seen[e] = true
				out = append(out, e)
			}
		}
	}
	if prompt.K == Str {
		add(x.evPrompt[prompt.S])
	}
	for _, id := range ids {
		add(x.evRID[id])
		add(x.evCRID[id])
	}
	for _, t := range tools {
		add(x.evTool[t])
	}
	add(x.stalls)
	sort.SliceStable(out, func(i, j int) bool { return lessEv(out[i], out[j]) })
	return out
}

// batchSession is request_detail._BATCH_SESSION: the session the batch's
// earliest event names, over every key's events of it (this key's rows, the
// others' batch parts).
func (s *scope) batchSession(cid string) Value {
	s.use(kBatch, cid, "")
	var best *canon.Event
	if b := s.batches[cid]; b != nil {
		best = b.session
	}
	ns, sid, ok := int64(0), "", best != nil
	if ok {
		ns, sid = best.NS, best.SessionID
	}
	var p BatchParts
	has := false
	if s.part != nil {
		p, has = s.part(cid)
	}
	if has && p.HasSession && (!ok || p.FirstNS < ns || p.FirstNS == ns && p.FirstSession < sid) {
		ns, sid, ok = p.FirstNS, p.FirstSession, true
	}
	if !ok {
		return None
	}
	return StrV(sid)
}

// wireFromTables is request_detail._wire_from_tables: the call from its request
// row.
func (s *scope) wireFromTables(cid string) *Wire {
	r := s.calls[strings.ReplaceAll(cid, "~", "#")]
	if r == nil || r.ts.IsNone() {
		return nil
	}
	c := r.c
	var complete []Member
	add := func(k string, v Value) {
		if !v.IsNone() {
			complete = append(complete, Member{k, v})
		}
	}
	add("model", optS(c.Model))
	add("stop_reason", optS(c.StopReason))
	add("message_id", optS(c.MessageID))
	add("input_tokens", optI(c.InputTokens))
	add("output_tokens", optI(c.OutputTokens))
	add("cache_read_tokens", optI(c.CacheReadTokens))
	add("cache_write_tokens", optI(c.CacheWriteTokens))
	add("error", optS(c.Error))
	add("session_id", optS(c.SessionID))
	add("device_id", optS(c.DeviceID))
	if c.ExtJSON != "" {
		if v, ok := Loads([]byte(c.ExtJSON)); ok {
			add("client_metadata", v)
		}
	}
	ts, _ := r.ts.Int64()
	w := &Wire{CID: cid, TS: ts, Method: optS(c.Method), Path: c.Path, ReqHeaders: jsonObj(c.ClientJSON),
		IP: optS(c.IP), KeyHash: optS(c.KeyHash), Status: optI(c.Status), RespTS: optI(c.ResponseStartNS),
		RespHeaders: None, Complete: None, EndTS: optI(c.EndNS), Up: optI(c.RequestBytes), Down: optI(c.ResponseBytes),
		FromTables: true}
	if !w.RespTS.IsNone() || !w.Status.IsNone() {
		w.RespHeaders = jsonObj(c.ProviderJSON)
	}
	if !w.EndTS.IsNone() {
		w.Complete = Value{K: Object, O: complete}
		if complete == nil {
			w.Complete.O = []Member{}
		}
	}
	return w
}

// jsonObj is request_detail._json_obj: a JSON object's keys and values as strs.
func jsonObj(text string) Value {
	out := Value{K: Object, O: []Member{}}
	if text == "" {
		return out
	}
	v, ok := Loads([]byte(text))
	if !ok || v.K != Object {
		return out
	}
	for _, m := range v.O {
		out.O = append(out.O, Member{m.Key, StrV(m.Val.Str())})
	}
	return out
}
