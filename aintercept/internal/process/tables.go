package process

// The shaped tables (docs/ui/BUILD_PLAN.md §7.5). Every table has a primary key
// and names its references to other tables; every fact row keeps the raw
// correlation_id and source file it came from. Times are Unix nanoseconds.
// `optional` columns are null when the value is the zero value: the raw rows
// omit empty fields, so zero means "not recorded".

// Request is one HTTP transaction, any host, or one model turn on a WebSocket.
// Fact table: rows are only ever added.
//
//	PK  call_id           correlation_id; correlation_id#turn for a WebSocket turn
//	FK  session_id        → sessions
//	FK  prompt_id         → prompts
//	FK  person_id         → people
//	FK  device_id         → person_devices (with person_id)
//	    request_id        the provider's id; analytics events reference it
type Request struct {
	CallID        string `parquet:"call_id"`
	CorrelationID string `parquet:"correlation_id"`
	// SourceFile is the raw file of the row that ended the transaction (its
	// complete, disconnect or WebSocket close), else of its first row.
	SourceFile string `parquet:"source_file"`
	// FirstFile is the raw file of its first row. A transaction's rows all come
	// from one pod, which uploads in order, so they lie in that pod's files
	// from FirstFile to SourceFile. Best effort: when a pod's file is found
	// late (the full listing), a call already written from its ending keeps
	// that file as its first.
	FirstFile       string `parquet:"first_file"`
	WSTurn          int64  `parquet:"ws_turn,optional"`
	Transport       string `parquet:"transport,optional"`
	StartNS         int64  `parquet:"start_ns,optional"`
	ResponseStartNS int64  `parquet:"response_start_ns,optional"`
	EndNS           int64  `parquet:"end_ns,optional"`
	Host            string `parquet:"host,optional"`
	Method          string `parquet:"method,optional"`
	Path            string `parquet:"path,optional"`
	Status          int64  `parquet:"status,optional"`
	Synthetic       bool   `parquet:"synthetic"`
	IP              string `parquet:"ip,optional"`
	KeyHash         string `parquet:"key_hash,optional"`
	KeyHint         string `parquet:"key_hint,optional"`
	Error           string `parquet:"error,optional"`

	SessionID       string `parquet:"session_id,optional"`
	PromptID        string `parquet:"prompt_id,optional"`
	PersonID        string `parquet:"person_id,optional"`
	DeviceID        string `parquet:"device_id,optional"`
	OrgID           string `parquet:"org_id,optional"`
	RequestClass    string `parquet:"request_class,optional"`
	ClientRequestID string `parquet:"client_request_id,optional"`
	RequestID       string `parquet:"request_id,optional"`
	AgentID         string `parquet:"agent_id,optional"`
	AgentType       string `parquet:"agent_type,optional"`
	UserAgent       string `parquet:"user_agent,optional"`

	Model             string `parquet:"model,optional"`
	StopReason        string `parquet:"stop_reason,optional"`
	MessageID         string `parquet:"message_id,optional"`
	PreviousMessageID string `parquet:"previous_message_id,optional"`
	ThreadType        string `parquet:"thread_type,optional"`
	Stream            bool   `parquet:"stream"`
	ServiceTier       string `parquet:"service_tier,optional"`
	InputTokens       int64  `parquet:"input_tokens,optional"`
	OutputTokens      int64  `parquet:"output_tokens,optional"`
	CacheReadTokens   int64  `parquet:"cache_read_tokens,optional"`
	CacheWriteTokens  int64  `parquet:"cache_write_tokens,optional"`
	ReasoningTokens   int64  `parquet:"reasoning_tokens,optional"`
	Effort            string `parquet:"effort,optional"` // the reasoning effort the call asked for
	// DetailJSON is what the call did, read from its recorded bodies (full interception
	// only; events.CallDetail): commands, patches, its first words, earlier calls' exit codes.
	DetailJSON      string `parquet:"detail_json,optional"`
	RequestBytes    int64  `parquet:"request_bytes,optional"`
	ResponseBytes   int64  `parquet:"response_bytes,optional"`
	ContentEncoding string `parquet:"content_encoding,optional"` // of the response
	// The rest of the allowlisted client and provider header values, and the
	// tools the model used, as JSON objects.
	ClientJSON   string `parquet:"client_json,optional"`
	ProviderJSON string `parquet:"provider_json,optional"`
	ToolUsesJSON string `parquet:"tool_uses_json,optional"`

	// Product is the coding harness (claude_code, codex), Client how it was run
	// (cli, exec, tui…) and ClientVersion its version. HiddenKind marks a call
	// the user never sees (Codex prewarm, title): kept, but out of the session's
	// calls and tokens. ExtJSON is what only that product reports, for this call.
	Product       string `parquet:"product,optional"`
	Client        string `parquet:"client,optional"`
	ClientVersion string `parquet:"client_version,optional"`
	HiddenKind    string `parquet:"hidden_kind,optional"`
	ExtJSON       string `parquet:"ext_json,optional"`
}

