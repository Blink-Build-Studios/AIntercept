"""The API's tables from the stream state's tables (docs/STREAMING_PIPELINE.md §4.5, §5).

The stream processor stores each key's rows (aintercept/internal/stream/state): values a row
used to copy from its session or person are lookups, and positions are sort keys. Here those
stored tables, loaded as st_<table>, become the tables today's endpoints read, with today's
names, columns and order: the endpoints' SQL runs unchanged. Each piece is held to the
single-pass reference (tests/test_events_sql.py, test_prompts_sql.py, test_stream_sql.py,
test_session_sql.py, test_person_sql.py), and the whole to today's API, endpoint by endpoint
(tests/test_stream_pipeline.py).
"""

import importlib.resources
import re
from collections.abc import Iterable

import duckdb

from aintercept_api import store as legacy
from aintercept_api import stream_sql

# Every table the stream state writes (state.TableTypes).
STATE_TABLES = (
    "presence_segments",
    "event_rows",
    "session_tools",
    "session_request_links",
    "prompt_index",
    "codex_turns",
    "dim_account",
    "dim_session_account",
    "dim_session_visible",
    "dim_session_person",
    "dim_person_device",
    "dim_session_credential",
    "dim_account_events",
    "dim_account_device",
    "dim_codex_plan",
    "prompt_items",
    "prompt_steps",
    "prompt_step_tools",
    "prompt_session",
    "session_marks",
    "mark_prompt_index",
    "request_txs",
    "request_reports",
    "request_missed",
    "batch_members",
    "session_last",
    "session_rows",
    "session_prompt_ids",
    "session_devices",
    "session_call_accounts",
    "session_missed",
    "session_edges",
    "session_title_calls",
    "call_tool_uses",
)

