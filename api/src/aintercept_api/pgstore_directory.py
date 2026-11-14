"""The directory on Postgres (docs/PIPELINE.md v7 §5, build step 7): what /people and /overview read,
in the request's one snapshot (pgstore.PgStore), for directory.py, rollups.py and person_sql.py to
run over unchanged.

Which keys (quirk 1): those whose `keys.day` is from's UTC day or later, plus every a: key (day ''),
exactly the keys v5's day partitions and index held; with no from (All), every key. A key's day is
the worker's (rollup.DayOf, at commit). The statements, each resolving the keys itself:
1. `dimensions`: the person dimensions' rows of those keys (one statement, each row tagged with its
   table): person_sql turns them into person_map, person_sessions, account_facts and
   account_devices, and dim_account gives accounts;
2. `session_rows`: those keys' sessions' rows (`key_day`), in the columns the directory reads;
3. `session_call_days`: their call-day folds (§3.5), which the three directory statements read in
   place of every call mark;
4. `missed_marks`: their missed calls (the coverage windows);
5. `agent_calls`, /overview only: its request count, the calls in [lo, hi]: agent_call_hours over the
   whole hours inside the range, agent_calls in the partial hours at its ends (and, with no range at
   all, the calls with no time, as the merge's count had them).

Everything else (the window, live, activity, filters, sort, page) is today's endpoint code.
"""

from typing import TYPE_CHECKING, Any

import duckdb

from aintercept_api import claude_code as cc
from aintercept_api import person_sql, ranges, state_sql
from aintercept_api import store as legacy
from aintercept_api.pgstore import SHAPES, _q

if TYPE_CHECKING:
    from aintercept_api.pgstore import _Run

# The dimensions person_sql reads (a session's credentials are the request logs').
DIMENSIONS = tuple(t for t in person_sql.DIMENSIONS if t != "dim_session_credential")
_PICK = "STRUCT(v VARCHAR, ts BIGINT, ok BOOLEAN)"  # flattened in Postgres as <col>_v, <col>_ts, <col>_ok
_PG_TYPES = {"VARCHAR": "text", "BIGINT": "bigint", "BOOLEAN": "boolean"}


def _dimension_shapes() -> dict[str, list[tuple[str, str]]]:
    con = duckdb.connect()
    try:
        out = {}
        for t in DIMENSIONS:
            con.execute(f"CREATE TABLE {t} ({state_sql.STATE_SCHEMA[t]})")
            out[t] = [(c, ty) for c, ty, *_ in con.execute(f"DESCRIBE {t}").fetchall()]  # noqa: S608
        return out
    finally:
        con.close()


_SHAPES = _dimension_shapes()


def _stored(t: str) -> list[tuple[str, str]]:
    """A dimension's Postgres columns and types (its picks flattened)."""
    out = []
    for c, ty in _SHAPES[t]:
        if ty.startswith("STRUCT"):
            if ty != _PICK:
                raise TypeError(f"{t}.{c}: {ty} isn't a pick (person_sql reads v, ts, ok)")
            out += [(f"{c}_v", "text"), (f"{c}_ts", "bigint"), (f"{c}_ok", "boolean")]
        else:
            out.append((c, _PG_TYPES[ty]))
    return out


# --- the statements -------------------------------------------------------------------------------

# The keys a from loads ($1: its day; '' for All, which every day is at or after).
_KEYS = "k AS (SELECT key FROM keys WHERE day >= $1 OR day = '')"
# Their sessions (a session's rows are its own key's, s:<id>).
_SESSIONS = "s AS (SELECT substr(key, 3) AS session_id FROM keys WHERE day >= $1 AND key LIKE 's:%')"


def _dimensions_sql() -> str:
    every: dict[str, str] = {}
    for t in DIMENSIONS:
        every.update(_stored(t))
    parts = []
    for t in DIMENSIONS:
        mine = dict(_stored(t))
        cols = ", ".join(_q(c) if c in mine else f"NULL::{ty} AS {_q(c)}" for c, ty in every.items())
        parts.append(f"SELECT '{t}' AS src, key, id, {cols} FROM {t} WHERE key IN (SELECT key FROM k)")  # noqa: S608
    return f"WITH {_KEYS}\n" + "\nUNION ALL ".join(parts)


DIMENSIONS_SQL = _dimensions_sql()

# The session_rows columns the directory reads (rollups.py, directory.py): a column it doesn't load is
# an SQL error, not a NULL.
SESSION_COLUMNS = [
    c
    for c, _ in SHAPES["session_rows"]
    if c
    in {
        "session_id", "start_ns", "last_ns", "product", "agent", "devices", "prompt_ids", "tokens_output",
        "outputs_known", "model_calls", "bypassed", "queries", "events", "cov_reported", "cov_not_captured",
        "limits_json", "limits_ns", "rl_status", "rl_util", "rl_overage", "rl_ns", "sandbox", "sandbox_ns",
        "custom_ca", "cli_version", "cli_version_ns", "client_version", "client_version_ns",
    }
]  # fmt: skip
SESSION_ROWS_SQL = f"SELECT {', '.join(map(_q, SESSION_COLUMNS))} FROM session_rows WHERE key_day >= $1"  # noqa: S608
CALL_DAYS_SQL = f"""WITH {_SESSIONS}
SELECT session_id, day, n, first_ns, last_ns FROM session_call_days WHERE session_id IN (SELECT session_id FROM s)"""  # noqa: S608
MISSED_SQL = f"""WITH {_SESSIONS}
SELECT session_id, seq, kind, t_ns FROM session_marks
WHERE kind = 'missed' AND session_id IN (SELECT session_id FROM s)"""  # noqa: S608
AGENT_CALLS_SQL = """SELECT
    (SELECT coalesce(sum(n), 0) FROM agent_call_hours WHERE hour >= $1 AND hour < $2)::bigint AS whole,
    (SELECT count(*) FROM agent_calls WHERE ts_ns >= $3 AND ts_ns <= $4) AS head,
    (SELECT count(*) FROM agent_calls WHERE ts_ns >= $5 AND ts_ns <= $6) AS tail,
    (SELECT count(*) FROM agent_calls WHERE $7 AND ts_ns IS NULL) AS untimed"""