// Event is one analytics event a client reported. Fact table.
//
//	PK  event_key         correlation_id/client id, or correlation_id/h<hash of the row> when it has none
//	                      (a repeat of an identical row: …/h<hash>-2, -3, …)
//	FK  correlation_id    → requests.call_id: the analytics request that carried it
//	FK  session_id        → sessions
//	FK  prompt_id         → prompts
//	FK  person_id         → people
//	FK  request_id        → requests.request_id: the model call it reports on (null: none,
//	                        or a call that was never captured, which is a coverage gap)
type Event struct {
	EventKey        string `parquet:"event_key"`
	CorrelationID   string `parquet:"correlation_id"`
	SourceFile      string `parquet:"source_file"`
	RowNS           int64  `parquet:"row_ns"`
	NS              int64  `parquet:"ns"`  // the client's clock when it has one, else row_ns
	Seq             int64  `parquet:"seq"` // the raw row's sequence in its transaction
	Source          string `parquet:"source"`
	Name            string `parquet:"name,optional"`
	Type            string `parquet:"type,optional"`
	ClientEventID   string `parquet:"client_event_id,optional"`
	SessionID       string `parquet:"session_id,optional"`
	PromptID        string `parquet:"prompt_id,optional"`
	PersonID        string `parquet:"person_id,optional"`
	OrgID           string `parquet:"org_id,optional"`
	DeviceID        string `parquet:"device_id,optional"`
	AgentID         string `parquet:"agent_id,optional"`
	AgentType       string `parquet:"agent_type,optional"`
	Model           string `parquet:"model,optional"`
	RequestID       string `parquet:"request_id,optional"`
	ClientRequestID string `parquet:"client_request_id,optional"`
	ToolUseID       string `parquet:"tool_use_id,optional"`
	PromptIndex     int64  `parquet:"prompt_index,optional"`
	PermissionMode  string `parquet:"permission_mode,optional"`
	HeadSHA         string `parquet:"head_sha,optional"`
	MetadataJSON    string `parquet:"metadata_json,optional"` // data.additional_metadata
	EnvJSON         string `parquet:"env_json,optional"`      // data.env
}

// Session is one coding-agent session: a Claude Code session or a Codex thread.
// Snapshot table: rewritten as sessions grow.
//
//	PK  session_id
//	FK  person_id         → people (null: unattributed)
type Session struct {
	SessionID        string `parquet:"session_id"`
	PersonID         string `parquet:"person_id,optional"`
	OrgID            string `parquet:"org_id,optional"`
	DeviceID         string `parquet:"device_id,optional"`
	FirstNS          int64  `parquet:"first_ns"`
	LastNS           int64  `parquet:"last_ns"`
	FirstCallNS      int64  `parquet:"first_call_ns,optional"`
	LastCallNS       int64  `parquet:"last_call_ns,optional"`
	FirstEventNS     int64  `parquet:"first_event_ns,optional"`
	LastEventNS      int64  `parquet:"last_event_ns,optional"`
	Calls            int64  `parquet:"calls"`
	CallsWithUsage   int64  `parquet:"calls_with_usage"`
	Events           int64  `parquet:"events"`
	Prompts          int64  `parquet:"prompts"`
	InputTokens      int64  `parquet:"input_tokens"`
	OutputTokens     int64  `parquet:"output_tokens"`
	CacheReadTokens  int64  `parquet:"cache_read_tokens"`
	CacheWriteTokens int64  `parquet:"cache_write_tokens"`

	Product       string `parquet:"product,optional"`
	Client        string `parquet:"client,optional"`
	ClientVersion string `parquet:"client_version,optional"`
	HiddenCalls   int64  `parquet:"hidden_calls"`
	// Where the thread came from (Codex): subagent or review of a parent
	// thread's turn, or a fork of a thread at a turn ordinal. Init is how its
	// history began when not new (warm_fork, cold_fork, cold_resume).
	OriginKind      string `parquet:"origin_kind,optional"`
	OriginSessionID string `parquet:"origin_session_id,optional"`
	OriginTurnID    string `parquet:"origin_turn_id,optional"`
	OriginTurn      int64  `parquet:"origin_turn,optional"`
	Init            string `parquet:"init,optional"`
	AgentName       string `parquet:"agent_name,optional"`
}

