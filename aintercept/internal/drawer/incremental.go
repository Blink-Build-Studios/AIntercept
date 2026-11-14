package drawer

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/cespare/xxhash/v2"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/stream/state"
)

// The incremental render (docs/PIPELINE.md §3.7, "Which rows a run renders").
//
// A row is a function of its own call (its request row and its wire) and of
// what its derivation looks up in the key's scope: every such lookup goes
// through an index (a session's events by prompt, request id or tool use id,
// its steps by prompt, the message-id chain, the prompt index, a batch's
// session, the reported calls' success events, the sandbox setting up to a
// time, the session's step order). A render records, per row, a token for each
// lookup; the next render files each input that changed since under the tokens
// of the index entries it's in (as it was, and as it is), and derives again
// only the rows that are new, whose own call changed, or that looked up a
// token filed. Every other row is the memo's. Tokens are hashes: a collision
// only derives a row again.

// Changed is the state's rows a run changed since the memo's render, by state
// table and row id (the run's deltas): fact_requests, fact_events and
// request_missed are read.
type Changed map[string]map[string]bool

// Memo is a render's rows and what the next render needs to tell which of
// them can have changed. It's kept with the warm state (render.Baseline) and
// consumed by the next RenderNext: never use one twice.
type Memo struct {
	used    bool
	derived int                 // the rows this render derived (the rest were its prev's)
	reqs    map[string]*memoReq // drawer request id → its row, or a reported id that gave none
	events  map[string]Event    // drawer event id → its row
	evIn    map[string]*filed   // fact_events row id → what it was filed under
	callIn  map[string]*filed   // fact_requests row id → what it was filed under
	wires   map[string]*Wire    // call id → the wire the render read (nil: none)
	parts   map[string]BatchParts
	exists  map[string]bool      // the sessions a call or an event names
	steps   map[string][]string  // per existing session, its steps' ids in order
	lists   map[string][]uint64  // per (session, prompt), its steps' hashes in order (stepsDep)
	md      map[string]*mdFields // fact_events row id → what renders read of its metadata
	counts  []StepCount          // the render's step counts
	tpls    map[string]string    // template id → template: its rows' (prev's, carried)
}

type memoReq struct {
	yield bool
	req   Request
	deps  rowDeps
}

// rowDeps is what a row's derivation looked up.
type rowDeps struct {
	toks  []uint64
	sand  []sandDep  // the sandbox setting of a session, up to a time
	order []orderDep // a step's place among its session's steps
	steps []stepsDep // its prompt's steps, up to a place
}

// stepsDep is a row's read of a session's steps of one prompt (promptKey), in
// order: up to place hi (len: up to the end, so a step added after them too).
type stepsDep struct {
	list string // session + "\x00" + promptKey
	hi   int
}

type sandDep struct {
	sess  string
	limit int64
}

// orderDep is the place (-1: none) at which a row found a step in its
// session's steps, all of them in order ("delivered in step N").
type orderDep struct {
	sess string
	at   int
}

// filed is an input as the indexes hold it: its tokens, its time when it's a
// bash event (the sandbox's), and for an event its drawer id and the hash of
// its rendered event row (eventRow's fields).
type filed struct {
	toks    []uint64
	bash    bool
	sess    string
	ts      int64
	id      string // an event's drawer id; a call's drawer id
	step    bool   // a call that's one of its session's steps
	rowHash uint64
}

// The tokens' kinds.
const (
	kExists   byte = iota + 1
	kMdRID         // (session, id): an event whose metadata's requestId (or a success's clientRequestId) is id
	kStallRID      // (session, id): a stall summary whose request_id is id
	kStalls        // (session): every stall summary (a row whose request id isn't a str reads them all)
	kQuery         // (session, prompt, 16 s bucket): a tengu_api_query
	kRetry         // (session, prompt or *, 30 s bucket): a tengu_api_retry
	kBash          // (session, tool use id): a bash run
	kIndex
	kChainK
	kChainM
	kChainV
	kBatch
	kReported
)

