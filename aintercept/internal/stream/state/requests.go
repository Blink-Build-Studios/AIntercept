package state

import (
	"encoding/json"
	"reflect"
	"strings"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// The request log's tables (docs/STREAMING_PIPELINE.md §4.5, request rows). A
// key stores its own calls' rows, not a person's: who a row belongs to, and the
// values that span a person's sessions (a prompt's number, a call's report, its
// step number, an analytics batch's summary), are joined by the query
// (api/src/aintercept_api/stream_sql.py).
const (
	// TableRequestTxs is each call the key holds (an ended call, or one in
	// flight): its request-log row as far as the call alone decides it, and what
	// the query joins on. Every non-synthetic call is here, IsTx says whether
	// the log lists it; the others only lend their credential.
	TableRequestTxs = "request_txs"
	// TableRequestReports is each tengu_api_success: the query takes a request
	// id's first (by time, then event key) for its TTFT and cache writes.
	TableRequestReports = "request_reports"
	// TableRequestMissed is each report no visible call of its session
	// matches: a call that went around the proxy.
	TableRequestMissed = "request_missed"
	// TableBatchMembers is each Claude Code or Codex analytics event, by its
	// batch: an analytics request's event count, least name and session.
	TableBatchMembers = "batch_members"
	// TableSessionLast is a session's newest visible model call or Claude Code
	// event: a person's lastDataAt is the newest of their sessions'.
	TableSessionLast = "session_last"
)

// ReqTx is one call's row (TableRequestTxs). Row leaves to the query its
// person, its prompt's number, its report's values, its step number, an
// analytics request's summary and session (for a call with none of its own).
type ReqTx struct {
	CallID      string             `parquet:"call_id"`
	CorrID      string             `parquet:"correlation_id"`
	KeyHash     string             `parquet:"key_hash"`
	OwnSession  string             `parquet:"own_session"`
	PromptID    string             `parquet:"prompt_id"`
	ReqID       string             `parquet:"req_id"`
	ClientReqID string             `parquet:"client_req_id"`
	Kind        string             `parquet:"kind"`
	Hidden      string             `parquet:"hidden_kind"`
	Product     string             `parquet:"product"`
	IsTx        bool               `parquet:"is_tx"`
	Row         process.RequestRow `parquet:"row"`
}

// ReportRow is a tengu_api_success (TableRequestReports).
type ReportRow struct {
	SessionID   string   `parquet:"session_id"`
	TS          int64    `parquet:"ts_ns"`
	EventKey    string   `parquet:"event_key"`
	ReqID       string   `parquet:"req_id"`
	ClientReqID string   `parquet:"client_req_id"`
	TTFT        *float64 `parquet:"ttft_ms,optional"`
	Cache5m     *int64   `parquet:"cache_write_5m,optional"`
	Cache1h     *int64   `parquet:"cache_write_1h,optional"`
}

// MissedRow is a reported call the proxy never saw (TableRequestMissed): its
// row, without the values the query takes from the request id's first report.
type MissedRow struct {
	SessionID   string             `parquet:"session_id"`
	EventKey    string             `parquet:"event_key"`
	ReqID       string             `parquet:"req_id"`
	ClientReqID string             `parquet:"client_req_id"`
	Row         process.RequestRow `parquet:"row"`
}

// BatchMember is one analytics event of a batch (TableBatchMembers).
type BatchMember struct {
	BatchID   string `parquet:"batch_id"`
	EventKey  string `parquet:"event_key"`
	NS        int64  `parquet:"ns"`
	SessionID string `parquet:"session_id"`
	Name      string `parquet:"name"`
}

// SessionLast is a session's newest data time (TableSessionLast).
type SessionLast struct {
	SessionID string `parquet:"session_id"`
	LastNS    int64  `parquet:"last_ns"`
}

// The request log's cases.
const (
	CaseTxEnd       = "req.tx.end"         // an ended call's row (under its own key)
	CaseTxEndCopy   = "req.tx.end_copy"    // an ended call's copy under its start's key: the start's row goes
	CaseTxStart     = "req.tx.start"       // a call in flight: its row from its start
	CaseTxNoRow     = "req.tx.no_row"      // a call with no row: synthetic, a WebSocket turn's start, a start after its end
	CaseReport      = "req.report"         // a tengu_api_success
	CaseMissed      = "req.missed"         // a report no visible call matches: a not-captured row
	CaseMatched     = "req.matched"        // a visible call matches a report: its not-captured row goes
	CaseRetry       = "req.retry"          // a tengu_api_retry
	CaseRetryRow    = "req.retry.row"      // a failed call's row redone: a retry came in its window
	CaseNeighbor    = "req.retry.neighbor" // a failed call's row redone: its next attempt may have changed
	CaseBatchMember = "req.batch_member"   // an analytics event, under its batch
	CaseSessionLast = "req.session_last"   // a session's newest data time changed
)

// reqState is a key's indexes for the request log.
type reqState struct {
	txs      map[string]*txEnt              // call id → its call (ended, or in flight)
	groups   map[string]*omap[tsID, string] // prompt and class → the session's model calls, by (time, row id)
	failedAt *omap[tsID, string]            // the session's failed model calls, by (time, row id)
	retries  *omap[tsID, process.Retry]     // the session's tengu_api_retry, by (time, key)
	visReq   map[string]int                 // request id → the session's visible calls carrying it
	visCRID  map[string]int                 // client request id → the same
	reps     map[string]repEnt              // event key → a report
	byReq    map[string]map[string]bool     // request id → the reports naming it
	byCRID   map[string]map[string]bool     // client request id → the same
	maxSpan  int64                          // the longest failed call, start to end: how far before a retry its call can start
	rebuilt  int                            // rows txRow has rebuilt (a test's measure of the work)
	best     map[string]nearest             // failed call id → the retry its row shows (as txRow last chose); absent: not known
	lastOK   bool                           // the session's newest time as last written
	lastNS   int64
}

// nearest is the retry a failed call's row shows, or none.
type nearest struct {
	none     bool
	ts, dist int64
	key      string
}

type txEnt struct {
	tx  *process.Tx
	src TxSource
}

// TxSource is what a call's row was built from, for Encode: its ending, or its start.
type TxSource struct {
	Ended bool
	Req   *canon.Request
	Pend  *canon.Pending
}

type repEnt struct {
	session, reqID, crid string
	ts                   int64
}

func newReqState() *reqState {
	return &reqState{txs: map[string]*txEnt{}, groups: map[string]*omap[tsID, string]{},
		failedAt: newOmap[tsID, string](lessTsID), retries: newOmap[tsID, process.Retry](lessTsID),
		visReq: map[string]int{}, visCRID: map[string]int{}, reps: map[string]repEnt{},
		byReq: map[string]map[string]bool{}, byCRID: map[string]map[string]bool{}, best: map[string]nearest{}}
}

// requests folds a record into the request log's tables.
func (s *KeyState) requests(r records.Record, d *Delta) {
	x, sid := s.rq, sessionOf(s.Key)
	switch {
	case r.Kind == records.KindCallEnd || r.Kind == records.KindWSConnection:
		c := r.Call
		switch {
		case records.RoutingKey(&r) != s.Key: // its copy under its start's key: the start's row goes
			d.Cases = append(d.Cases, CaseTxEndCopy)
			s.setTx(d, c.CallID, nil)
		case c.Synthetic:
			d.Cases = append(d.Cases, CaseTxNoRow)
			s.setTx(d, c.CallID, nil)
		default:
			d.Cases = append(d.Cases, CaseTxEnd)
			s.setTx(d, c.CallID, &txEnt{process.TxOfRequest(*c), TxSource{Ended: true, Req: c}})
		}
	case r.Kind == records.KindCallStart:
		c := r.Call
		if s.ses.ended[c.CallID] || strings.Contains(c.CallID, "#") {
			d.Cases = append(d.Cases, CaseTxNoRow)
			break
		}
		p := pendingOfStart(c)
		d.Cases = append(d.Cases, CaseTxStart)
		s.setTx(d, c.CallID, &txEnt{process.TxOfPending(p), TxSource{Pend: &p}})
	case r.Kind == records.KindClientEvent && r.Event != nil:
		e := r.Event
		if process.IsAnalyticsEvent(*e) {
			d.Cases = append(d.Cases, CaseBatchMember)
			s.rows.put(d, TableBatchMembers, "bm:"+e.EventKey, BatchMember{e.CorrelationID, e.EventKey, e.NS, e.SessionID, e.Name})
		}
		if e.Source != canon.ProductClaudeCode || sid == "" || e.SessionID != sid {
			break
		}
		if rep, ok := process.ReportOf(*e); ok {
			d.Cases = append(d.Cases, CaseReport)
			s.rows.put(d, TableRequestReports, "rp:"+e.EventKey, ReportRow{sid, e.NS, e.EventKey, rep.ReqID, rep.ClientReqID, rep.TTFT, rep.Cache5m, rep.Cache1h})
			x.addReport(e.EventKey, repEnt{sid, rep.ReqID, rep.ClientReqID, e.NS})
			s.missed(d, e.EventKey)
		}
		if rt, ok := process.RetryOf(*e); ok {
			d.Cases = append(d.Cases, CaseRetry)
			x.retries.Set(tsID{rt.TS, rt.Key}, rt)
			s.retryArrived(d, rt)
		}
	}
	if sid == "" {
		return
	}
	if sp := s.ses.spanNow(); sp.ok != x.lastOK || sp.ok && sp.last != x.lastNS {
		x.lastOK, x.lastNS = sp.ok, sp.last
		d.Cases = append(d.Cases, CaseSessionLast)
		if sp.ok {
			s.rows.put(d, TableSessionLast, "sl", SessionLast{sid, sp.last})
		} else {
			s.rows.drop(d, TableSessionLast, "sl")
		}
	}
}

func (x *reqState) addReport(key string, e repEnt) {
	x.reps[key] = e
	if e.reqID != "" {
		addSetReq(x.byReq, e.reqID, key)
	}
	if e.crid != "" {
		addSetReq(x.byCRID, e.crid, key)
	}
}

func addSetReq(m map[string]map[string]bool, k, v string) {
	set := m[k]
	if set == nil {
		set = map[string]bool{}
		m[k] = set
	}
	set[v] = true
}

// missed writes a report's not-captured row, or takes it back, by whether a
// visible call of its session carries its request id or client request id.
func (s *KeyState) missed(d *Delta, key string) {
	x := s.rq
	e := x.reps[key]
	if e.reqID != "" && x.visReq[e.reqID] > 0 || e.crid != "" && x.visCRID[e.crid] > 0 {
		if _, ok := s.rows[TableRequestMissed]["ms:"+key]; ok {
			d.Cases = append(d.Cases, CaseMatched)
			s.rows.drop(d, TableRequestMissed, "ms:"+key)
		}
		return
	}
	if _, ok := s.rows[TableRequestMissed]["ms:"+key]; ok {
		return
	}
	d.Cases = append(d.Cases, CaseMissed)
	s.rows.put(d, TableRequestMissed, "ms:"+key, MissedRow{e.session, key, e.reqID, e.crid, process.MissedRow(e.session, e.ts, e.reqID, e.crid)})
}

// grouped: a model call of the session that can be a retried call's next attempt.
func (s *KeyState) grouped(t *process.Tx) bool {
	return t.Kind() == "model" && t.Session() != "" && t.Session() == sessionOf(s.Key) && t.IsTransaction() && t.KeyHash() != ""
}

// retried: a failed model call of the session, which a retry may follow.
func (s *KeyState) retried(t *process.Tx) bool {
	return t.Kind() == "model" && t.Failed() && t.Session() != "" && t.Session() == sessionOf(s.Key)
}

func groupKey(t *process.Tx) string { return t.Prompt() + "\x00" + t.Class() }

func atOf(t *process.Tx) tsID { return tsID{t.TS(), t.RowID()} }

// setTx makes a call's entry e (nil: none), and redoes the rows it affects: its
// own, its group's previous call's (whose next attempt it may be), and the
// not-captured rows of the request ids it carries or carried.
func (s *KeyState) setTx(d *Delta, callID string, e *txEnt) {
	x, sid := s.rq, sessionOf(s.Key)
	redo := map[string]bool{callID: true}
	ids := map[string]bool{}
	if old := x.txs[callID]; old != nil {
		t := old.tx
		if s.grouped(t) {
			g := x.groups[groupKey(t)]
			g.Delete(atOf(t))
			if _, p, ok := g.Floor(atOf(t)); ok {
				redo[p] = true
			}
		}
		x.failedAt.Delete(atOf(t))
		if t.Visible() && t.Session() == sid {
			x.count(t, -1, ids)
		}
		delete(x.txs, callID)
	}
	if e != nil {
		t := e.tx
		x.txs[callID] = e
		if s.grouped(t) {
			g := x.groups[groupKey(t)]
			if g == nil {
				g = newOmap[tsID, string](lessTsID)
				x.groups[groupKey(t)] = g
			}
			if _, p, ok := g.Floor(atOf(t)); ok {
				redo[p] = true
			}
			g.Set(atOf(t), callID)
		}
		if s.retried(t) {
			x.failedAt.Set(atOf(t), callID)
			x.maxSpan = max(x.maxSpan, t.End()-t.TS())
		}
		if t.Visible() && t.Session() == sid {
			x.count(t, 1, ids)
		}
	}
	for c := range redo {
		if c != callID {
			if p := x.txs[c]; p != nil && s.retried(p.tx) {
				d.Cases = append(d.Cases, CaseNeighbor)
			}
		}
		s.txRow(d, c)
	}
	for k := range ids {
		for key := range x.byReq[strings.TrimPrefix(k, "r:")] {
			if strings.HasPrefix(k, "r:") {
				s.missed(d, key)
			}
		}
		for key := range x.byCRID[strings.TrimPrefix(k, "c:")] {
			if strings.HasPrefix(k, "c:") {
				s.missed(d, key)
			}
		}
	}
}

// count adds n to the visible calls carrying t's ids, noting the ids whose count
// went from or to none.
func (x *reqState) count(t *process.Tx, n int, changed map[string]bool) {
	if id := t.ReqID(); id != "" {
		was := x.visReq[id]
		x.visReq[id] += n
		if (was == 0) != (x.visReq[id] == 0) {
			changed["r:"+id] = true
		}
		if x.visReq[id] == 0 {
			delete(x.visReq, id)
		}
	}
	if id := t.ClientReqID(); id != "" {
		was := x.visCRID[id]
		x.visCRID[id] += n
		if (was == 0) != (x.visCRID[id] == 0) {
			changed["c:"+id] = true
		}
		if x.visCRID[id] == 0 {
			delete(x.visCRID, id)
		}
	}
}

// retryArrived redoes the rows of the failed calls a retry becomes the nearest
// retry of (the reference's nearestRetry): the others' rows don't change, so it
// costs a comparison per failed call in its window and a rebuild per row it
// changes.
func (s *KeyState) retryArrived(d *Delta, rt process.Retry) {
	x := s.rq
	var hit []string
	x.failedAt.Ascend(tsID{rt.TS - process.RetryAfterNS - x.maxSpan, ""}, func(k tsID, c string) bool {
		if k.ts > rt.TS {
			return false
		}
		t := x.txs[c].tx
		if !s.retried(t) {
			return true
		}
		stop := t.End()
		if stop == 0 {
			stop = t.TS()
		}
		if rt.TS > stop+process.RetryAfterNS || rt.TS < t.TS() {
			return true
		}
		if p := t.Prompt(); p != "" && rt.HasPromptID && rt.PromptID != p {
			return true
		}
		dist := rt.TS - stop
		if dist < 0 {
			dist = -dist
		}
		b, known := x.best[c]
		if !known || b.none || dist < b.dist || dist == b.dist && (rt.TS < b.ts || rt.TS == b.ts && rt.Key < b.key) {
			hit = append(hit, c)
		}
		return true
	})
	for _, c := range hit {
		d.Cases = append(d.Cases, CaseRetryRow)
		s.txRow(d, c)
	}
}

// txRow writes a call's row as it stands (or takes it back, when it has none).
func (s *KeyState) txRow(d *Delta, callID string) {
	x := s.rq
	x.rebuilt++
	e := x.txs[callID]
	if e == nil {
		delete(x.best, callID)
		s.rows.drop(d, TableRequestTxs, "tx:"+callID)
		return
	}
	t := e.tx
	var retry *process.Retry
	var next *process.Tx
	if s.retried(t) {
		stop := t.End()
		if stop == 0 {
			stop = t.TS()
		}
		var cands []process.Retry
		x.retries.Ascend(tsID{t.TS(), ""}, func(k tsID, r process.Retry) bool {
			if k.ts > stop+process.RetryAfterNS {
				return false
			}
			cands = append(cands, r)
			return true
		})
		x.best[callID] = nearest{none: true}
		if i := process.NearestRetry(cands, t.TS(), t.End(), t.Prompt()); i >= 0 {
			retry = &cands[i]
			dist := retry.TS - stop
			if dist < 0 {
				dist = -dist
			}
			x.best[callID] = nearest{ts: retry.TS, dist: dist, key: retry.Key}
			if g := x.groups[groupKey(t)]; g != nil {
				at := atOf(t)
				g.Ascend(at, func(k tsID, c string) bool {
					if k == at {
						return true
					}
					next = x.txs[c].tx
					return false
				})
			}
		}
	}
	row := ReqTx{CallID: callID, CorrID: t.CorrID(), KeyHash: t.KeyHash(), OwnSession: t.Session(), PromptID: t.Prompt(),
		ReqID: t.ReqID(), ClientReqID: t.ClientReqID(), Kind: t.Kind(), Hidden: t.Hidden(), Product: t.Product(),
		IsTx: t.IsTransaction(), Row: t.Row(retry, next)}
	s.rows.putEq(d, TableRequestTxs, "tx:"+callID, row)
}

// putEq is put for a row that holds slices (compared deeply).
func (rs rowSet) putEq(d *Delta, table, id string, row any) {
	if cur, ok := rs[table][id]; ok && reflect.DeepEqual(cur, row) {
		return
	}
	t := rs[table]
	if t == nil {
		t = map[string]any{}
		rs[table] = t
	}
	t[id] = row
	d.Changes = append(d.Changes, Change{Table: table, ID: id, Row: row})
}

// pendingOfStart is a call's start as the processor holds a call in flight: what
// its headers say.
func pendingOfStart(c *canon.Request) canon.Pending {
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

// reqSnap is the request log's state for Encode: each call's source, the
// retries and reports, and the newest time last written.
type reqSnap struct {
	Txs     map[string]TxSource
	Retries []process.Retry
	Reports map[string]RepSnap
	LastOK  bool
	LastNS  int64
}

// RepSnap is a report's place, for Encode.
type RepSnap struct {
	Session, ReqID, CRID string
	TS                   int64
}

func (x *reqState) snap() reqSnap {
	y := reqSnap{Txs: map[string]TxSource{}, Reports: map[string]RepSnap{}, LastOK: x.lastOK, LastNS: x.lastNS}
	for c, e := range x.txs {
		y.Txs[c] = e.src
	}
	x.retries.All(func(_ tsID, r process.Retry) bool { y.Retries = append(y.Retries, r); return true })
	for k, e := range x.reps {
		y.Reports[k] = RepSnap{e.session, e.reqID, e.crid, e.ts}
	}
	return y
}

// reqFrom rebuilds the indexes from a snapshot (the rows come back with it).
func (s *KeyState) reqFrom(y reqSnap) {
	x := newReqState()
	s.rq = x
	sid := sessionOf(s.Key)
	for c, src := range y.Txs {
		var t *process.Tx
		if src.Ended {
			t = process.TxOfRequest(*src.Req)
		} else {
			t = process.TxOfPending(*src.Pend)
		}
		x.txs[c] = &txEnt{t, src}
		if s.grouped(t) {
			g := x.groups[groupKey(t)]
			if g == nil {
				g = newOmap[tsID, string](lessTsID)
				x.groups[groupKey(t)] = g
			}
			g.Set(atOf(t), c)
		}
		if s.retried(t) {
			x.failedAt.Set(atOf(t), c)
			x.maxSpan = max(x.maxSpan, t.End()-t.TS())
		}
		if t.Visible() && t.Session() == sid {
			x.count(t, 1, map[string]bool{})
		}
	}
	for _, r := range y.Retries {
		x.retries.Set(tsID{r.TS, r.Key}, r)
	}
	for k, e := range y.Reports {
		x.addReport(k, repEnt{e.Session, e.ReqID, e.CRID, e.TS})
	}
	x.lastOK, x.lastNS = y.LastOK, y.LastNS
}