// Prompt is one prompt in a session: a Claude Code prompt or a Codex turn. Snapshot table.
//
//	PK  prompt_id
//	FK  session_id        → sessions
type Prompt struct {
	PromptID    string `parquet:"prompt_id"`
	SessionID   string `parquet:"session_id,optional"`
	PromptIndex int64  `parquet:"prompt_index,optional"`
	FirstNS     int64  `parquet:"first_ns"`
	LastNS      int64  `parquet:"last_ns"`
	Calls       int64  `parquet:"calls"`
	Events      int64  `parquet:"events"`
}

// PersonDevice links a person to a device they used. Snapshot table.
//
//	PK  (person_id, device_id)
//	FK  person_id         → people
type PersonDevice struct {
	PersonID string `parquet:"person_id"`
	DeviceID string `parquet:"device_id"`
	FirstNS  int64  `parquet:"first_ns"`
	LastNS   int64  `parquet:"last_ns"`
}

// Account names a person: Claude Code's account endpoints (/api/oauth/profile,
// /api/claude_cli/bootstrap) or a JWT's claims carry the email and name. Snapshot
// table, latest-seen per account.
//
//	PK  account_id (= person_id)
type Account struct {
	AccountID string `parquet:"account_id"`
	Email     string `parquet:"email,optional"`
	Name      string `parquet:"name,optional"`
	Org       string `parquet:"org,optional"`
	SeenNS    int64  `parquet:"seen_ns"`
	EmailNS   int64  `parquet:"email_ns,optional"` // when each field was last seen (0: before these were kept)
	NameNS    int64  `parquet:"name_ns,optional"`
	OrgNS     int64  `parquet:"org_ns,optional"`
}

// Person is everyone seen by account id. Snapshot table, derived from sessions
// and person_devices each run.
//
//	PK  person_id
type Person struct {
	PersonID string `parquet:"person_id"`
	FirstNS  int64  `parquet:"first_ns"`
	LastNS   int64  `parquet:"last_ns"`
	Sessions int64  `parquet:"sessions"`
	Calls    int64  `parquet:"calls"`
	Events   int64  `parquet:"events"`
	Devices  int64  `parquet:"devices"`
}

// Pending is a transaction whose headers were seen but whose ending (complete,
// disconnect, WebSocket close) hasn't been yet, or, with Done, one that has
// ended recently: a marker so its rows arriving late (another file, another
// run) are recognized and not written twice. Processor state, committed with
// each run so a restart resumes it.
type Pending struct {
	CorrelationID string `parquet:"correlation_id"`
	SourceFile    string `parquet:"source_file,optional"`
	SeenNS        int64  `parquet:"seen_ns"` // its first row, or for Done its ending
	Done          bool   `parquet:"done"`
	// Expired marks a Done transaction that was written without its ending
	// (it waited past pendingTTL): its marker is kept for expiredTTL.
	Expired bool `parquet:"expired,optional"`
	// A disconnect that arrived before the request's headers: the row is
	// written, with this ending, once they arrive.
	EndNS           int64  `parquet:"end_ns,optional"`
	Error           string `parquet:"error,optional"`
	EndFile         string `parquet:"end_file,optional"`
	StartNS         int64  `parquet:"start_ns,optional"`
	ResponseStartNS int64  `parquet:"response_start_ns,optional"`
	Host            string `parquet:"host,optional"`
	Method          string `parquet:"method,optional"`
	Path            string `parquet:"path,optional"`
	Status          int64  `parquet:"status,optional"`
	Synthetic       bool   `parquet:"synthetic"`
	IP              string `parquet:"ip,optional"`
	KeyHash         string `parquet:"key_hash,optional"`
	KeyHint         string `parquet:"key_hint,optional"`
	RequestBytes    int64  `parquet:"request_bytes,optional"`
	ResponseBytes   int64  `parquet:"response_bytes,optional"`
	WebSocket       bool   `parquet:"websocket"`
	// Header values the request row needs when its ending doesn't carry them
	// (rows from before the observer put them on `complete`).
	ClientJSON      string `parquet:"client_json,optional"`
	RequestID       string `parquet:"request_id,optional"`
	ContentEncoding string `parquet:"content_encoding,optional"`
	ProviderJSON    string `parquet:"provider_json,optional"` // allowlisted response header values
}

