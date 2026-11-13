package records

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/membudget"
)

// The assembler's cases: every branch a row can take is labelled, counted in
// Cases, and has an atomic test (TestEveryCaseHasAnAtomicTest). Replaying real
// data reports which cases it hit; one without a test fails the run
// (Stats.CheckCoverage).
const (
	CasePendingHeaders           = "pending.headers"             // request headers, body to come
	CasePendingRequestChunk      = "pending.request_chunk"       // request body, more to come
	CasePendingResponseHeaders   = "pending.response_headers"    // response headers
	CasePendingResponseChunk     = "pending.response_chunk"      // response body
	CaseStartHeadersEOS          = "start.headers_eos"           // request headers end the request: call_start
	CaseStartBodyEOS             = "start.body_eos"              // request body ends: call_start
	CaseStartWSUpgrade           = "start.ws_upgrade"            // a WebSocket upgrade's headers (its request stream never ends): call_start
	CaseStartWSTurn              = "start.ws_turn"               // a WebSocket turn's response.create: call_start
	CaseStartResponse            = "start.response_headers"      // a response to a request whose end was never seen: call_start
	CaseStartUploading           = "start.uploading"             // a request still uploading at a flush (or past the sink's age): call_start
	CaseEndComplete              = "end.complete"                // complete after its headers: call_end
	CaseEndCompleteNoHeaders     = "end.complete_no_headers"     // complete with no headers seen: call_end from it alone
	CaseEndWSTurn                = "end.ws_turn"                 // a WebSocket turn ends: call_end
	CaseEndWSConnection          = "end.ws_connection"           // a WebSocket closes: its own row
	CaseEndDisconnect            = "end.disconnect"              // a disconnect before any complete: call_end with its error
	CaseEndDisconnectEarly       = "end.disconnect_early"        // a disconnect before its headers: held for them
	CaseEndDisconnectThenHeaders = "end.disconnect_then_headers" // the headers of an early disconnect: call_end
	CaseEndSplitKey              = "end.split_key"               // a call whose end names a session its start didn't: written to both keys
	CaseLateAfterDone            = "late.after_done"             // a row of a call that already ended: ignored
	CaseEventWithID              = "event.with_id"               // an analytics event with the client's id
	CaseEventNoID                = "event.no_id"                 // one without: keyed by a hash of its row
	CaseEventRepeat              = "event.repeat"                // an identical repeat of one without an id: -2, -3…
	CaseEventNoPayload           = "event.no_payload"            // a client_event row with no event: ignored
	CaseBatchSession             = "batch.session"               // an analytics request's call, released: its batch names a session
	CaseBatchNoSession           = "batch.no_session"            // released: its batch names none
	CaseAccountSighting          = "account.sighting"            // a row saying who an account is
	CaseVerdict                  = "verdict"                     // an Interceptor verdict: not a record
	CaseUnknownPhase             = "unknown.phase"               // a phase this code doesn't know: ignored, and fails a real-data run
)

// AllCases is every case label.
var AllCases = []string{
	CasePendingHeaders, CasePendingRequestChunk, CasePendingResponseHeaders, CasePendingResponseChunk,
	CaseStartHeadersEOS, CaseStartBodyEOS, CaseStartWSUpgrade, CaseStartWSTurn, CaseStartResponse, CaseStartUploading,
	CaseEndComplete, CaseEndCompleteNoHeaders, CaseEndWSTurn, CaseEndWSConnection,
	CaseEndDisconnect, CaseEndDisconnectEarly, CaseEndDisconnectThenHeaders, CaseEndSplitKey, CaseLateAfterDone,
	CaseEventWithID, CaseEventNoID, CaseEventRepeat, CaseEventNoPayload,
	CaseBatchSession, CaseBatchNoSession, CaseAccountSighting, CaseVerdict, CaseUnknownPhase,
}

