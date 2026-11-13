"""Shared pieces of the API's store (docs/PIPELINE.md §4).

The store the endpoints query is `v1.V1Store` (per-key folders and the merge, read from S3);
`Store` here is the interface the endpoint code types against. This module also keeps the
table shapes (`_EMPTY`), the views over them (`VIEWS`) and the raw-row readers the drawer
uses.
"""

import hashlib
import json
import pathlib
import re
from collections.abc import Callable, Sequence
from typing import Any, Protocol, TypeVar

from aintercept_api import raw
from aintercept_api.raw import RawSource

T = TypeVar("T")
MEMO_SIZE = 64  # Store.memo entries (a few sessions' events, steps and calls)
_PROBES_EMPTY = "CREATE OR REPLACE TABLE probes (filename VARCHAR, first_ns BIGINT)"

# An empty table of each shape, for before the first commit (columns the views use).
_EMPTY = {
    "requests": """CREATE OR REPLACE TABLE requests (
        call_id VARCHAR, correlation_id VARCHAR, source_file VARCHAR, first_file VARCHAR, ws_turn BIGINT,
        transport VARCHAR, start_ns BIGINT, response_start_ns BIGINT, end_ns BIGINT, host VARCHAR, method VARCHAR,
        path VARCHAR, status BIGINT, synthetic BOOLEAN, ip VARCHAR, key_hash VARCHAR, key_hint VARCHAR, error VARCHAR,
        session_id VARCHAR, prompt_id VARCHAR, person_id VARCHAR, device_id VARCHAR, org_id VARCHAR,
        request_class VARCHAR, client_request_id VARCHAR, request_id VARCHAR, agent_id VARCHAR, agent_type VARCHAR,
        user_agent VARCHAR, model VARCHAR, stop_reason VARCHAR, message_id VARCHAR, previous_message_id VARCHAR,
        thread_type VARCHAR, stream BOOLEAN, service_tier VARCHAR, input_tokens BIGINT, output_tokens BIGINT,
        cache_read_tokens BIGINT, cache_write_tokens BIGINT, reasoning_tokens BIGINT, request_bytes BIGINT,
        response_bytes BIGINT, content_encoding VARCHAR, client_json VARCHAR, provider_json VARCHAR,
        tool_uses_json VARCHAR, product VARCHAR, client VARCHAR, client_version VARCHAR, hidden_kind VARCHAR,
        effort VARCHAR, detail_json VARCHAR,
        ext_json VARCHAR, filename VARCHAR)""",
    "analytics": """CREATE OR REPLACE TABLE analytics (
        event_key VARCHAR, correlation_id VARCHAR, source_file VARCHAR, row_ns BIGINT, ns BIGINT, seq BIGINT,
        source VARCHAR, name VARCHAR, type VARCHAR, client_event_id VARCHAR, session_id VARCHAR, prompt_id VARCHAR,
        person_id VARCHAR, org_id VARCHAR, device_id VARCHAR, agent_id VARCHAR, agent_type VARCHAR, model VARCHAR,
        request_id VARCHAR, client_request_id VARCHAR, tool_use_id VARCHAR, prompt_index BIGINT,
        permission_mode VARCHAR, head_sha VARCHAR, metadata_json VARCHAR, env_json VARCHAR, filename VARCHAR)""",
    "ledger": """CREATE OR REPLACE TABLE ledger (raw_key VARCHAR, size BIGINT, rows BIGINT, bad BIGINT, run BIGINT,
        filename VARCHAR)""",
    "sessions": """CREATE OR REPLACE TABLE sessions (session_id VARCHAR, person_id VARCHAR, org_id VARCHAR,
        device_id VARCHAR, first_ns BIGINT, last_ns BIGINT, calls BIGINT, events BIGINT, prompts BIGINT,
        product VARCHAR, client VARCHAR, client_version VARCHAR, hidden_calls BIGINT, origin_kind VARCHAR,
        origin_session_id VARCHAR, origin_turn_id VARCHAR, origin_turn BIGINT, init VARCHAR, agent_name VARCHAR)""",
    "prompts": """CREATE OR REPLACE TABLE prompts (prompt_id VARCHAR, session_id VARCHAR, prompt_index BIGINT,
        first_ns BIGINT, last_ns BIGINT, calls BIGINT, events BIGINT)""",
    "person_devices": """CREATE OR REPLACE TABLE person_devices (person_id VARCHAR, device_id VARCHAR,
        first_ns BIGINT, last_ns BIGINT)""",
    "people": """CREATE OR REPLACE TABLE people (person_id VARCHAR, first_ns BIGINT, last_ns BIGINT,
        sessions BIGINT, calls BIGINT, events BIGINT, devices BIGINT)""",
    "pending": """CREATE OR REPLACE TABLE pending (correlation_id VARCHAR, source_file VARCHAR, seen_ns BIGINT,
        done BOOLEAN, expired BOOLEAN, end_ns BIGINT, error VARCHAR, end_file VARCHAR, start_ns BIGINT,
        response_start_ns BIGINT, host VARCHAR, method VARCHAR, path VARCHAR, status BIGINT, synthetic BOOLEAN,
        ip VARCHAR, key_hash VARCHAR, key_hint VARCHAR, request_bytes BIGINT, response_bytes BIGINT, websocket BOOLEAN,
        client_json VARCHAR, request_id VARCHAR, content_encoding VARCHAR, provider_json VARCHAR)""",
    "accounts": """CREATE OR REPLACE TABLE accounts (account_id VARCHAR, email VARCHAR, name VARCHAR, org VARCHAR,
        seen_ns BIGINT)""",
    "request_rows": """CREATE OR REPLACE TABLE request_rows (person_id VARCHAR, request_id VARCHAR, ts_ns BIGINT,
        kind VARCHAR, agent VARCHAR, client VARCHAR, hidden_kind VARCHAR, session_id VARCHAR, session_label VARCHAR,
        prompt_index BIGINT, method VARCHAR, path VARCHAR, detail VARCHAR, status BIGINT, status_err BOOLEAN,
        duration_ms DOUBLE, retries BIGINT, bytes_up BIGINT, bytes_down BIGINT, background BOOLEAN, captured BOOLEAN,
        status_text VARCHAR, stop_reason VARCHAR, ttft_ms DOUBLE, tokens_input BIGINT, tokens_cache_read BIGINT,
        tokens_cache_write BIGINT, tokens_cache_write_5m BIGINT, tokens_cache_write_1h BIGINT, tokens_output BIGINT,
        tokens_accuracy VARCHAR, content_encoding VARCHAR, error_message VARCHAR, error_after_ms DOUBLE,
        error_bytes_received BIGINT, error_retries BIGINT, error_retry_succeeded_ns BIGINT, model VARCHAR,
        effort VARCHAR, step_index BIGINT, tool_calls VARCHAR[], usage_missing VARCHAR, text VARCHAR)""",
    "person_last": "CREATE OR REPLACE TABLE person_last (person_id VARCHAR, last_ns BIGINT)",
    "session_rows": """CREATE OR REPLACE TABLE session_rows (session_id VARCHAR, start_ns BIGINT, last_ns BIGINT,
        product VARCHAR, agent VARCHAR, client VARCHAR, client_version VARCHAR, origin_kind VARCHAR,
        origin_session_id VARCHAR, origin_json VARCHAR, children_json VARCHAR, hidden_calls BIGINT, label_text VARCHAR,
        label_source VARCHAR, label_text_alone VARCHAR, label_source_alone VARCHAR, repo VARCHAR, commit VARCHAR,
        prompts BIGINT, captured_from_prompt BIGINT, model_calls BIGINT, events BIGINT, frustrated BIGINT,
        interrupted BIGINT, destructive BIGINT, bypass_pct DOUBLE, cov_reported BIGINT, cov_captured BIGINT,
        cov_not_captured BIGINT, cov_window_from BIGINT[], cov_window_to BIGINT[], cov_window_count BIGINT[],
        fields_missing BOOLEAN, person_id VARCHAR, device_id VARCHAR, device_platform VARCHAR, device_terminal VARCHAR,
        device_shell VARCHAR, networks BIGINT, effort VARCHAR, tokens_input BIGINT, tokens_cache_read BIGINT,
        tokens_cache_write BIGINT, tokens_output BIGINT, tokens_accuracy VARCHAR, tool_calls BIGINT, has_facts BOOLEAN,
        facts_cwd VARCHAR, facts_approval VARCHAR, facts_sandbox VARCHAR, limits_json VARCHAR, limits_ns BIGINT,
        prompt_ids VARCHAR[], devices VARCHAR[], call_accounts VARCHAR[], outputs_known BIGINT, queries BIGINT,
        bypassed BIGINT, cli_version VARCHAR, cli_version_ns BIGINT, client_version_ns BIGINT, rl_status VARCHAR,
        rl_util DOUBLE, rl_overage VARCHAR, rl_ns BIGINT, sandbox VARCHAR, sandbox_ns BIGINT, custom_ca BOOLEAN)""",
    "presence_segments": """CREATE OR REPLACE TABLE presence_segments (session_id VARCHAR, seq BIGINT,
        from_ns BIGINT, to_ns BIGINT, state VARCHAR)""",
    "session_marks": """CREATE OR REPLACE TABLE session_marks (session_id VARCHAR, seq BIGINT, kind VARCHAR,
        t_ns BIGINT, to_ns BIGINT, request_id VARCHAR, request_class VARCHAR, model VARCHAR, context BIGINT,
        output BIGINT, prompt_index BIGINT, friction_kind VARCHAR, detail VARCHAR, tokens_from BIGINT, tokens_to BIGINT,
        idle BOOLEAN, wait_ms DOUBLE, mode VARCHAR, prev_mode VARCHAR, ip VARCHAR, ipv6 BOOLEAN, input BIGINT,
        cache_read BIGINT, cache_write BIGINT, prompt_id VARCHAR, unreported BOOLEAN)""",
    "prompt_rows": """CREATE OR REPLACE TABLE prompt_rows (session_id VARCHAR, seq BIGINT, t_ns BIGINT, type VARCHAR,
        typed BOOLEAN, session_last_ns BIGINT, prompt_id VARCHAR, prompt_index BIGINT, duration_turn_ms DOUBLE,
        duration_report_ms DOUBLE, open_tail BOOLEAN, unfinished_max_ns BIGINT, text_retained BOOLEAN, text VARCHAR,
        text_chars BIGINT, pasted_bytes BIGINT, images BIGINT, steps BIGINT, tokens_in BIGINT, tokens_out BIGINT,
        tool_counts_json VARCHAR, frustrated BOOLEAN, interrupted BOOLEAN, destructive BIGINT,
        turn_report_json VARCHAR, hidden_json VARCHAR, between_kind VARCHAR, detail VARCHAR, request_id VARCHAR,
        tokens_from BIGINT, tokens_to BIGINT, wait_ms DOUBLE)""",
    "event_rows": """CREATE OR REPLACE TABLE event_rows (session_id VARCHAR, seq BIGINT, event_id VARCHAR,
        ts_ns BIGINT, name VARCHAR, name_lower VARCHAR, internal BOOLEAN, summary VARCHAR, summary_lower VARCHAR,
        marker VARCHAR, link_kind VARCHAR, link_id VARCHAR, link_label VARCHAR, prompt_index BIGINT, head_sha VARCHAR,
        hook VARCHAR, git_op VARCHAR, pr BIGINT, recap_key BOOLEAN, recap BOOLEAN, count BIGINT, unnamed BOOLEAN,
        tool_name VARCHAR, is_mcp BOOLEAN, hook_ms DOUBLE, argv0 VARCHAR, category VARCHAR, outside_cwd BOOLEAN,
        hook_name VARCHAR, operation VARCHAR, pr_number VARCHAR, cache_5m BIGINT, cache_1h BIGINT, cost_usd DOUBLE)""",
    "person_map": "CREATE OR REPLACE TABLE person_map (person_id VARCHAR, account_id VARCHAR, directory BOOLEAN)",
    "person_sessions": "CREATE OR REPLACE TABLE person_sessions (person_id VARCHAR, session_id VARCHAR)",
    "account_facts": """CREATE OR REPLACE TABLE account_facts (account_id VARCHAR, org VARCHAR, plan VARCHAR,
        codex_plan VARCHAR)""",
    "account_devices": """CREATE OR REPLACE TABLE account_devices (account_id VARCHAR, device_id VARCHAR,
        on_events BOOLEAN, platform VARCHAR, terminal VARCHAR, shell VARCHAR)""",
    "call_tool_uses": """CREATE OR REPLACE TABLE call_tool_uses (session_id VARCHAR, ts_ns BIGINT, kind VARCHAR,
        server VARCHAR, tool VARCHAR)""",
    "session_tools": """CREATE OR REPLACE TABLE session_tools (session_id VARCHAR, kind VARCHAR, server VARCHAR,
        tool VARCHAR, first_ns BIGINT)""",
    "session_request_rows": """CREATE OR REPLACE TABLE session_request_rows (scope_session_id VARCHAR, seq BIGINT,
        person_id VARCHAR, request_id VARCHAR, ts_ns BIGINT,
        kind VARCHAR, agent VARCHAR, client VARCHAR, hidden_kind VARCHAR, session_id VARCHAR, session_label VARCHAR,
        prompt_index BIGINT, method VARCHAR, path VARCHAR, detail VARCHAR, status BIGINT, status_err BOOLEAN,
        duration_ms DOUBLE, retries BIGINT, bytes_up BIGINT, bytes_down BIGINT, background BOOLEAN, captured BOOLEAN,
        status_text VARCHAR, stop_reason VARCHAR, ttft_ms DOUBLE, tokens_input BIGINT, tokens_cache_read BIGINT,
        tokens_cache_write BIGINT, tokens_cache_write_5m BIGINT, tokens_cache_write_1h BIGINT, tokens_output BIGINT,
        tokens_accuracy VARCHAR, content_encoding VARCHAR, error_message VARCHAR, error_after_ms DOUBLE,
        error_bytes_received BIGINT, error_retries BIGINT, error_retry_succeeded_ns BIGINT, model VARCHAR,
        effort VARCHAR, step_index BIGINT, tool_calls VARCHAR[], usage_missing VARCHAR, text VARCHAR)""",
    "prompt_steps": """CREATE OR REPLACE TABLE prompt_steps (session_id VARCHAR, seq BIGINT, step BIGINT,
        request_id VARCHAR, t_ns BIGINT, end_ns BIGINT, kind VARCHAR, model VARCHAR, request_class VARCHAR,
        tokens_input BIGINT, tokens_cache_read BIGINT, tokens_cache_write BIGINT, tokens_cache_write_5m BIGINT,
        tokens_cache_write_1h BIGINT, tokens_output BIGINT, tokens_accuracy VARCHAR, duration_ms DOUBLE,
        ttft_ms DOUBLE, stop_reason VARCHAR, cost_usd DOUBLE, failed BOOLEAN, events_pending_if_live BOOLEAN,
        agent_type VARCHAR, tools_json VARCHAR, tools_live_json VARCHAR, does VARCHAR, commands VARCHAR[],
        exit_codes BIGINT[], content_live BOOLEAN, commands_live VARCHAR[], exit_codes_live BIGINT[],
        patches_json VARCHAR, text VARCHAR, usage_missing VARCHAR)""",
}