// PendingBodies is an in-flight model call's recorded bodies so far (full
// interception only), so a restart mid-call still builds its complete
// detail_json. Processor state, like pending's in-flight rows; the API never
// reads it. Snapshot table.
type PendingBodies struct {
	CorrelationID    string `parquet:"correlation_id"`
	Request          []byte `parquet:"request,optional"`
	Response         []byte `parquet:"response,optional"`
	RequestEncoding  string `parquet:"request_encoding,optional"`
	ResponseEncoding string `parquet:"response_encoding,optional"`
}

// Ledger records each raw file a committed run processed: what makes a file
// processed exactly once. Fact table.
//
//	PK  raw_key
type Ledger struct {
	RawKey string `parquet:"raw_key"`
	Size   int64  `parquet:"size"`
	Rows   int64  `parquet:"rows"`
	Bad    int64  `parquet:"bad"` // lines that weren't a JSON row
	Run    int64  `parquet:"run"`
}

// RequestRow is one row of a person's request log (the API's RequestRow,
// docs/ui/handoff §4 RequestRowData), with every value already as the UI shows
// it. Snapshot table, recomputed from all the committed data each run
// (requestrows.go; docs/architecture.md, Decision 4).
//
//	PK  (person_id, request_id)
//	FK  person_id         → people / accounts (or "unattributed": sessions no account appears on)
//	FK  request_id        → requests.call_id, '#' written '~'; "reported:<id>" for a call
//	                        Claude Code reported that was never captured
//	FK  session_id        → sessions
//
// A transaction belongs to every person one of whose sessions used its
// credential (key_hash), so it can have a row for several people.
//
// Columns are RequestRow's fields in snake_case, nullable exactly where it is
// (pointers: null is nil, never the zero value), so the API maps a row to JSON
// by copying fields. Three fields are spread over columns:
//   - status: `status` holds the HTTP status (null when none); `status_err` is
//     true for a transaction that ended without one, which RequestRow shows as
//     the string "ERR".
//   - tokens: the tokens_* columns; tokens_accuracy is null when the row has
//     no tokens (it isn't a model call).
//   - error: the error_* columns; error_message is null when the row has no
//     error, and is never null when it has one.
//
// t is ts_ns (Unix ns; the API truncates it to microseconds), also the sort
// key; error.retrySucceededAt is error_retry_succeeded_ns.
type RequestRow struct {
	PersonID     string   `parquet:"person_id"`
	RequestID    string   `parquet:"request_id"`
	TsNS         int64    `parquet:"ts_ns"`
	Kind         string   `parquet:"kind"`
	Agent        *string  `parquet:"agent,optional"`
	Client       *string  `parquet:"client,optional"`
	HiddenKind   *string  `parquet:"hidden_kind,optional"`
	SessionID    *string  `parquet:"session_id,optional"`
	SessionLabel *string  `parquet:"session_label,optional"` // null until M7 extraction
	PromptIndex  *int64   `parquet:"prompt_index,optional"`
	Method       *string  `parquet:"method,optional"`
	Path         string   `parquet:"path"`
	Detail       string   `parquet:"detail"`
	Status       *int64   `parquet:"status,optional"`
	StatusErr    bool     `parquet:"status_err"`
	DurationMS   *float64 `parquet:"duration_ms,optional"`
	Retries      *int64   `parquet:"retries,optional"`
	BytesUp      *int64   `parquet:"bytes_up,optional"`
	BytesDown    *int64   `parquet:"bytes_down,optional"`
	Background   bool     `parquet:"background"`
	Captured     bool     `parquet:"captured"`
	StatusText   *string  `parquet:"status_text,optional"`
	StopReason   *string  `parquet:"stop_reason,optional"`
	TTFTMS       *float64 `parquet:"ttft_ms,optional"`

	TokensInput        *int64  `parquet:"tokens_input,optional"`
	TokensCacheRead    *int64  `parquet:"tokens_cache_read,optional"`
	TokensCacheWrite   *int64  `parquet:"tokens_cache_write,optional"`
	TokensCacheWrite5m *int64  `parquet:"tokens_cache_write_5m,optional"`
	TokensCacheWrite1h *int64  `parquet:"tokens_cache_write_1h,optional"`
	TokensOutput       *int64  `parquet:"tokens_output,optional"`
	TokensAccuracy     *string `parquet:"tokens_accuracy,optional"` // exact, missing; null: no tokens

	ContentEncoding *string `parquet:"content_encoding,optional"`

	ErrorMessage          *string  `parquet:"error_message,optional"` // null: no error
	ErrorAfterMS          *float64 `parquet:"error_after_ms,optional"`
	ErrorBytesReceived    *int64   `parquet:"error_bytes_received,optional"`
	ErrorRetries          *int64   `parquet:"error_retries,optional"`
	ErrorRetrySucceededNS *int64   `parquet:"error_retry_succeeded_ns,optional"`

	Model        *string  `parquet:"model,optional"`
	Effort       *string  `parquet:"effort,optional"`
	StepIndex    *int64   `parquet:"step_index,optional"`
	ToolCalls    []string `parquet:"tool_calls,list"` // "exec ×2", in order; empty, never null
	UsageMissing *string  `parquet:"usage_missing,optional"`
	Text         *string  `parquet:"text,optional"`
}

