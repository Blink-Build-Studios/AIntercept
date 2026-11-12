package state

import (
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// Session rows (stub: tests first).
const (
	TableSessionRows         = "session_rows"
	TableSessionPromptIDs    = "session_prompt_ids"
	TableSessionDevices      = "session_devices"
	TableSessionCallAccounts = "session_call_accounts"
	TableSessionMissed       = "session_missed"
	TableSessionEdges        = "session_edges"
	TableSessionTitleCalls   = "session_title_calls"
)

const (
	CaseSesCallVisible   = "ses.call.visible"
	CaseSesCallUnvisible = "ses.call.unvisible"
	CaseSesCallHidden    = "ses.call.hidden"
	CaseSesCallTitle     = "ses.call.title"
	CaseSesCallCodex     = "ses.call.codex"
	CaseSesCallOther     = "ses.call.other"
	CaseSesTitleThread   = "ses.title.thread"
	CaseSesTitleDrop     = "ses.title.drop"
	CaseSesOrigin        = "ses.origin"
	CaseSesEdge          = "ses.edge"
	CaseSesEvent         = "ses.event"
	CaseSesEventQuery    = "ses.event.query"
	CaseSesEventPrompt   = "ses.event.prompt"
	CaseSesEventFriction = "ses.event.friction"
	CaseSesEventReport   = "ses.event.report"
	CaseSesEventTurn     = "ses.event.turn"
	CaseSesReportMissed  = "ses.report.missed"
	CaseSesReportMatched = "ses.report.matched"
)

var sessionCaseList = []string{CaseSesCallVisible, CaseSesCallUnvisible, CaseSesCallHidden, CaseSesCallTitle, CaseSesCallCodex,
	CaseSesCallOther, CaseSesTitleThread, CaseSesTitleDrop, CaseSesOrigin, CaseSesEdge, CaseSesEvent, CaseSesEventQuery,
	CaseSesEventPrompt, CaseSesEventFriction, CaseSesEventReport, CaseSesEventTurn, CaseSesReportMissed, CaseSesReportMatched}

func init() { AllCases = append(AllCases, sessionCaseList...) }

type SessionRowK struct {
	SessionID string `parquet:"session_id"`
	Events    *int64 `parquet:"events,optional"`
}

func (k SessionRowK) row() process.SessionRow { return process.SessionRow{SessionID: k.SessionID} }

type SessionPromptID struct {
	SessionID string `parquet:"session_id"`
	PromptID  string `parquet:"prompt_id"`
}
type SessionDevice struct {
	SessionID string `parquet:"session_id"`
	DeviceID  string `parquet:"device_id"`
}
type SessionCallAccount struct {
	SessionID string `parquet:"session_id"`
	AccountID string `parquet:"account_id"`
}
type SessionMissed struct {
	SessionID string `parquet:"session_id"`
	TSNS      int64  `parquet:"ts_ns"`
	Key       string `parquet:"event_key"`
}
type SessionEdge struct {
	ParentSessionID string `parquet:"parent_session_id"`
	ChildSessionID  string `parquet:"child_session_id"`
	Kind            string `parquet:"kind"`
	ChildFirstNS    int64  `parquet:"child_first_ns"`
}
type SessionTitleCall struct {
	SessionID string `parquet:"session_id"`
	TSNS      int64  `parquet:"ts_ns"`
	CallID    string `parquet:"call_id"`
	AccountID string `parquet:"account_id"`
	Title     string `parquet:"title"`
}

func (s *KeyState) sessionRows(r records.Record, d *Delta) {}