# Each stored table's columns (key, id and tomb aside), as the processor's table codec writes
# them: an empty table in scope is made with these (tests/test_stream_pipeline.py checks them
# against a layout the processor wrote).
STATE_SCHEMA: dict[str, str] = {
    "batch_members": "batch_id VARCHAR, event_key VARCHAR, ns BIGINT, session_id VARCHAR, name VARCHAR",
    "call_tool_uses": "session_id VARCHAR, ts_ns BIGINT, kind VARCHAR, server VARCHAR, tool VARCHAR, call_id VARCHAR",
    "codex_turns": "session_id VARCHAR, prompt_id VARCHAR, first_ns BIGINT",
    "dim_account": "account_id VARCHAR, email VARCHAR, name VARCHAR, org VARCHAR, seen_ns BIGINT, email_ns BIGINT, name_ns BIGINT, org_ns BIGINT",  # noqa: E501
    "dim_account_device": "account_id VARCHAR, device_id VARCHAR, on_events BOOLEAN, platform STRUCT(v VARCHAR, ts BIGINT, ok BOOLEAN), terminal STRUCT(v VARCHAR, ts BIGINT, ok BOOLEAN), shell STRUCT(v VARCHAR, ts BIGINT, ok BOOLEAN)",  # noqa: E501
    "dim_account_events": "account_id VARCHAR, org VARCHAR, org_ns BIGINT, plan STRUCT(v VARCHAR, ts BIGINT, ok BOOLEAN)",  # noqa: E501
    "dim_codex_plan": "account_id VARCHAR, plan STRUCT(v VARCHAR, ts BIGINT, ok BOOLEAN)",
    "dim_person_device": "person_id VARCHAR, device_id VARCHAR",
    "dim_session_account": "session_id VARCHAR, account_id VARCHAR",
    "dim_session_credential": "session_id VARCHAR, key_hash VARCHAR",
    "dim_session_person": "session_id VARCHAR, person_id VARCHAR",
    "dim_session_visible": "session_id VARCHAR",
    "event_rows": "session_id VARCHAR, seq BIGINT, event_id VARCHAR, ts_ns BIGINT, name VARCHAR, name_lower VARCHAR, internal BOOLEAN, summary VARCHAR, summary_lower VARCHAR, marker VARCHAR, link_kind VARCHAR, link_id VARCHAR, link_label VARCHAR, prompt_index BIGINT, head_sha VARCHAR, hook VARCHAR, git_op VARCHAR, pr BIGINT, recap_key BOOLEAN, recap BOOLEAN, count BIGINT, unnamed BOOLEAN, tool_name VARCHAR, is_mcp BOOLEAN, hook_ms DOUBLE, argv0 VARCHAR, category VARCHAR, outside_cwd BOOLEAN, hook_name VARCHAR, operation VARCHAR, pr_number VARCHAR, cache_5m BIGINT, cache_1h BIGINT, cost_usd DOUBLE, batch_id VARCHAR, n BIGINT, prompt_id VARCHAR, ev_seq BIGINT, row_ns BIGINT, order_md VARCHAR, event_key VARCHAR",  # noqa: E501
    "mark_prompt_index": "session_id VARCHAR, prompt_id VARCHAR, prompt_index BIGINT",
    "presence_segments": "session_id VARCHAR, grp INTEGER, from_ns BIGINT, to_ns BIGINT, state VARCHAR",
    "prompt_index": "session_id VARCHAR, prompt_id VARCHAR, prompt_index BIGINT, ts_ns BIGINT, event_key VARCHAR",
    "prompt_items": "\"row\" STRUCT(session_id VARCHAR, seq BIGINT, t_ns BIGINT, \"type\" VARCHAR, typed BOOLEAN, session_last_ns BIGINT, prompt_id VARCHAR, prompt_index BIGINT, duration_turn_ms DOUBLE, duration_report_ms DOUBLE, open_tail BOOLEAN, unfinished_max_ns BIGINT, text_retained BOOLEAN, \"text\" VARCHAR, text_chars BIGINT, pasted_bytes BIGINT, images BIGINT, steps BIGINT, tokens_in BIGINT, tokens_out BIGINT, tool_counts_json VARCHAR, frustrated BOOLEAN, interrupted BOOLEAN, destructive BIGINT, turn_report_json VARCHAR, hidden_json VARCHAR, between_kind VARCHAR, detail VARCHAR, request_id VARCHAR, tokens_from BIGINT, tokens_to BIGINT, wait_ms DOUBLE), \"group\" INTEGER, ord_corr VARCHAR, ord_seq BIGINT, ord_row_ns BIGINT, ord_name VARCHAR, ord_md VARCHAR, ord_key VARCHAR, has_input BOOLEAN, has_turn_end BOOLEAN, report_status BOOLEAN",  # noqa: E501
    "prompt_session": "session_id VARCHAR, product VARCHAR, last_ns BIGINT, has_prompt_event BOOLEAN, last_prompt_event_ns BIGINT",  # noqa: E501
    "prompt_step_tools": "session_id VARCHAR, tools_request_id VARCHAR, tools STRUCT(tools_json VARCHAR, tools_live_json VARCHAR, commands VARCHAR[], exit_codes BIGINT[], content_live BOOLEAN, commands_live VARCHAR[], exit_codes_live BIGINT[])",  # noqa: E501
    "prompt_steps": "step STRUCT(session_id VARCHAR, seq BIGINT, step BIGINT, request_id VARCHAR, t_ns BIGINT, end_ns BIGINT, kind VARCHAR, model VARCHAR, request_class VARCHAR, tokens_input BIGINT, tokens_cache_read BIGINT, tokens_cache_write BIGINT, tokens_cache_write_5m BIGINT, tokens_cache_write_1h BIGINT, tokens_output BIGINT, tokens_accuracy VARCHAR, duration_ms DOUBLE, ttft_ms DOUBLE, stop_reason VARCHAR, cost_usd DOUBLE, failed BOOLEAN, events_pending_if_live BOOLEAN, agent_type VARCHAR, tools_json VARCHAR, tools_live_json VARCHAR, does VARCHAR, commands VARCHAR[], exit_codes BIGINT[], content_live BOOLEAN, commands_live VARCHAR[], exit_codes_live BIGINT[], patches_json VARCHAR, \"text\" VARCHAR, usage_missing VARCHAR), prompt_id VARCHAR, has_success BOOLEAN, pend_end BIGINT, tools_rid VARCHAR, detail_calls BOOLEAN",  # noqa: E501
    "request_missed": "session_id VARCHAR, event_key VARCHAR, req_id VARCHAR, client_req_id VARCHAR, \"row\" STRUCT(person_id VARCHAR, request_id VARCHAR, ts_ns BIGINT, kind VARCHAR, agent VARCHAR, client VARCHAR, hidden_kind VARCHAR, session_id VARCHAR, session_label VARCHAR, prompt_index BIGINT, \"method\" VARCHAR, path VARCHAR, detail VARCHAR, status BIGINT, status_err BOOLEAN, duration_ms DOUBLE, retries BIGINT, bytes_up BIGINT, bytes_down BIGINT, background BOOLEAN, captured BOOLEAN, status_text VARCHAR, stop_reason VARCHAR, ttft_ms DOUBLE, tokens_input BIGINT, tokens_cache_read BIGINT, tokens_cache_write BIGINT, tokens_cache_write_5m BIGINT, tokens_cache_write_1h BIGINT, tokens_output BIGINT, tokens_accuracy VARCHAR, content_encoding VARCHAR, error_message VARCHAR, error_after_ms DOUBLE, error_bytes_received BIGINT, error_retries BIGINT, error_retry_succeeded_ns BIGINT, model VARCHAR, effort VARCHAR, step_index BIGINT, tool_calls VARCHAR[], usage_missing VARCHAR, \"text\" VARCHAR)",  # noqa: E501
    "request_reports": "session_id VARCHAR, ts_ns BIGINT, event_key VARCHAR, req_id VARCHAR, client_req_id VARCHAR, ttft_ms DOUBLE, cache_write_5m BIGINT, cache_write_1h BIGINT",  # noqa: E501
    "request_txs": "call_id VARCHAR, correlation_id VARCHAR, key_hash VARCHAR, own_session VARCHAR, prompt_id VARCHAR, req_id VARCHAR, client_req_id VARCHAR, kind VARCHAR, hidden_kind VARCHAR, product VARCHAR, is_tx BOOLEAN, \"row\" STRUCT(person_id VARCHAR, request_id VARCHAR, ts_ns BIGINT, kind VARCHAR, agent VARCHAR, client VARCHAR, hidden_kind VARCHAR, session_id VARCHAR, session_label VARCHAR, prompt_index BIGINT, \"method\" VARCHAR, path VARCHAR, detail VARCHAR, status BIGINT, status_err BOOLEAN, duration_ms DOUBLE, retries BIGINT, bytes_up BIGINT, bytes_down BIGINT, background BOOLEAN, captured BOOLEAN, status_text VARCHAR, stop_reason VARCHAR, ttft_ms DOUBLE, tokens_input BIGINT, tokens_cache_read BIGINT, tokens_cache_write BIGINT, tokens_cache_write_5m BIGINT, tokens_cache_write_1h BIGINT, tokens_output BIGINT, tokens_accuracy VARCHAR, content_encoding VARCHAR, error_message VARCHAR, error_after_ms DOUBLE, error_bytes_received BIGINT, error_retries BIGINT, error_retry_succeeded_ns BIGINT, model VARCHAR, effort VARCHAR, step_index BIGINT, tool_calls VARCHAR[], usage_missing VARCHAR, \"text\" VARCHAR)",  # noqa: E501
    "session_call_accounts": "session_id VARCHAR, account_id VARCHAR",
    "session_devices": "session_id VARCHAR, device_id VARCHAR",
    "session_edges": "parent_session_id VARCHAR, child_session_id VARCHAR, kind VARCHAR, child_first_ns BIGINT",
    "session_last": "session_id VARCHAR, last_ns BIGINT",
    "session_marks": "mark STRUCT(session_id VARCHAR, seq BIGINT, kind VARCHAR, t_ns BIGINT, to_ns BIGINT, request_id VARCHAR, request_class VARCHAR, model VARCHAR, context BIGINT, output BIGINT, prompt_index BIGINT, \"input\" BIGINT, cache_read BIGINT, cache_write BIGINT, prompt_id VARCHAR, unreported BOOLEAN, friction_kind VARCHAR, detail VARCHAR, tokens_from BIGINT, tokens_to BIGINT, idle BOOLEAN, wait_ms DOUBLE, \"mode\" VARCHAR, prev_mode VARCHAR, ip VARCHAR, ipv6 BOOLEAN), \"group\" INTEGER, sub INTEGER, ord_corr VARCHAR, ord_seq BIGINT, ord_row_ns BIGINT, ord_name VARCHAR, ord_md VARCHAR, ord_key VARCHAR, prompt_ref VARCHAR, point_ip VARCHAR",  # noqa: E501
    "session_missed": "session_id VARCHAR, ts_ns BIGINT, event_key VARCHAR",
    "session_prompt_ids": "session_id VARCHAR, prompt_id VARCHAR",
    "session_request_links": "session_id VARCHAR, request_id VARCHAR, call_id VARCHAR, label VARCHAR",
    "session_rows": "session_id VARCHAR, start_ns BIGINT, last_ns BIGINT, product VARCHAR, agent VARCHAR, client VARCHAR, client_version VARCHAR, client_version_ns BIGINT, origin_kind VARCHAR, origin_session_id VARCHAR, origin_json VARCHAR, hidden_calls BIGINT, own_title VARCHAR, repo VARCHAR, \"commit\" VARCHAR, prompts BIGINT, captured_from_prompt BIGINT, model_calls BIGINT, events BIGINT, frustrated BIGINT, interrupted BIGINT, destructive BIGINT, bypass_pct DOUBLE, cov_reported BIGINT, cov_captured BIGINT, cov_not_captured BIGINT, fields_missing BOOLEAN, person_id VARCHAR, device_id VARCHAR, device_platform VARCHAR, device_terminal VARCHAR, device_shell VARCHAR, networks BIGINT, effort VARCHAR, tokens_input BIGINT, tokens_cache_read BIGINT, tokens_cache_write BIGINT, tokens_output BIGINT, tokens_accuracy VARCHAR, tool_calls BIGINT, has_facts BOOLEAN, facts_cwd VARCHAR, facts_approval VARCHAR, facts_sandbox VARCHAR, limits_json VARCHAR, limits_ns BIGINT, outputs_known BIGINT, queries BIGINT, bypassed BIGINT, cli_version VARCHAR, cli_version_ns BIGINT, rl_status VARCHAR, rl_util DOUBLE, rl_overage VARCHAR, rl_ns BIGINT, sandbox VARCHAR, sandbox_ns BIGINT, custom_ca BOOLEAN",  # noqa: E501
    "session_title_calls": "session_id VARCHAR, ts_ns BIGINT, call_id VARCHAR, account_id VARCHAR, title VARCHAR",
    "session_tools": "session_id VARCHAR, kind VARCHAR, server VARCHAR, tool VARCHAR, first_ns BIGINT",
}  # fmt: skip

