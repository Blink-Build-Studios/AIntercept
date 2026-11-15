-- The v7 schema (docs/PIPELINE.md §3). GENERATED from internal/pg/tables.go by
-- `go test ./internal/pg -run TestSchemaFile -update`, then internal/pg/facts.sql
-- verbatim; do not edit (edit facts.sql). Applied by pg.Apply to an
-- empty database, which records this file's hash (SchemaHash) in schema_version. There are no
-- migrations: a change to this file means a fresh database.

-- §3.2 Coordination: the key rows (the commit), the flags, the workers, the tail.

CREATE TABLE schema_version (
    hash text NOT NULL
);

CREATE TABLE keys (
    key text NOT NULL,
    n bigint NOT NULL,
    state text[],
    state_levels integer[],
    last_applied text,
    grace text[],
    garbage jsonb,
    render_version integer,
    updated_ns bigint,
    newest_row_ns bigint,
    expires_ns bigint,
    day text,
    first_request_ns bigint,
    earliest_ns bigint,
    content_retained boolean,
    state_records bigint[],
    snapshot text,
    snapshot_records bigint,
    PRIMARY KEY (key)
);
CREATE INDEX ON keys (day);
CREATE INDEX ON keys (newest_row_ns);
CREATE INDEX ON keys (expires_ns) WHERE expires_ns > 0;
CREATE INDEX ON keys (first_request_ns) WHERE first_request_ns > 0;
CREATE INDEX ON keys (earliest_ns) WHERE earliest_ns > 0;
CREATE INDEX ON keys (content_retained) WHERE content_retained;

CREATE TABLE dirty_keys (
    key text NOT NULL,
    seq bigint NOT NULL,
    since timestamptz NOT NULL,
    failures integer NOT NULL DEFAULT 0,
    last_error text,
    failed_at timestamptz,
    render boolean NOT NULL DEFAULT false,
    PRIMARY KEY (key)
);
CREATE INDEX ON dirty_keys (since);

CREATE TABLE dirty_inputs (
    key text NOT NULL,
    kind text NOT NULL,
    id text NOT NULL,
    seq bigint NOT NULL,
    PRIMARY KEY (key, kind, id)
);

CREATE TABLE workers (
    worker text NOT NULL,
    seen timestamptz NOT NULL,
    PRIMARY KEY (worker)
);

CREATE TABLE tail (
    last_name text,
    grace text[],
    checked_at timestamptz
);
INSERT INTO tail (last_name, grace) VALUES ('', '{}');

CREATE TABLE ingest_pods (
    incarnation text NOT NULL,
    last_ns bigint,
    at timestamptz,
    PRIMARY KEY (incarnation)
);

-- §3.3 Session tables: a session's rows of state.RenderSession, written by its own key.

CREATE TABLE session_rows (
    session_id text,
    start_ns bigint,
    last_ns bigint,
    product text,
    agent text,
    client text,
    client_version text,
    origin_kind text,
    origin_session_id text,
    origin_json text,
    children_json text,
    hidden_calls bigint,
    label_text text,
    label_source text,
    label_text_alone text,
    label_source_alone text,
    repo text,
    commit text,
    prompts bigint,
    captured_from_prompt bigint,
    model_calls bigint,
    events bigint,
    frustrated bigint,
    interrupted bigint,
    destructive bigint,
    bypass_pct double precision,
    cov_reported bigint,
    cov_captured bigint,
    cov_not_captured bigint,
    cov_window_from bigint[],
    cov_window_to bigint[],
    cov_window_count bigint[],
    fields_missing boolean,
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
    tokens_accuracy text,
    tool_calls bigint,
    has_facts boolean,
    facts_cwd text,
    facts_approval text,
    facts_sandbox text,
    limits_json text,
    limits_ns bigint,
    prompt_ids text[],
    devices text[],
    call_accounts text[],
    outputs_known bigint,
    queries bigint,
    bypassed bigint,
    cli_version text,
    cli_version_ns bigint,
    client_version_ns bigint,
    rl_status text,
    rl_util double precision,
    rl_overage text,
    rl_ns bigint,
    sandbox text,
    sandbox_ns bigint,
    custom_ca boolean,
    own_title text,
    prompt_last_ns bigint,
    key_day text,
    PRIMARY KEY (session_id)
);
CREATE INDEX ON session_rows (key_day);

