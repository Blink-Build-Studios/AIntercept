"""A person's rollups, as SQL over the processor's tables (docs/architecture.md, Decision 4).

Who a person is comes from `person_map` (their linked accounts, and the People page's
rows) and `person_sessions`; what their sessions hold, from the per-session facts in
`session_rows`, `session_marks` (each call, report and missed call) and `event_rows`
(each analytics event, with what the summary counts of it). A range, the sessions it
shows and `now` (live) are the request's, so they're applied here; nothing is derived
in Python but rounding (Python's round, half to even, as the rows always were).
"""

from collections.abc import Sequence
from typing import Any

from aintercept_api import claude_code as cc
from aintercept_api.store import Store

# A session's row with `live` (the request's now, then the live window).
ROWS = """SELECT s.*, ? - s.last_ns <= ? AS live FROM session_rows s"""

# The sessions a range shows (pp.sessions_in_range): started by its end, and still
# going (live: now) at its start. Parameters: lo, hi, now.
SHOWN = "start_ns <= ? AND CASE WHEN live THEN ? ELSE last_ns END >= ?"


def person_sessions(store: Store, person_id: str) -> list[str]:
    """The person's sessions (all their linked accounts'); `UNATTRIBUTED`: those no account appears on."""
    return [
        r[0]
        for r in store.query(
            "SELECT session_id FROM person_sessions WHERE person_id = ? ORDER BY session_id", [person_id]
        )
    ]


def linked(store: Store, person_id: str) -> list[str]:
    """The person's accounts: this one, and every account with the same email (sorted)."""
    rows = [r[0] for r in store.query("SELECT account_id FROM person_map WHERE person_id = ? ORDER BY 1", [person_id])]
    return rows or [person_id]


def sessions(store: Store, now: int, sids: Sequence[str]) -> list[dict[str, Any]]:
    """The sessions' rows, with `live`, by id."""
    return store.query_dicts(
        f"{ROWS} WHERE session_id IN (SELECT unnest(?::VARCHAR[])) ORDER BY session_id",  # noqa: S608 (fixed text)
        [now, cc.LIVE_WITHIN_NS, list(sids)],
    )


def windows(store: Store, timestamps_sql: str, params: Sequence[object]) -> list[tuple[Any, int, int, int]]:
    """(group, from, to, count) coverage windows: each group's timestamps (`timestamps_sql`
    selects `grp, t`) split at gaps of 15 minutes or more (claude_code.py windows)."""
    return store.query(
        f"""WITH t AS ({timestamps_sql}),
                 g AS (SELECT grp, t, lag(t) OVER (PARTITION BY grp ORDER BY t) AS prev FROM t),
                 w AS (SELECT grp, t, sum(CASE WHEN prev IS NULL OR t - prev >= ? THEN 1 ELSE 0 END)
                              OVER (PARTITION BY grp ORDER BY t ROWS UNBOUNDED PRECEDING) AS k FROM g)
            SELECT grp, min(t), max(t), count(*) FROM w GROUP BY grp, k ORDER BY grp, 2""",  # noqa: S608 (fixed text)
        [*params, cc.GAP_SPLIT_NS],
    )


# --- the People page's rows and the person header --------------------------------------------

# Each scope's (person's) sessions: `scope` selects (row_id, session_id) pairs; `kept`,
# the ones the request's `where` keeps.
_SCOPED = f"""WITH scope AS ({{scope}}),
  rs AS (SELECT sc.row_id, r.* FROM scope sc JOIN ({ROWS}) r USING (session_id)),
  kept AS (SELECT * FROM rs WHERE {{where}})"""  # noqa: S608 (fixed text)