# Which keys each stored table is read from: "s:" a session's (its bucket), "*" every bucket.
# The "*" ones are small dimensions, or rows a person's view needs from keys no session names:
# the credential-and-day keys' transactions (request_txs) and analytics batches spanning
# sessions (batch_members).
STATE_ROUTING: dict[str, tuple[str, str]] = {
    t: (
        "*"
        if t.startswith("dim_")
        or t in ("request_txs", "batch_members", "session_call_accounts", "session_edges", "session_title_calls")
        else "s:",
        "",
    )
    for t in STATE_TABLES
}

# The stored tables whose names are also the API's (they're rebuilt into the API's shape).
_SHARED_NAMES = {
    "presence_segments",
    "event_rows",
    "session_tools",
    "prompt_steps",
    "session_marks",
    "session_rows",
    "call_tool_uses",
}
# session_rows' inputs, under the names buckets._session_rows reads (_s_<table>).
SESSION_INPUTS = (
    "session_rows",
    "session_prompt_ids",
    "session_devices",
    "session_call_accounts",
    "session_missed",
    "session_edges",
    "session_title_calls",
)
_PROMPT_INPUTS = (
    "prompt_items",
    "prompt_steps",
    "prompt_step_tools",
    "prompt_session",
    "session_marks",
    "mark_prompt_index",
)
_PROMPTS_SQL = importlib.resources.files("aintercept_api").joinpath("sql/prompts.sql").read_text()
_PROMPTS_ON_STORED = re.sub(r"\b(" + "|".join(_PROMPT_INPUTS) + r")\b", r"st_\1", _PROMPTS_SQL)

