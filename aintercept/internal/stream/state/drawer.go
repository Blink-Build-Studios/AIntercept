package state

import (
	"strings"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

// The request drawer's tables (docs/STREAMING_PIPELINE.md §4.9): each call's
// own rows, so opening one call reads only those, never its session's.
const (
	TableDrawerLocator = "drawer_locator"
	TableDrawerCalls   = "drawer_calls"
	TableDrawerSteps   = "drawer_steps"
	TableDrawerFacts   = "drawer_facts"
	TableDrawerSandbox = "drawer_sandbox"
	TableDrawerReports = "drawer_reports"
	TableDrawerEvents  = "drawer_events"
)

// The roles of a call's facts.
const (
	RoleSuccess = "success" // its tengu_api_success
	RoleStall   = "stall"   // its streaming stall summary
	RoleTool    = "tool"    // a tool use it asked for
	RoleBash    = "bash"    // the Bash command of one of its tool uses
	RoleQuery   = "query"   // the tengu_api_query just before it
	RoleRetry   = "retry"   // a failed call's tengu_api_retry
)

// The drawer's cases.
const (
	CaseDrawerLocator   = "drawer.locator"
	CaseDrawerCallStart = "drawer.call_start"
	CaseDrawerCallEnd   = "drawer.call_end"
	CaseDrawerChain     = "drawer.chain"
	CaseDrawerEvent     = "drawer.event"
	CaseDrawerFact      = "drawer.fact"
	CaseDrawerSandbox   = "drawer.sandbox"
	CaseDrawerReport    = "drawer.report"
)

var drawerCases = []string{CaseDrawerLocator, CaseDrawerCallStart, CaseDrawerCallEnd, CaseDrawerChain,
	CaseDrawerEvent, CaseDrawerFact, CaseDrawerSandbox, CaseDrawerReport}

// DrawerLocator is where a call (or a reported call) is: the key its rows are under.
type DrawerLocator struct {
	Target string `parquet:"target"`
}

// DrawerCall is a call's request row, in flight until it ends.
type DrawerCall struct {
	canon.Request
	InFlight     bool   `parquet:"in_flight"`
	BatchSession string `parquet:"batch_session,optional"`
}

// DrawerStep is a visible model call's place in its session.
type DrawerStep struct {
	CallID        string `parquet:"call_id"` // connection~turn for a WebSocket turn
	PromptID      string `parquet:"prompt_id,optional"`
	RequestClass  string `parquet:"request_class,optional"`
	TS            int64  `parquet:"ts"`
	EndTS         *int64 `parquet:"end_ts,optional"`
	Status        *int64 `parquet:"status,optional"`
	RequestID     string `parquet:"request_id,optional"`
	Model         string `parquet:"model,optional"`
	StopReason    string `parquet:"stop_reason,optional"`
	MessageID     string `parquet:"message_id,optional"`
	PrevMessageID string `parquet:"prev_message_id,optional"`
	PrevCall      string `parquet:"prev_call,optional"` // by the message-id chain
	NextCall      string `parquet:"next_call,optional"`
}

// DrawerEvent is one Claude Code event as the drawer shows it.
type DrawerEvent struct {
	EventID  string `parquet:"event_id"` // <batch>:<n>
	BatchID  string `parquet:"batch_id"`
	TS       int64  `parquet:"ts"`
	RowTS    int64  `parquet:"row_ts"`
	Seq      int64  `parquet:"seq"`
	Name     string `parquet:"name,optional"`
	Session  string `parquet:"session_id,optional"`
	PromptID string `parquet:"prompt_id,optional"`
	DeviceID string `parquet:"device_id,optional"`
	HeadSHA  string `parquet:"head_sha,optional"`
	MD       string `parquet:"md,optional"`
	EventKey string `parquet:"event_key"`
}

// DrawerFact is one event about a call, by its role.
type DrawerFact struct {
	CallID string `parquet:"fact_call_id"`
	Role   string `parquet:"role"`
	Event  DrawerEvent
}

// DrawerSandbox is the session's sandbox setting from one Bash command on.
type DrawerSandbox struct {
	TS      int64  `parquet:"ts"`
	EventID string `parquet:"event_id"`
	Value   string `parquet:"value"`
}

// DrawerReport is a tengu_api_success, by the id its reported row opens with.
type DrawerReport struct {
	ReportID string `parquet:"report_id"`
	Event    DrawerEvent
}

func drawerEventOf(id string, e *canon.Event) DrawerEvent {
	return DrawerEvent{}
}

func stepOf(id string, c DrawerCall, sid string) (DrawerStep, bool) { return DrawerStep{}, false }

func lessStep(a, b DrawerStep) bool { return a.TS < b.TS || a.TS == b.TS && a.CallID < b.CallID }

func callFailed(c DrawerCall) bool {
	if c.InFlight {
		return false
	}
	return c.Status == 0 && c.EndNS != 0 || c.Status >= 400
}

func (s *KeyState) drawer(r records.Record, d *Delta) {}

var _ = strings.HasPrefix