// PersonLast is a person's newest data time (the person request log's
// lastDataAt): the latest of their sessions' visible model calls and Claude Code
// events. Null when they have no session. Derived with request_rows.
type PersonLast struct {
	PersonID string `parquet:"person_id"`
	LastNS   *int64 `parquet:"last_ns,optional"`
}

// PromptRow is one item of a session's prompt list (the API's PromptItem or
// BetweenItem, docs/ui/handoff §5.2.9): a prompt, with its steps in
// prompt_steps, or a between row (a compaction, a recap, a model call outside
// any prompt). Snapshot table, recomputed from all the committed data each run
// (promptrows.go; docs/architecture.md, Decision 4).
//
//	PK  (session_id, seq)
//	FK  session_id  → sessions
//	FK  prompt_id   → prompts (or "input:<event id>" for a prompt sent with none)
//	FK  request_id  → requests.call_id, '#' written '~'
//
// seq is the item's place in the session's list. What depends on the time of the
// request is stored as its inputs (promptrows.go says how the API combines
// them): session_last_ns for live; open_tail and unfinished_max_ns for inFlight;
// duration_turn_ms and duration_report_ms for durationMs. typed: the prompt's
// tengu_input_prompt was captured (or it's a Codex turn): the timelines' marks.
// The *_json columns hold the API's JSON for a dict or object, in its order.
type PromptRow struct {
	SessionID     string `parquet:"session_id"`
	Seq           int64  `parquet:"seq"`
	TNS           int64  `parquet:"t_ns"`
	Type          string `parquet:"type"` // prompt, between
	Typed         bool   `parquet:"typed"`
	SessionLastNS int64  `parquet:"session_last_ns"`

	// A prompt's (null for a between row)
	PromptID         *string  `parquet:"prompt_id,optional"`
	PromptIndex      *int64   `parquet:"prompt_index,optional"`
	DurationTurnMS   *float64 `parquet:"duration_turn_ms,optional"`
	DurationReportMS *float64 `parquet:"duration_report_ms,optional"`
	OpenTail         bool     `parquet:"open_tail"`
	UnfinishedMaxNS  *int64   `parquet:"unfinished_max_ns,optional"`
	TextRetained     bool     `parquet:"text_retained"`
	Text             *string  `parquet:"text,optional"` // content: only in full interception mode
	TextChars        *int64   `parquet:"text_chars,optional"`
	PastedBytes      *int64   `parquet:"pasted_bytes,optional"`
	Images           *int64   `parquet:"images,optional"`
	Steps            int64    `parquet:"steps"`
	TokensIn         *int64   `parquet:"tokens_in,optional"`
	TokensOut        *int64   `parquet:"tokens_out,optional"`
	ToolCountsJSON   string   `parquet:"tool_counts_json"`
	Frustrated       bool     `parquet:"frustrated"`
	Interrupted      bool     `parquet:"interrupted"`
	Destructive      int64    `parquet:"destructive"`
	TurnReportJSON   *string  `parquet:"turn_report_json,optional"`
	HiddenJSON       string   `parquet:"hidden_json"`

	// A between row's (null for a prompt)
	BetweenKind *string  `parquet:"between_kind,optional"`
	Detail      *string  `parquet:"detail,optional"`
	RequestID   *string  `parquet:"request_id,optional"`
	TokensFrom  *int64   `parquet:"tokens_from,optional"`
	TokensTo    *int64   `parquet:"tokens_to,optional"`
	WaitMS      *float64 `parquet:"wait_ms,optional"`
}