# The order today's store keeps each table in (store._SNAPSHOT_SQL).
_ORDER = {
    "request_rows": "person_id, ts_ns, request_id",
    "prompt_rows": "session_id, seq",
    "session_marks": "session_id, kind, seq",
    "presence_segments": "session_id, seq",
    "prompt_steps": "session_id, seq, step",
    "event_rows": "session_id, seq",
    "session_tools": "session_id",
    "session_request_rows": "scope_session_id, seq",
    "call_tool_uses": "session_id, ts_ns",
}


def _empty(con: duckdb.DuckDBPyConnection, table: str) -> None:
    con.execute(legacy._EMPTY[table])


def _insert(con: duckdb.DuckDBPyConnection, table: str, select: str, params: object = None) -> None:
    """Today's table, in today's columns and order, from select (rows by name)."""
    _empty(con, table)
    order = _ORDER.get(table)
    sql = f"INSERT INTO {table} BY NAME SELECT * FROM ({select})" + (f" ORDER BY {order}" if order else "")  # noqa: S608 (fixed names)
    con.execute(sql, params) if params is not None else con.execute(sql)


def stored_views(con: duckdb.DuckDBPyConnection) -> None:
    """The stored tables (st_<table>) under their own names where the API has no table of that
    name, and session_rows' inputs as _s_<table>: what the SQL modules read."""
    for t in STATE_TABLES:
        if t not in _SHARED_NAMES:
            con.execute(f"CREATE OR REPLACE VIEW {t} AS SELECT * FROM st_{t}")  # noqa: S608 (fixed names)
    for t in SESSION_INPUTS:
        con.execute(f"CREATE OR REPLACE VIEW _s_{t} AS SELECT * FROM st_{t}")  # noqa: S608


