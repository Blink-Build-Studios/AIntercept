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

// Memo is a render's scope and rows, and what the next render needs to tell
// which rows its changes can reach. It's kept with the warm state
// (render.Baseline) and consumed by the next RenderNext: never use one twice.
type Memo struct {
	used     bool
	derived  int // the request rows this render derived
	scope    *scope
	reqs     map[string]*memoReq        // drawer request id → its row (or a reported id's none)
	byTok    map[uint64]map[string]bool // token → the request ids that recorded it
	events   map[string]Event           // drawer event id → its row
	counts   map[string]StepCount       // session \x00 prompt → its step count
	evIn     map[string]*filed          // fact_events id → what it's filed under
	callIn   map[string]*filed          // fact_requests id → what it's filed under
	missed   map[string]string          // request_missed id → the request id it names
	reported map[string]int             // reported:… id → how many missed rows name it
	limits   map[string]int64           // session → the latest sandbox limit a row read
	tpls     map[string]string          // template id → template
	written  map[string]bool            // the templates the key's commits wrote, since the memo was made whole
	delta    *Delta                     // this render's changes to the rows (nil: it rendered them whole)
}

// Delta is what a render changed of the rows its prev held.
type Delta struct {
	Requests     []Request
	GoneRequests []string
	Events       []Event
	GoneEvents   []string
	Counts       []StepCount
	GoneCounts   []StepCount
}

type memoReq struct {
	yield bool
	call  bool // a call's row (not a reported id's)
	req   Request
	deps  rowDeps
}

// rowDeps is what a row's derivation read, as tokens.
type rowDeps struct {
	toks   []uint64
	limits []sandboxRead
}

type sandboxRead struct {
	sess  string
	limit int64
}

