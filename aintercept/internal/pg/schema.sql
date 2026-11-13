-- The v7 schema (docs/PIPELINE.md §3). GENERATED from internal/pg/tables.go by
-- `go test ./internal/pg -run TestSchemaFile -update`; do not edit. Applied by pg.Apply to an
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
    PRIMARY KEY (key)
);
CREATE INDEX ON dirty_keys (since);

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
    to_ns bigint,
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
    mode text,
    prev_mode text,
    ip text,
    ipv6 boolean,
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

CREATE TABLE presence_segments (
    session_id text,
    seq bigint,
    from_ns bigint,
    to_ns bigint,
    state text,
    PRIMARY KEY (session_id, seq)
);

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

CREATE TABLE analytics (
    key text NOT NULL,
    id bytea NOT NULL,
    event_key text,
    correlation_id text,
    source_file text,
    row_ns bigint,
    ns bigint,
    seq bigint,
    source text,
    name text,
    type text,
    client_event_id text,
    session_id text,
    prompt_id text,
    person_id text,
    org_id text,
    device_id text,
    agent_id text,
    agent_type text,
    model text,
    request_id text,
    client_request_id text,
    tool_use_id text,
    prompt_index bigint,
    permission_mode text,
    head_sha text,
    metadata_json text COMPRESSION lz4,
    batch_n bigint,
    PRIMARY KEY (key, id)
) WITH (toast_tuple_target = 128);
CREATE INDEX ON analytics (correlation_id);
CREATE INDEX ON analytics (key, name);

-- §3.4-3.5 Batch parts and the directory folds.

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