const (
	queryBucket = 16 * ns
	retryBucket = 30 * ns
)

// bucket is t's bucket of width w (floor).
func bucket(t, w int64) int64 {
	b := t / w
	if t%w != 0 && t < 0 {
		b--
	}
	return b
}

// promptKey is a prompt column's value as a key: a str's, or none's.
func promptKey(v Value) string {
	switch {
	case v.K == Str:
		return "s" + v.S
	case v.IsNone():
		return "n"
	}
	return "x" + v.Repr()
}

func tok(k byte, a, b string) uint64 {
	d := xxhash.New()
	_, _ = d.Write([]byte{k})
	_, _ = d.WriteString(a)
	_, _ = d.Write([]byte{0})
	_, _ = d.WriteString(b)
	return d.Sum64()
}

// use records a lookup of the row being derived.
func (s *scope) use(k byte, a, b string) {
	if s.rec != nil {
		s.rec.toks = append(s.rec.toks, tok(k, a, b))
	}
}

func (s *scope) useSandbox(sess string, limit int64) {
	if s.rec != nil {
		s.rec.sand = append(s.rec.sand, sandDep{sess, limit})
	}
}

func (s *scope) useSteps(sess, prompt string, hi int) {
	if s.rec != nil {
		s.rec.steps = append(s.rec.steps, stepsDep{sess + "\x00" + prompt, hi})
	}
}

func (s *scope) useOrder(sess string, at int) {
	if s.rec != nil {
		s.rec.order = append(s.rec.order, orderDep{sess, at})
	}
}

// record derives f's row, recording its lookups.
func (s *scope) record(f func()) rowDeps {
	var d rowDeps
	s.rec = &d
	f()
	s.rec = nil
	slices.Sort(d.toks)
	d.toks = slices.Compact(d.toks)
	return d
}

// fileEvent is what the indexes hold of an analytics row (x, its Claude Code
// event's view; nil for another source's): every role it can play in a row's
// facts, under its session: its metadata's requestId (a tool use's request,
// a success's or a tool event's call) and a success's clientRequestId; a
// stall summary's request_id; a query's and a retry's prompt and time; a
// bash run's tool use id and time (the sandbox setting); a typed prompt's
// index; a success's keys for the reported calls; and under its batch, an
// analytics call's session.
func fileEvent(e *canon.Event, x *ev) *filed {
	f := &filed{}
	if e.SessionID != "" {
		f.toks = append(f.toks, tok(kBatch, e.CorrelationID, ""))
	}
	if e.Source != "claude_code" || x == nil {
		return f
	}
	sid := e.SessionID
	add := func(k byte, v *string) {
		if v != nil {
			f.toks = append(f.toks, tok(k, sid, *v))
		}
	}
	add(kMdRID, x.mdStr("requestId"))
	switch x.name {
	case "tengu_api_success":
		add(kMdRID, x.mdStr("clientRequestId"))
		keys := []string{x.extract("requestId"), x.extract("clientRequestId"), itoa(x.ts)}
		for _, k := range []*string{x.mdStr("requestId"), x.mdStr("clientRequestId")} {
			if k != nil {
				keys = append(keys, *k)
			}
		}
		for _, k := range keys {
			f.toks = append(f.toks, tok(kReported, "", k))
		}
	case "tengu_streaming_stall_summary":
		f.toks = append(f.toks, tok(kStalls, sid, ""))
		if v := x.get("request_id"); v.K == Str {
			f.toks = append(f.toks, tok(kStallRID, sid, v.S))
		}
	case "tengu_api_query":
		f.toks = append(f.toks, tok(kQuery, sid, promptKey(x.prompt)+"\x00"+itoa(bucket(x.ts, queryBucket))))
	case "tengu_api_retry":
		b := itoa(bucket(x.ts, retryBucket))
		f.toks = append(f.toks, tok(kRetry, sid, promptKey(x.prompt)+"\x00"+b), tok(kRetry, sid, "*\x00"+b))
	case "tengu_input_prompt":
		if pid, _, ok := process.PromptIndexOf(canon.Event{Name: e.Name, MetadataJSON: e.MetadataJSON}); ok {
			f.toks = append(f.toks, tok(kIndex, sid, pid))
		}
	case "tengu_bash_tool_command_executed":
		f.bash, f.sess, f.ts = true, sid, e.NS
		if v := x.get("tool_use_id"); v.Truthy() {
			f.toks = append(f.toks, tok(kBash, sid, v.Str()))
		}
	}
	return f
}