def event_tables(con: duckdb.DuckDBPyConnection) -> None:
    """event_rows and call_tool_uses from the stored rows (st_event_rows, st_session_request_links,
    st_prompt_index, st_codex_turns, st_call_tool_uses). An event's place is (time, batch, its
    rank in the batch); a prompt's number is its Codex turn's place in the thread when it's a
    turn, else its latest tengu_input_prompt's; a request link names the request id's newest
    visible call, or is "reported:<id>" when no call carries it."""
    con.execute("""CREATE OR REPLACE TEMP VIEW _prompt_numbers AS
WITH cx AS (
    SELECT session_id, prompt_id, row_number() OVER (PARTITION BY session_id ORDER BY first_ns, prompt_id) AS n
    FROM st_codex_turns),
cc AS (SELECT session_id, prompt_id, prompt_index AS n FROM st_prompt_index)
SELECT coalesce(x.session_id, c.session_id) AS session_id, coalesce(x.prompt_id, c.prompt_id) AS prompt_id,
       coalesce(x.n, c.n) AS n
FROM cc c FULL OUTER JOIN cx x ON x.session_id = c.session_id AND x.prompt_id = c.prompt_id""")
    _insert(
        con,
        "event_rows",
        """SELECT e.* EXCLUDE (seq, link_id, link_label, prompt_index, batch_id, n, prompt_id, ev_seq, row_ns,
                          order_md, event_key),
       row_number() OVER (PARTITION BY e.session_id ORDER BY e.ts_ns, e.batch_id, e.n) - 1 AS seq,
       CASE WHEN e.link_kind = 'request' THEN coalesce(l.call_id, 'reported:' || e.link_id) ELSE e.link_id END
           AS link_id,
       CASE WHEN e.link_kind = 'request' THEN coalesce(l.label, 'not captured')
            WHEN e.link_kind = 'prompt' THEN coalesce('#' || p.n::VARCHAR, 'prompt')
            ELSE e.link_label END AS link_label,
       CASE WHEN coalesce(e.prompt_id, '') <> '' THEN pi.n END AS prompt_index
FROM st_event_rows e
LEFT JOIN st_session_request_links l
    ON e.link_kind = 'request' AND l.session_id = e.session_id AND l.request_id = e.link_id
LEFT JOIN _prompt_numbers p ON e.link_kind = 'prompt' AND p.session_id = e.session_id AND p.prompt_id = e.link_id
LEFT JOIN _prompt_numbers pi ON pi.session_id = e.session_id AND pi.prompt_id = e.prompt_id""",
    )
    _insert(
        con,
        "call_tool_uses",
        "SELECT DISTINCT session_id, ts_ns, kind, server, tool FROM st_call_tool_uses "
        "ORDER BY session_id, ts_ns, kind, server, tool",
    )
    con.execute("DROP VIEW _prompt_numbers")


