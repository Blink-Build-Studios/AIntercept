-- v9 facts (docs/PIPELINE.md §3), the dirty list (§5.1) and the rollups (§5.2). Hand-written: DDL
-- appends this file to the generated v7 tables above, which stay until step 8 deletes them. Only
-- the flush (internal/flush) writes the facts, but for the open-call sweep (§5.5); only the
-- refresher (internal/refresh) writes the rollups. Plain DDL only, one statement per line-ending
-- semicolon (the API's tests split the file there).

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
    batch_events integer,
    batch_name text,
    batch_other_sessions jsonb,
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

-- An event's environment, stored once: keyed by the sha256 of its canonical JSON (jsonb's
-- text), so equal environments share a row.
CREATE TABLE event_envs (
    env_hash bytea PRIMARY KEY,
    env jsonb NOT NULL
);

-- §3.2 One row per analytics event, keyed <source>/<client event id> where the client sends
-- one, else as canon keys it (corr/h<hash>). A resent batch lands on the same rows.
CREATE TABLE events (
    event_key text PRIMARY KEY,
    batch_call_id text NOT NULL,
    batch_n integer NOT NULL,
    batch_cc_n integer,
    session_id text,
    prompt_id text,
    prompt_index bigint,
    request_id text,
    client_request_id text,
    tool_use_id text,
    device_id text,
    account_id text,
    org_id text,
    agent_id text,
    agent_type text,
    key_hash text,
    source text NOT NULL,
    name text,
    type text,
    client_event_id text,
    model text,
    permission_mode text,
    head_sha text,
    ts_ns bigint NOT NULL,
    row_ns bigint NOT NULL,
    seq bigint NOT NULL,
    metadata jsonb,
    env_hash bytea REFERENCES event_envs,
    summary text NOT NULL,
    internal boolean NOT NULL,
    marker text
);
CREATE INDEX ON events (session_id, ts_ns);
CREATE INDEX ON events (session_id, name, ts_ns);
CREATE INDEX ON events (request_id);
CREATE INDEX ON events (client_request_id);
CREATE INDEX ON events (tool_use_id);
CREATE INDEX ON events (batch_call_id, batch_n);

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
    plan text,
    plan_ns bigint,
    codex_plan text,
    codex_plan_ns bigint,
    seen_ns bigint NOT NULL
);

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
    on_events boolean NOT NULL,
    platform text,
    platform_ns bigint,
    terminal text,
    terminal_ns bigint,
    shell text,
    shell_ns bigint,
    agent text,
    agent_ns bigint,
    version text,
    version_ns bigint,
    first_ns bigint NOT NULL,
    last_ns bigint NOT NULL,
    PRIMARY KEY (account_id, device_id)
);

-- The spec's session_edges: named apart while v7's session_edges exists.
CREATE TABLE session_parents (
    child_session_id text NOT NULL,
    parent_session_id text NOT NULL,
    kind text NOT NULL,
    first_ns bigint NOT NULL,
    PRIMARY KEY (child_session_id, parent_session_id)
);
-- A session's children (§5.4), by its id.
CREATE INDEX ON session_parents (parent_session_id);

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
CREATE INDEX ON events USING brin (ts_ns);

-- §5.2 Rollups, written only by the refresher (internal/refresh), each row recomputed from the
-- facts (or a lower rollup) for one dirty key. Times are UTC minute and hour starts in ns.