VIEWS = """
CREATE OR REPLACE VIEW calls AS
SELECT call_id, correlation_id, ws_turn, coalesce(start_ns, end_ns) AS ts, end_ns AS end_ts, response_start_ns,
       host, method, path, status, coalesce(synthetic, false) AS synthetic, ip, key_hash, error,
       session_id, prompt_id, person_id, device_id, org_id, request_class, client_request_id, request_id,
       agent_id, agent_type, user_agent, model, stop_reason, message_id, previous_message_id,
       input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, request_bytes, response_bytes,
       content_encoding, provider_json, client_json, source_file, first_file, false AS in_flight,
       coalesce(product, CASE WHEN host = 'api.anthropic.com' THEN 'claude_code' END) AS product,
       client, client_version, hidden_kind, ext_json, reasoning_tokens, tool_uses_json, effort, detail_json
FROM requests
UNION ALL BY NAME
SELECT correlation_id AS call_id, correlation_id, coalesce(start_ns, seen_ns) AS ts, NULL::BIGINT AS end_ts,
       response_start_ns, host, method, path, status, coalesce(synthetic, false) AS synthetic, ip, key_hash,
       json_extract_string(client_json, '$."x-claude-code-session-id"') AS session_id,
       json_extract_string(client_json, '$."x-claude-code-prompt-id"') AS prompt_id,
       json_extract_string(client_json, '$."x-organization-uuid"') AS org_id,
       json_extract_string(client_json, '$."x-claude-code-request-class"') AS request_class,
       json_extract_string(client_json, '$."x-client-request-id"') AS client_request_id, request_id,
       json_extract_string(client_json, '$."x-claude-code-agent-id"') AS agent_id,
       json_extract_string(client_json, '$."x-claude-code-agent-type"') AS agent_type,
       json_extract_string(client_json, '$."user-agent"') AS user_agent,
       request_bytes, response_bytes, content_encoding, provider_json,
       client_json, source_file, source_file AS first_file, true AS in_flight,
       CASE WHEN host = 'api.anthropic.com' THEN 'claude_code'
            WHEN json_extract_string(client_json, '$.originator') LIKE 'codex%' THEN 'codex' END AS product
FROM pending WHERE NOT done AND start_ns IS NOT NULL;

-- Every coding agent's model calls (docs/harness-parity.md), the ones the user sees: Claude
-- Code's /v1/messages calls, Codex's response.create turns. `hidden_calls` are the rest
-- (Codex prewarm and title calls). `correlation_id` is the call's id: a WebSocket turn's
-- call_id, written connection~turn (URL-safe; the processor's is connection#turn), so each
-- turn opens on its own.
CREATE OR REPLACE VIEW agent_calls AS
SELECT replace(call_id, '#', '~') AS correlation_id, ts, end_ts, ip, key_hash, session_id, prompt_id,
       request_class, client_request_id, agent_id, request_id, status, person_id AS account_uuid, device_id,
       model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, stop_reason, agent_type,
       product, client, client_version, hidden_kind, ext_json, provider_json, ws_turn,
       reasoning_tokens, tool_uses_json, effort, response_bytes, method, detail_json
FROM calls
WHERE NOT synthetic AND session_id IS NOT NULL
  AND ((product = 'claude_code' AND host = 'api.anthropic.com'
        AND path LIKE '/v1/messages%' AND path NOT LIKE '/v1/messages/count_tokens%')
    OR (product = 'codex' AND host = 'chatgpt.com' AND path = '/backend-api/codex/responses'
        AND (ws_turn IS NOT NULL OR method = 'POST')));

CREATE OR REPLACE VIEW cc_calls AS SELECT * FROM agent_calls WHERE hidden_kind IS NULL;
CREATE OR REPLACE VIEW hidden_calls AS SELECT * FROM agent_calls WHERE hidden_kind IS NOT NULL;

-- Claude Code views (handoff §1.1): field names verified against captured traffic.

CREATE OR REPLACE VIEW cc_events AS
SELECT correlation_id, row_ns AS row_ts, ns AS ts, name, session_id, person_id AS account_uuid,
       org_id AS organization_uuid, device_id, metadata_json::JSON AS md, env_json::JSON AS env, head_sha, seq,
       prompt_id, event_key, source_file, request_id, client_request_id, tool_use_id
FROM analytics WHERE source = 'claude_code';

-- Codex's own analytics (codex_turn_event, codex_thread_initialized, …): the params are `md`.
CREATE OR REPLACE VIEW codex_events AS
SELECT correlation_id, ns AS ts, name, session_id, prompt_id, metadata_json::JSON AS md, event_key
FROM analytics WHERE source = 'codex';
"""


