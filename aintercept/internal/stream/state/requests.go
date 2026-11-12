package state

import (
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

// requests folds a record into the request log's tables.
func (s *KeyState) requests(r records.Record, d *Delta) {}
