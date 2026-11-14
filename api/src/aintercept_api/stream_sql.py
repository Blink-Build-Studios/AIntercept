"""The request log over the streaming processor's stored tables (docs/STREAMING_PIPELINE.md
§4.5, request rows): what the API's /people/{id}/requests, lastDataAt and
/sessions/{id}/requests read once the bucketed tables replace the snapshots.

A key stores its own calls' rows (request_txs), its reports (request_reports), its
not-captured calls (request_missed), its analytics events by batch (batch_members) and its
newest time (session_last). Who a row belongs to comes from the dimensions (dim_account,
dim_session_account, dim_session_visible, dim_session_credential), and the values that span
a person's sessions are joined here: a prompt's number (prompt_index, codex_turns), a
request id's first report, the step number, an analytics request's summary and session.
Held to the single-pass reference row for row (tests/test_stream_sql.py).
"""

from typing import Any

import duckdb

# request_rows' columns, in order (process.RequestRow).
COLUMNS = [
    "person_id", "request_id", "ts_ns", "kind", "agent", "client", "hidden_kind", "session_id", "session_label",
    "prompt_index", "method", "path", "detail", "status", "status_err", "duration_ms", "retries", "bytes_up",
    "bytes_down", "background", "captured", "status_text", "stop_reason", "ttft_ms", "tokens_input",
    "tokens_cache_read", "tokens_cache_write", "tokens_cache_write_5m", "tokens_cache_write_1h", "tokens_output",
    "tokens_accuracy", "content_encoding", "error_message", "error_after_ms", "error_bytes_received",
    "error_retries", "error_retry_succeeded_ns", "model", "effort", "step_index", "tool_calls", "usage_missing",
    "text",
]  # fmt: skip

# A person's sessions: their email group's accounts' (nothing weaker than an email links accounts).
_PERSON_SCOPE = """
acct AS (SELECT account_id, lower(email) AS em FROM dim_account WHERE coalesce(email, '') <> ''),
linked AS (
    SELECT $person AS account_id
    UNION SELECT a.account_id FROM acct a JOIN acct me ON me.account_id = $person AND a.em = me.em),
scope AS (SELECT DISTINCT session_id FROM dim_session_account WHERE account_id IN (SELECT account_id FROM linked))
"""

# "unattributed": the sessions of visible model calls no account appears on.
_UNATTRIBUTED_SCOPE = """
scope AS (SELECT session_id FROM dim_session_visible
          WHERE session_id NOT IN (SELECT session_id FROM dim_session_account))
"""

_SESSION_SCOPE = """
scope AS (SELECT $session AS session_id)
"""

# The rows of the transactions made with the scope's credentials (with $only, just that
# session's), and the scope's not-captured calls, with what spans them joined.
_BATCH_MEMBERS = """
batch_session AS (
    SELECT batch_id, session_id FROM batch_members WHERE session_id <> ''
    QUALIFY row_number() OVER (PARTITION BY batch_id ORDER BY ns, session_id) = 1),
batch AS (
    SELECT batch_id, count(*) AS n, min(name) FILTER (WHERE name <> '') AS name FROM batch_members
    GROUP BY batch_id),"""

# The same two values from each key's part of a batch (docs/PIPELINE.md v7 §3.4, batch_parts: a
# key's events of the batch, their least non-empty name, its earliest (ns, session) with a session):
# the count is the parts' sum, the name their least, the session the earliest (ns, session).
_BATCH_PARTS = """
batch_session AS (
    SELECT batch_id, first_session AS session_id FROM batch_parts WHERE coalesce(first_session, '') <> ''
    QUALIFY row_number() OVER (PARTITION BY batch_id ORDER BY first_ns, first_session) = 1),
batch AS (SELECT batch_id, sum(n)::BIGINT AS n, min(min_name) AS name FROM batch_parts GROUP BY batch_id),"""