CREATE TABLE prompt_rows (
    session_id text,
    seq bigint,
    t_ns bigint,
    type text,
    typed boolean,
    session_last_ns bigint,
    prompt_id text,
    prompt_index bigint,
    duration_turn_ms double precision,
    duration_report_ms double precision,
    open_tail boolean,
    unfinished_max_ns bigint,
    text_retained boolean,
    text text,
    text_chars bigint,
    pasted_bytes bigint,
    images bigint,
    steps bigint,
    tokens_in bigint,
    tokens_out bigint,
    tool_counts_json text,
    frustrated boolean,
    interrupted boolean,
    destructive bigint,
    turn_report_json text,
    hidden_json text,
    between_kind text,
    detail text,
    request_id text,
    tokens_from bigint,
    tokens_to bigint,
    wait_ms double precision,
    PRIMARY KEY (session_id, seq)
);

CREATE TABLE prompt_steps (
    session_id text,
    seq bigint,
    step bigint,
    request_id text,
    t_ns bigint,
    end_ns bigint,
    kind text,
    model text,
    request_class text,
    tokens_input bigint,
    tokens_cache_read bigint,
    tokens_cache_write bigint,
    tokens_cache_write_5m bigint,
    tokens_cache_write_1h bigint,
    tokens_output bigint,
    tokens_accuracy text,
    duration_ms double precision,
    ttft_ms double precision,
    stop_reason text,
    cost_usd double precision,
    failed boolean,
    events_pending_if_live boolean,
    agent_type text,
    tools_json text,
    tools_live_json text,
    does text,
    commands text[],
    exit_codes bigint[],
    content_live boolean,
    commands_live text[],
    exit_codes_live bigint[],
    patches_json text,
    text text,
    usage_missing text,
    PRIMARY KEY (session_id, seq, step)
);

CREATE TABLE session_marks (
    session_id text,
    seq bigint,
    kind text,
    t_ns bigint,
    request_id text,
    request_class text,
    model text,
    context bigint,
    output bigint,
    prompt_index bigint,
    input bigint,
    cache_read bigint,
    cache_write bigint,
    prompt_id text,
    unreported boolean,
    friction_kind text,
    detail text,
    tokens_from bigint,
    tokens_to bigint,
    idle boolean,
    wait_ms double precision,
    PRIMARY KEY (session_id, seq)
);
CREATE INDEX ON session_marks (session_id) WHERE kind = 'missed';

CREATE TABLE event_rows (
    session_id text,
    seq bigint,
    event_id text,
    ts_ns bigint,
    name text,
    name_lower text,
    internal boolean,
    summary text,
    summary_lower text,
    marker text,
    link_kind text,
    link_id text,
    link_label text,
    prompt_index bigint,
    head_sha text,
    hook text,
    git_op text,
    pr bigint,
    recap_key boolean,
    recap boolean,
    count bigint,
    unnamed boolean,
    tool_name text,
    is_mcp boolean,
    hook_ms double precision,
    argv0 text,
    category text,
    outside_cwd boolean,
    hook_name text,
    operation text,
    pr_number text,
    cache_5m bigint,
    cache_1h bigint,
    cost_usd double precision,
    batch text,
    PRIMARY KEY (session_id, seq)
);
CREATE INDEX ON event_rows (session_id, name, ts_ns) WHERE name IN ('tengu_tool_search_mode_decision', 'tengu_skills_sync_success');

CREATE TABLE session_tools (
    session_id text,
    kind text,
    server text,
    tool text,
    first_ns bigint,
    PRIMARY KEY (session_id, kind, server, tool)
);

CREATE TABLE call_tool_uses (
    session_id text,
    ts_ns bigint,
    kind text,
    server text,
    tool text,
    PRIMARY KEY (session_id, ts_ns, kind, server, tool)
);