_PEOPLE = """SELECT row_id, max(last_ns) AS last_ns, bool_or(live) AS live, count(*) AS sessions,
       len(list_distinct(flatten(list(devices)))) AS devices,
       len(list_distinct(flatten(list(prompt_ids)))) AS prompts,
       sum(tokens_output) AS tokens_out, sum(outputs_known) AS outputs_known, sum(model_calls) AS calls,
       sum(bypassed) AS bypassed, sum(queries) AS queries,
       count(*) FILTER (WHERE events IS NOT NULL) AS with_events,
       sum(cov_reported) FILTER (WHERE events IS NOT NULL) AS reported,
       sum(cov_not_captured) FILTER (WHERE events IS NOT NULL) AS missed,
       sum(cov_not_captured) FILTER (WHERE events IS NOT NULL AND product IS DISTINCT FROM 'codex') AS missed_cc,
       count(*) FILTER (WHERE product IS DISTINCT FROM 'codex' AND events IS NOT NULL) AS with_events_cc,
       max(last_ns) FILTER (WHERE product IS DISTINCT FROM 'codex') AS last_cc,
       count(*) FILTER (WHERE product IS DISTINCT FROM 'codex') AS sessions_cc,
       (list(limits_json ORDER BY limits_ns DESC) FILTER (WHERE agent = 'codex' AND limits_ns IS NOT NULL))[1]
         AS codex_limits,
       (list({'status': rl_status, 'util': rl_util, 'overage': rl_overage} ORDER BY rl_ns DESC)
          FILTER (WHERE rl_ns IS NOT NULL))[1] AS rl,
       (list(sandbox ORDER BY sandbox_ns DESC) FILTER (WHERE sandbox_ns IS NOT NULL))[1] AS sandbox,
       bool_or(custom_ca) AS custom_ca
FROM kept GROUP BY row_id"""

_AGENTS = """SELECT row_id, agent, min(start_ns) AS first_ns, len(list_distinct(flatten(list(devices)))) AS devices,
       (list(nullif(cli_version, '') ORDER BY cli_version_ns DESC, session_id)
          FILTER (WHERE cli_version_ns IS NOT NULL))[1] AS cli_version,
       (list(client_version ORDER BY client_version_ns DESC, session_id DESC)
          FILTER (WHERE client_version_ns IS NOT NULL))[1] AS client_version
FROM kept GROUP BY row_id, agent"""

# Each row's model calls per UTC day, from its sessions' call-day folds (session_call_days: per session
# and day, its call marks' count, first and last time; the day is floor(t_ns / 1e9 / 86400) in float,
# as Python's datetime.fromtimestamp read a call's time).
_ACTIVITY = """SELECT k.row_id, strftime(DATE '1970-01-01' + CAST(d.day AS INTEGER), '%Y-%m-%d') AS day, sum(d.n) AS n
FROM kept k JOIN session_call_days d USING (session_id) GROUP BY 1, 2"""

_MISSED = """SELECT k.row_id AS grp, m.t_ns AS t FROM kept k JOIN session_marks m USING (session_id)
WHERE m.kind = 'missed' AND k.events IS NOT NULL"""


def rollups(
    store: Store,
    scope_sql: str,
    scope_params: Sequence[object],
    now: int,
    where: str = "true",
    where_params: Sequence[object] = (),
) -> dict[str, dict[str, Any]]:
    """Each scope's rollup over its sessions that `where` keeps (none kept: no rollup): the
    People page row's counts, its agents (`agents`: agent -> first_ns, devices, versions),
    its daily calls (`activity`: day -> count) and missed-call windows (`windows`)."""
    head = _SCOPED.format(scope=scope_sql, where=where)
    params = [*scope_params, now, cc.LIVE_WITHIN_NS, *where_params]
    rows = {
        r["row_id"]: {**r, "agents": {}, "activity": {}, "windows": []}
        for r in store.query_dicts(f"{head} {_PEOPLE}", params)
    }
    for r in store.query_dicts(f"{head} {_AGENTS}", params):
        rows[r["row_id"]]["agents"][r["agent"]] = r
    for row_id, day, n in store.query(f"{head} {_ACTIVITY}", params):
        rows[row_id]["activity"][day] = n
    for grp, a, b, n in windows(store, f"{head} {_MISSED}", params):
        rows[grp]["windows"].append((a, b, n))
    return rows


def pct(part: int | None, whole: int | None) -> float | None:
    """`part` as a percentage of `whole`, to one decimal (Python's round); None without a whole."""
    return round(100 * (part or 0) / whole, 1) if whole else None
