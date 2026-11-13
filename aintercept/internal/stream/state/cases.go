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
	CaseTxEnd, CaseTxEndCopy, CaseTxStart, CaseTxNoRow, CaseReport, CaseMissed, CaseMatched,
	CaseRetry, CaseRetryRow, CaseNeighbor, CaseBatchMember, CaseSessionLast,
}

// The prompt list's and marks' cases (prompts.go).
const (
	CasePromptInput        = "prompt.input"         // a tengu_input_prompt with a prompt id: the prompt's input, or a later one
	CasePromptInputUnnamed = "prompt.input_unnamed" // one with no prompt id: a prompt of its own, named by its event
	CasePromptTurnEnd      = "prompt.turn_end"      // a turn end: the prompt's duration
	CasePromptCancel       = "prompt.cancel"        // a cancel: the prompt was interrupted
	CasePromptDestructive  = "prompt.destructive"   // a destructive Bash command in the prompt
	CasePromptTool         = "prompt.tool"          // a finished tool in the prompt: its tool counts
	CasePromptPaste        = "prompt.paste"         // a paste: the typed prompt after it
	CasePromptHidden       = "prompt.hidden"        // a hidden call (prewarm, title) given to a prompt, or given again
	CasePromptCodexReport  = "prompt.codex_report"  // a Codex turn's report, or a later one
	CaseStepCall           = "step.call"            // a visible call with a prompt: its step
	CaseStepPending        = "step.pending"         // a visible call in flight: its step until it ends
	CaseStepGone           = "step.gone"            // a call that was a step isn't one after its ending
	CaseStepSuccess        = "step.success"         // a tengu_api_success: the steps it reports
	CaseStepTool           = "step.tool"            // a finished tool: its request's step
	CaseStepTick           = "step.tick"            // a progress tick: its request's step's running tools
	CaseStepBash           = "step.bash"            // a Bash event: the steps whose tool it is
	CaseStepResults        = "step.results"         // recorded exit codes another step's tool calls show
	CaseBetweenCall        = "between.call"         // a visible call with no prompt: a between row
	CaseBetweenRecap       = "between.recap"        // a recap shown on return: a between row
	CaseCompaction         = "compaction.succeeded" // a compaction: its between row and mark, and the next one's window
	CaseCompactionAttempt  = "compaction.attempt"   // an attempt: its compaction's start and wait
	CaseCompactionFired    = "compaction.fired"     // an idle compaction firing: the compactions it makes idle
	CaseCompactionContext  = "compaction.context"   // a main call's context: the compactions around it
	CaseCompactionCall     = "compaction.call"      // a compaction call: the compaction it's attached to
	CaseMarkCall           = "mark.call"            // a visible call's mark
	CaseMarkReported       = "mark.reported"        // a report's marks: reported, and missed when no step has its ids
	CaseMarkMissed         = "mark.missed"          // a report's missed mark made or taken back by a step
	CaseMarkUnreported     = "mark.unreported"      // a call mark's unreported flag changed by a report
	CaseMarkFriction       = "mark.friction"        // a friction mark: frustrated, interrupted or destructive
	CaseMarkPermission     = "mark.permission"      // a permission mode point
	CaseMarkPromptIndex    = "mark.prompt_index"    // a prompt's number as the marks show it
)

// PromptCases is the prompt list's and marks' cases, for the coverage test.
var PromptCases = []string{
	CasePromptInput, CasePromptInputUnnamed, CasePromptTurnEnd, CasePromptCancel, CasePromptDestructive, CasePromptTool,
	CasePromptPaste, CasePromptHidden, CasePromptCodexReport, CaseStepCall, CaseStepPending, CaseStepGone, CaseStepSuccess,
	CaseStepTool, CaseStepTick, CaseStepBash, CaseStepResults, CaseBetweenCall, CaseBetweenRecap, CaseCompaction,
	CaseCompactionAttempt, CaseCompactionFired, CaseCompactionContext, CaseCompactionCall, CaseMarkCall, CaseMarkReported,
	CaseMarkMissed, CaseMarkUnreported, CaseMarkFriction, CaseMarkPermission, CaseMarkPromptIndex,
}

func init() { AllCases = append(AllCases, PromptCases...) }