// How long a call's state is kept, in data time (the newest row seen): a call
// still waiting for its ending, and an ended call's marker, which turns away
// its late rows. A call whose ending never comes has its call_start and no
// call_end; so does a single pass over the same records.
const (
	pendingTTL   = 6 * time.Hour
	pendingTTLWS = 7 * 24 * time.Hour
	doneTTL      = 10 * time.Minute
	sweepEvery   = 4096 // rows between sweeps
)

// maxCallBody bounds a recorded body held for a call's detail_json.
const maxCallBody = 32 << 20

// defaultDetailCap bounds what a call in flight holds for its detail object;
// past it, its rows are kept without their bodies.
const defaultDetailCap = 16 << 20

// call is one transaction's state while it's open, then its end marker.
type call struct {
	p        canon.Pending
	headers  map[string]string // the request headers (a Codex turn's identity at its start)
	done     bool
	startKey string // its call_start's routing key ("": none written)
	instance string // the observer its rows came from
	bodies   *bodies
	// Its analytics batch: the events seen, and the earliest that names a session.
	batchN    int64
	session   string
	sessionNS int64
}

type bodies struct {
	req, resp       []byte
	reqEnc, respEnc string
}

// Assembler turns one observer's rows, in the order it emitted them, into
// records. Rows of different transactions may interleave; a transaction's own
// rows come in order (one ext_proc stream). Not safe for concurrent use.
type Assembler struct {
	inc      string
	seq      int64
	calls    map[string]*call
	turns    map[string]string         // a WebSocket turn's call id -> its call_start's key
	copies   map[string]map[string]int // per transaction, identical unnumbered events seen
	held     *heldBatch                // an analytics batch, until its last event
	newestNS int64
	rows     int
	cases    map[string]int64
	accounts map[string]Account // the last sighting written per account

	// KeepDetail keeps each call's rows and writes them as its detail object when
	// it ends (TakeDetails): what the request drawer reads, without scanning the
	// raw log. DetailCap bounds what a call holds: past it, rows are kept
	// without their bodies.
	KeepDetail bool
	DetailCap  int
	// Budget is charged for the rows held for detail objects (the observer's
	// memory budget), so they're counted and visible like the stream buffers.
	Budget  *membudget.Budget
	lines   map[string]*detailLines
	details []Detail
	marked  map[string]map[string]bool // each call's keys marked so far (KeepDetail)
	marks   []KeyMark
}

// heldBatch is an analytics batch being read: its request's call_end (when
// its complete came first, as the observer emits it) and its events, held
// until the batch's last event, so each event can be ranked among the batch's
// Claude Code events and the call routed with the batch's earliest session.
type heldBatch struct {
	corr   string
	call   *Record
	c      *call
	events []Record
}

// Detail is one call's detail object: its rows, as the raw log has them, in order.
type Detail struct {
	CorrelationID string
	Lines         [][]byte
}

type detailLines struct {
	lines [][]byte
	bytes int
}

// DetailKey is where a call's detail object is written.
func DetailKey(prefix, correlationID string) string {
	return withSlash(prefix) + "calls/" + correlationID + ".ndjson.gz"
}

// FlushDetails completes the detail object of every call still in flight, with
// its rows so far: at the end of a replayed capture (canon), so the drawer's raw
// view shows what the raw log has for a call that never ended. The live
// observer doesn't call it.
func (a *Assembler) FlushDetails() []Detail {
	ids := make([]string, 0, len(a.lines))
	for id := range a.lines {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		a.writeDetail(id)
	}
	return a.TakeDetails()
}

// TakeDetails returns the detail objects completed since the last call.
func (a *Assembler) TakeDetails() []Detail {
	d := a.details
	a.details = nil
	return d
}

