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

-- §2 The raw files ingest wrote: when the lister saw each and when a worker loaded it (step 3).
CREATE TABLE ingest_files (
    key text PRIMARY KEY,
    listed_at timestamptz,
    loaded_at timestamptz
);
-- The files still to load: what a worker claims from, oldest first, and /lag.
CREATE INDEX ON ingest_files (key) WHERE loaded_at IS NULL;