// PromptStep is one step of a prompt (the API's StepData): a captured model
// call carrying the prompt's id. Snapshot table, derived with prompt_rows.
//
//	PK  (session_id, seq, step)
//	FK  (session_id, seq)  → prompt_rows
//	FK  request_id         → requests.call_id, '#' written '~'
//
// Its status is the API's from end_ns, failed and the session's liveness; its
// events are pending when live and events_pending_if_live. While the session is
// live, tools_live_json (when not null) replaces tools_json, and when
// content_live the *_live lists replace commands and exit_codes.
type PromptStep struct {
	SessionID string `parquet:"session_id"`
	Seq       int64  `parquet:"seq"`
	Step      int64  `parquet:"step"`
	RequestID string `parquet:"request_id"`
	TNS       int64  `parquet:"t_ns"`
	EndNS     *int64 `parquet:"end_ns,optional"`

	Kind                string   `parquet:"kind"` // model, subagent
	Model               *string  `parquet:"model,optional"`
	RequestClass        *string  `parquet:"request_class,optional"`
	TokensInput         *int64   `parquet:"tokens_input,optional"`
	TokensCacheRead     *int64   `parquet:"tokens_cache_read,optional"`
	TokensCacheWrite    *int64   `parquet:"tokens_cache_write,optional"`
	TokensCacheWrite5m  *int64   `parquet:"tokens_cache_write_5m,optional"`
	TokensCacheWrite1h  *int64   `parquet:"tokens_cache_write_1h,optional"`
	TokensOutput        *int64   `parquet:"tokens_output,optional"`
	TokensAccuracy      string   `parquet:"tokens_accuracy"`
	DurationMS          *float64 `parquet:"duration_ms,optional"`
	TTFTMS              *float64 `parquet:"ttft_ms,optional"`
	StopReason          *string  `parquet:"stop_reason,optional"`
	CostUSD             *float64 `parquet:"cost_usd,optional"`
	Failed              bool     `parquet:"failed"` // ended with no status or an HTTP error
	EventsPendingIfLive bool     `parquet:"events_pending_if_live"`
	AgentType           *string  `parquet:"agent_type,optional"`
	ToolsJSON           string   `parquet:"tools_json"`
	ToolsLiveJSON       *string  `parquet:"tools_live_json,optional"`
	Does                string   `parquet:"does"`
	Commands            []string `parquet:"commands,list"`
	ExitCodes           []int64  `parquet:"exit_codes,list"`
	ContentLive         bool     `parquet:"content_live"`
	CommandsLive        []string `parquet:"commands_live,list"`
	ExitCodesLive       []int64  `parquet:"exit_codes_live,list"`
	PatchesJSON         string   `parquet:"patches_json"`
	Text                *string  `parquet:"text,optional"`
	UsageMissing        *string  `parquet:"usage_missing,optional"`
}