// fileCall is what the indexes hold of a request row: its message-id chain
// entry's keys. (Its place in its session's steps is compared list by list.)
func fileCall(r *callRow) *filed {
	return &filed{id: r.id, step: r.ccCall(), toks: chainToks(r.c)}
}

func chainToks(c *canon.Request) []uint64 {
	out := []uint64{tok(kChainK, "", c.CorrelationID)}
	if c.MessageID != "" {
		out = append(out, tok(kChainM, "", c.MessageID))
	}
	if c.PreviousMessageID != "" {
		out = append(out, tok(kChainV, "", c.PreviousMessageID))
	}
	return out
}

func eventRowHash(r process.EventRow, ok bool) uint64 {
	if !ok {
		return 0
	}
	d := xxhash.New()
	if r.PromptIndex != nil {
		_, _ = d.WriteString(strconv.FormatInt(*r.PromptIndex, 10))
	} else {
		_, _ = d.WriteString("-")
	}
	for _, v := range []string{r.LinkKind, r.LinkID, r.LinkLabel, r.Summary, r.Marker, strconv.FormatBool(r.Internal)} {
		_, _ = d.Write([]byte{0})
		_, _ = d.WriteString(v)
	}
	return d.Sum64() | 1
}

// dirty is what changed since the memo's render.
type dirty struct {
	own    map[string]bool  // drawer request ids whose own call changed
	toks   map[uint64]bool  // tokens filed
	bash   map[string]int64 // session → its earliest changed bash event
	order  map[string]int   // session → the first place its steps differ at
	lists  map[string]int   // (session, prompt) → the first place its steps differ at
	events map[string]bool  // drawer event ids to render again
}

func (d *dirty) file(f *filed) {
	if f == nil {
		return
	}
	for _, t := range f.toks {
		d.toks[t] = true
	}
	if f.bash {
		if at, ok := d.bash[f.sess]; !ok || f.ts < at {
			d.bash[f.sess] = f.ts
		}
	}
}

// hits is whether a row's lookups can have changed.
func (d *dirty) hits(r rowDeps) bool {
	for _, t := range r.toks {
		if d.toks[t] {
			return true
		}
	}
	for _, sd := range r.sand {
		if at, ok := d.bash[sd.sess]; ok && at <= sd.limit {
			return true
		}
	}
	for _, od := range r.order {
		if k, ok := d.order[od.sess]; ok && (od.at < 0 || od.at >= k) {
			return true
		}
	}
	for _, sd := range r.steps {
		if k, ok := d.lists[sd.list]; ok && k <= sd.hi {
			return true
		}
	}
	return false
}

// VerifyIncremental makes every incremental render also render whole and
// panic where they differ (the tests and the gate set it;
// AINTERCEPT_DRAWER_VERIFY=1 does too).
var VerifyIncremental = os.Getenv("AINTERCEPT_DRAWER_VERIFY") == "1"