CREATE TABLE session_request_rows (
    scope_session_id text,
    seq bigint,
    person_id text,
    request_id text,
    ts_ns bigint,
    kind text,
    agent text,
    client text,
    hidden_kind text,
    session_id text,
    session_label text,
    prompt_index bigint,
    method text,
    path text,
    detail text,
    status bigint,
    status_err boolean,
    duration_ms double precision,
    retries bigint,
    bytes_up bigint,
    bytes_down bigint,
    background boolean,
    captured boolean,
    status_text text,
    stop_reason text,
    ttft_ms double precision,
    tokens_input bigint,
    tokens_cache_read bigint,
    tokens_cache_write bigint,
    tokens_cache_write_5m bigint,
    tokens_cache_write_1h bigint,
    tokens_output bigint,
    tokens_accuracy text,
    content_encoding text,
    error_message text,
    error_after_ms double precision,
    error_bytes_received bigint,
    error_retries bigint,
    error_retry_succeeded_ns bigint,
    model text,
    effort text,
    step_index bigint,
    tool_calls text[],
    usage_missing text,
    text text,
    PRIMARY KEY (scope_session_id, seq)
);

-- §3.4 Stored inputs: the state's own rows, keyed by (key, the state's row id).

CREATE TABLE dim_account (
    key text NOT NULL,
    id bytea NOT NULL,
    account_id text,
    email text,
    name text,
    org text,
    seen_ns bigint,
    email_ns bigint,
    name_ns bigint,
    org_ns bigint,
    PRIMARY KEY (key, id)
);
CREATE INDEX ON dim_account (account_id);
CREATE INDEX ON dim_account (lower(email));

CREATE TABLE dim_session_account (
    key text NOT NULL,
    id bytea NOT NULL,
    session_id text,
    account_id text,
    PRIMARY KEY (key, id)
);
CREATE INDEX ON dim_session_account (account_id);
CREATE INDEX ON dim_session_account (session_id);

CREATE TABLE dim_session_visible (
    key text NOT NULL,
    id bytea NOT NULL,
    session_id text,
    PRIMARY KEY (key, id)
);

CREATE TABLE dim_session_person (
    key text NOT NULL,
    id bytea NOT NULL,
    session_id text,
    person_id text,
    PRIMARY KEY (key, id)
);
CREATE INDEX ON dim_session_person (person_id);

CREATE TABLE dim_person_device (
    key text NOT NULL,
    id bytea NOT NULL,
    person_id text,
    device_id text,
    PRIMARY KEY (key, id)
);
CREATE INDEX ON dim_person_device (person_id);

CREATE TABLE dim_session_credential (
    key text NOT NULL,
    id bytea NOT NULL,
    session_id text,
    key_hash text,
    PRIMARY KEY (key, id)
);
CREATE INDEX ON dim_session_credential (session_id);

CREATE TABLE dim_account_events (
    key text NOT NULL,
    id bytea NOT NULL,
    account_id text,
    org text,
    org_ns bigint,
    plan_v text,
    plan_ts bigint,
    plan_ok boolean,
    PRIMARY KEY (key, id)
);
CREATE INDEX ON dim_account_events (account_id);

CREATE TABLE dim_account_device (
    key text NOT NULL,
    id bytea NOT NULL,
    account_id text,
    device_id text,
    on_events boolean,
    platform_v text,
    platform_ts bigint,
    platform_ok boolean,
    terminal_v text,
    terminal_ts bigint,
    terminal_ok boolean,
    shell_v text,
    shell_ts bigint,
    shell_ok boolean,
    PRIMARY KEY (key, id)
);
CREATE INDEX ON dim_account_device (account_id);

CREATE TABLE dim_codex_plan (
    key text NOT NULL,
    id bytea NOT NULL,
    account_id text,
    plan_v text,
    plan_ts bigint,
    plan_ok boolean,
    PRIMARY KEY (key, id)
);
CREATE INDEX ON dim_codex_plan (account_id);