_ROWS = """
hashes AS (SELECT DISTINCT key_hash FROM dim_session_credential WHERE session_id IN (SELECT session_id FROM scope)),
{batch}
txs AS (
    SELECT t.*, coalesce(nullif(t.own_session, ''), bs.session_id) AS tx_session
    FROM request_txs t LEFT JOIN batch_session bs ON bs.batch_id = t.call_id
    WHERE t.is_tx AND t.key_hash <> '' AND t.key_hash IN (SELECT key_hash FROM hashes)),
scoped AS (SELECT * FROM txs WHERE CAST($only AS VARCHAR) IS NULL OR tx_session = CAST($only AS VARCHAR)),
cc_index AS (
    SELECT prompt_id, prompt_index AS n FROM prompt_index WHERE session_id IN (SELECT session_id FROM scope)
    QUALIFY row_number() OVER (PARTITION BY prompt_id ORDER BY ts_ns DESC, event_key DESC) = 1),
codex_numbers AS (
    SELECT session_id, prompt_id, row_number() OVER (PARTITION BY session_id ORDER BY first_ns, prompt_id) AS n
    FROM codex_turns WHERE session_id IN (SELECT session_id FROM scope)),
codex_index AS (
    SELECT prompt_id, n FROM codex_numbers
    QUALIFY row_number() OVER (PARTITION BY prompt_id ORDER BY session_id DESC) = 1),
prompt_numbers AS (
    SELECT coalesce(x.prompt_id, c.prompt_id) AS prompt_id, coalesce(x.n, c.n) AS n
    FROM cc_index c FULL OUTER JOIN codex_index x ON x.prompt_id = c.prompt_id),
report_ids AS (
    SELECT req_id AS rid, ts_ns, event_key, ttft_ms, cache_write_5m, cache_write_1h FROM request_reports
    WHERE session_id IN (SELECT session_id FROM scope) AND req_id <> ''
    UNION ALL
    SELECT client_req_id, ts_ns, event_key, ttft_ms, cache_write_5m, cache_write_1h FROM request_reports
    WHERE session_id IN (SELECT session_id FROM scope) AND client_req_id <> ''),
first_report AS (
    SELECT * FROM report_ids QUALIFY row_number() OVER (PARTITION BY rid ORDER BY ts_ns, event_key) = 1),
steps AS (
    SELECT call_id, row_number() OVER (PARTITION BY prompt_id ORDER BY row.ts_ns, row.request_id) AS n
    FROM scoped WHERE kind = 'model' AND prompt_id <> '' AND hidden_kind = ''),
tx_rows AS (
    SELECT 0 AS src, '' AS ord_session, '' AS ord_key, t.row AS r, t.tx_session AS row_session,
        CASE WHEN t.prompt_id <> '' THEN pn.n END AS row_prompt_index,
        CASE WHEN t.kind <> 'model' THEN NULL WHEN r1.rid IS NOT NULL THEN r1.ttft_ms ELSE r2.ttft_ms END AS row_ttft,
        CASE WHEN t.kind <> 'model' THEN t.row.tokens_cache_write_5m
             WHEN r1.rid IS NOT NULL THEN r1.cache_write_5m ELSE r2.cache_write_5m END AS row_5m,
        CASE WHEN t.kind <> 'model' THEN t.row.tokens_cache_write_1h
             WHEN r1.rid IS NOT NULL THEN r1.cache_write_1h ELSE r2.cache_write_1h END AS row_1h,
        st.n AS row_step,
        CASE WHEN t.kind <> 'analytics' THEN t.row.detail
             WHEN t.product = 'codex' AND b.name IS NOT NULL THEN
                 CASE WHEN b.n = 1 THEN b.name ELSE b.name || ' · ' || b.n::VARCHAR || ' events' END
             ELSE coalesce(b.n, 0)::VARCHAR || ' events' END AS row_detail
    FROM scoped t
    LEFT JOIN prompt_numbers pn ON pn.prompt_id = t.prompt_id
    LEFT JOIN first_report r1 ON t.req_id <> '' AND r1.rid = t.req_id
    LEFT JOIN first_report r2 ON t.client_req_id <> '' AND r2.rid = t.client_req_id
    LEFT JOIN steps st ON st.call_id = t.call_id
    LEFT JOIN batch b ON b.batch_id = t.call_id),
missed_rows AS (
    SELECT 1 AS src, m.session_id AS ord_session, m.event_key AS ord_key, m.row AS r, m.row.session_id AS row_session,
        m.row.prompt_index AS row_prompt_index,
        CASE WHEN r1.rid IS NOT NULL THEN r1.ttft_ms ELSE r2.ttft_ms END AS row_ttft,
        CASE WHEN r1.rid IS NOT NULL THEN r1.cache_write_5m ELSE r2.cache_write_5m END AS row_5m,
        CASE WHEN r1.rid IS NOT NULL THEN r1.cache_write_1h ELSE r2.cache_write_1h END AS row_1h,
        m.row.step_index AS row_step, m.row.detail AS row_detail
    FROM request_missed m
    LEFT JOIN first_report r1 ON m.req_id <> '' AND r1.rid = m.req_id
    LEFT JOIN first_report r2 ON m.client_req_id <> '' AND r2.rid = m.client_req_id
    WHERE m.session_id IN (SELECT session_id FROM scope)),
all_rows AS (SELECT * FROM tx_rows UNION ALL SELECT * FROM missed_rows)
"""