// keepLine adds a row to its call's detail, unless the call has ended (a late
// row isn't part of the object already written).
func (a *Assembler) keepLine(e *events.Event, line []byte) {
	if !a.KeepDetail || e.Phase == events.PhaseVerdict {
		return
	}
	if c := a.calls[e.CorrelationID]; c != nil && c.done && (a.held == nil || a.held.c != c) {
		return
	}
	d := a.lines[e.CorrelationID]
	if d == nil {
		d = &detailLines{}
		a.lines[e.CorrelationID] = d
	}
	limit := a.DetailCap
	if limit <= 0 {
		limit = defaultDetailCap
	}
	if d.bytes+len(line) > limit && e.BodyB64 != "" {
		if b, err := json.Marshal(e.WithoutContent()); err == nil {
			line = b
		}
	}
	d.lines = append(d.lines, line)
	d.bytes += len(line)
	a.charge(int64(len(line)))
}

func (a *Assembler) charge(n int64) {
	if a.Budget != nil && n != 0 {
		a.Budget.Add(n)
	}
}

// dropLines forgets a call's held rows and releases their budget.
func (a *Assembler) dropLines(id string) *detailLines {
	delete(a.marked, id)
	d := a.lines[id]
	if d != nil {
		delete(a.lines, id)
		a.charge(-int64(d.bytes))
	}
	return d
}

// writeDetail completes a call's detail object.
func (a *Assembler) writeDetail(id string) {
	if d := a.dropLines(id); d != nil {
		a.details = append(a.details, Detail{CorrelationID: id, Lines: d.lines})
	}
}

// NewAssembler starts an assembler whose record ids begin with incarnation.
func NewAssembler(incarnation string) *Assembler {
	return &Assembler{inc: incarnation, calls: map[string]*call{}, turns: map[string]string{},
		copies: map[string]map[string]int{}, cases: map[string]int64{}, accounts: map[string]Account{},
		lines: map[string]*detailLines{}}
}

// Cases is how often each case was hit.
func (a *Assembler) Cases() map[string]int64 {
	out := make(map[string]int64, len(a.cases))
	for k, v := range a.cases {
		out[k] = v
	}
	return out
}

func (a *Assembler) hit(c string) { a.cases[c]++ }

// record finishes a record: its id, routing key and bucket.
func (a *Assembler) record(r Record, instance string) Record {
	a.seq++
	r.Version, r.Instance = Version, instance
	r.RecordID = fmt.Sprintf("%s-%020d", a.inc, a.seq)
	if r.Kind == KindClientEvent {
		r.EventKey = r.RecordID
	}
	if r.RoutingKey == "" {
		r.RoutingKey = RoutingKey(&r)
	}
	r.Bucket = int32(BucketOf(r.RoutingKey, DefaultBucketBits))
	a.mark(&r)
	return r
}