_COPY = re.compile(r"^(h[0-9a-f]{16})-\d+$")


class Store(Protocol):
    """What the endpoint code needs from a store (implemented by buckets.BucketStore / v1.V1Store)."""

    data_dir: pathlib.Path
    memory_limit: str | None

    def query(self, sql: str, params: Sequence[object] | None = None) -> list[tuple[Any, ...]]: ...

    def query_dicts(self, sql: str, params: Sequence[object] | None = None) -> list[dict[str, Any]]: ...

    def memo(self, key: tuple[Any, ...], build: Callable[[], T]) -> T: ...

    def raw_rows(self, correlation_id: str) -> list[dict[str, Any]]: ...

    def raw_event(self, correlation_id: str, event_key: str, source_file: str) -> dict[str, Any] | None: ...

    def maybe_ingest(self, min_interval_s: float) -> None: ...

    @property
    def commit(self) -> object: ...

    @property
    def newest_ns(self) -> int | None: ...

    @property
    def content_retained(self) -> bool | None: ...


class _RawReader(Protocol):
    raw: RawSource | None

    def query(self, sql: str, params: Sequence[object] | None = None) -> list[tuple[Any, ...]]: ...


def raw_rows(store: _RawReader, correlation_id: str) -> list[dict[str, Any]]:
    """Every raw row of one transaction, in the order the observer wrote them, read from its
    lineage (the pod's files from first_file to source_file; a few earlier ones too when its
    headers aren't there)."""
    if store.raw is None:
        return []
    found = store.query(
        "SELECT min(first_file), max(source_file) FROM calls WHERE correlation_id = ?", [correlation_id]
    )
    first, last = found[0] if found else (None, None)
    if not first:
        found = store.query(
            "SELECT min(source_file), max(source_file) FROM analytics WHERE correlation_id = ?", [correlation_id]
        )
        first, last = found[0] if found else (None, None)
    if not first:
        return []
    last = last or first
    a, b = raw.folder_of(first), raw.folder_of(last)
    first_name, last_name = first.rsplit("/", 1)[1], last.rsplit("/", 1)[1]
    keys = []
    for d in raw.pod_hours(a, b):
        keys += [d + n for n in store.raw.folder(d) if (d != a or n >= first_name) and (d != b or n <= last_name)]
    rows = read_rows(store.raw, keys, correlation_id)
    if not any(r.get("phase") == "request_headers" for r in rows):  # first_file is best effort
        earlier = [a + n for n in store.raw.folder(a) if n < first_name][-3:]
        rows = read_rows(store.raw, earlier, correlation_id) + rows
    return rows


