package process

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
)

// How long a transaction may wait for its ending before it's written without
// one (data time: the newest row seen). WebSocket connections live far longer.
const (
	pendingTTL   = 6 * time.Hour
	pendingTTLWS = 7 * 24 * time.Hour
)

// Request header values a pending transaction keeps for its request row.
var pendingClientHeaders = []string{
	"x-claude-code-session-id", "x-claude-code-prompt-id", "x-claude-code-request-class",
	"x-client-request-id", "x-claude-code-agent-id", "x-claude-code-agent-type", "x-organization-uuid", "user-agent",
	"originator", "version",
}

// state is what the shaped tables accumulate: the snapshot tables keyed by
// their primary keys, and the transactions waiting for their ending. Every
// update is a count, sum, min or max, so applying the same rows in any order
// gives the same state. Applying a row twice doesn't: a run that fails to
// commit leaves the processor dirty, and the next poll reloads (Poll).
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
		p.ClientJSON = marshal(pick(e.Headers, pendingClientHeaders))
		if b := s.bodiesFor(e.CorrelationID); b != nil {
			b.reqEnc = e.Headers["content-encoding"]
		}
		if p.Error != "" { // its disconnect came first
			s.addRequest(requestFromPending(p), out)
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
			r := requestFrom(e, nil, file)
			r.CallID = e.CorrelationID + "#" + strconv.Itoa(e.WSTurn)
			s.addRequest(r, out)
			return
		}
		r := requestFrom(e, s.pending[e.CorrelationID], file)
		r.DetailJSON = s.detail(e.CorrelationID)
		s.addRequest(r, out)
		s.done(e.CorrelationID, e.TimestampNS)
	case events.PhaseWSConnection:
		s.addRequest(requestFrom(e, s.pending[e.CorrelationID], file), out)
		s.done(e.CorrelationID, e.TimestampNS)
	case events.PhaseDisconnect:
		// Only a transaction that never completed becomes a row here; a disconnect
		// after the complete row adds nothing. One that arrives before the
		// request's headers waits for them.
		p := s.pendingFor(e, file)
		p.EndNS, p.Error, p.EndFile = e.TimestampNS, e.Error, file
		if p.StartNS != 0 {
			s.addRequest(requestFromPending(p), out)
			s.done(e.CorrelationID, e.TimestampNS)
		}
	case events.PhaseClientEvent:
		if e.ClientEvent != nil {
			ev := eventFrom(e, line, file)
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

// markDone restores an end marker at open, unless a newer state already has one.
func (s *state) markDone(id string, at int64) {
	if p := s.pending[id]; p == nil || p.Done {
		if s.newestNS-at <= int64(pendingTTL) {
			s.done(id, at)
		}
	}
}

// dropOldMarkers forgets restored markers past their window, as expire would have.
func (s *state) dropOldMarkers() {
	for id, p := range s.pending {
		if p.Done && s.newestNS-p.SeenNS > int64(pendingTTL) {
			delete(s.pending, id)
			delete(s.eventCopies, id)
		}
	}
}

// expire writes the transactions that waited too long for their ending (in
// data time), and forgets end markers once late rows can no longer arrive.
func (s *state) expire(out *batch) {
	for id, p := range s.pending {
		ttl := pendingTTL
		if p.WebSocket {
			ttl = pendingTTLWS
		}
		if s.newestNS-p.SeenNS <= int64(ttl) {
			continue
		}
		if p.Done {
			delete(s.pending, id)
			delete(s.eventCopies, id)
			continue
		}
		s.addRequest(requestFromPending(p), out)
		s.done(id, s.newestNS)
	}
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

// account records who an account is; the latest sighting wins (order independent).
func (s *state) account(a *events.AuthIdentity, at int64) {
	cur := s.accounts[a.AccountID]
	if cur != nil && cur.SeenNS > at {
		return
	}
	next := &Account{AccountID: a.AccountID, Email: a.Email, Name: a.Name, Org: a.Org, SeenNS: at}
	if cur != nil { // keep what this sighting doesn't say (bootstrap names no one)
		next.Email, next.Name, next.Org = firstStr(a.Email, cur.Email), firstStr(a.Name, cur.Name), firstStr(a.Org, cur.Org)
	}
	s.accounts[a.AccountID] = next
}

// accountRows is the accounts snapshot, by account id.
func (s *state) accountRows() []Account {
	return sortedValues(s.accounts, func(a *Account) string { return a.AccountID })
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

// inFlight is the pending transactions still waiting for their ending, by key.
func inFlight(m map[string]*Pending) []Pending {
	var out []Pending
	for _, p := range m {
		if !p.Done {
			out = append(out, *p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CorrelationID < out[j].CorrelationID })
	return out
}

// bodyRows is the in-flight calls' recorded bodies, by key, to commit.
func (s *state) bodyRows() []PendingBodies {
	out := make([]PendingBodies, 0, len(s.bodies))
	for id, b := range s.bodies {
		if p := s.pending[id]; p == nil || p.Done || len(b.req)+len(b.resp) == 0 && b.reqEnc == "" && b.respEnc == "" {
			continue
		}
		out = append(out, PendingBodies{CorrelationID: id, Request: b.req, Response: b.resp,
			RequestEncoding: b.reqEnc, ResponseEncoding: b.respEnc})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CorrelationID < out[j].CorrelationID })
	return out
}

// loadBodies restores the in-flight calls' recorded bodies.
func (s *state) loadBodies(rows []PendingBodies) {
	for _, r := range rows {
		s.bodies[r.CorrelationID] = &callBodies{req: r.Request, resp: r.Response, reqEnc: r.RequestEncoding, respEnc: r.ResponseEncoding}
	}
}

// load restores the state from committed snapshot tables.
func (s *state) load(sessions []Session, prompts []Prompt, devices []PersonDevice, pending []Pending, newest int64) {
	for i := range sessions {
		s.sessions[sessions[i].SessionID] = &sessions[i]
	}
	for i := range prompts {
		s.prompts[prompts[i].PromptID] = &prompts[i]
	}
	for i := range devices {
		s.devices[[2]string{devices[i].PersonID, devices[i].DeviceID}] = &devices[i]
	}
	for i := range pending {
		s.pending[pending[i].CorrelationID] = &pending[i]
	}
	s.newestNS = newest
}

// requestFrom builds a request row from a transaction's ending row, filling
// what the ending doesn't carry (rows from older observers) from its headers.
func requestFrom(e *events.Event, p *Pending, file string) Request {
	r := Request{CallID: e.CorrelationID}
	if p != nil {
		r = requestFromPending(p)
	}
	r.CorrelationID, r.SourceFile, r.EndNS = e.CorrelationID, file, e.TimestampNS
	if r.FirstFile == "" {
		r.FirstFile = file
	}
	r.WSTurn, r.Transport = int64(e.WSTurn), e.Transport
	r.StartNS = firstNZ(e.RequestStartNS, r.StartNS)
	r.ResponseStartNS = firstNZ(e.ResponseStartNS, r.ResponseStartNS)
	r.RequestBytes = firstNZ(int64(e.RequestBytes), r.RequestBytes)
	r.ResponseBytes = firstNZ(int64(e.ResponseBytes), r.ResponseBytes)
	r.Host, r.Method, r.Path = firstStr(e.Host, r.Host), firstStr(e.Method, r.Method), firstStr(e.Path, r.Path)
	r.Status = firstNZ(int64(e.Status), r.Status)
	r.Synthetic = e.Synthetic || r.Synthetic
	r.IP, r.KeyHash, r.KeyHint = firstStr(e.IP, r.IP), firstStr(e.KeyHash, r.KeyHash), firstStr(e.KeyHint, r.KeyHint)
	r.Error = e.Error
	if len(e.Client) > 0 {
		setClient(&r, e.Client)
	}
	r.SessionID = firstStr(r.SessionID, e.SessionID)
	r.PersonID, r.DeviceID = e.AccountUUID, e.DeviceID
	if r.Product == productCodex {
		applyCodex(&r, e)
	}
	r.RequestID = firstStr(e.Provider["request-id"], r.RequestID)
	r.Model, r.StopReason, r.MessageID, r.PreviousMessageID = e.Model, e.StopReason, e.MessageID, e.PreviousMessageID
	r.ThreadType, r.Stream, r.ServiceTier = e.ThreadType, e.Stream, e.ServiceTier
	r.InputTokens, r.OutputTokens = int64(e.InputTokens), int64(e.OutputTokens)
	r.CacheReadTokens, r.CacheWriteTokens = int64(e.CacheReadTokens), int64(e.CacheWriteTokens)
	r.ReasoningTokens = int64(e.ReasoningTokens)
	r.Effort = e.Effort
	// Provider values from the response headers, overridden by the complete row's own.
	provider := map[string]string{}
	if p != nil && p.ProviderJSON != "" {
		json.Unmarshal([]byte(p.ProviderJSON), &provider)
	}
	for k, v := range e.Provider {
		provider[k] = v
	}
	r.ProviderJSON = marshal(provider)
	if len(e.ToolUses) > 0 {
		r.ToolUsesJSON = marshal(e.ToolUses)
	}
	return r
}

func requestFromPending(p *Pending) Request {
	r := Request{
		CallID: p.CorrelationID, CorrelationID: p.CorrelationID, SourceFile: p.SourceFile, FirstFile: p.SourceFile,
		ContentEncoding: p.ContentEncoding, ProviderJSON: p.ProviderJSON,
		StartNS: p.StartNS, ResponseStartNS: p.ResponseStartNS,
		Host: p.Host, Method: p.Method, Path: p.Path, Status: p.Status, Synthetic: p.Synthetic,
		IP: p.IP, KeyHash: p.KeyHash, KeyHint: p.KeyHint,
		RequestBytes: p.RequestBytes, ResponseBytes: p.ResponseBytes, RequestID: p.RequestID,
		EndNS: p.EndNS, Error: p.Error,
	}
	if p.EndFile != "" {
		r.SourceFile = p.EndFile
	}
	if p.WebSocket {
		r.Transport = events.TransportWebSocket
	}
	if p.ClientJSON != "" {
		var c map[string]string
		if json.Unmarshal([]byte(p.ClientJSON), &c) == nil {
			setClient(&r, c)
		}
	}
	return r
}

func setClient(r *Request, c map[string]string) {
	r.SessionID = c["x-claude-code-session-id"]
	r.PromptID = c["x-claude-code-prompt-id"]
	r.RequestClass = c["x-claude-code-request-class"]
	r.ClientRequestID = c["x-client-request-id"]
	r.AgentID, r.AgentType = c["x-claude-code-agent-id"], c["x-claude-code-agent-type"]
	r.OrgID = c["x-organization-uuid"]
	r.UserAgent = c["user-agent"]
	r.ClientJSON = marshal(c)
	r.Product = productOf(r, c["originator"])
	r.Client, r.ClientVersion = clientOf(r, c["originator"])
}

func eventFrom(e *events.Event, line []byte, file string) Event {
	ce := e.ClientEvent
	md, _ := ce.Data["additional_metadata"].(map[string]any)
	key := e.CorrelationID + "/" + ce.ID
	if ce.ID == "" {
		h := sha256.Sum256(line)
		key = e.CorrelationID + "/h" + hex.EncodeToString(h[:8])
	}
	ev := Event{
		EventKey: key, CorrelationID: e.CorrelationID, SourceFile: file,
		RowNS: e.TimestampNS, NS: e.TimestampNS, Seq: int64(e.Seq),
		Source: ce.Source, Name: ce.Name, Type: ce.Type, ClientEventID: ce.ID,
		SessionID: ce.SessionID, PersonID: ce.AccountUUID, OrgID: ce.OrganizationUUID, DeviceID: ce.DeviceID,
		AgentID: ce.AgentID, AgentType: ce.AgentType, Model: ce.Model,
		PromptID: str(md["cc_prompt_id"]), RequestID: str(md["requestId"]), ClientRequestID: str(md["clientRequestId"]),
		ToolUseID:      firstStr(str(md["toolUseID"]), str(md["tool_use_id"])),
		PromptIndex:    num(md["prompt_index"]),
		PermissionMode: str(md["permissionMode"]),
		HeadSHA:        str(ce.Data["head_sha"]),
	}
	if t, err := time.Parse(time.RFC3339Nano, ce.Timestamp); err == nil {
		ev.NS = t.UnixNano()
	}
	if ce.Source == "codex" { // Codex sends no additional_metadata: its params are the event
		ev.PromptID = str(ce.Data["turn_id"])
		ev.MetadataJSON = marshal(ce.Data)
		ev.PersonID = firstStr(ev.PersonID, e.AccountUUID)
		return ev
	}
	// The metadata and env exactly as the client sent them: key order is the
	// client's (summaries list fields in it), which a decoded map loses.
	var raw struct {
		ClientEvent struct {
			Data struct {
				Metadata json.RawMessage `json:"additional_metadata"`
				Env      json.RawMessage `json:"env"`
			} `json:"data"`
		} `json:"client_event"`
	}
	if json.Unmarshal(line, &raw) == nil {
		d := raw.ClientEvent.Data
		if len(d.Metadata) > 0 && string(d.Metadata) != "null" {
			ev.MetadataJSON = string(d.Metadata)
		}
		if len(d.Env) > 0 && string(d.Env) != "null" {
			ev.EnvJSON = string(d.Env)
		}
	}
	return ev
}

// --- small helpers -------------------------------------------------------------------

func pick(h map[string]string, names []string) map[string]string {
	out := map[string]string{}
	for _, n := range names {
		if v := h[n]; v != "" {
			out[n] = v
		}
	}
	return out
}

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