// Apply folds one row in and returns the records it completes. line is the
// row as written to the raw log (an unnumbered event's key is a hash of it);
// file is the raw file it's in ("" live), kept as the call's provenance.
func (a *Assembler) Apply(e *events.Event, line []byte, file string) []Record {
	var out []Record
	if a.held != nil && (e.CorrelationID != a.held.corr || e.Phase != events.PhaseClientEvent) {
		out = append(out, a.release()...)
	}
	if e.TimestampNS > a.newestNS {
		a.newestNS = e.TimestampNS
	}
	if a.rows++; a.rows%sweepEvery == 0 {
		a.sweep()
	}
	if au := e.Auth; au != nil && au.AccountID != "" && (au.Email != "" || au.Name != "") {
		a.hit(CaseAccountSighting)
		acct := Account{AccountID: au.AccountID, Email: au.Email, Name: au.Name, Org: au.Org, NS: e.TimestampNS}
		// A batch's event rows repeat their request's sighting exactly (same
		// time): written once. Any other sighting is written, even with the same
		// values: which value is latest is decided by time, downstream.
		if last, ok := a.accounts[au.AccountID]; !ok || last != acct {
			a.accounts[au.AccountID] = acct
			out = append(out, a.record(Record{Kind: KindAccount, Account: &acct}, e.ObserverInstance))
		}
	}
	a.keepLine(e, line)
	if c := a.calls[e.CorrelationID]; c != nil && c.done && describesRequest(e) {
		a.hit(CaseLateAfterDone)
		return out
	}
	switch e.Phase {
	case events.PhaseRequestHeaders:
		c := a.callFor(e, file)
		early := c.p.Error != ""
		c.p.StartNS = e.TimestampNS
		c.p.Host, c.p.Method, c.p.Path = e.Host, e.Method, e.Path
		c.p.Synthetic = e.Synthetic
		c.p.IP, c.p.KeyHash, c.p.KeyHint = e.IP, e.KeyHash, e.KeyHint
		c.p.WebSocket = strings.EqualFold(e.Headers["upgrade"], "websocket")
		c.p.ClientJSON = canon.Marshal(canon.Pick(e.Headers, canon.PendingClientHeaders))
		c.headers = e.Headers
		if b := a.bodiesFor(c); b != nil {
			b.reqEnc = e.Headers["content-encoding"]
		}
		switch {
		case early: // its disconnect came first
			a.hit(CaseEndDisconnectThenHeaders)
			out = append(out, a.end(c, canon.RequestFromPending(&c.p), e)...)
		case e.EndOfStream:
			a.hit(CaseStartHeadersEOS)
			out = append(out, a.start(c, e))
		case c.p.WebSocket: // an upgrade's request stream stays open: it has started
			a.hit(CaseStartWSUpgrade)
			out = append(out, a.start(c, e))
		default:
			a.hit(CasePendingHeaders)
		}
	case events.PhaseResponseHeaders:
		c := a.callFor(e, file)
		c.p.ResponseStartNS = e.TimestampNS
		if e.Status != 0 {
			c.p.Status = int64(e.Status)
		}
		c.p.RequestID = e.Headers["request-id"]
		c.p.ContentEncoding = e.Headers["content-encoding"]
		if b := a.bodiesFor(c); b != nil {
			b.respEnc = c.p.ContentEncoding
		}
		c.p.ProviderJSON = canon.Marshal(events.ProviderHeaderValues(e.Headers))
		if c.p.StartNS != 0 && c.startKey == "" && !c.p.WebSocket {
			// The request was sent, though its end of stream was never seen.
			a.hit(CaseStartResponse)
			out = append(out, a.start(c, e))
		} else {
			a.hit(CasePendingResponseHeaders)
		}
	case events.PhaseRequestBodyChunk:
		c := a.callFor(e, file)
		c.p.RequestBytes += int64(e.Bytes)
		a.body(c, e, false)
		if e.EndOfStream && c.p.StartNS != 0 && c.startKey == "" {
			a.hit(CaseStartBodyEOS)
			out = append(out, a.start(c, e))
		} else {
			a.hit(CasePendingRequestChunk)
		}
	case events.PhaseResponseBodyChunk:
		a.hit(CasePendingResponseChunk)
		c := a.callFor(e, file)
		c.p.ResponseBytes += int64(e.Bytes)
		a.body(c, e, true)
	case events.PhaseWSTurnStart:
		a.hit(CaseStartWSTurn)
		r := canon.RequestFrom(e, nil, file)
		r.CallID = e.CorrelationID + "#" + strconv.Itoa(e.WSTurn)
		r.EndNS, r.SourceFile = 0, file
		r.StartNS = e.TimestampNS
		rec := a.record(Record{Kind: KindCallStart, CallID: r.CallID, Call: &r}, e.ObserverInstance)
		a.turns[r.CallID] = rec.RoutingKey
		out = append(out, rec)
	case events.PhaseComplete:
		if e.Transport == events.TransportWebSocket { // a model turn; the connection stays open
			a.hit(CaseEndWSTurn)
			r := canon.RequestFrom(e, nil, file)
			r.CallID = e.CorrelationID + "#" + strconv.Itoa(e.WSTurn)
			out = append(out, a.endKeys(r, a.turns[r.CallID], "", e.ObserverInstance)...)
			delete(a.turns, r.CallID)
			return out
		}
		c := a.calls[e.CorrelationID]
		if c == nil || c.p.StartNS == 0 {
			a.hit(CaseEndCompleteNoHeaders)
		} else {
			a.hit(CaseEndComplete)
		}
		var p *canon.Pending
		if c != nil {
			p = &c.p
		}
		r := canon.RequestFrom(e, p, file)
		r.DetailJSON = a.detail(c)
		if c == nil {
			c = a.callFor(e, file)
		}
		if events.IsAnalyticsRequest(r.Host, r.Path) { // its events follow: held until they've all come
			c.done = true
			c.p.SeenNS = e.TimestampNS
			c.bodies = nil
			a.held = &heldBatch{corr: e.CorrelationID, c: c,
				call: &Record{Kind: KindCallEnd, CallID: r.CallID, BatchID: r.CallID, Call: &r, Instance: e.ObserverInstance}}
			return out
		}
		out = append(out, a.end(c, r, e)...)
	case events.PhaseWSConnection:
		a.hit(CaseEndWSConnection)
		c := a.calls[e.CorrelationID]
		var p *canon.Pending
		if c != nil {
			p = &c.p
		}
		r := canon.RequestFrom(e, p, file)
		if c == nil {
			c = a.callFor(e, file)
		}
		c.done, c.p.SeenNS, c.bodies = true, e.TimestampNS, nil
		out = append(out, a.record(Record{Kind: KindWSConnection, CallID: r.CallID, Call: &r}, e.ObserverInstance))
		a.writeDetail(e.CorrelationID)
	case events.PhaseDisconnect:
		c := a.callFor(e, file)
		c.p.EndNS, c.p.Error, c.p.EndFile = e.TimestampNS, e.Error, file
		if c.p.StartNS != 0 {
			a.hit(CaseEndDisconnect)
			out = append(out, a.end(c, canon.RequestFromPending(&c.p), e)...)
		} else {
			a.hit(CaseEndDisconnectEarly)
		}
	case events.PhaseClientEvent:
		if e.ClientEvent == nil {
			a.hit(CaseEventNoPayload)
			return out
		}
		rec := a.event(e, line, file)
		if a.held == nil { // events without their request's row before them
			a.held = &heldBatch{corr: e.CorrelationID}
		}
		a.held.events = append(a.held.events, rec)
	case events.PhaseVerdict:
		a.hit(CaseVerdict)
	default:
		a.hit(CaseUnknownPhase)
	}
	return out
}

