package state

import (
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
)

// TableTypes is each table's row type (a zero value): the processor registers
// every table's file format from it.
var TableTypes = map[string]any{
	TablePresence:            Presence{},
	TableEvents:              Event{},
	TableSessionTools:        process.SessionTool{},
	TableRequestLinks:        RequestLink{},
	TablePromptIndex:         PromptIndex{},
	TableCodexTurns:          CodexTurn{},
	TableAccounts:            process.Account{},
	TableSessionAccounts:     SessionAccount{},
	TableSessionVisible:      SessionVisible{},
	TableSessionPerson:       SessionPerson{},
	TablePersonDevices:       PersonDevice{},
	TableSessionCredential:   SessionCredential{},
	TableAccountEvents:       AccountEvents{},
	TableAccountDevices:      AccountDevice{},
	TableCodexPlans:          CodexPlan{},
	TablePromptItems:         PromptItem{},
	TablePromptSteps:         StepRow{},
	TablePromptSession:       PromptSession{},
	TablePromptStepTools:     StepToolsRow{},
	TableMarks:               MarkRow{},
	TableMarkPromptIndex:     MarkPromptIndex{},
	TableRequestTxs:          ReqTx{},
	TableRequestReports:      ReportRow{},
	TableRequestMissed:       MissedRow{},
	TableBatchMembers:        BatchMember{},
	TableSessionLast:         SessionLast{},
	TableSessionRows:         SessionRowK{},
	TableSessionPromptIDs:    SessionPromptID{},
	TableSessionDevices:      SessionDevice{},
	TableSessionCallAccounts: SessionCallAccount{},
	TableSessionMissed:       SessionMissed{},
	TableSessionEdges:        SessionEdge{},
	TableSessionTitleCalls:   SessionTitleCall{},
	TableCallToolUses:        CallToolUse{},
	TableFactRequests:        canon.Request{},
	TableFactPending:         canon.Pending{},
	TableFactEvents:          canon.Event{},
	TableFactProbes:          Probes{},
}