def raw_event(source: RawSource | None, correlation_id: str, event_key: str, source_file: str) -> dict[str, Any] | None:
    """The raw client_event row an analytics event came from."""
    if source is None:
        return None
    return event_row(raw.lines(source.read(source_file)), correlation_id, event_key)


def read_rows(source: RawSource, keys: list[str], correlation_id: str) -> list[dict[str, Any]]:
    out = []
    for k in keys:
        out += rows_of(raw.lines(source.read(k)), correlation_id)
    return out


def rows_of(lines: list[bytes], correlation_id: str) -> list[dict[str, Any]]:
    """The raw rows among lines that are the transaction's."""
    out = []
    for line in lines:
        if correlation_id.encode() not in line:
            continue
        try:
            r = json.loads(line)
        except ValueError:
            continue
        if r.get("correlation_id") == correlation_id:
            out.append(r)
    return out


def event_row(lines: list[bytes], correlation_id: str, event_key: str) -> dict[str, Any] | None:
    """The client_event row among lines that event_key names."""
    want = event_key.split("/", 1)[1] if "/" in event_key else ""
    # A repeat of an identical row is keyed <hash>-2, -3 (the processor's eventCopies):
    # any of the identical rows is it.
    want = _COPY.sub(r"\1", want)
    for line in lines:
        try:
            r = json.loads(line)
        except ValueError:
            continue
        if r.get("correlation_id") != correlation_id or r.get("phase") != "client_event":
            continue
        ce = r.get("client_event") or {}
        ident = ce.get("id") or ("h" + hashlib.sha256(line).hexdigest()[:16])
        if ident == want and isinstance(r, dict):
            return r
    return None