def session_tables(con: duckdb.DuckDBPyConnection) -> None:
    """presence_segments, session_tools, and the prompt list's and marks' tables (sql/prompts.sql)."""
    _insert(
        con,
        "presence_segments",
        "SELECT session_id, row_number() OVER (PARTITION BY session_id ORDER BY grp, from_ns) - 1 AS seq, "
        "from_ns, to_ns, state FROM st_presence_segments",
    )
    _insert(con, "session_tools", "SELECT session_id, kind, server, tool, first_ns FROM st_session_tools")
    con.execute(_PROMPTS_ON_STORED)
    for view, table in (
        ("v_prompt_rows", "prompt_rows"),
        ("v_prompt_steps", "prompt_steps"),
        ("v_session_marks", "session_marks"),
    ):
        _insert(con, table, f"SELECT * FROM {view}")  # noqa: S608 (fixed names)


def accounts(con: duckdb.DuckDBPyConnection) -> None:
    """accounts (who an account is): the dimension's newest sightings."""
    _insert(con, "accounts", "SELECT account_id, email, name, org, seen_ns FROM st_dim_account")


def request_tables(
    con: duckdb.DuckDBPyConnection, people: Iterable[str] | None, sessions: Iterable[str] | None
) -> None:
    """request_rows and person_last for the scope's people, session_request_rows for its sessions
    (stream_sql): the request log and Requests tabs the endpoints page through."""
    _empty(con, "request_rows")
    _empty(con, "person_last")
    _empty(con, "session_request_rows")
    for person in sorted(set(people or ())):
        sql, params = stream_sql.person_request_rows_sql(person)
        con.execute(f"INSERT INTO request_rows BY NAME SELECT * FROM ({sql})", params)  # noqa: S608 (stream_sql's)
        con.execute("INSERT INTO person_last VALUES (?, ?)", [person, stream_sql.person_last(con, person)])
    for session in sorted(set(sessions or ())):
        sql, params = stream_sql.session_request_rows_sql(con, session)
        insert = f"INSERT INTO session_request_rows BY NAME SELECT $session AS scope_session_id, * FROM ({sql})"  # noqa: S608
        con.execute(insert, params)
    for t in ("request_rows", "session_request_rows"):
        con.execute(f"CREATE OR REPLACE TABLE {t} AS SELECT * FROM {t} ORDER BY {_ORDER[t]}")  # noqa: S608
