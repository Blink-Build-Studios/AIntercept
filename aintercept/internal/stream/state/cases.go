package state

// The cases a record can take: each is a branch of Apply, and each must have
// an atomic test (cases_test.go). The real capture's histogram of them is the
// coverage check.
const (
	CaseDuplicate        = "dup"                // a record id already applied
	CaseOtherRecord      = "other"              // a kind no session table reads (an account sighting)
	CaseEventNotSession  = "event.not_session"  // not a Claude Code event of a session
	CaseEventSpan        = "event.span"         // a Claude Code event: in the session's span
	CaseEventBlur        = "event.blur"         // with a blur window
	CaseEventSleep       = "event.sleep"        // with a sleep window
	CaseCallVisible      = "call.visible"       // an ended visible model call
	CaseCallNotVisible   = "call.not_visible"   // an ended call the session doesn't count (hidden, other host…)
	CaseStartVisible     = "start.visible"      // a Claude Code model call in flight
	CaseStartNotVisible  = "start.not_visible"  // a call in flight the session doesn't count
	CaseStartAfterEnd    = "start.after_end"    // a call's start applied after its ending
	CasePresenceRecut    = "presence.recut"     // the span or its blur windows changed: a stretch cut again
	CasePresenceSleepRow = "presence.sleep_row" // a sleep window made or grew
	CaseEventRow         = "event.row"          // a Claude Code event's row, by its link: .request .prompt .tool .device .session
	CasePromptIndex      = "event.prompt_index" // a prompt's number set or replaced by a later input prompt
	CaseSessionTool      = "tool.first"         // a session tool seen, or seen earlier
	CaseRequestLink      = "call.request_link"  // a visible call put under its request id
	CaseCodexTurn        = "call.codex_turn"    // a Codex turn's first visible call, or an earlier one

	CaseAccount           = "dim.account"            // an account sighting
	CaseSessionAccount    = "dim.session_account"    // a session names an account
	CaseSessionVisible    = "dim.session_visible"    // a session's visible-call count went from or to none
	CaseSessionPerson     = "dim.session_person"     // a session's least account (as the people list counts)
	CasePersonDevice      = "dim.person_device"      // a person and device named together
	CaseSessionCredential = "dim.session_credential" // a session's credential
	CaseAccountEvents     = "dim.account_events"     // an account's org or plan from a Claude Code event
	CaseAccountDevice     = "dim.account_device"     // an account's device (and env)
	CaseCodexPlan         = "dim.codex_plan"         // an account's Codex plan
)

// AllCases is every case, for the coverage test.
var AllCases = []string{
	CaseDuplicate, CaseOtherRecord, CaseEventNotSession, CaseEventSpan, CaseEventBlur, CaseEventSleep,
	CaseCallVisible, CaseCallNotVisible, CaseStartVisible, CaseStartNotVisible, CaseStartAfterEnd,
	CasePresenceRecut, CasePresenceSleepRow,
	CaseEventRow + ".request", CaseEventRow + ".prompt", CaseEventRow + ".tool", CaseEventRow + ".device", CaseEventRow + ".session",
	CasePromptIndex, CaseSessionTool, CaseRequestLink, CaseCodexTurn,
	CaseAccount, CaseSessionAccount, CaseSessionVisible, CaseSessionPerson, CasePersonDevice, CaseSessionCredential,
	CaseAccountEvents, CaseAccountDevice, CaseCodexPlan,
}
