-- +goose Up
-- The schema (docs/PIPELINE.md §3, §5): the facts, the dirty list and the rollups. Hand-written.
-- Applied by pg.Apply to an empty database, which records this file's hash (SchemaHash) in
-- schema_version. There are no migrations: a change to this file means a fresh database. Only the
-- flush (internal/flush) writes the facts, but for the open-call sweep (§5.5); only the refresher
-- (internal/refresh) writes the rollups. Plain DDL only, one statement per line-ending semicolon
-- (the API's tests split the file there).

-- §3.3 The schema's hash (pg.Apply).
CREATE TABLE schema_version (
    hash text NOT NULL
);

-- §3.3 One row per observer pod: its newest record (/status's lastDataAt).
CREATE TABLE ingest_pods (
    incarnation text NOT NULL,
    last_ns bigint,
    at timestamptz,
    PRIMARY KEY (incarnation)
);

-- §3.1 One row per HTTP call. A start inserts it, its end fills it in, and a start never
-- overwrites a row that has an end. raw_key, raw_offset and raw_len are the call's gzip member
-- in its raw file (§2), filled in by ingest from step 3, NULL from canon -load.
CREATE TABLE calls (
    call_id text PRIMARY KEY,
    correlation_id text NOT NULL,
    session_id text,
    scope_session_id text,
    prompt_id text,
    account_id text,
    device_id text,
    org_id text,
    agent_id text,
    agent_type text,
    key_hash text,
    key_hint text,
    source text,
    client_mode text,
    client_version text,
    kind text NOT NULL,
    request_class text,
    host text,
    method text,
    path text,
    status integer,
    error text,
    ip text,
    transport text,
    ws_turn integer,
    start_ns bigint,
    response_start_ns bigint,
    end_ns bigint,
    swept boolean NOT NULL DEFAULT false,
    model text,
    effort text,
    stop_reason text,
    thread_type text,
    stream boolean NOT NULL,
    service_tier text,
    input_tokens bigint,
    output_tokens bigint,
    cache_read_tokens bigint,
    cache_write_tokens bigint,
    reasoning_tokens bigint,
    request_bytes bigint,
    response_bytes bigint,
    content_encoding text,
    user_agent text,
    tool_uses jsonb,
    -- The request's tool list (handoff §11S): its hash, the list itself in tool_lists.
    tools_list text,
    detail jsonb,
    client jsonb,
    provider jsonb,
    ext jsonb,
    hidden_kind text,
    synthetic boolean NOT NULL,
    request_id text,
    client_request_id text,
    message_id text,
    parent_message_id text,
    raw_key text,
    raw_offset bigint,
    raw_len bigint
);
CREATE INDEX ON calls (scope_session_id, start_ns);
CREATE INDEX ON calls (session_id, start_ns);
CREATE INDEX ON calls (prompt_id, start_ns);
CREATE INDEX ON calls (key_hash, start_ns);
CREATE INDEX ON calls (request_id);
CREATE INDEX ON calls (client_request_id);
CREATE INDEX ON calls (message_id);
CREATE INDEX ON calls (parent_message_id);
CREATE INDEX ON calls (start_ns) WHERE end_ns IS NULL;
CREATE INDEX ON calls (start_ns) WHERE NOT synthetic;

-- §3.3 Identity. Each value with an _ns column is the newest sighting that says it (the greater
-- value at the same time); first_ns and last_ns are the earliest and latest.
CREATE TABLE accounts (
    account_id text PRIMARY KEY,
    email text,
    email_ns bigint,
    name text,
    name_ns bigint,
    org text,
    org_ns bigint,
    codex_plan text,
    codex_plan_ns bigint,
    seen_ns bigint NOT NULL
);

-- Who each credential belongs to (credential records): many credentials to one account. key_hash
-- is the salted hash, never the credential. account_id, source (lookup | observed) and key_hint
-- are the newest sighting's; first_ns and verified_ns the earliest and latest.
CREATE TABLE credentials (
    key_hash text PRIMARY KEY,
    key_hint text NOT NULL,
    account_id text NOT NULL,
    source text NOT NULL,
    first_ns bigint NOT NULL,
    verified_ns bigint NOT NULL
);
CREATE INDEX ON credentials (account_id);

CREATE TABLE session_accounts (
    session_id text NOT NULL,
    account_id text NOT NULL,
    first_ns bigint NOT NULL,
    last_ns bigint NOT NULL,
    PRIMARY KEY (session_id, account_id)
);
CREATE INDEX ON session_accounts (account_id, first_ns);

CREATE TABLE session_credentials (
    session_id text NOT NULL,
    key_hash text NOT NULL,
    first_ns bigint NOT NULL,
    last_ns bigint NOT NULL,
    PRIMARY KEY (session_id, key_hash)
);

CREATE TABLE account_devices (
    account_id text NOT NULL,
    device_id text NOT NULL,
    agent text,
    agent_ns bigint,
    version text,
    version_ns bigint,
    first_ns bigint NOT NULL,
    last_ns bigint NOT NULL,
    PRIMARY KEY (account_id, device_id)
);

-- §3.3 A session's parent (a subagent's, a Codex thread's).
CREATE TABLE session_edges (
    child_session_id text NOT NULL,
    parent_session_id text NOT NULL,
    kind text NOT NULL,
    first_ns bigint NOT NULL,
    PRIMARY KEY (child_session_id, parent_session_id)
);
-- A session's children (§5.4), by its id.
CREATE INDEX ON session_edges (parent_session_id);

-- §5.1 The rollup rows to recompute. seq changes each time a row is marked again; since is
-- when it was first marked, or marked again during its refresh (a busy row's refresh waits for
-- it). The refresher claims oldest first, and /lag is the oldest.
CREATE TABLE dirty (
    kind text NOT NULL,
    key text NOT NULL,
    seq bigint GENERATED BY DEFAULT AS IDENTITY,
    since timestamptz NOT NULL DEFAULT now(),
    claimed_at timestamptz,
    PRIMARY KEY (kind, key)
);
CREATE INDEX ON dirty (since);

-- §5.5 Retention deletes facts by time: block-range indexes, nearly free to keep (rows arrive
-- roughly in time order).
CREATE INDEX ON calls USING brin (start_ns);

-- §5.2 Rollups, written only by the refresher (internal/refresh), each row recomputed from the
-- facts (or a lower rollup) for one dirty key. Times are UTC minute and hour starts in ns.

-- A session's minute: its visible model calls (cc_calls) by start, else end, and its Requests
-- tab rows (by scope_session_id) per kind. A NULL sum had nothing to add (as today's marks: a zero token count is NULL).
CREATE TABLE session_minutes (
    session_id text NOT NULL,
    minute_ns bigint NOT NULL,
    model_calls bigint NOT NULL,
    main_calls bigint NOT NULL,
    subagent_calls bigint NOT NULL,
    hidden_calls bigint NOT NULL,
    input_tokens bigint,
    cache_read_tokens bigint,
    cache_write_tokens bigint,
    output_tokens bigint,
    outputs_known bigint NOT NULL,
    context_sum bigint,
    tool_calls bigint,
    requests jsonb NOT NULL,
    requests_hidden jsonb NOT NULL,
    PRIMARY KEY (session_id, minute_ns)
);

-- A session's hour: the sums of its minutes.
CREATE TABLE session_hours (
    session_id text NOT NULL,
    hour_ns bigint NOT NULL,
    model_calls bigint NOT NULL,
    main_calls bigint NOT NULL,
    subagent_calls bigint NOT NULL,
    hidden_calls bigint NOT NULL,
    input_tokens bigint,
    cache_read_tokens bigint,
    cache_write_tokens bigint,
    output_tokens bigint,
    outputs_known bigint NOT NULL,
    context_sum bigint,
    tool_calls bigint,
    requests jsonb NOT NULL,
    requests_hidden jsonb NOT NULL,
    PRIMARY KEY (session_id, hour_ns)
);

-- The distinct things a session's minute holds, each with how often it occurs: what the
-- minute's facts give, and what a bin under an hour reads (the timeline's top event names).
CREATE TABLE session_minute_keys (
    session_id text NOT NULL,
    minute_ns bigint NOT NULL,
    kind text NOT NULL,
    key text NOT NULL,
    n bigint NOT NULL,
    PRIMARY KEY (session_id, minute_ns, kind, key)
);

-- The same for a session's hour: the sums of its minutes' rows. A range's distinct count is
-- count(DISTINCT key) over its rows.
CREATE TABLE session_keys (
    session_id text NOT NULL,
    hour_ns bigint NOT NULL,
    kind text NOT NULL,
    key text NOT NULL,
    n bigint NOT NULL,
    PRIMARY KEY (session_id, hour_ns, kind, key)
);
-- A session's first and newest hour with a key: where its session refresh reads a newest value
-- (kind 'has', refresh/sessions.go hasKeys).
CREATE INDEX ON session_keys (session_id, kind, key, hour_ns);

-- A credential's coding-agent calls (Claude Code's and Codex's, not probes) per minute and
-- hour: n of them, n_tx the request log's transactions (a WebSocket's turns, not its
-- connection). key_hash '' is the calls without one.
CREATE TABLE cred_minutes (
    key_hash text NOT NULL,
    minute_ns bigint NOT NULL,
    kind text NOT NULL,
    hidden boolean NOT NULL,
    agent text NOT NULL,
    n bigint NOT NULL,
    n_tx bigint NOT NULL,
    PRIMARY KEY (key_hash, minute_ns, kind, hidden, agent)
);

CREATE TABLE cred_hours (
    key_hash text NOT NULL,
    hour_ns bigint NOT NULL,
    kind text NOT NULL,
    hidden boolean NOT NULL,
    agent text NOT NULL,
    n bigint NOT NULL,
    n_tx bigint NOT NULL,
    PRIMARY KEY (key_hash, hour_ns, kind, hidden, agent)
);
CREATE INDEX ON cred_hours (hour_ns);

-- A session's calls with no prompt or a hidden kind (the Prompts page's call rows and hidden
-- counts): what a session refresh reads, without the rest of the session's calls.
CREATE INDEX ON calls (session_id, start_ns) WHERE prompt_id IS NULL OR hidden_kind IS NOT NULL;
-- A session's calls by their time (start, else end: today's row time), newest or earliest first:
-- the session and day refreshes' newest values and first and last calls.
CREATE INDEX ON calls (session_id, (coalesce(start_ns, end_ns)));

