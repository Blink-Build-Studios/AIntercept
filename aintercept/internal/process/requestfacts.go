package process

import (
	"math"
	"strconv"
	"strings"
)

// What the streaming processor's per-key state (internal/stream/state) reads of
// the request log's builders: one call's row as far as the call alone decides it,
// and the events that rows join (reports, retries). The same code as the
// single-pass reference, so the two can't drift.

// Tx is one call as the request log reads it: an ended request, or one in flight.
type Tx struct{ c rowCall }

// TxOfRequest is an ended request.
func TxOfRequest(r Request) *Tx { return &Tx{callFromRequest(colsOf(r), interner{})} }

// TxOfPending is a call in flight.
func TxOfPending(p Pending) *Tx { return &Tx{callFromPending(p)} }

// RowID is its row's request id (its call id, '#' written '~').
func (t *Tx) RowID() string       { return t.c.id }
func (t *Tx) CorrID() string      { return t.c.corrID }
func (t *Tx) TS() int64           { return t.c.ts }
func (t *Tx) End() int64          { return t.c.end }
func (t *Tx) Session() string     { return t.c.session }
func (t *Tx) Prompt() string      { return t.c.prompt }
func (t *Tx) Class() string       { return t.c.class }
func (t *Tx) KeyHash() string     { return t.c.keyHash }
func (t *Tx) ReqID() string       { return t.c.reqID }
func (t *Tx) ClientReqID() string { return t.c.clientReqID }
func (t *Tx) Hidden() string      { return t.c.hidden }
func (t *Tx) Product() string     { return t.c.product }
func (t *Tx) Status() int64       { return t.c.status }
func (t *Tx) Kind() string        { return kindOf(t.c.path) }
func (t *Tx) Synthetic() bool     { return t.c.synthetic }
func (t *Tx) IsTransaction() bool { return t.c.transaction() }
func (t *Tx) Visible() bool       { return t.c.ccCall() }
func (t *Tx) Failed() bool        { return t.c.failed() }
func (t *Tx) Succeeded() bool     { return t.c.status != 0 && t.c.status < 400 }

// Row is the call's request-log row, given its retry (nil: none) and the next
// attempt after it (nil: none captured). What spans the person's sessions is
// left for the query: the person, the prompt's number, the report's TTFT and
// cache writes, the step number, and an analytics request's summary (its detail
// here is "0 events") and session (here only the call's own).
func (t *Tx) Row(retry *Retry, next *Tx) RequestRow {
	var a *attempt
	if retry != nil {
		a = &attempt{retry: rowEvent{ts: retry.TS, key: retry.Key, attempt: retry.Attempt}}
		if next != nil {
			a.next = &next.c
		}
	}
	return noRowData.requestRow(&t.c, nil, nil, a, nil)
}

// noRowData is what a call's own row reads of the other data: nothing (its
// reads of the batches and maps find nil maps empty).
var noRowData = &rowData{}

// Retry is a tengu_api_retry as a failed attempt's retry reads it.
type Retry struct {
	TS          int64
	Key         string
	PromptID    string
	HasPromptID bool
	Attempt     *int64
}

// RetryOf is a tengu_api_retry's retry.
func RetryOf(e Event) (Retry, bool) {
	if e.Name != "tengu_api_retry" {
		return Retry{}, false
	}
	md := jsonFields(e.MetadataJSON, "cc_prompt_id", "attempt")
	pid, ok := jsonString(md["cc_prompt_id"])
	return Retry{TS: e.NS, Key: e.EventKey, PromptID: pid, HasPromptID: ok, Attempt: jsonInt(md["attempt"], math.MinInt32, math.MaxInt32)}, true
}

// RetryAfterNS is how long after a failed attempt's end its retry may be logged.
const RetryAfterNS = retryAfterNS

// NearestRetry is the index of a failed attempt's retry among rs (in time, then
// key, order: the reference's), or -1.
func NearestRetry(rs []Retry, ts, end int64, prompt string) int {
	evs := make([]rowEvent, len(rs))
	for i, r := range rs {
		evs[i] = rowEvent{ts: r.TS, key: r.Key, promptID: r.PromptID, hasPromptID: r.HasPromptID, attempt: r.Attempt}
	}
	best := nearestRetry(evs, ts, end, prompt)
	if best == nil {
		return -1
	}
	for i := range evs {
		if &evs[i] == best {
			return i
		}
	}
	return -1
}

// Report is a tengu_api_success as the rows read it.
type Report struct {
	ReqID, ClientReqID string
	TTFT               *float64
	Cache5m, Cache1h   *int64
}

// ReportOf is a tengu_api_success's report.
func ReportOf(e Event) (Report, bool) {
	if e.Name != "tengu_api_success" {
		return Report{}, false
	}
	md := jsonFields(e.MetadataJSON, "requestId", "clientRequestId", "ttftMs",
		"cache_creation_5m_input_tokens", "cache_creation_1h_input_tokens")
	rid, _ := jsonString(md["requestId"])
	crid, _ := jsonString(md["clientRequestId"])
	return Report{ReqID: rid, ClientReqID: crid, TTFT: jsonDouble(md["ttftMs"]),
		Cache5m: jsonInt(md["cache_creation_5m_input_tokens"], math.MinInt64, math.MaxInt64),
		Cache1h: jsonInt(md["cache_creation_1h_input_tokens"], math.MinInt64, math.MaxInt64)}, true
}

// MissedRow is a reported call's not-captured row, without the values the query
// takes from its request id's first report (TTFT, cache writes).
func MissedRow(session string, ts int64, reqID, clientReqID string) RequestRow {
	id := firstStr(reqID, clientReqID)
	if id == "" {
		id = strconv.FormatInt(ts, 10)
	}
	return RequestRow{
		RequestID: "reported:" + id, TsNS: ts, Kind: "model", Agent: ptr("claude-code"),
		SessionID: ptr(session), Path: "/v1/messages", Detail: "reported by Claude Code · not captured",
		TokensAccuracy: ptr("missing"), ToolCalls: []string{},
	}
}

// IsAnalyticsEvent is an event an analytics request's summary counts: Claude
// Code's or Codex's.
func IsAnalyticsEvent(e Event) bool {
	return e.Source == productClaudeCode || e.Source == productCodex
}

// TrimCallID is a call id as its row's request id.
func TrimCallID(id string) string { return strings.ReplaceAll(id, "#", "~") }
