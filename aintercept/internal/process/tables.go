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