// SessionRow is one session (the API's SessionRow, TimelineSession band and
// SessionHeader), with everything that doesn't depend on the time of the request.
// Snapshot table, derived with request_rows (sessionrows.go): live is
// now - last_ns <= 5 minutes, and a live session has no end. A session is one
// with a visible model call or a Claude Code event.
//
//	PK  session_id
//
// label_*: the session list's and person timeline's label; label_*_alone the
// session page's (a Codex title thread can name a different thread when the
// session loads alone: claude_code.py _attach_titles). origin_kind and
// origin_session_id nest subagent and review threads under their parent; the
// *_json columns hold the API's JSON for an object. cov_window_*: the
// not-captured windows, parallel lists.
type SessionRow struct {
	SessionID     string  `parquet:"session_id"`
	StartNS       int64   `parquet:"start_ns"`
	LastNS        int64   `parquet:"last_ns"`
	Product       string  `parquet:"product,optional"`
	Agent         string  `parquet:"agent"`
	Client        *string `parquet:"client,optional"`
	ClientVersion *string `parquet:"client_version,optional"`

	OriginKind      *string `parquet:"origin_kind,optional"`
	OriginSessionID *string `parquet:"origin_session_id,optional"`
	OriginJSON      *string `parquet:"origin_json,optional"`
	ChildrenJSON    *string `parquet:"children_json,optional"`
	HiddenCalls     int64   `parquet:"hidden_calls"`

	Title            *string `parquet:"-"`
	TitleAlone       *string `parquet:"-"`
	LabelText        string  `parquet:"label_text"`
	LabelSource      string  `parquet:"label_source"`
	LabelTextAlone   string  `parquet:"label_text_alone"`
	LabelSourceAlone string  `parquet:"label_source_alone"`
	Repo             *string `parquet:"repo,optional"`
	Commit           *string `parquet:"commit,optional"`

	Prompts            *int64   `parquet:"prompts,optional"`
	CapturedFromPrompt *int64   `parquet:"captured_from_prompt,optional"`
	ModelCalls         int64    `parquet:"model_calls"`
	Events             *int64   `parquet:"events,optional"`
	Frustrated         int64    `parquet:"frustrated"`
	Interrupted        int64    `parquet:"interrupted"`
	Destructive        int64    `parquet:"destructive"`
	BypassPct          *float64 `parquet:"bypass_pct,optional"`
	CovReported        *int64   `parquet:"cov_reported,optional"`
	CovCaptured        int64    `parquet:"cov_captured"`
	CovNotCaptured     int64    `parquet:"cov_not_captured"`
	CovWindowFrom      []int64  `parquet:"cov_window_from,list"`
	CovWindowTo        []int64  `parquet:"cov_window_to,list"`
	CovWindowCount     []int64  `parquet:"cov_window_count,list"`
	FieldsMissing      bool     `parquet:"fields_missing"`

	// The page header's
	PersonID         *string `parquet:"person_id,optional"`
	DeviceID         *string `parquet:"device_id,optional"`
	DevicePlatform   *string `parquet:"device_platform,optional"`
	DeviceTerminal   *string `parquet:"device_terminal,optional"`
	DeviceShell      *string `parquet:"device_shell,optional"`
	Networks         *int64  `parquet:"networks,optional"`
	Effort           *string `parquet:"effort,optional"`
	TokensInput      *int64  `parquet:"tokens_input,optional"`
	TokensCacheRead  *int64  `parquet:"tokens_cache_read,optional"`
	TokensCacheWrite *int64  `parquet:"tokens_cache_write,optional"`
	TokensOutput     *int64  `parquet:"tokens_output,optional"`
	TokensAccuracy   string  `parquet:"tokens_accuracy"`
	ToolCalls        *int64  `parquet:"tool_calls,optional"`
	HasFacts         bool    `parquet:"has_facts"` // a Codex thread's SessionFacts
	FactsCwd         *string `parquet:"facts_cwd,optional"`
	FactsApproval    *string `parquet:"facts_approval,optional"`
	FactsSandbox     *string `parquet:"facts_sandbox,optional"`
	LimitsJSON       *string `parquet:"limits_json,optional"` // AgentLimits on its newest Codex response
	LimitsNS         *int64  `parquet:"limits_ns,optional"`   // … that response's time

	// What the person pages roll up over a person's sessions (personmap.go;
	// claude_code.py Session as directory.py and the person header read it).
	PromptIDs       []string `parquet:"prompt_ids,list"`      // claude_code.py prompt_ids
	Devices         []string `parquet:"devices,list"`         // its calls' and sessionEventNames' devices
	CallAccounts    []string `parquet:"call_accounts,list"`   // its visible calls' accounts
	OutputsKnown    int64    `parquet:"outputs_known"`        // visible calls with an output count
	Queries         int64    `parquet:"queries"`              // tengu_api_query with a permission mode,
	Bypassed        int64    `parquet:"bypassed"`             // … bypassPermissions
	CLIVersion      *string  `parquet:"cli_version,optional"` // the newest claude-cli call's version ("" for none)
	CLIVersionNS    *int64   `parquet:"cli_version_ns,optional"`
	ClientVersionNS *int64   `parquet:"client_version_ns,optional"` // the time of the call client_version is from
	RLStatus        *string  `parquet:"rl_status,optional"`         // the newest response's unified rate-limit status,
	RLUtil          *float64 `parquet:"rl_util,optional"`           // 7-day utilization
	RLOverage       *string  `parquet:"rl_overage,optional"`
	RLNS            *int64   `parquet:"rl_ns,optional"`   // … that response's coalesce(response_start_ns, ts)
	Sandbox         *string  `parquet:"sandbox,optional"` // the newest Bash event's sandbox_enabled
	SandboxNS       *int64   `parquet:"sandbox_ns,optional"`
	CustomCA        bool     `parquet:"custom_ca"` // a tengu_api_success's gzipSkipReason is 'custom_ca'
}