HOUR_NS = 3600 * cc.NS
_MIN, _MAX = -(2**63), 2**63 - 1
_NONE = (0, -1)  # an empty range


def agent_call_ranges(lo: int | None, hi: int | None) -> list[tuple[object, str]]:
    """AGENT_CALLS_SQL's parameters for [lo, hi] (either None: open): the whole hours inside it, the
    partial hours at its two ends, and whether the calls with no time count (no range at all)."""
    a = _MIN if lo is None else lo
    b = _MAX if hi is None else hi
    first = a if lo is None else -(-a // HOUR_NS) * HOUR_NS  # the first hour starting at or after lo
    end = b if hi is None else (b + 1) // HOUR_NS * HOUR_NS  # the hours before it end by hi
    if a > b:
        whole, head, tail = _NONE, _NONE, _NONE
    elif first < end:
        whole = (first, end)
        head = _NONE if lo is None else (a, first - 1)
        tail = _NONE if hi is None else (end, b)
    else:  # inside one hour, or across one boundary with no whole hour between
        whole, head, tail = _NONE, (a, b), _NONE
    ints = [*whole, *head, *tail]
    return [*((v, "BIGINT") for v in ints), (lo is None and hi is None, "BOOLEAN")]


# --- the request's range ----------------------------------------------------------------------------


def day_of(value: str | None) -> str | None:
    """A from= instant's UTC day (YYYY-MM-DD), or None for none or one that doesn't parse: the day v5
    loaded partitions from (v1._day_of)."""
    from datetime import UTC, datetime

    if not value:
        return None
    try:
        t = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        return None
    if t.tzinfo is None:
        t = t.replace(tzinfo=UTC)
    return t.astimezone(UTC).strftime("%Y-%m-%d")


# --- loading --------------------------------------------------------------------------------------


def load(run: "_Run", *, overview: bool, params: dict[str, list[str]]) -> None:
    """The directory's tables into run's DuckDB; /overview's request count into run.values."""
    con = run.con
    day = [(day_of(params.get("from", [None])[0]) or "", "VARCHAR")]
    dims = run.fetch("dimensions", DIMENSIONS_SQL, day)
    sessions = run.fetch("session_rows", SESSION_ROWS_SQL, day)
    call_days = run.fetch("session_call_days", CALL_DAYS_SQL, day)
    missed = run.fetch("missed_marks", MISSED_SQL, day)
    if overview:
        lo, hi = ranges.of_query(params)  # the router's range, snapped
        t = run.fetch("agent_calls", AGENT_CALLS_SQL, agent_call_ranges(lo, hi))
        (n,) = con.execute(f"SELECT whole + head + tail + untimed FROM {t}").fetchall()[0]  # noqa: S608
        run.values["agent_calls"] = (lo, hi, int(n))
    for t in DIMENSIONS:
        cols = ", ".join(
            f"struct_pack(v := {c}_v, ts := {c}_ts, ok := {c}_ok) AS {_q(c)}" if ty.startswith("STRUCT") else _q(c)
            for c, ty in _SHAPES[t]
        )
        con.execute(f"CREATE TABLE {t} AS SELECT {cols} FROM {dims} WHERE src = '{t}' ORDER BY key, id")  # noqa: S608
    for sql in person_sql.PERSON_TABLES.values():
        con.execute(sql)
    con.execute(legacy._EMPTY["accounts"])
    con.execute("INSERT INTO accounts SELECT account_id, email, name, org, seen_ns FROM dim_account")
    con.execute(f"CREATE TABLE session_rows AS SELECT * FROM {sessions} ORDER BY session_id")  # noqa: S608
    con.execute(f"CREATE TABLE session_call_days AS SELECT * FROM {call_days} ORDER BY session_id, day")  # noqa: S608
    con.execute(f"CREATE TABLE session_marks AS SELECT * FROM {missed} ORDER BY session_id, kind, seq")  # noqa: S608


def agent_calls_in(values: dict[str, Any], lo: int | None, hi: int | None) -> int:
    """/overview's request count, as its load counted it for the same range."""
    got = values.get("agent_calls")
    if got is None:
        raise RuntimeError("this page's load didn't count the agent calls (only /overview's does)")
    if got[:2] != (lo, hi):
        raise RuntimeError(f"agent calls counted for {got[:2]}, asked for {(lo, hi)}")
    return int(got[2])