// inputs files every input of the scope into next, and, with prev, what
// changed since prev into d. next reuses prev's input maps (prev is consumed).
func (s *scope) inputs(in Inputs, prev *Memo, changed Changed, next *Memo) *dirty {
	var d *dirty
	if prev != nil {
		d = &dirty{own: map[string]bool{}, toks: map[uint64]bool{}, bash: map[string]int64{}, order: map[string]int{},
			lists: map[string]int{}, events: map[string]bool{}}
		next.evIn, next.callIn, next.wires, next.md, next.tpls = prev.evIn, prev.callIn, prev.wires, prev.md, prev.tpls
	}
	mark := func(ids ...string) {
		for _, id := range ids {
			if id != "" {
				d.events[id] = true
			}
		}
	}
	// Events: each analytics row, its drawer id (Claude Code's) and event row.
	ccByKey := make(map[string]*ev, len(s.events))
	for _, e := range s.events {
		ccByKey[e.rowKey] = e
	}
	rows := in.Rows[state.TableFactEvents]
	for k, v := range rows {
		e, ok := v.(canon.Event)
		if !ok {
			continue
		}
		x := ccByKey[k]
		id, rh := "", uint64(0)
		if x != nil {
			r, ok := s.rows[x.id]
			id, rh = x.id, eventRowHash(r, ok)
		}
		old := next.evIn[k]
		if d == nil || old == nil || changed[state.TableFactEvents][k] {
			f := fileEvent(&e, x)
			f.id, f.rowHash = id, rh
			next.evIn[k] = f
			if d != nil {
				d.file(old)
				d.file(f)
				if old != nil {
					mark(old.id)
				}
				mark(id)
			}
			continue
		}
		if old.id != id || old.rowHash != rh {
			d.file(old)
			mark(old.id, id)
			old.id, old.rowHash = id, rh
		}
	}
	if d != nil {
		for k := range changed[state.TableFactEvents] { // gone
			if _, ok := rows[k]; !ok {
				if old := next.evIn[k]; old != nil {
					d.file(old)
					mark(old.id)
					delete(next.evIn, k)
					delete(next.md, k)
				}
			}
		}
	}
	// Calls: each request row, and the wire the render reads for it.
	byCorr := map[string][]*callRow{}
	if d != nil {
		for _, r := range s.calls {
			byCorr[r.c.CorrelationID] = append(byCorr[r.c.CorrelationID], r)
		}
	}
	stepChanged := func(id string) { // a step id that came or went: the calls whose chain entry it keys
		for _, r := range byCorr[id] {
			for _, t := range chainToks(r.c) {
				d.toks[t] = true
			}
		}
	}
	gone := func(old *filed) {
		d.file(old)
		d.own[old.id] = true
		if old.step {
			stepChanged(old.id)
		}
	}
	for _, r := range s.calls {
		k := r.c.CallID
		var w *Wire
		if !strings.Contains(r.id, "~") && in.Wire != nil {
			w = in.Wire(k)
		}
		if d != nil {
			if old, ok := next.wires[k]; !ok || old != w {
				d.own[r.id] = true
			}
		}
		next.wires[k] = w
		old := next.callIn[k]
		if d != nil && old != nil && !changed[state.TableFactRequests][k] {
			continue
		}
		f := fileCall(r)
		next.callIn[k] = f
		if d != nil {
			if old != nil {
				gone(old)
			}
			d.file(f)
			d.own[f.id] = true
			if f.step {
				stepChanged(f.id)
			}
		}
	}
	if d != nil {
		calls := in.Rows[state.TableFactRequests]
		for k := range changed[state.TableFactRequests] { // gone
			if _, ok := calls[k]; !ok {
				if old := next.callIn[k]; old != nil {
					gone(old)
					delete(next.callIn, k)
					delete(next.wires, k)
				}
			}
		}
	}
	// Other keys' parts of the key's batches.
	for b, p := range in.Parts {
		next.parts[b] = p
		if d != nil {
			if o, ok := prev.parts[b]; !ok || o != p {
				d.toks[tok(kBatch, b, "")] = true
			}
		}
	}
	if d != nil {
		for b := range prev.parts {
			if _, ok := in.Parts[b]; !ok {
				d.toks[tok(kBatch, b, "")] = true
			}
		}
	}
	// The sessions that exist, and each one's step order.
	for _, r := range s.calls {
		if r.c.SessionID != "" && r.ccCall() {
			next.exists[r.c.SessionID] = true
		}
	}
	for _, e := range s.events {
		if e.session != "" {
			next.exists[e.session] = true
		}
	}
	for sid := range next.exists {
		steps := s.session(sid).steps
		ids := make([]string, len(steps))
		lists := map[string][]uint64{}
		for i, st := range steps {
			ids[i] = st.id
			k := sid + "\x00" + promptKey(st.prompt)
			lists[k] = append(lists[k], stepHash(st))
		}
		next.steps[sid] = ids
		for k, l := range lists {
			next.lists[k] = l
			if d != nil {
				if i := firstDiff(prev.lists[k], l); i >= 0 {
					d.lists[k] = i
				}
			}
		}
		if d != nil {
			if !prev.exists[sid] {
				d.toks[tok(kExists, sid, "")] = true
			}
			// A step's place changes from the first place the lists differ;
			// appended steps move none.
			old, k := prev.steps[sid], 0
			for k < len(old) && k < len(ids) && old[k] == ids[k] {
				k++
			}
			if k < len(old) {
				d.order[sid] = k
			}
		}
	}
	if d != nil {
		for k, l := range prev.lists { // a prompt whose steps are all gone
			if _, ok := next.lists[k]; !ok && len(l) > 0 {
				d.lists[k] = 0
			}
		}
		for sid := range prev.exists {
			if !next.exists[sid] {
				d.toks[tok(kExists, sid, "")] = true
				d.order[sid] = 0
			}
		}
	}
	return d
}