// filed is an input as the indexes hold it: its tokens, its time when it's a
// bash event (the sandbox's), and for an event its drawer id and the hash of
// its rendered event row.
type filed struct {
	toks    []uint64
	bash    bool
	sess    string
	ts      int64
	id      string // an event's drawer id; a call's drawer id
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
	kSandbox // (session, 16 s bucket of a row's sandbox limit)
	kOrder   // (session, a step's place among the session's steps)
	kSteps   // (session \x00 prompt, a place in that prompt's steps)
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

// useSandbox records a read of sess's sandbox setting up to limit: a bash run
// at or before limit can change it (filed by the buckets from its time on).
func (s *scope) useSandbox(sess string, limit int64) {
	if s.rec != nil {
		s.rec.toks = append(s.rec.toks, tok(kSandbox, sess, itoa(bucket(limit, sandboxBucket))))
		s.rec.limits = append(s.rec.limits, sandboxRead{sess, limit})
	}
}

// useSteps records a read of sess's steps of a prompt (promptKey) up to place
// hi (len: to the end): a change at any place up to it can change the row.
func (s *scope) useSteps(sess, prompt string, hi int) {
	if s.rec != nil {
		s.rec.toks = append(s.rec.toks, tok(kSteps, sess+"\x00"+prompt, itoa(int64(hi))))
	}
}

// useOrder records the place (-1: none) a row found a step at among all its
// session's steps: a step added or removed at or before it moves it.
func (s *scope) useOrder(sess string, at int) {
	if s.rec != nil {
		s.rec.toks = append(s.rec.toks, tok(kOrder, sess, itoa(int64(at))))
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

const sandboxBucket = 16 * ns

// dirty is what a run's changes reach.
type dirty struct {
	m      *Memo
	own    map[string]bool // drawer request ids whose own call changed
	toks   map[uint64]bool // tokens filed
	events map[string]bool // drawer event ids to render again
	counts map[string]bool // session \x00 prompt whose step count can have changed
}

func (d *dirty) file(f *filed) {
	if f == nil {
		return
	}
	for _, t := range f.toks {
		d.toks[t] = true
	}
	if f.bash { // the rows whose sandbox limit is at or after it
		for b, last := bucket(f.ts, sandboxBucket), bucket(d.m.limits[f.sess], sandboxBucket); b <= last; b++ {
			d.toks[tok(kSandbox, f.sess, itoa(b))] = true
		}
	}
}

// apply applies a run's changed rows to the memo's scope, and files what they
// reach. It reads only what changed.
func (m *Memo) apply(in Inputs, changed Changed) *dirty {
	s := m.scope
	s.ch, s.part = newChanges(), in.Part
	d := &dirty{m: m, own: map[string]bool{}, toks: map[uint64]bool{}, events: map[string]bool{}, counts: map[string]bool{}}
	for _, id := range sortedIDs(changed[state.TableFactRequests]) {
		var c *canon.Request
		if v, ok := in.Rows[state.TableFactRequests][id].(canon.Request); ok {
			c = &v
		}
		if old := m.callIn[id]; old != nil {
			d.own[old.id] = true
			delete(m.callIn, id)
		}
		s.putCall(id, c)
		if r := s.calls[id]; r != nil {
			m.callIn[id] = &filed{id: r.id}
			d.own[r.id] = true
		}
	}
	for _, id := range sortedIDs(changed[state.TableFactEvents]) {
		var e *canon.Event
		if v, ok := in.Rows[state.TableFactEvents][id].(canon.Event); ok {
			e = &v
		}
		if old := m.evIn[id]; old != nil {
			d.file(old)
			d.events[old.id] = true
			delete(m.evIn, id)
		}
		s.putEvent(id, e)
		if e != nil {
			f := fileEvent(e, s.evs[id])
			if x := s.evs[id]; x != nil {
				r, ok := s.rows[x.id]
				f.id, f.rowHash = x.id, eventRowHash(r, ok)
				d.events[x.id] = true
			}
			m.evIn[id] = f
			d.file(f)
		}
	}
	for e, was := range s.ch.moved { // renumbered: another drawer id, another place in the lists
		d.events[was], d.events[e.id] = true, true
		if f := m.evIn[e.rowKey]; f != nil {
			f.id = e.id
			d.file(f)
		}
	}
	for _, r := range in.EventRowsChanged { // the session's rendered event rows that changed
		s.rows[r.EventID] = r
		for _, e := range s.evByID[r.EventID] {
			if f := m.evIn[e.rowKey]; f != nil {
				if h := eventRowHash(r, true); h != f.rowHash {
					f.rowHash = h
					d.file(f)
				}
			}
		}
	}
	for _, id := range in.ExternalsChanged {
		switch {
		case strings.HasPrefix(id, "wire:"):
			c := strings.TrimPrefix(id, "wire:")
			d.own[strings.ReplaceAll(c, "#", "~")] = true
		case strings.HasPrefix(id, "parts:"):
			d.toks[tok(kBatch, strings.TrimPrefix(id, "parts:"), "")] = true
		}
	}
	for id := range changed[state.TableRequestMissed] {
		if was, ok := m.missed[id]; ok {
			m.unreport(was, d)
			delete(m.missed, id)
		}
		if v, ok := in.Rows[state.TableRequestMissed][id].(state.MissedRow); ok {
			m.missed[id] = v.Row.RequestID
			m.report(v.Row.RequestID, d)
		}
	}
	for key, sp := range s.ch.order {
		for i := sp.k; i < sp.n; i++ {
			d.toks[tok(kOrder, key, itoa(int64(i)))] = true
		}
		d.toks[tok(kOrder, key, "-1")] = true
	}
	for key, sp := range s.ch.lists {
		for i := sp.k; i <= sp.n; i++ {
			d.toks[tok(kSteps, key, itoa(int64(i)))] = true
		}
		d.counts[key] = true
	}
	for sid, was := range s.ch.exists {
		if x := s.sessions[sid]; x != nil && x.exists != was {
			d.toks[tok(kExists, sid, "")] = true
		}
	}
	for _, c := range s.ch.chain {
		d.toks[tok(kChainK, "", c)] = true
	}
	for _, m := range s.ch.mids {
		d.toks[tok(kChainM, "", m)] = true
	}
	for _, p := range s.ch.prevs {
		d.toks[tok(kChainV, "", p)] = true
	}
	s.ch = nil
	return d
}

// report and unreport keep the reported ids the missed rows name.
func (m *Memo) report(id string, d *dirty) {
	if !strings.HasPrefix(id, "reported:") {
		return
	}
	if m.reported[id]++; m.reported[id] == 1 {
		d.own[id] = true
	}
}

func (m *Memo) unreport(id string, d *dirty) {
	if !strings.HasPrefix(id, "reported:") {
		return
	}
	if m.reported[id]--; m.reported[id] <= 0 {
		delete(m.reported, id)
		d.own[id] = true
	}
}

func sortedIDs(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// rows are the request ids a run's changes reach: their own, and those that
// recorded a token filed.
func (d *dirty) rows() []string {
	set := map[string]bool{}
	for id := range d.own {
		set[id] = true
	}
	for t := range d.toks {
		for id := range d.m.byTok[t] {
			set[id] = true
		}
	}
	return sortedIDs(set)
}

// keep records a row's lookups in the memo's index (taking its old ones out).
func (m *Memo) keep(id string, r *memoReq) {
	if old := m.reqs[id]; old != nil {
		for _, t := range old.deps.toks {
			if ids := m.byTok[t]; ids != nil {
				delete(ids, id)
				if len(ids) == 0 {
					delete(m.byTok, t)
				}
			}
		}
	}
	if r == nil {
		delete(m.reqs, id)
		return
	}
	m.reqs[id] = r
	for _, t := range r.deps.toks {
		if m.byTok[t] == nil {
			m.byTok[t] = map[string]bool{}
		}
		m.byTok[t][id] = true
	}
	for _, l := range r.deps.limits {
		if l.limit > m.limits[l.sess] {
			m.limits[l.sess] = l.limit
		}
	}
}

func newMemo() *Memo {
	return &Memo{reqs: map[string]*memoReq{}, byTok: map[uint64]map[string]bool{}, events: map[string]Event{},
		counts: map[string]StepCount{}, evIn: map[string]*filed{}, callIn: map[string]*filed{}, missed: map[string]string{},
		reported: map[string]int{}, limits: map[string]int64{}, tpls: map[string]string{}, written: map[string]bool{}}
}

// Derived is how many request rows the render derived; the rest were its prev's.
func (m *Memo) Derived() int {
	if m == nil {
		return 0
	}
	return m.derived
}

// VerifyIncremental makes every incremental render also render whole and
// panic where they differ (the tests and the gate set it;
// AINTERCEPT_DRAWER_VERIFY=1 does too).
var VerifyIncremental = os.Getenv("AINTERCEPT_DRAWER_VERIFY") == "1"

// verify panics where the memo's rows aren't a whole render's.
func verify(in Inputs, m *Memo) {
	full := in
	full.EventRowsChanged, full.ExternalsChanged = nil, nil
	w := RenderNext(full, nil, nil)
	reqs, evs, counts := m.All()
	wr, we, wc := w.All()
	if len(wr) != len(reqs) || len(we) != len(evs) || len(wc) != len(counts) {
		panic(fmt.Sprintf("drawer: key %s: the incremental render holds %d requests, %d events, %d counts; the whole one %d, %d, %d",
			in.Key, len(reqs), len(evs), len(counts), len(wr), len(we), len(wc)))
	}
	for i := range wr {
		a, b := wr[i], reqs[i]
		if a.ID != b.ID || a.DataVals != b.DataVals || a.DetailsVals != b.DetailsVals || w.tpls[a.DataTpl] != m.tpls[b.DataTpl] ||
			w.tpls[a.DetailsTpl] != m.tpls[b.DetailsTpl] || a.StepSession != b.StepSession || a.StepPrompt != b.StepPrompt {
			panic(fmt.Sprintf("drawer: key %s: the incremental render's request %q isn't the whole render's", in.Key, a.ID))
		}
	}
	for i := range we {
		if we[i] != evs[i] {
			panic(fmt.Sprintf("drawer: key %s: the incremental render's event %q isn't the whole render's", in.Key, we[i].ID))
		}
	}
	for i := range wc {
		if wc[i] != counts[i] {
			panic(fmt.Sprintf("drawer: key %s: the incremental render's step count %v isn't the whole render's %v", in.Key, counts[i], wc[i]))
		}
	}
}

// All is the memo's rows, each kind by id.
func (m *Memo) All() ([]Request, []Event, []StepCount) {
	var reqs []Request
	for _, r := range m.reqs {
		if r.yield {
			reqs = append(reqs, r.req)
		}
	}
	slices.SortFunc(reqs, func(a, b Request) int { return strings.Compare(a.ID, b.ID) })
	evs := make([]Event, 0, len(m.events))
	for _, e := range m.events {
		evs = append(evs, e)
	}
	slices.SortFunc(evs, func(a, b Event) int { return strings.Compare(a.ID, b.ID) })
	counts := make([]StepCount, 0, len(m.counts))
	for _, c := range m.counts {
		counts = append(counts, c)
	}
	slices.SortFunc(counts, func(a, b StepCount) int {
		if c := strings.Compare(a.Session, b.Session); c != 0 {
			return c
		}
		return strings.Compare(a.Prompt, b.Prompt)
	})
	return reqs, evs, counts
}

// Between is what changes from a's rows to b's, compared whole: for a render
// diffed from a baseline it didn't render after (a's memo must be unused:
// one consumed holds its successor's rows).
func Between(a, b *Memo) Delta {
	if a.used {
		panic("drawer: rows compared with a memo another render consumed")
	}
	var d Delta
	ar, ae, ac := a.All()
	br, be, bc := b.All()
	oldReq := map[string]Request{}
	for _, r := range ar {
		oldReq[r.ID] = r
	}
	for _, r := range br {
		o, ok := oldReq[r.ID]
		if !ok || o.DataVals != r.DataVals || o.DetailsVals != r.DetailsVals || a.tpls[o.DataTpl] != b.tpls[r.DataTpl] ||
			a.tpls[o.DetailsTpl] != b.tpls[r.DetailsTpl] || o.StepSession != r.StepSession || o.StepPrompt != r.StepPrompt {
			d.Requests = append(d.Requests, r)
		}
		delete(oldReq, r.ID)
	}
	for id := range oldReq {
		d.GoneRequests = append(d.GoneRequests, id)
	}
	oldEv := map[string]Event{}
	for _, e := range ae {
		oldEv[e.ID] = e
	}
	for _, e := range be {
		if o, ok := oldEv[e.ID]; !ok || o != e {
			d.Events = append(d.Events, e)
		}
		delete(oldEv, e.ID)
	}
	for id := range oldEv {
		d.GoneEvents = append(d.GoneEvents, id)
	}
	oldC := map[[2]string]StepCount{}
	for _, c := range ac {
		oldC[[2]string{c.Session, c.Prompt}] = c
	}
	for _, c := range bc {
		if o, ok := oldC[[2]string{c.Session, c.Prompt}]; !ok || o != c {
			d.Counts = append(d.Counts, c)
		}
		delete(oldC, [2]string{c.Session, c.Prompt})
	}
	for _, c := range oldC {
		d.GoneCounts = append(d.GoneCounts, c)
	}
	d.sort()
	return d
}