-- A call's recorded tool calls (detail's calls, by id) and the results it carries back (detail's
-- results, keyed by tool call id): a step's exit codes are its session's first result for each
-- of its tool calls, which a later prompt's call can carry (the prompt after an interrupt). The
-- prompt refresh finds that result, and the flush the prompt to mark, one lookup per id. Without
-- GIN's pending list (fastupdate), so a lookup never scans one.
CREATE INDEX ON calls USING gin ((detail -> 'results')) WITH (fastupdate = off);
CREATE INDEX ON calls USING gin ((detail -> 'calls') jsonb_path_ops) WITH (fastupdate = off);

-- A Codex thread's label (§6.1): with no title of its own, the earliest title call of its accounts
-- in its first five minutes, by account and time.
CREATE INDEX ON calls (account_id, start_ns) WHERE hidden_kind = 'title';

-- A step (docs/PIPELINE.md §5.2): one per visible model call with a prompt, keyed by its call.
-- A step's number in its prompt isn't stored: it's its place by (t_ns, ord), counted at read
-- (ord is its call id with '#' written '~', compared byte-wise). end_ns is a real end (not the
-- sweep's). Everything here is read from the call itself: its tool calls (tool_uses: names and
-- counts; tool_counts by name), and with recorded content (detail) their commands, files and
-- patches, and the exit codes or errors their results carry back in a later call, matched by tool
-- call id.
-- interrupted: the call stopped on an interrupt; follows_interrupt: its request carries the
-- harness's note that the turn before it was interrupted. prompt_chars is set when its request's
-- new messages say something (detail's said: the person's words and the harness's notes, typed):
-- the length of the person's words (0 for none).
CREATE TABLE steps (
    call_id text PRIMARY KEY,
    prompt_id text NOT NULL,
    session_id text NOT NULL,
    t_ns bigint NOT NULL,
    ord text COLLATE "C" NOT NULL,
    end_ns bigint,
    kind text NOT NULL,
    model text,
    request_class text,
    tokens_input bigint,
    tokens_cache_read bigint,
    tokens_cache_write bigint,
    tokens_output bigint,
    stop_reason text,
    failed boolean NOT NULL,
    agent_type text,
    tools jsonb NOT NULL,
    tool_counts jsonb NOT NULL,
    does text NOT NULL,
    commands text[] NOT NULL,
    exit_codes bigint[] NOT NULL,
    patches jsonb NOT NULL,
    text text,
    usage_missing text,
    interrupted boolean NOT NULL,
    follows_interrupt boolean NOT NULL,
    prompt_chars bigint,
    -- The step's work (handoff §11R): what it did (read, edit, run, web, mcp, spawn, tool, think),
    -- its first command's program or MCP server (a new group starts where these change), its
    -- subagent's id, and its pieces of work from its tool calls ([kind, key, n, exit], refresh/work.go).
    work_kind text,
    program text,
    server text,
    agent_id text,
    work_pieces jsonb
);
-- A prompt's steps in order (their numbers, counted at read; its first step), its open steps, its
-- last end, and its first step that says the prompt: each an indexed LIMIT 1 or count.
CREATE INDEX ON steps (prompt_id, session_id, t_ns, ord);
CREATE INDEX ON steps (prompt_id, session_id, t_ns) WHERE end_ns IS NULL;
CREATE INDEX ON steps (prompt_id, session_id, (coalesce(end_ns, t_ns)));
CREATE INDEX ON steps (prompt_id, session_id, t_ns, ord) WHERE prompt_chars IS NOT NULL;
-- A minute's steps (its work keys).
CREATE INDEX ON steps (session_id, t_ns);

-- What one step adds to its prompt, summed per (kind, key): s:steps, s:tokens_in, s:tokens_out,
-- s:interrupted, s:follows_interrupt (n), tool (its tool calls by name) and its work pieces (w:…,
-- refresh/work.go). For a w:run key, at/ord/i/exit are its last run (by the step's order, then its
-- place in the step) and that run's exit code.
CREATE TABLE step_keys (
    call_id text NOT NULL,
    prompt_id text NOT NULL,
    session_id text NOT NULL,
    kind text NOT NULL,
    key text NOT NULL,
    n bigint NOT NULL,
    t_ns bigint NOT NULL,
    ord text COLLATE "C" NOT NULL,
    i bigint NOT NULL,
    exit bigint,
    PRIMARY KEY (call_id, kind, key)
);
-- A command's last run in a prompt (its last exit code): an indexed LIMIT 1.
CREATE INDEX ON step_keys (prompt_id, session_id, kind, key, t_ns, ord, i) WHERE kind = 'w:run';

-- A prompt's keys: its steps' step_keys summed (steps: how many steps have the key). A step's
-- refresh adds what it adds now and takes away what it added before, so its prompt's row is
-- recomputed from these (tens to a few hundred rows) and a few indexed lookups, never from the
-- prompt's other steps.
CREATE TABLE prompt_keys (
    prompt_id text NOT NULL,
    session_id text NOT NULL,
    kind text NOT NULL,
    key text NOT NULL,
    steps bigint NOT NULL,
    n bigint NOT NULL,
    PRIMARY KEY (prompt_id, session_id, kind, key)
);

-- One per prompt of a session: a prompt id (Claude Code's prompt-id header, a Codex turn id) seen
-- on its visible model calls, from its prompt_keys and a few lookups on its steps. Its time is its
-- first step's. Its text is what its first main-class step's new messages say: the person's words
-- (text, text_chars) and every piece typed (parts: [{role, note, text}], the harness's notes named
-- by convo's markers). interrupted: a step stopped on an interrupt; follows_interrupt: a step's
-- request says the turn before it was interrupted (a page marks that turn: the one before it).
CREATE TABLE prompts (
    prompt_id text NOT NULL,
    session_id text NOT NULL,
    t_ns bigint NOT NULL,
    product text,
    text_chars bigint,
    steps bigint NOT NULL,
    unfinished_max_ns bigint,
    text text,
    parts jsonb,
    tokens_in bigint,
    tokens_out bigint,
    tool_counts jsonb NOT NULL,
    interrupted boolean NOT NULL,
    follows_interrupt boolean NOT NULL,
    -- The turn (handoff §11R): its last step's end, and its work (refresh/work.go: TurnWork's JSON)
    -- from its steps' tool calls.
    ended_ns bigint,
    work jsonb,
    PRIMARY KEY (prompt_id, session_id)
);
CREATE INDEX ON prompts (session_id);
-- A person's turns in a range (the header row's Active): their sessions' prompts by time.
CREATE INDEX ON prompts (session_id, t_ns);

-- The Prompts page's list of a session: its prompts in order (their values in prompts) with its
-- visible calls with no prompt interleaved. prompt_index is the prompt's place.
CREATE TABLE prompt_items (
    session_id text NOT NULL,
    seq bigint NOT NULL,
    t_ns bigint NOT NULL,
    type text NOT NULL,
    prompt_id text,
    prompt_index bigint,
    open_tail boolean NOT NULL,
    hidden jsonb,
    between_kind text,
    detail text,
    call_id text,
    PRIMARY KEY (session_id, seq)
);
CREATE INDEX ON prompt_items (session_id, prompt_id);
-- The Prompts page's range: a session's items by time (seq follows time, so a range is a run of
-- seqs).
CREATE INDEX ON prompt_items (session_id, t_ns);

-- One per session with a visible model call: its row. Sums from session_hours, distinct counts
-- from session_keys, newest values by index. limits is the newest
-- Codex response's x-codex-* headers (agents.codex_limits renders them).
CREATE TABLE sessions (
    session_id text PRIMARY KEY,
    start_ns bigint NOT NULL,
    last_ns bigint NOT NULL,
    product text,
    client text,
    client_version text,
    client_version_ns bigint,
    hidden_calls bigint NOT NULL,
    own_title text,
    origin_kind text,
    origin_session_id text,
    origin jsonb,
    repo text,
    commit text,
    prompts bigint,
    model_calls bigint NOT NULL,
    fields_missing boolean NOT NULL,
    person_id text,
    device_id text,
    networks bigint,
    effort text,
    tokens_input bigint,
    tokens_cache_read bigint,
    tokens_cache_write bigint,
    tokens_output bigint,
    outputs_known bigint NOT NULL,
    tokens_accuracy text NOT NULL,
    tool_calls bigint,
    has_facts boolean NOT NULL,
    facts_cwd text,
    facts_approval text,
    facts_sandbox text,
    limits jsonb,
    limits_ns bigint,
    cli_version text,
    cli_version_ns bigint,
    rl_status text,
    rl_util double precision,
    rl_overage text,
    rl_ns bigint,
    unattributed boolean NOT NULL,
    -- The turn ledger's header (handoff §11R): its turns (prompts rows), those interrupted, their
    -- times summed (prompt to last step) and its work from its session_keys (TurnWork's JSON).
    turns bigint,
    turns_interrupted bigint,
    active_ns bigint,
    work jsonb,
    -- Without the agents' own analytics (handoff §11S): its failed model calls, and whether any
    -- call kept its content (its session_keys `failed` and `recorded`).
    failed_calls bigint NOT NULL DEFAULT 0,
    recorded boolean NOT NULL DEFAULT false,
    -- Its tool calls by tool name as sent (`Bash`, `mcp__linear__list_issues`): the content-free work line.
    tools jsonb
);

-- An account's activity in one UTC day, per agent (the directory's numbers, owner decision 2):
-- the sessions it appears on that were active that day (and their ids: a window's distinct sessions
-- are the distinct ids of its days; and the ids of those with a model call that day, which the
-- overview's agent cards count), and their hours' sums in the day.
CREATE TABLE account_days (
    account_id text NOT NULL,
    day_ns bigint NOT NULL,
    agent text NOT NULL,
    sessions bigint NOT NULL,
    session_ids text[] NOT NULL,
    call_session_ids text[] NOT NULL,
    prompts bigint NOT NULL,
    model_calls bigint NOT NULL,
    outputs_known bigint NOT NULL,
    tokens_input bigint,
    tokens_cache_read bigint,
    tokens_cache_write bigint,
    tokens_output bigint,
    first_ns bigint NOT NULL,
    last_ns bigint NOT NULL,
    -- The day's sessions' repos (the People page's Working on, handoff §11R).
    repos text[],
    -- Their tool calls and failed calls (the People page's Tool calls and Failed, handoff §11S).
    tool_calls bigint,
    failed_calls bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (account_id, day_ns, agent)
);
CREATE INDEX ON account_days (day_ns);

-- §2 The raw files ingest wrote: when the lister saw each and when a worker loaded it (step 3).
CREATE TABLE ingest_files (
    key text PRIMARY KEY,
    listed_at timestamptz,
    loaded_at timestamptz
);
-- The files still to load: what a worker claims from, oldest first, and /lag.
CREATE INDEX ON ingest_files (key) WHERE loaded_at IS NULL;

-- §6.3, §6.4 The person pages, the directory and /status. A person is an email group, resolved at read
-- from accounts by lower(email); unattributed is the sessions with a visible call and no account; the
-- overview's requests at a window's edge minutes are every credential's; /status's first call is the
-- first start, else the first end of a call with none (a WebSocket turn), its waiting time the first
-- probe's, and whether content is kept whether any call kept its detail.
CREATE INDEX ON accounts (lower(email));
CREATE INDEX ON sessions (start_ns) WHERE unattributed;
CREATE INDEX ON cred_minutes (minute_ns);
CREATE INDEX ON calls (start_ns) WHERE synthetic;
CREATE INDEX ON calls (end_ns) WHERE start_ns IS NULL;
-- Whether any call kept its content (/status). Its predicate names the kind so the step refresh's
-- lookup of a tool call's result (detail's results, by id) can't take it for a scan of every call
-- with content: on the cluster it did, 22,933 rows per tool call, and a long turn's refresh never
-- finished. That lookup uses the GIN index on detail's results.
CREATE INDEX ON calls (start_ns) WHERE detail IS NOT NULL AND kind = 'model';

-- The tool lists requests carried (handoff §11S, "offered vs used"), each once: its tools' names,
-- sorted and distinct, by the hash calls.tools_list names it by. A session sends a few lists (the
-- main thread's, a subagent's, one more when an MCP server connects), so a range's offered tools
-- are its distinct lists (session_minute_keys `offer`) and these few rows. Written by ingest.
CREATE TABLE tool_lists (
    hash text PRIMARY KEY,
    names jsonb NOT NULL
);

-- Interceptor verdicts (docs/INTERCEPTOR_V2.md, "What's recorded"): one row per verdict a call's
-- Interceptor returned (every answer other than a plain accept, and each error), keyed by its call
-- and its record's sequence number. Written by the flush that writes the call; at most 1000 per call
-- (the assembler drops the rest, counted).
CREATE TABLE verdicts (
    call_id text NOT NULL,
    seq bigint NOT NULL,
    ts_ns bigint NOT NULL,
    callback text NOT NULL,
    action text NOT NULL,
    applied boolean NOT NULL,
    mode text,
    message_id text,
    respond_mode text,
    reason text,
    annotations jsonb,
    latency_ms bigint NOT NULL,
    PRIMARY KEY (call_id, seq)
);