CREATE TABLE request_txs (
    key text NOT NULL,
    id bytea NOT NULL,
    call_id text,
    correlation_id text,
    key_hash text,
    own_session text,
    prompt_id text,
    req_id text,
    client_req_id text,
    kind text,
    hidden_kind text,
    product text,
    is_tx boolean,
    row_person_id text,
    row_request_id text,
    row_ts_ns bigint,
    row_kind text,
    row_agent text,
    row_client text,
    row_hidden_kind text,
    row_session_id text,
    row_session_label text,
    row_prompt_index bigint,
    row_method text,
    row_path text,
    row_detail text,
    row_status bigint,
    row_status_err boolean,
    row_duration_ms double precision,
    row_retries bigint,
    row_bytes_up bigint,
    row_bytes_down bigint,
    row_background boolean,
    row_captured boolean,
    row_status_text text,
    row_stop_reason text,
    row_ttft_ms double precision,
    row_tokens_input bigint,
    row_tokens_cache_read bigint,
    row_tokens_cache_write bigint,
    row_tokens_cache_write_5m bigint,
    row_tokens_cache_write_1h bigint,
    row_tokens_output bigint,
    row_tokens_accuracy text,
    row_content_encoding text,
    row_error_message text,
    row_error_after_ms double precision,
    row_error_bytes_received bigint,
    row_error_retries bigint,
    row_error_retry_succeeded_ns bigint,
    row_model text,
    row_effort text,
    row_step_index bigint,
    row_tool_calls text[],
    row_usage_missing text,
    row_text text,
    PRIMARY KEY (key, id)
);
CREATE INDEX ON request_txs (key_hash, row_ts_ns);
CREATE INDEX ON request_txs (key_hash, prompt_id);
CREATE INDEX ON request_txs (call_id);

CREATE TABLE prompt_index (
    key text NOT NULL,
    id bytea NOT NULL,
    session_id text,
    prompt_id text,
    prompt_index bigint,
    ts_ns bigint,
    event_key text,
    PRIMARY KEY (key, id)
);
CREATE INDEX ON prompt_index (session_id, prompt_id);

CREATE TABLE codex_turns (
    key text NOT NULL,
    id bytea NOT NULL,
    session_id text,
    prompt_id text,
    first_ns bigint,
    PRIMARY KEY (key, id)
);
CREATE INDEX ON codex_turns (session_id, prompt_id);

CREATE TABLE session_last (
    key text NOT NULL,
    id bytea NOT NULL,
    session_id text,
    last_ns bigint,
    PRIMARY KEY (key, id)
);
CREATE INDEX ON session_last (session_id);

CREATE TABLE request_reports (
    key text NOT NULL,
    id bytea NOT NULL,
    session_id text,
    ts_ns bigint,
    event_key text,
    req_id text,
    client_req_id text,
    ttft_ms double precision,
    cache_write_5m bigint,
    cache_write_1h bigint,
    PRIMARY KEY (key, id)
);
CREATE INDEX ON request_reports (req_id);
CREATE INDEX ON request_reports (client_req_id);

CREATE TABLE request_missed (
    key text NOT NULL,
    id bytea NOT NULL,
    session_id text,
    event_key text,
    req_id text,
    client_req_id text,
    row_person_id text,
    row_request_id text,
    row_ts_ns bigint,
    row_kind text,
    row_agent text,
    row_client text,
    row_hidden_kind text,
    row_session_id text,
    row_session_label text,
    row_prompt_index bigint,
    row_method text,
    row_path text,
    row_detail text,
    row_status bigint,
    row_status_err boolean,
    row_duration_ms double precision,
    row_retries bigint,
    row_bytes_up bigint,
    row_bytes_down bigint,
    row_background boolean,
    row_captured boolean,
    row_status_text text,
    row_stop_reason text,
    row_ttft_ms double precision,
    row_tokens_input bigint,
    row_tokens_cache_read bigint,
    row_tokens_cache_write bigint,
    row_tokens_cache_write_5m bigint,
    row_tokens_cache_write_1h bigint,
    row_tokens_output bigint,
    row_tokens_accuracy text,
    row_content_encoding text,
    row_error_message text,
    row_error_after_ms double precision,
    row_error_bytes_received bigint,
    row_error_retries bigint,
    row_error_retry_succeeded_ns bigint,
    row_model text,
    row_effort text,
    row_step_index bigint,
    row_tool_calls text[],
    row_usage_missing text,
    row_text text,
    PRIMARY KEY (key, id)
);
CREATE INDEX ON request_missed (session_id);
CREATE INDEX ON request_missed (row_request_id);

CREATE TABLE session_call_accounts (
    key text NOT NULL,
    id bytea NOT NULL,
    session_id text,
    account_id text,
    PRIMARY KEY (key, id)
);
CREATE INDEX ON session_call_accounts (account_id);
CREATE INDEX ON session_call_accounts (session_id);