-- A session's minute: its visible model calls (cc_calls) by start, else end; its Claude Code
-- events; its reports and those not captured; its Requests tab rows (by scope_session_id) per
-- kind. A NULL sum had nothing to add (as today's marks: a zero token count is NULL).
CREATE TABLE session_minutes (
    session_id text NOT NULL,
    minute_ns bigint NOT NULL,
    model_calls bigint NOT NULL,
    main_calls bigint NOT NULL,
    subagent_calls bigint NOT NULL,
    hidden_calls bigint NOT NULL,
    unreported bigint NOT NULL,
    input_tokens bigint,
    cache_read_tokens bigint,
    cache_write_tokens bigint,
    output_tokens bigint,
    outputs_known bigint NOT NULL,
    context_sum bigint,
    tool_calls bigint,
    events bigint NOT NULL,
    events_shown bigint NOT NULL,
    frustrated bigint NOT NULL,
    interrupted bigint NOT NULL,
    destructive bigint NOT NULL,
    outside_cwd bigint NOT NULL,
    queries bigint NOT NULL,
    bypassed bigint NOT NULL,
    reported bigint NOT NULL,
    missed bigint NOT NULL,
    hook_ms_sum double precision,
    hook_ms_n bigint NOT NULL,
    cache_5m bigint,
    cache_1h bigint,
    cost_usd double precision,
    custom_ca bigint NOT NULL,
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
    unreported bigint NOT NULL,
    input_tokens bigint,
    cache_read_tokens bigint,
    cache_write_tokens bigint,
    output_tokens bigint,
    outputs_known bigint NOT NULL,
    context_sum bigint,
    tool_calls bigint,
    events bigint NOT NULL,
    events_shown bigint NOT NULL,
    frustrated bigint NOT NULL,
    interrupted bigint NOT NULL,
    destructive bigint NOT NULL,
    outside_cwd bigint NOT NULL,
    queries bigint NOT NULL,
    bypassed bigint NOT NULL,
    reported bigint NOT NULL,
    missed bigint NOT NULL,
    hook_ms_sum double precision,
    hook_ms_n bigint NOT NULL,
    cache_5m bigint,
    cache_1h bigint,
    cost_usd double precision,
    custom_ca bigint NOT NULL,
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

-- One per Claude Code report (tengu_api_success) whose session has no visible model call with
-- its request id or client request id.
CREATE TABLE missed_reports (
    event_key text PRIMARY KEY,
    session_id text NOT NULL,
    minute_ns bigint NOT NULL,
    ts_ns bigint NOT NULL,
    request_id text,
    client_request_id text
);
CREATE INDEX ON missed_reports (session_id, ts_ns);
CREATE INDEX ON missed_reports (session_id, minute_ns);

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

-- A prompt's events by name (its input, turn end, cancels, tools): what a prompt refresh reads.
CREATE INDEX ON events (prompt_id, name);
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

-- One per prompt of a session (process/promptrows.go rows): a tengu_input_prompt, or a prompt
-- id seen only on visible model calls; an input with no prompt id is keyed input:<batch>:<n>.
-- What the session's other prompts decide (its place, the Codex number, open tail, pastes and
-- images, hidden calls) is in prompt_items. turn_report is Codex's codex_turn_event.
CREATE TABLE prompts (
    prompt_id text NOT NULL,
    session_id text NOT NULL,
    t_ns bigint NOT NULL,
    product text,
    has_input boolean NOT NULL,
    prompt_index bigint,
    text_chars bigint,
    frustrated boolean NOT NULL,
    steps bigint NOT NULL,
    unfinished_max_ns bigint,
    turn_end_ns bigint,
    text text,
    tokens_in bigint,
    tokens_out bigint,
    tool_counts jsonb NOT NULL,
    interrupted boolean NOT NULL,
    destructive bigint NOT NULL,
    turn_report jsonb,
    PRIMARY KEY (prompt_id, session_id)
);
CREATE INDEX ON prompts (session_id);

-- One per visible model call with a prompt, numbered within its prompt by (time, call id): a
-- step (process/promptrows.go step). end_ns is a real end (not the sweep's). reported is
-- whether a tengu_api_success names it; tools and tools_live are its tool events.
CREATE TABLE steps (
    prompt_id text NOT NULL,
    session_id text NOT NULL,
    step bigint NOT NULL,
    call_id text NOT NULL,
    t_ns bigint NOT NULL,
    end_ns bigint,
    kind text NOT NULL,
    model text,
    request_class text,
    tokens_input bigint,
    tokens_cache_read bigint,
    tokens_cache_write bigint,
    tokens_cache_write_5m bigint,
    tokens_cache_write_1h bigint,
    tokens_output bigint,
    ttft_ms double precision,
    stop_reason text,
    cost_usd double precision,
    failed boolean NOT NULL,
    reported boolean NOT NULL,
    agent_type text,
    tools jsonb NOT NULL,
    tools_live jsonb,
    does text NOT NULL,
    commands text[] NOT NULL,
    exit_codes bigint[] NOT NULL,
    content_live boolean NOT NULL,
    commands_live text[] NOT NULL,
    exit_codes_live bigint[] NOT NULL,
    patches jsonb NOT NULL,
    text text,
    usage_missing text,
    PRIMARY KEY (prompt_id, session_id, step)
);
CREATE INDEX ON steps (call_id);

-- The Prompts page's list of a session (process/promptrows.go rows): its prompts in order
-- (their values in prompts) with the between rows (compactions, recaps, calls with no prompt)
-- interleaved. prompt_index is the prompt's number: its input's, or a Codex turn's place.
CREATE TABLE prompt_items (
    session_id text NOT NULL,
    seq bigint NOT NULL,
    t_ns bigint NOT NULL,
    type text NOT NULL,
    prompt_id text,
    prompt_index bigint,
    open_tail boolean NOT NULL,
    pasted_bytes bigint,
    images bigint,
    hidden jsonb,
    between_kind text,
    detail text,
    call_id text,
    tokens_from bigint,
    tokens_to bigint,
    attempt_ns bigint,
    PRIMARY KEY (session_id, seq)
);
CREATE INDEX ON prompt_items (session_id, prompt_id);
-- The Prompts page's range: a session's items by time (seq follows time, so a range is a run of
-- seqs).
CREATE INDEX ON prompt_items (session_id, t_ns);

-- One per session with a visible model call or a Claude Code event: today's session row
-- (process/sessionrows.go sessionRow). Sums from session_hours, distinct counts from
-- session_keys, coverage from missed_reports, newest values by index. limits is the newest
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
    captured_from_prompt bigint,
    model_calls bigint NOT NULL,
    events bigint,
    frustrated bigint NOT NULL,
    interrupted bigint NOT NULL,
    destructive bigint NOT NULL,
    queries bigint NOT NULL,
    bypassed bigint NOT NULL,
    cov_reported bigint,
    cov_captured bigint NOT NULL,
    cov_not_captured bigint NOT NULL,
    cov_window_from bigint[],
    cov_window_to bigint[],
    cov_window_count bigint[],
    fields_missing boolean NOT NULL,
    person_id text,
    device_id text,
    device_platform text,
    device_terminal text,
    device_shell text,
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
    sandbox text,
    sandbox_ns bigint,
    custom_ca boolean NOT NULL,
    unattributed boolean NOT NULL,
    prompt_event_last_ns bigint
);

-- An account's activity in one UTC day, per agent (the directory's numbers, owner decision 2):
-- the sessions it appears on that were active that day, and their hours' sums in the day.
CREATE TABLE account_days (
    account_id text NOT NULL,
    day_ns bigint NOT NULL,
    agent text NOT NULL,
    sessions bigint NOT NULL,
    sessions_with_events bigint NOT NULL,
    prompts bigint NOT NULL,
    model_calls bigint NOT NULL,
    outputs_known bigint NOT NULL,
    tokens_input bigint,
    tokens_cache_read bigint,
    tokens_cache_write bigint,
    tokens_output bigint,
    queries bigint NOT NULL,
    bypassed bigint NOT NULL,
    reported bigint NOT NULL,
    missed bigint NOT NULL,
    first_ns bigint NOT NULL,
    last_ns bigint NOT NULL,
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
CREATE INDEX ON calls (start_ns) WHERE detail IS NOT NULL;