func newMemo() *Memo {
	return &Memo{reqs: map[string]*memoReq{}, events: map[string]Event{}, evIn: map[string]*filed{},
		callIn: map[string]*filed{}, wires: map[string]*Wire{}, parts: map[string]BatchParts{},
		exists: map[string]bool{}, steps: map[string][]string{}, lists: map[string][]uint64{}, md: map[string]*mdFields{},
		tpls: map[string]string{}}
}

// Derived is how many request rows the render derived; the rest were its prev's.
func (m *Memo) Derived() int {
	if m == nil {
		return 0
	}
	return m.derived
}

// reqOf is prev's row of id when nothing it read changed (nil: derive it).
func (m *Memo) reqOf(id string, d *dirty) *memoReq {
	if m == nil || d == nil || d.own[id] {
		return nil
	}
	if r := m.reqs[id]; r != nil && !d.hits(r.deps) {
		return r
	}
	return nil
}

// verify panics where an incremental render's rows aren't a whole render's.
func verify(in Inputs, reqs []Request, evs []Event) {
	wr, we, _ := RenderNext(in, nil, nil)
	for i := 0; i < max(len(wr), len(reqs)); i++ {
		if i >= len(wr) || i >= len(reqs) || wr[i] != reqs[i] {
			id := ""
			if i < len(wr) {
				id = wr[i].ID
			} else {
				id = reqs[i].ID
			}
			panic(fmt.Sprintf("drawer: key %s: the incremental render's request %q isn't the whole render's", in.Key, id))
		}
	}
	for i := 0; i < max(len(we), len(evs)); i++ {
		if i >= len(we) || i >= len(evs) || we[i] != evs[i] {
			panic(fmt.Sprintf("drawer: key %s: the incremental render's event rows aren't the whole render's (at %d)", in.Key, i))
		}
	}
}

// stepHash is what rows read of a step: its id, times, status, class and prompt.
func stepHash(st *step) uint64 {
	d := xxhash.New()
	for _, v := range []Value{StrV(st.id), st.ts, st.end, st.status, st.class, st.prompt} {
		_, _ = d.WriteString(v.Repr())
		_, _ = d.Write([]byte{0})
	}
	return d.Sum64()
}

// firstDiff is the first place two lists differ (len(old) when cur only adds
// to old: nothing old moved, but a read up to its end changed); -1: the same.
func firstDiff(old, cur []uint64) int {
	i := 0
	for i < len(old) && i < len(cur) && old[i] == cur[i] {
		i++
	}
	if i == len(old) && i == len(cur) {
		return -1
	}
	return i
}
