package process

import (
	"encoding/base64"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// state is what the shaped tables accumulate: the snapshot tables keyed by
// their primary keys, and the transactions waiting for their ending. Every
// update is a count, sum, min or max, so applying the same rows in any order
// gives the same state. Applying a row twice doesn't.
type state struct {
	sessions map[string]*Session
	prompts  map[string]*Prompt
	devices  map[[2]string]*PersonDevice
	pending  map[string]*Pending
	accounts map[string]*Account
	newestNS int64 // the newest row time seen (data time, for pending expiry)
	// bodies are an HTTP model call's recorded bodies (full interception only), held
	// until its complete row, which carries what they show (Request.DetailJSON).
	// Committed with the in-flight transactions (pending_bodies), so a restart
	// mid-call keeps what came before it.
	bodies map[string]*callBodies
	// eventCopies counts, per transaction, the identical client_event rows seen
	// (by the hash their key is made of): rows from observers that didn't number
	// a batch's events repeat byte for byte when a client sends an event twice,
	// and each copy needs a key of its own. Forgotten with the transaction.
	eventCopies map[string]map[string]int
}

// callBodies is one call's request and response bodies as recorded, and their encodings.
type callBodies struct {
	req, resp       []byte
	reqEnc, respEnc string
}

// maxCallBody bounds one recorded body held for detail (a Codex request resends the conversation).
const maxCallBody = 32 << 20

// modelPath is a model call's path: the only bodies whose detail is read.
func modelPath(method, path string) bool {
	p, _, _ := strings.Cut(path, "?")
	return method == "POST" && (strings.HasPrefix(p, "/v1/messages") && !strings.HasPrefix(p, "/v1/messages/count_tokens") ||
		p == "/backend-api/codex/responses")
}

// bodiesFor is the call's body record, once its request headers say it's a model call.
func (s *state) bodiesFor(id string) *callBodies {
	p := s.pending[id]
	if p == nil || p.WebSocket || !modelPath(p.Method, p.Path) {
		return nil
	}
	b := s.bodies[id]
	if b == nil {
		b = &callBodies{}
		s.bodies[id] = b
	}
	return b
}

// body records a body chunk of a model call, when the raw row carries its bytes.
func (s *state) body(e *events.Event, resp bool) {
	if e.BodyB64 == "" {
		return
	}
	b := s.bodiesFor(e.CorrelationID)
	if b == nil {
		return
	}
	raw, err := base64.StdEncoding.DecodeString(e.BodyB64)
	if err != nil {
		return
	}
	dst := &b.req
	if resp {
		dst = &b.resp
	}
	if len(*dst)+len(raw) <= maxCallBody {
		*dst = append(*dst, raw...)
	}
}

// detail is what a model call's recorded bodies show, as JSON ("" without them).
func (s *state) detail(id string) string {
	b := s.bodies[id]
	if b == nil || len(b.req)+len(b.resp) == 0 {
		return ""
	}
	if d := events.DetailFromBodies(b.req, b.reqEnc, b.resp, b.respEnc); d != nil {
		return marshal(d)
	}
	return ""
}

func newState() *state {
	return &state{
		sessions:    map[string]*Session{},
		prompts:     map[string]*Prompt{},
		devices:     map[[2]string]*PersonDevice{},
		pending:     map[string]*Pending{},
		accounts:    map[string]*Account{},
		bodies:      map[string]*callBodies{},
		eventCopies: map[string]map[string]int{},
	}
}

// batch is the fact rows one run produces.
type batch struct {
	requests []Request
	events   []Event
}

// apply folds one raw row into the state and appends any fact rows it completes.
func (s *state) apply(e *events.Event, line []byte, file string, out *batch) {
	if e.TimestampNS > s.newestNS {
		s.newestNS = e.TimestampNS
	}
	if a := e.Auth; a != nil && a.AccountID != "" && (a.Email != "" || a.Name != "") {
		s.account(a, e.TimestampNS)
	}
	if p := s.pending[e.CorrelationID]; p != nil && p.Done && describesRequest(e) {
		return // the transaction already has its row; this is a late part of it
	}
	switch e.Phase {
	case events.PhaseRequestHeaders:
		p := s.pendingFor(e, file)
		p.StartNS = e.TimestampNS
		p.Host, p.Method, p.Path = e.Host, e.Method, e.Path
		p.Synthetic = e.Synthetic
		p.IP, p.KeyHash, p.KeyHint = e.IP, e.KeyHash, e.KeyHint
		p.WebSocket = strings.EqualFold(e.Headers["upgrade"], "websocket")
		p.ClientJSON = marshal(canon.Pick(e.Headers, canon.PendingClientHeaders))
		if b := s.bodiesFor(e.CorrelationID); b != nil {
			b.reqEnc = e.Headers["content-encoding"]
		}
		if p.Error != "" { // its disconnect came first
			s.addRequest(canon.RequestFromPending(p), out)
			s.done(e.CorrelationID, p.EndNS)
		}
	case events.PhaseResponseHeaders:
		p := s.pendingFor(e, file)
		p.ResponseStartNS = e.TimestampNS
		if e.Status != 0 {
			p.Status = int64(e.Status)
		}
		p.RequestID = e.Headers["request-id"]
		p.ContentEncoding = e.Headers["content-encoding"]
		if b := s.bodiesFor(e.CorrelationID); b != nil {
			b.respEnc = p.ContentEncoding
		}
		p.ProviderJSON = marshal(events.ProviderHeaderValues(e.Headers))
	case events.PhaseRequestBodyChunk:
		s.pendingFor(e, file).RequestBytes += int64(e.Bytes)
		s.body(e, false)
	case events.PhaseResponseBodyChunk:
		s.pendingFor(e, file).ResponseBytes += int64(e.Bytes)
		s.body(e, true)
	case events.PhaseComplete:
		if e.Transport == events.TransportWebSocket { // a model turn; the connection stays open
			r := canon.RequestFrom(e, nil, file)
			r.CallID = e.CorrelationID + "#" + strconv.Itoa(e.WSTurn)
			s.addRequest(r, out)
			return
		}
		r := canon.RequestFrom(e, s.pending[e.CorrelationID], file)
		r.DetailJSON = s.detail(e.CorrelationID)
		s.addRequest(r, out)
		s.done(e.CorrelationID, e.TimestampNS)
	case events.PhaseWSConnection:
		s.addRequest(canon.RequestFrom(e, s.pending[e.CorrelationID], file), out)
		s.done(e.CorrelationID, e.TimestampNS)
	case events.PhaseDisconnect:
		// Only a transaction that never completed becomes a row here; a disconnect
		// after the complete row adds nothing. One that arrives before the
		// request's headers waits for them.
		p := s.pendingFor(e, file)
		p.EndNS, p.Error, p.EndFile = e.TimestampNS, e.Error, file
		if p.StartNS != 0 {
			s.addRequest(canon.RequestFromPending(p), out)
			s.done(e.CorrelationID, e.TimestampNS)
		}
	case events.PhaseClientEvent:
		if e.ClientEvent != nil {
			ev := canon.EventFrom(e, line, file)
			if ev.ClientEventID == "" {
				// A repeat of an identical row (an unnumbered batch's duplicate event)
				// is its own event: key it <key>-2, -3, ….
				copies := s.eventCopies[e.CorrelationID]
				if copies == nil {
					copies = map[string]int{}
					s.eventCopies[e.CorrelationID] = copies
				}
				copies[ev.EventKey]++
				if n := copies[ev.EventKey]; n > 1 {
					ev.EventKey += "-" + strconv.Itoa(n)
				}
			}
			s.addEvent(ev, out)
		}
	}
}

// describesRequest is true for the rows that make up a transaction's request
// row; analytics events a request carried are rows of their own.
func describesRequest(e *events.Event) bool {
	switch e.Phase {
	case events.PhaseClientEvent:
		return false
	case events.PhaseComplete:
		return e.Transport != events.TransportWebSocket
	}
	return true
}

// done replaces a transaction's pending entry with a marker that it has ended.
func (s *state) done(id string, at int64) {
	s.pending[id] = &Pending{CorrelationID: id, SeenNS: at, Done: true}
	delete(s.bodies, id)
}

func (s *state) pendingFor(e *events.Event, file string) *Pending {
	p := s.pending[e.CorrelationID]
	if p == nil {
		p = &Pending{CorrelationID: e.CorrelationID, SourceFile: file}
		s.pending[e.CorrelationID] = p
	}
	p.SeenNS = minNZ(p.SeenNS, e.TimestampNS)
	return p
}

func (s *state) addRequest(r Request, out *batch) {
	out.requests = append(out.requests, r)
	if !isModelCall(&r) {
		return
	}
	at := r.StartNS
	if at == 0 {
		at = r.EndNS
	}
	ss := s.session(r.SessionID)
	ss.Product, ss.Client = minStr(ss.Product, r.Product), minStr(ss.Client, r.Client)
	ss.ClientVersion = newerVersion(ss.ClientVersion, r.ClientVersion)
	if r.Product == productCodex {
		ss.addLineage(lineageOf(r.ExtJSON))
	}
	ss.FirstNS, ss.LastNS = minNZ(ss.FirstNS, at), max(ss.LastNS, at)
	ss.PersonID, ss.OrgID, ss.DeviceID = minStr(ss.PersonID, r.PersonID), minStr(ss.OrgID, r.OrgID), minStr(ss.DeviceID, r.DeviceID)
	s.device(r.PersonID, r.DeviceID, at)
	if r.HiddenKind != "" { // prewarm and title calls: counted apart, never in calls or tokens
		ss.HiddenCalls++
		return
	}
	ss.Calls++
	ss.FirstCallNS, ss.LastCallNS = minNZ(ss.FirstCallNS, at), max(ss.LastCallNS, at)
	if r.InputTokens != 0 || r.OutputTokens != 0 {
		ss.CallsWithUsage++
	}
	ss.InputTokens += r.InputTokens
	ss.OutputTokens += r.OutputTokens
	ss.CacheReadTokens += r.CacheReadTokens
	ss.CacheWriteTokens += r.CacheWriteTokens
	if r.PromptID != "" {
		p := s.prompt(r.PromptID)
		p.SessionID = minStr(p.SessionID, r.SessionID)
		p.FirstNS, p.LastNS = minNZ(p.FirstNS, at), max(p.LastNS, at)
		p.Calls++
	}
}

func (s *state) addEvent(ev Event, out *batch) {
	out.events = append(out.events, ev)
	if (ev.Source != "claude_code" && ev.Source != "codex") || ev.SessionID == "" {
		s.device(ev.PersonID, ev.DeviceID, ev.NS)
		return
	}
	ss := s.session(ev.SessionID)
	ss.Events++
	ss.FirstEventNS, ss.LastEventNS = minNZ(ss.FirstEventNS, ev.NS), max(ss.LastEventNS, ev.NS)
	ss.FirstNS, ss.LastNS = minNZ(ss.FirstNS, ev.NS), max(ss.LastNS, ev.NS)
	ss.PersonID, ss.OrgID, ss.DeviceID = minStr(ss.PersonID, ev.PersonID), minStr(ss.OrgID, ev.OrgID), minStr(ss.DeviceID, ev.DeviceID)
	if ev.PromptID != "" {
		p := s.prompt(ev.PromptID)
		p.SessionID = minStr(p.SessionID, ev.SessionID)
		p.PromptIndex = minNZ(p.PromptIndex, ev.PromptIndex)
		p.FirstNS, p.LastNS = minNZ(p.FirstNS, ev.NS), max(p.LastNS, ev.NS)
		p.Events++
	}
	s.device(ev.PersonID, ev.DeviceID, ev.NS)
}

func (s *state) session(id string) *Session {
	ss := s.sessions[id]
	if ss == nil {
		ss = &Session{SessionID: id}
		s.sessions[id] = ss
	}
	return ss
}

func (s *state) prompt(id string) *Prompt {
	p := s.prompts[id]
	if p == nil {
		p = &Prompt{PromptID: id}
		s.prompts[id] = p
	}
	return p
}

func (s *state) device(person, device string, at int64) {
	if person == "" || device == "" {
		return
	}
	k := [2]string{person, device}
	d := s.devices[k]
	if d == nil {
		d = &PersonDevice{PersonID: person, DeviceID: device}
		s.devices[k] = d
	}
	d.FirstNS, d.LastNS = minNZ(d.FirstNS, at), max(d.LastNS, at)
}

// account records who an account is. Each field is the latest sighting's that
// says it (the greatest value at the same time), so the result is the same in
// any arrival order: a sighting without an email never hides an older one's.
func (s *state) account(a *events.AuthIdentity, at int64) {
	cur := s.accounts[a.AccountID]
	if cur == nil {
		cur = &Account{AccountID: a.AccountID}
		s.accounts[a.AccountID] = cur
	}
	cur.SeenNS = max(cur.SeenNS, at)
	cur.Email, cur.EmailNS = newer(cur.Email, cur.fieldNS(cur.EmailNS, cur.Email), a.Email, at)
	cur.Name, cur.NameNS = newer(cur.Name, cur.fieldNS(cur.NameNS, cur.Name), a.Name, at)
	cur.Org, cur.OrgNS = newer(cur.Org, cur.fieldNS(cur.OrgNS, cur.Org), a.Org, at)
}

// fieldNS is when a field was seen; a snapshot written before the per-field times
// has only the account's latest sighting.
func (a *Account) fieldNS(ns int64, v string) int64 {
	if ns == 0 && v != "" {
		return a.SeenNS
	}
	return ns
}

// newer is (v, at) if it's set and later than (cur, curNS), or as late and greater.
func newer(cur string, curNS int64, v string, at int64) (string, int64) {
	if v != "" && (cur == "" || at > curNS || at == curNS && v > cur) {
		return v, at
	}
	return cur, curNS
}

// loadAccounts restores the accounts snapshot.
func (s *state) loadAccounts(rows []Account) {
	for i := range rows {
		s.accounts[rows[i].AccountID] = &rows[i]
	}
}

// snapshot returns the snapshot tables, sorted by primary key. Session prompt
// counts and the people table are derived here, from the other tables.
func (s *state) snapshot() ([]Session, []Prompt, []PersonDevice, []Person, []Pending) {
	prompts := sortedValues(s.prompts, func(p *Prompt) string { return p.PromptID })
	perSession := map[string]int64{}
	for _, p := range prompts {
		if p.SessionID != "" {
			perSession[p.SessionID]++
		}
	}
	sessions := sortedValues(s.sessions, func(x *Session) string { return x.SessionID })
	people := map[string]*Person{}
	person := func(id string) *Person {
		p := people[id]
		if p == nil {
			p = &Person{PersonID: id}
			people[id] = p
		}
		return p
	}
	for i := range sessions {
		x := &sessions[i]
		x.Prompts = perSession[x.SessionID]
		if x.PersonID == "" {
			continue
		}
		p := person(x.PersonID)
		p.Sessions++
		p.Calls += x.Calls
		p.Events += x.Events
		p.FirstNS, p.LastNS = minNZ(p.FirstNS, x.FirstNS), max(p.LastNS, x.LastNS)
	}
	devices := make([]PersonDevice, 0, len(s.devices))
	for _, d := range s.devices {
		devices = append(devices, *d)
		p := person(d.PersonID)
		p.Devices++
		p.FirstNS, p.LastNS = minNZ(p.FirstNS, d.FirstNS), max(p.LastNS, d.LastNS)
	}
	sort.Slice(devices, func(i, j int) bool {
		a, b := devices[i], devices[j]
		return a.PersonID < b.PersonID || a.PersonID == b.PersonID && a.DeviceID < b.DeviceID
	})
	return sessions, prompts, devices,
		sortedValues(people, func(p *Person) string { return p.PersonID }),
		inFlight(s.pending)
}

// inFlight is the pending transactions still waiting for their ending, and the
// markers of those written without one, by key.
func inFlight(m map[string]*Pending) []Pending {
	var out []Pending
	for _, p := range m {
		if !p.Done || p.Expired {
			out = append(out, *p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CorrelationID < out[j].CorrelationID })
	return out
}

// --- small helpers -------------------------------------------------------------------

// marshal is JSON with sorted keys (encoding/json sorts maps), so output is stable.
func marshal(v any) string {
	if m, ok := v.(map[string]string); ok && len(m) == 0 {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return ""
}

func num(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return 0
}

func minNZ(a, b int64) int64 {
	if a == 0 || (b != 0 && b < a) {
		return b
	}
	return a
}

func minStr(a, b string) string {
	if a == "" || (b != "" && b < a) {
		return b
	}
	return a
}

func firstNZ(a, b int64) int64 {
	if a != 0 {
		return a
	}
	return b
}

func firstStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func sortedValues[T any](m map[string]*T, key func(*T) string) []T {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]T, 0, len(m))
	for _, k := range keys {
		out = append(out, *m[k])
	}
	return out
}
