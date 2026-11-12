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
)

// AllCases is every case, for the coverage test.
var AllCases = []string{
	CaseDuplicate, CaseOtherRecord, CaseEventNotSession, CaseEventSpan, CaseEventBlur, CaseEventSleep,
	CaseCallVisible, CaseCallNotVisible, CaseStartVisible, CaseStartNotVisible, CaseStartAfterEnd,
	CasePresenceRecut, CasePresenceSleepRow,
}
