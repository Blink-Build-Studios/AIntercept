package state

import "github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"

// The prompt list's tables (process promptrows.go) in their stored shape, and
// the lookups the query joins to rebuild today's rows (the API's
// /sessions/{id}/prompts and the timelines' prompt marks):
//
//   - prompt_items: a session's prompts and between rows (compactions, recaps,
//     calls with no prompt). Their place is a sort key, (t, type, group, then the
//     group's order: a prompt's id; a compaction's or recap's event order; a
//     call's id); the query numbers them (seq).
//   - prompt_steps: each prompt's steps, by (t, request id); the query numbers
//     them and joins their prompt's seq.
//   - prompt_session: the session's values every row used to copy: its last
//     time, its product (Codex numbers turns and shows their reports), and its
//     last prompt-row event (a step's events are pending until one follows it).
//
// What a prompt row shows that depends on the other prompts is the query's too:
// its number when it's a Codex turn (its rank), whether it's the open tail (the
// last prompt with no turn end), and whether it's typed.
const (
	TablePromptItems   = "prompt_items"
	TablePromptSteps   = "prompt_steps"
	TablePromptSession = "prompt_session"
)

// PromptItem is a prompt or between row (process.PromptRow) as stored: Seq,
// SessionLastNS, Typed and OpenTail are left to the query; PromptIndex is its
// input prompt's; DurationReportMS and TurnReportJSON are its Codex turn
// report's (shown for a Codex session only).
type PromptItem struct {
	process.PromptRow
	// Group orders rows at the same time and type: 0 a prompt, 1 a compaction,
	// 2 a recap, 3 a call with no prompt.
	Group int32 `parquet:"group"`
	// The order within the group: a compaction's or recap's event order (batch,
	// then its place in it); OrdKey is a prompt's id, a call's request id or an
	// event's key.
	OrdCorr  string `parquet:"ord_corr"`
	OrdSeq   int64  `parquet:"ord_seq"`
	OrdRowNS int64  `parquet:"ord_row_ns"`
	OrdName  string `parquet:"ord_name"`
	OrdMD    string `parquet:"ord_md"`
	OrdKey   string `parquet:"ord_key"`
	// A prompt's: whether an input prompt was captured, its turn ended, and its
	// Codex report has a status.
	HasInput     bool `parquet:"has_input"`
	HasTurnEnd   bool `parquet:"has_turn_end"`
	ReportStatus bool `parquet:"report_status"`
}

// StepRow is a step (process.PromptStep) as stored: its Seq and Step are the
// query's (its prompt's seq; its rank by time and id), and its events are
// pending when live, unreported (HasSuccess false) and no prompt-row event
// followed PendEnd (its end, else its start).
type StepRow struct {
	process.PromptStep
	PromptID   string `parquet:"prompt_id"`
	HasSuccess bool   `parquet:"has_success"`
	PendEnd    int64  `parquet:"pend_end"`
}

// PromptSession is a session's values the prompt rows and marks join.
type PromptSession struct {
	SessionID string `parquet:"session_id"`
	Product   string `parquet:"product"` // its first step's
	LastNS    int64  `parquet:"last_ns"` // its span's end (the rows' session_last_ns)
	// Its last prompt-row event's time, when it has one.
	HasPromptEvent    bool  `parquet:"has_prompt_event"`
	LastPromptEventNS int64 `parquet:"last_prompt_event_ns"`
}

// The session marks (process sessionmarks.go) as stored, and their prompt
// numbers' lookup.
const (
	TableMarks           = "session_marks"
	TableMarkPromptIndex = "mark_prompt_index"
)

// MarkRow is a mark (process.SessionMark) as stored. Seq is the query's, by
// (group, then the group's order). The permission and network lanes are the
// query's too: a segment per run of one mode (from the permission points) or IP
// (from the call marks' PointIP), each ending where the next starts.
type MarkRow struct {
	process.SessionMark
	// Group: 0 a call, 1 a report (Sub 0 reported, 1 missed), 2 friction, 3 a
	// compaction, 4 a permission point.
	Group    int32  `parquet:"group"`
	Sub      int32  `parquet:"sub"`
	OrdCorr  string `parquet:"ord_corr"`
	OrdSeq   int64  `parquet:"ord_seq"`
	OrdRowNS int64  `parquet:"ord_row_ns"`
	OrdName  string `parquet:"ord_name"`
	OrdMD    string `parquet:"ord_md"`
	OrdKey   string `parquet:"ord_key"`
	// PromptRef is the prompt whose number the mark shows (joined from
	// mark_prompt_index); a frustrated prompt's is its own (PromptIndex).
	PromptRef string `parquet:"prompt_ref"`
	PointIP   string `parquet:"point_ip"` // a call's client IP, for the network lane
}

// MarkPromptIndex is a prompt's number as the marks show it: its newest input
// prompt's (by the session's event order).
type MarkPromptIndex struct {
	SessionID string `parquet:"session_id"`
	PromptID  string `parquet:"prompt_id"`
	Index     int64  `parquet:"prompt_index"`
}
