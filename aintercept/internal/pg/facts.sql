-- v9 facts (docs/PIPELINE.md §3) and the dirty list (§5.1). Hand-written: DDL appends this file
-- to the generated v7 tables above, which stay until step 8 deletes them. Only the flush
-- (internal/flush) writes these tables. Plain DDL only, one statement per line-ending semicolon
-- (the API's tests split the file there).

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
-- when it was first marked (a busy row's refresh waits for it).
CREATE TABLE dirty (
    kind text NOT NULL,
    key text NOT NULL,
    seq bigint GENERATED BY DEFAULT AS IDENTITY,
    since timestamptz NOT NULL DEFAULT now(),
    claimed_at timestamptz,
    PRIMARY KEY (kind, key)
);

-- §2 The raw files ingest wrote: when the lister saw each and when a worker loaded it (step 3).
CREATE TABLE ingest_files (
    key text PRIMARY KEY,
    listed_at timestamptz,
    loaded_at timestamptz
);
-- The files still to load: what a worker claims from, oldest first, and /lag.
CREATE INDEX ON ingest_files (key) WHERE loaded_at IS NULL;
