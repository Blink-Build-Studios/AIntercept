package drawer

import (
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
	strs    map[string]*string
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

func (e *ev) get(k string) Value { return e.MD().Get(k) }

// mdStr is _str(e.md.get(k)), cached: the fields every call's facts read of
// every event about it.
func (e *ev) mdStr(k string) *string {
	if e.strs == nil {
		e.strs = map[string]*string{}
	}
	if v, ok := e.strs[k]; ok {
		return v
	}
	v := pyStr(e.get(k))
	e.strs[k] = v
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

// scope is a key's facts as the drawer reads them.
type scope struct {
	key      string
	calls    []*callRow          // by call id
	byCall   map[string]*callRow // call_id → its row
	events   []*ev               // cc_events: the Claude Code events
	analytic []canon.Event       // every analytics row (the batch session reads every source)
	sessions map[string]*sess
	parts    map[string]BatchParts
	rows     map[string]process.EventRow // the session's rendered event rows, by event id
	batchSes map[string]*canon.Event     // each batch's earliest event that names a session
	reports  map[string]*ev              // find_report's first match, by requestId, clientRequestId, ts
}

func newScope(key string, rows state.Rows, parts map[string]BatchParts, eventRows []process.EventRow) *scope {
	s := &scope{key: key, byCall: map[string]*callRow{}, sessions: map[string]*sess{}, parts: parts,
		rows: map[string]process.EventRow{}}
	ids := make([]string, 0, len(rows[state.TableFactRequests]))
	for id := range rows[state.TableFactRequests] {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		c, ok := rows[state.TableFactRequests][id].(canon.Request)
		if !ok {
			continue
		}
		r := newCallRow(&c)
		s.calls = append(s.calls, r)
		s.byCall[c.CallID] = r
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
		s.analytic = append(s.analytic, e)
		if e.Source != "claude_code" {
			continue
		}
		name := e.Name
		if name == "" {
			name = "(unnamed)"
		}
		s.events = append(s.events, &ev{ts: e.NS, name: name, device: optS(e.DeviceID), mdJSON: e.MetadataJSON,
			prompt: optS(e.PromptID), session: e.SessionID, rid: e.RequestID, crid: e.ClientRequestID, tool: e.ToolUseID,
			corr: e.CorrelationID, n: e.BatchN, seq: e.Seq, rowNS: e.RowNS, key: e.EventKey})
	}
	s.number()
	s.batchSes = map[string]*canon.Event{}
	for i := range s.analytic {
		e := &s.analytic[i]
		if e.SessionID == "" {
			continue
		}
		if b := s.batchSes[e.CorrelationID]; b == nil || e.NS < b.NS || e.NS == b.NS && e.SessionID < b.SessionID {
			s.batchSes[e.CorrelationID] = e
		}
	}
	for _, r := range eventRows {
		s.rows[r.EventID] = r
	}
	return s
}

// number gives each event its id, <batch>:<n>: its stored rank, else its row
// number in the batch by (seq, row time, time, name, metadata, event key).
func (s *scope) number() {
	byBatch := map[string][]*ev{}
	for _, e := range s.events {
		if e.n == 0 {
			byBatch[e.corr] = append(byBatch[e.corr], e)
		}
	}
	if len(byBatch) > 0 {
		all := map[string][]*ev{}
		for _, e := range s.events {
			if _, ok := byBatch[e.corr]; ok {
				all[e.corr] = append(all[e.corr], e)
			}
		}
		for _, es := range all {
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
				if e.n == 0 {
					e.n = -int64(i + 1) // numbered by row: kept apart from stored ranks until the ids are set
				}
			}
		}
	}
	for _, e := range s.events {
		n := e.n
		if n < 0 {
			n = -n
		}
		e.n = n
		e.id = e.corr + ":" + strconv.FormatInt(n, 10)
	}
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
	id       string
	exists   bool // cc.load_session isn't None: a call or an event carries it
	steps    []*step
	stepOf   map[string]int // drawer id → its place in steps
	byPrompt map[string][]*step
	events   []*ev // its cc_events, oldest first
	evPrompt map[string][]*ev
	evRID    map[string][]*ev
	evCRID   map[string][]*ev
	evTool   map[string][]*ev
	stalls   []*ev
	bash     []*ev
	success  []*ev
	index    map[string]int64 // cc.prompt_index
	chain    map[string][2]Value
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
	i := sort.Search(len(x.sandbox), func(i int) bool { return x.sandbox[i].ts > limit })
	if i == 0 {
		return nil
	}
	return x.sandbox[i-1].last
}

func (s *scope) session(sid string) *sess {
	if x := s.sessions[sid]; x != nil {
		return x
	}
	x := &sess{id: sid, stepOf: map[string]int{}, byPrompt: map[string][]*step{}, evPrompt: map[string][]*ev{},
		evRID: map[string][]*ev{}, evCRID: map[string][]*ev{}, evTool: map[string][]*ev{}, index: map[string]int64{},
		chain: map[string][2]Value{}}
	s.sessions[sid] = x
	var rows []*callRow
	for _, r := range s.calls {
		if r.c.SessionID == sid && r.ccCall() {
			rows = append(rows, r)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
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
		return a.id < b.id
	})
	ids := map[string]bool{}
	for _, r := range rows {
		c := r.c
		st := &step{id: r.id, ts: r.ts, end: optI(c.EndNS), status: optI(c.Status), prompt: optS(c.PromptID),
			class: optS(c.RequestClass), model: c.Model, stop: c.StopReason, requestID: optS(c.RequestID)}
		x.stepOf[st.id] = len(x.steps)
		x.steps = append(x.steps, st)
		if c.PromptID != "" {
			x.byPrompt[c.PromptID] = append(x.byPrompt[c.PromptID], st)
		}
		ids[st.id] = true
	}
	// The message-id chain: calls whose correlation id is one of the steps' ids.
	for _, r := range s.calls {
		if ids[r.c.CorrelationID] {
			x.chain[r.c.CorrelationID] = [2]Value{optS(r.c.MessageID), optS(r.c.PreviousMessageID)}
		}
	}
	x.byMid, x.byPrev = map[string]string{}, map[string]string{}
	chainIDs := make([]string, 0, len(x.chain))
	for cid := range x.chain {
		chainIDs = append(chainIDs, cid)
	}
	sort.Strings(chainIDs)
	for _, cid := range chainIDs {
		c := x.chain[cid]
		if c[0].Truthy() {
			x.byMid[c[0].S] = cid
		}
		if c[1].Truthy() {
			x.byPrev[c[1].S] = cid
		}
	}
	x.exists = len(rows) > 0
	for _, e := range s.events {
		if e.session != sid {
			continue
		}
		x.exists = true
		x.events = append(x.events, e)
	}
	sort.SliceStable(x.events, func(i, j int) bool { return lessEv(x.events[i], x.events[j]) })
	var prompts []*ev
	for _, e := range x.events {
		if e.prompt.K == Str {
			x.evPrompt[e.prompt.S] = append(x.evPrompt[e.prompt.S], e)
		}
		if e.rid != "" {
			x.evRID[e.rid] = append(x.evRID[e.rid], e)
		}
		if e.crid != "" {
			x.evCRID[e.crid] = append(x.evCRID[e.crid], e)
		}
		if e.tool != "" {
			x.evTool[e.tool] = append(x.evTool[e.tool], e)
		}
		switch e.name {
		case "tengu_streaming_stall_summary":
			x.stalls = append(x.stalls, e)
		case "tengu_bash_tool_command_executed":
			x.bash = append(x.bash, e)
		case "tengu_api_success":
			x.success = append(x.success, e)
		case "tengu_input_prompt":
			prompts = append(prompts, e)
		}
	}
	var last *string
	for _, e := range x.bash {
		if v := e.mdStr("sandbox_enabled"); v != nil && *v != "" {
			last = v
		}
		x.sandbox = append(x.sandbox, sandboxAt{e.ts, last})
	}
	// cc.prompt_index: by time, the last of a prompt id wins.
	sort.SliceStable(prompts, func(i, j int) bool { return prompts[i].ts < prompts[j].ts })
	for _, e := range prompts {
		if pid, n, ok := process.PromptIndexOf(canon.Event{Name: e.name, MetadataJSON: e.mdJSON}); ok {
			x.index[pid] = n
		}
	}
	return x
}

// about is session.events(…, about=(prompt, ids, tools)): the session's events
// about one call, oldest first.
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
	best := s.batchSes[cid]
	ns, sid, ok := int64(0), "", best != nil
	if ok {
		ns, sid = best.NS, best.SessionID
	}
	if p, has := s.parts[cid]; has && p.HasSession && (!ok || p.FirstNS < ns || p.FirstNS == ns && p.FirstSession < sid) {
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
	r := s.byCall[strings.ReplaceAll(cid, "~", "#")]
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