// SessionMark is one mark of a session's timeline. Snapshot table, derived with
// prompt_rows (sessionmarks.go; docs/architecture.md, Decision 4).
//
//	PK  (session_id, seq)
//	FK  session_id  → sessions
//	FK  request_id  → requests.call_id, '#' written '~'
//
// kind:
//   - call: a visible model call (request_id, request_class, model, context and
//     output tokens, its prompt's number).
//   - reported: a tengu_api_success; missed: one no captured call matches.
//   - friction: friction_kind (frustrated, interrupted, destructive), its prompt's
//     number and, for a destructive command, detail.
//   - compaction: the prompt list's compaction between row.
//   - permission, network: a state band's segment, from t_ns to to_ns (the next
//     segment's start; null for the last, which ends where the timeline does).
//
// seq orders a session's marks of a kind.
type SessionMark struct {
	SessionID string `parquet:"session_id"`
	Seq       int64  `parquet:"seq"`
	Kind      string `parquet:"kind"`
	TNS       int64  `parquet:"t_ns"`
	ToNS      *int64 `parquet:"to_ns,optional"`

	RequestID    *string `parquet:"request_id,optional"`
	RequestClass *string `parquet:"request_class,optional"`
	Model        *string `parquet:"model,optional"`
	Context      *int64  `parquet:"context,optional"`
	Output       *int64  `parquet:"output,optional"`
	PromptIndex  *int64  `parquet:"prompt_index,optional"`
	// A call's, for the person summary: its usage, prompt, and whether no
	// tengu_api_success reports it (its cost is unknown).
	Input      *int64  `parquet:"input,optional"`
	CacheRead  *int64  `parquet:"cache_read,optional"`
	CacheWrite *int64  `parquet:"cache_write,optional"`
	PromptID   *string `parquet:"prompt_id,optional"`
	Unreported bool    `parquet:"unreported"`

	FrictionKind *string `parquet:"friction_kind,optional"`
	Detail       *string `parquet:"detail,optional"`

	TokensFrom *int64   `parquet:"tokens_from,optional"`
	TokensTo   *int64   `parquet:"tokens_to,optional"`
	Idle       bool     `parquet:"idle"`
	WaitMS     *float64 `parquet:"wait_ms,optional"`

	Mode     *string `parquet:"mode,optional"`
	PrevMode *string `parquet:"prev_mode,optional"`
	IP       *string `parquet:"ip,optional"`
	IPv6     bool    `parquet:"ipv6"`
}

// PresenceSegment is a presence_segments row (presence.go): a session's focused
// or away span, or one of its sleep windows; [from_ns, to_ns).
type PresenceSegment struct {
	SessionID string `parquet:"session_id"`
	Seq       int64  `parquet:"seq"`
	FromNS    int64  `parquet:"from_ns"`
	ToNS      int64  `parquet:"to_ns"`
	State     string `parquet:"state"`
}