CREATE TABLE session_edges (
    key text NOT NULL,
    id bytea NOT NULL,
    parent_session_id text,
    child_session_id text,
    kind text,
    child_first_ns bigint,
    PRIMARY KEY (key, id)
);
CREATE INDEX ON session_edges (parent_session_id);

CREATE TABLE session_title_calls (
    key text NOT NULL,
    id bytea NOT NULL,
    session_id text,
    ts_ns bigint,
    call_id text,
    account_id text,
    title text,
    PRIMARY KEY (key, id)
);
CREATE INDEX ON session_title_calls (account_id, ts_ns);

CREATE TABLE requests (
    key text NOT NULL,
    id bytea NOT NULL,
    call_id text,
    correlation_id text,
    source_file text,
    first_file text,
    ws_turn bigint,
    transport text,
    start_ns bigint,
    response_start_ns bigint,
    end_ns bigint,
    host text,
    method text,
    path text,
    status bigint,
    synthetic boolean,
    ip text,
    key_hash text,
    key_hint text,
    error text,
    session_id text,
    prompt_id text,
    person_id text,
    device_id text,
    org_id text,
    request_class text,
    client_request_id text,
    request_id text,
    agent_id text,
    agent_type text,
    user_agent text,
    model text,
    stop_reason text,
    message_id text,
    previous_message_id text,
    thread_type text,
    stream boolean,
    service_tier text,
    input_tokens bigint,
    output_tokens bigint,
    cache_read_tokens bigint,
    cache_write_tokens bigint,
    reasoning_tokens bigint,
    effort text,
    detail_json text,
    request_bytes bigint,
    response_bytes bigint,
    content_encoding text,
    client_json text,
    provider_json text,
    tool_uses_json text,
    product text,
    client text,
    client_version text,
    hidden_kind text,
    ext_json text,
    PRIMARY KEY (key, id)
);
CREATE INDEX ON requests (call_id);

-- §3.4-3.5, §3.7 Batch parts, the directory folds and the request drawer.

CREATE TABLE batch_parts (
    batch_id text NOT NULL,
    key text NOT NULL,
    n bigint,
    min_name text,
    first_ns bigint,
    first_session text,
    PRIMARY KEY (batch_id, key)
);
CREATE INDEX ON batch_parts (key);

CREATE TABLE session_call_days (
    session_id text NOT NULL,
    day bigint NOT NULL,
    n bigint,
    first_ns bigint,
    last_ns bigint,
    PRIMARY KEY (session_id, day)
);

CREATE TABLE agent_calls (
    key text NOT NULL,
    call_id text NOT NULL,
    ts_ns bigint,
    PRIMARY KEY (key, call_id)
);
CREATE INDEX ON agent_calls (ts_ns);

CREATE TABLE agent_call_hours (
    hour bigint NOT NULL,
    key text NOT NULL,
    n bigint,
    PRIMARY KEY (hour, key)
);

CREATE TABLE drawer_templates (
    key text NOT NULL,
    id text NOT NULL,
    body text COMPRESSION lz4,
    PRIMARY KEY (key, id)
);

CREATE TABLE drawer_requests (
    request_id text NOT NULL,
    key text NOT NULL,
    data_tpl text,
    data_vals text COMPRESSION lz4,
    details_tpl text,
    details_vals text COMPRESSION lz4,
    step_session text,
    step_prompt text,
    PRIMARY KEY (request_id, key),
    FOREIGN KEY (key, data_tpl) REFERENCES drawer_templates (key, id),
    FOREIGN KEY (key, details_tpl) REFERENCES drawer_templates (key, id)
);
CREATE INDEX ON drawer_requests (key);

CREATE TABLE drawer_step_counts (
    session_id text NOT NULL,
    prompt_id text NOT NULL,
    key text NOT NULL,
    n bigint,
    PRIMARY KEY (session_id, prompt_id, key)
);
CREATE INDEX ON drawer_step_counts (key);

CREATE TABLE drawer_events (
    event_id text NOT NULL,
    key text NOT NULL,
    data text,
    correlation_id text,
    event_key text,
    PRIMARY KEY (event_id, key)
);
CREATE INDEX ON drawer_events (key);

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