def _rows(parts: bool) -> str:
    return _ROWS.format(batch=_BATCH_PARTS if parts else _BATCH_MEMBERS)


_OVERRIDES = {
    "session_id": "row_session",
    "prompt_index": "row_prompt_index",
    "ttft_ms": "row_ttft",
    "tokens_cache_write_5m": "row_5m",
    "tokens_cache_write_1h": "row_1h",
    "step_index": "row_step",
    "detail": "row_detail",
}

_ORDER = "r.ts_ns, r.request_id, src, ord_session, ord_key"


def _select(person_expr: str) -> str:
    cols = []
    for c in COLUMNS:
        if c == "person_id":
            cols.append(f"{person_expr} AS person_id")
        else:
            cols.append(f"{_OVERRIDES.get(c, 'r.' + c)} AS {c}")
    return ", ".join(cols)


def _dicts(cur: duckdb.DuckDBPyConnection) -> list[dict[str, Any]]:
    cols = [d[0] for d in cur.description or []]
    return [dict(zip(cols, r, strict=True)) for r in cur.fetchall()]


def person_request_rows_sql(person: str) -> tuple[str, dict[str, object]]:
    """A person's request log's query and parameters (request_rows, person_id = person)."""
    scope = _UNATTRIBUTED_SCOPE if person == "unattributed" else _PERSON_SCOPE
    sql = f"WITH {scope}, {_rows(False)} SELECT {_select('$person')} FROM all_rows ORDER BY {_ORDER}"  # noqa: S608
    return sql, {"person": person, "only": None}


def person_request_rows(con: duckdb.DuckDBPyConnection, person: str) -> list[dict[str, Any]]:
    """A person's request log (request_rows, person_id = person), by time then id."""
    sql, params = person_request_rows_sql(person)
    return _dicts(con.execute(sql, params))


def person_last(con: duckdb.DuckDBPyConnection, person: str) -> int | None:
    """A person's newest data time: the newest of their sessions' (lastDataAt)."""
    scope = _UNATTRIBUTED_SCOPE if person == "unattributed" else _PERSON_SCOPE
    sql = f"WITH {scope} SELECT max(last_ns) FROM session_last WHERE session_id IN (SELECT session_id FROM scope)"  # noqa: S608
    params = {"person": person} if person != "unattributed" else {}
    row = con.execute(sql, params).fetchone()
    return None if row is None else row[0]


def session_request_rows_sql(
    con: duckdb.DuckDBPyConnection, session: str, *, parts: bool = False
) -> tuple[str, dict[str, object]]:
    """A session's Requests tab's query and parameters (session_request_rows): its own rows,
    and for a Codex thread its client's calls no thread names, from its start to a minute after
    its last activity. Each row with its place (seq) and an empty person_id. With parts, a batch's
    session, count and name come from batch_parts rather than batch_members."""
    sr = con.execute(
        "SELECT product, coalesce(client, ''), start_ns // 1000, (last_ns + 60000000000) // 1000"
        " FROM session_rows WHERE session_id = $session",
        {"session": session},
    ).fetchone()
    codex = sr is not None and sr[0] == "codex"
    client, lo, hi = (sr[1], sr[2], sr[3]) if sr is not None else ("", 0, 0)
    keep = "row_session = $session"
    if codex:
        keep += (
            " OR (row_session IS NULL AND r.agent = 'codex' AND r.kind <> 'model'"
            " AND coalesce(r.client, '') = $client AND r.ts_ns // 1000 BETWEEN $lo AND $hi)"
        )
    sql = (
        f"WITH {_SESSION_SCOPE}, {_rows(parts)} SELECT row_number() OVER (ORDER BY {_ORDER}) - 1 AS seq,"  # noqa: S608
        f" {_select(chr(39) * 2)} FROM all_rows WHERE {keep} ORDER BY {_ORDER}"
    )
    params: dict[str, object] = {"session": session, "only": None if codex else session}
    if codex:
        params.update(client=client, lo=lo, hi=hi)
    return sql, params


def session_request_rows(con: duckdb.DuckDBPyConnection, session: str) -> list[dict[str, Any]]:
    """A session's Requests tab (session_request_rows_sql's rows)."""
    sql, params = session_request_rows_sql(con, session)
    return _dicts(con.execute(sql, params))