// start is a call's call_start.
func (a *Assembler) start(c *call, e *events.Event) Record {
	r := canon.RequestFromPending(&c.p)
	r.EndNS, r.Error = 0, ""
	if r.Product == canon.ProductCodex { // its turn's identity is in its header
		ce := events.Event{ClientMetadata: events.CodexTurnMetadata(c.headers["x-codex-turn-metadata"], nil)}
		canon.ApplyCodex(&r, &ce)
	}
	rec := a.record(Record{Kind: KindCallStart, CallID: r.CallID, Call: &r}, e.ObserverInstance)
	c.startKey = rec.RoutingKey
	return rec
}

// end is a call's call_end, and marks it ended.
func (a *Assembler) end(c *call, r canon.Request, e *events.Event) []Record {
	c.done, c.p.SeenNS, c.bodies = true, e.TimestampNS, nil
	out := a.endKeys(r, c.startKey, "", e.ObserverInstance)
	a.writeDetail(c.p.CorrelationID)
	return out
}

// endKeys is a call_end under its own key, and under its start's too when that
// differs (its session became known only at its end), so whichever key holds
// its start also holds its end.
func (a *Assembler) endKeys(r canon.Request, startKey, batchSession, instance string) []Record {
	rec := a.record(Record{Kind: KindCallEnd, CallID: r.CallID, Call: &r, BatchSession: batchSession}, instance)
	out := []Record{rec}
	if startKey != "" && startKey != rec.RoutingKey {
		a.hit(CaseEndSplitKey)
		out = append(out, a.record(Record{Kind: KindCallEnd, CallID: r.CallID, Call: &r, BatchSession: batchSession, RoutingKey: startKey}, instance))
	}
	return out
}

// release ends the held analytics batch: its request's call_end goes with the
// session its batch's earliest event names, and each Claude Code event gets
// its rank among the batch's Claude Code events.
func (a *Assembler) release() []Record {
	h := a.held
	a.held = nil
	if h == nil {
		return nil
	}
	var out []Record
	if h.call != nil {
		c := h.c
		if c.session != "" {
			a.hit(CaseBatchSession)
		} else {
			a.hit(CaseBatchNoSession)
		}
		out = a.endKeys(*h.call.Call, c.startKey, c.session, h.call.Instance)
		for i := range out {
			out[i].BatchID = h.corr
		}
		a.writeDetail(h.corr)
	}
	// An event row without its batch's credential takes the batch request's (the
	// processor joins them by correlation id).
	kh := ""
	if h.call != nil && h.call.Call != nil {
		kh = h.call.Call.KeyHash
	}
	if kh == "" && h.c != nil {
		kh = h.c.p.KeyHash
	}
	for i := range h.events {
		if c := h.events[i].Call; c != nil && c.KeyHash == "" {
			c.KeyHash = kh
		}
	}
	rankClaudeCode(h.events)
	return append(out, h.events...)
}

// rankClaudeCode numbers a batch's Claude Code events as the event rows do:
// by sequence, row time, client time, name ("(unnamed)" for none), metadata,
// then event key (a repeat of an identical row is keyed <key>-2, so identical
// ones keep arrival order).
func rankClaudeCode(evs []Record) {
	var cc []*Record
	for i := range evs {
		if evs[i].Event.Source == canon.ProductClaudeCode {
			cc = append(cc, &evs[i])
		}
	}
	name := func(r *Record) string {
		if r.Event.Name == "" {
			return "(unnamed)"
		}
		return r.Event.Name
	}
	sort.SliceStable(cc, func(i, j int) bool {
		x, y := cc[i].Event, cc[j].Event
		switch {
		case x.Seq != y.Seq:
			return x.Seq < y.Seq
		case x.RowNS != y.RowNS:
			return x.RowNS < y.RowNS
		case x.NS != y.NS:
			return x.NS < y.NS
		case name(cc[i]) != name(cc[j]):
			return name(cc[i]) < name(cc[j])
		case x.MetadataJSON != y.MetadataJSON:
			return x.MetadataJSON < y.MetadataJSON
		}
		return x.EventKey < y.EventKey
	})
	for i, r := range cc {
		r.BatchCCN = int64(i + 1)
		r.Event.BatchN = r.BatchCCN
	}
}

// Flush releases what's held (an analytics request waiting for more events).
// Call it when no more rows will come for now: at a sink's seal, at the end of
// a file set.
func (a *Assembler) Flush() []Record { return append(a.StartUploading(0), a.release()...) }

// StartUploading writes the start of each request still uploading (its headers
// seen, neither its end of stream nor a response yet) that began at or before
// before (0: every one): today's processor shows such a call in flight.
func (a *Assembler) StartUploading(before int64) []Record {
	var ids []string
	for id, c := range a.calls {
		if c.p.StartNS != 0 && c.startKey == "" && !c.done && !c.p.WebSocket && c.p.Error == "" &&
			(before == 0 || c.p.StartNS <= before) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	out := make([]Record, 0, len(ids))
	for _, id := range ids {
		a.hit(CaseStartUploading)
		c := a.calls[id]
		out = append(out, a.start(c, &events.Event{ObserverInstance: c.instance}))
	}
	return out
}

// event is one analytics event's record.
func (a *Assembler) event(e *events.Event, line []byte, file string) Record {
	ev := canon.EventFrom(e, line, file)
	if ev.ClientEventID == "" {
		copies := a.copies[e.CorrelationID]
		if copies == nil {
			copies = map[string]int{}
			a.copies[e.CorrelationID] = copies
		}
		copies[ev.EventKey]++
		if n := copies[ev.EventKey]; n > 1 {
			a.hit(CaseEventRepeat)
			ev.EventKey += "-" + strconv.Itoa(n)
		} else {
			a.hit(CaseEventNoID)
		}
	} else {
		a.hit(CaseEventWithID)
	}
	c := a.calls[e.CorrelationID]
	if c == nil {
		c = a.callFor(e, file)
	}
	c.batchN++
	if (ev.Source == canon.ProductClaudeCode || ev.Source == canon.ProductCodex) && ev.SessionID != "" &&
		(c.session == "" || ev.NS < c.sessionNS || ev.NS == c.sessionNS && ev.SessionID < c.session) {
		c.session, c.sessionNS = ev.SessionID, ev.NS
	}
	return a.record(Record{Kind: KindClientEvent, BatchID: e.CorrelationID, BatchN: c.batchN, Event: &ev,
		Call: &canon.Request{KeyHash: e.KeyHash}}, e.ObserverInstance)
}

func (a *Assembler) callFor(e *events.Event, file string) *call {
	c := a.calls[e.CorrelationID]
	if c == nil {
		c = &call{p: canon.Pending{CorrelationID: e.CorrelationID, SourceFile: file}, instance: e.ObserverInstance}
		a.calls[e.CorrelationID] = c
	}
	if c.p.SeenNS == 0 || e.TimestampNS != 0 && e.TimestampNS < c.p.SeenNS {
		c.p.SeenNS = e.TimestampNS
	}
	return c
}

// bodiesFor is the call's recorded bodies, once its request says it's a model call.
func (a *Assembler) bodiesFor(c *call) *bodies {
	if c.p.WebSocket || !modelPath(c.p.Method, c.p.Path) {
		return nil
	}
	if c.bodies == nil {
		c.bodies = &bodies{}
	}
	return c.bodies
}

// body records a body chunk of a model call, when the row carries its bytes (full interception).
func (a *Assembler) body(c *call, e *events.Event, resp bool) {
	if e.BodyB64 == "" {
		return
	}
	b := a.bodiesFor(c)
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
func (a *Assembler) detail(c *call) string {
	if c == nil || c.bodies == nil || len(c.bodies.req)+len(c.bodies.resp) == 0 {
		return ""
	}
	b := c.bodies
	if d := events.DetailFromBodies(b.req, b.reqEnc, b.resp, b.respEnc); d != nil {
		return canon.Marshal(d)
	}
	return ""
}

// sweep forgets calls past their time: ended ones' markers, and open ones that
// waited too long for their ending.
func (a *Assembler) sweep() {
	for id, c := range a.calls {
		ttl := pendingTTL
		switch {
		case c.done:
			ttl = doneTTL
		case c.p.WebSocket:
			ttl = pendingTTLWS
		}
		if a.held != nil && c == a.held.c || a.newestNS-c.p.SeenNS <= int64(ttl) {
			continue
		}
		delete(a.calls, id)
		delete(a.copies, id)
		a.dropLines(id)
	}
	for id := range a.lines { // rows of a call with no state left (a turn of a connection never seen)
		if a.calls[id] == nil {
			a.dropLines(id)
		}
	}
}

// modelPath is a model call's path: the only bodies whose detail is read.
func modelPath(method, path string) bool {
	p, _, _ := strings.Cut(path, "?")
	return method == "POST" && (strings.HasPrefix(p, "/v1/messages") && !strings.HasPrefix(p, "/v1/messages/count_tokens") ||
		p == "/backend-api/codex/responses")
}

// describesRequest is true for the rows that make up a transaction's request
// row; analytics events a request carried, and WebSocket turns, are rows of their own.
func describesRequest(e *events.Event) bool {
	switch e.Phase {
	case events.PhaseClientEvent, events.PhaseWSTurnStart:
		return false
	case events.PhaseComplete:
		return e.Transport != events.TransportWebSocket
	}
	return true
}
