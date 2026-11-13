"""The rendered session tables side by side with the oracle (docs/PIPELINE.md v7 build step 3).

`aintercept v1layout -pg <dsn>` writes every key's rendered rows into a fresh Postgres
(internal/render: state.RenderSession re-encoded, the folds); this reads each session's 9 tables
back as the API will load them, and compares them with the oracle's for that session, row for row,
seq by order. The load fills back the two re-encodings (prompt_rows.session_last_ns from
session_rows.prompt_last_ns; session_marks.seq keyed per block, compared by order) and drops the
columns the API doesn't load (own_title, prompt_last_ns, key_day, event_rows.batch). The folds are
checked against the oracle's tables too: session_call_days, agent_calls (and their hours) and
batch_parts.

What the session's own key can't know isn't rendered, so it isn't compared:
- children_json (the children's keys): NULL in Postgres, joined at read time;
- a Codex thread's label from a title thread, another key. The datasets' Codex threads have none,
  so every label is compared;
- a Codex thread's Requests tab rows that no thread names (the credential's c: key);
- an analytics batch's call still in flight: its start names no session, so it sits in its
  credential's c: key until its ending brings the batch's session (records.Assembler.release).
  The builders' batches never end, so their rows are left out here; an ended batch's are compared.
"""

import contextlib
import pathlib
from collections.abc import Iterator
from typing import Any

import duckdb
import pytest

from tests import pgdb
from tests.builders import ClaudeCode, Dataset, load_fixture
from tests.conftest import lay_out
from tests.oracle.harness import SESSION_TABLES, Layout, Oracle, Table, Tables, assert_same
from tests.oracle.store import Scope
from tests.test_codex import claude_code_account, codex_work
from tests.test_directory import OTHER, S1, person, unattributed
from tests.test_session import H, prompt
from tests.test_stream_pipeline import one_stream_one_id

SPAN_A, SPAN_B = "aaaaaaaa-0000-4000-8000-0000000000a1", "bbbbbbbb-0000-4000-8000-0000000000b2"
# Today's table order (the oracle's store keeps each table sorted so): the render's rows are loaded
# the same way, sorted by these columns. Ties keep the render's order.
_ORDER = {
    "prompt_rows": ("session_id", "seq"),
    "session_marks": ("session_id", "kind", "seq"),
    "presence_segments": ("session_id", "seq"),
    "prompt_steps": ("session_id", "seq", "step"),
    "event_rows": ("session_id", "seq"),
    "session_request_rows": ("scope_session_id", "seq"),
    "call_tool_uses": ("session_id", "ts_ns"),
}
# Kept in no particular order by today's store (sorted by session only, or with ties): multisets.
_UNORDERED = ("session_tools", "call_tool_uses")
_CROSS_SESSION = ("children_json",)


# The columns a table holds that the API doesn't load (docs/PIPELINE.md §3.3).
_EXTRAS = {"session_rows": ("own_title", "prompt_last_ns", "key_day"), "event_rows": ("batch",)}


def _quoted(s: str) -> str:
    return "'" + s.replace("'", "''") + "'"


def rendered_tables(dsn: str, sid: str) -> Tables:
    """A session's rendered tables read back from Postgres as the API loads them: the extras dropped,
    prompt_rows.session_last_ns from its session row (a session with no row keeps its own)."""
    out: Tables = {}
    con, pg = duckdb.connect(), pgdb.connect(dsn)
    try:
        con.execute("SET threads = 1")
        for name in SESSION_TABLES:
            owner = "scope_session_id" if name == "session_request_rows" else "session_id"
            pgdb.load(con, pg, name, f"SELECT * FROM {name} WHERE {owner} = {_quoted(sid)}")  # noqa: S608
        con.execute(
            """UPDATE prompt_rows p SET session_last_ns = s.prompt_last_ns FROM session_rows s
               WHERE s.session_id = p.session_id AND s.prompt_last_ns IS NOT NULL"""
        )
        for name in SESSION_TABLES:
            cols = f"* EXCLUDE ({', '.join(_EXTRAS[name])})" if name in _EXTRAS else "*"
            cur = con.execute(f"SELECT {cols} FROM {name}")  # noqa: S608 (our table and column names)
            t = Table(tuple(c[0] for c in cur.description or []), cur.fetchall())
            if t.rows:
                out[name] = t
    finally:
        con.close()
        pg.close()
    return out


def _shaped(want: Table, got: Table | None, name: str) -> Table:
    """got with want's columns (loaded BY NAME), in today's table order."""
    if got is None:  # an empty table has no file
        return Table(want.columns, [])
    assert set(got.columns) == set(want.columns), set(got.columns) ^ set(want.columns)
    idx = [got.columns.index(c) for c in want.columns]
    rows = [tuple(r[i] for i in idx) for r in got.rows]
    if name in _ORDER:
        keys = [want.columns.index(c) for c in _ORDER[name]]
        rows.sort(key=lambda r: tuple(r[k] for k in keys))
    return Table(want.columns, rows)


def _without(t: Table, cols: tuple[str, ...]) -> Table:
    keep = [i for i, c in enumerate(t.columns) if c not in cols]
    return Table(tuple(t.columns[i] for i in keep), [tuple(r[i] for i in keep) for r in t.rows])


def comparable(want: Tables, got: Tables, sid: str) -> tuple[Tables, Tables]:
    """The oracle's session tables and the render's, in today's columns, with what the session's
    own key can't know left out (module docstring)."""
    w = {name: want[name] for name in SESSION_TABLES}
    g = {name: _shaped(want[name], got.get(name), name) for name in SESSION_TABLES}
    sr = w["session_rows"]
    codex = any(r[sr.columns.index("product")] == "codex" for r in sr.rows)
    w["session_rows"], g["session_rows"] = _without(sr, _CROSS_SESSION), _without(g["session_rows"], _CROSS_SESSION)
    t = w["session_request_rows"]
    col = {c: t.columns.index(c) for c in ("seq", "session_id", "kind", "status", "status_err", "duration_ms")}

    def own(r: tuple[Any, ...]) -> bool:
        if codex and r[col["session_id"]] != sid:  # a thread's unthreaded client calls: the c: key's
            return False
        in_flight = r[col["status"]] is None and not r[col["status_err"]] and r[col["duration_ms"]] is None
        return not (r[col["kind"]] == "analytics" and in_flight)

    kept = [r for r in t.rows if own(r)]
    if len(kept) != len(t.rows):  # numbered over what's kept, as the render numbers its own
        s = col["seq"]
        kept = [(*r[:s], n, *r[s + 1 :]) for n, r in enumerate(kept)]
    w["session_request_rows"] = Table(t.columns, kept)
    return w, g


def _datasets(tmp: pathlib.Path) -> Iterator[tuple[str, pathlib.Path]]:
    ds = Dataset(tmp / "mixed" / "data")
    load_fixture(ds, "demo")
    claude_code_account(ds)
    codex_work(ds)
    person(ds, S1, account=OTHER, hour=11, calls=3)
    unattributed(ds)
    one_stream_one_id(ds)
    yield "demo and builders", pathlib.Path(ds.write())
    # One analytics batch carrying two sessions' events (test_v1_pipeline's spanning batch).
    ds = Dataset(tmp / "spanning" / "data")
    a, b = ClaudeCode(ds, SPAN_A), ClaudeCode(ds, SPAN_B)
    prompt(a, f"{H}:00:01", "pa", 1)
    prompt(b, f"{H}:00:02", "pb", 1)
    rid = b.call(f"{H}:00:09", prompt="pb")
    for i in range(4):
        b.event(f"{H}:00:0{i + 3}", "tengu_feature_ok", n=i)
    b.event(f"{H}:00:10", "tengu_api_success", batch="batch-span", cc_prompt_id="pb", requestId=rid)
    for i in range(3):
        a.event(f"{H}:00:1{i}", "tengu_feature_ok", batch="batch-span", n=i)
    yield "spanning batch", pathlib.Path(ds.write())


@pytest.fixture(scope="module")
def layouts(tmp_path_factory) -> Iterator[list[tuple[str, pathlib.Path, str]]]:
    """Each dataset's v1 layout (the oracle's) and a fresh Postgres holding its rendered rows."""
    admin = pgdb.admin_dsn()
    if admin is None:
        if pgdb.required():
            pytest.fail("AINTERCEPT_TEST_PG isn't set (CI's service container should set it)")
        pytest.skip("no test Postgres: set AINTERCEPT_TEST_PG (make check starts one: scripts/test-pg.sh)")
    tmp = tmp_path_factory.mktemp("v7s")
    with contextlib.ExitStack() as stack:
        out = []
        for name, raw in _datasets(tmp):
            dsn = stack.enter_context(pgdb.fresh_database(admin))
            out.append((name, lay_out(raw, pg=dsn), dsn))
        yield out


def test_every_sessions_rendered_rows_equal_the_oracles_tables(layouts):
    compared: dict[str, int] = {}
    rows: dict[str, int] = {}
    for name, root, dsn in layouts:
        with Oracle(Layout(root)) as oracle:
            sids = oracle.session_ids()
            assert sids, name
            for sid in sids:
                w, g = comparable(oracle.session(sid), rendered_tables(dsn, sid), sid)
                assert_same(w, g, entity=f"{name}: session {sid}", unordered=_UNORDERED)
                compared[name] = compared.get(name, 0) + 1
                for t, v in w.items():
                    rows[t] = rows.get(t, 0) + len(v.rows)
    print("sessions compared:", compared, "rows:", rows)  # noqa: T201 (the gate's coverage, in -s output)
    assert compared["demo and builders"] >= 5 and compared["spanning batch"] == 2, compared
    # Not vacuous: every session table holds rows somewhere.
    assert all(rows[t] for t in SESSION_TABLES), rows


def _pg_rows(dsn: str, sql: str) -> list[tuple[Any, ...]]:
    con, pg = duckdb.connect(), pgdb.connect(dsn)
    try:
        return pgdb.load(con, pg, "t", sql).fetchall()
    finally:
        con.close()
        pg.close()


def test_the_folds_equal_the_oracles(layouts):
    """session_call_days are the oracle's call marks per session and day (_ACTIVITY's); agent_calls
    are the merge's (/overview's count) and agent_call_hours their hours; batch_parts folded per
    batch are the request log's batch CTEs over batch_members; event_rows.batch is the summary's
    regexp over event_id."""
    for name, root, dsn in layouts:
        with Oracle(Layout(root)) as oracle:
            con = oracle.store._load(Scope.everything()).con
            want = sorted(
                con.execute(
                    """SELECT session_id, CAST(floor(t_ns / 1e9 / 86400) AS BIGINT), count(*), min(t_ns), max(t_ns)
                       FROM session_marks WHERE kind = 'call' GROUP BY 1, 2"""
                ).fetchall()
            )
            assert want, name
            assert sorted(_pg_rows(dsn, "SELECT session_id, day, n, first_ns, last_ns FROM session_call_days")) == want
            (n,) = _pg_rows(dsn, "SELECT count(*) FROM agent_calls")[0]
            assert n and n == oracle.store.agent_calls_in(None, None), name
            hours = _pg_rows(
                dsn,
                """SELECT count(*) FROM (SELECT key, ts_ns / 3600000000000 * 3600000000000 AS hour, count(*) AS n
                   FROM agent_calls WHERE ts_ns IS NOT NULL GROUP BY 1, 2) a
                   FULL JOIN agent_call_hours h USING (key, hour) WHERE a.n IS DISTINCT FROM h.n""",
            )
            assert hours == [(0,)], name
            want_batches = sorted(
                con.execute(
                    """SELECT batch_id, count(*), min(name) FILTER (WHERE name <> ''),
                         arg_min(session_id, (ns, session_id)) FILTER (WHERE session_id <> '')
                       FROM batch_members GROUP BY 1"""
                ).fetchall()
            )
            got_batches = sorted(
                _pg_rows(
                    dsn,
                    """SELECT batch_id, sum(n)::bigint, min(min_name),
                         (array_agg(first_session ORDER BY first_ns, first_session)
                           FILTER (WHERE first_session IS NOT NULL))[1]
                       FROM batch_parts GROUP BY 1""",
                )
            )
            assert want_batches and got_batches == want_batches, name
            bad = _pg_rows(
                dsn,
                """SELECT count(*) FROM event_rows
                   WHERE batch IS DISTINCT FROM coalesce(substring(event_id FROM '^(.*):[0-9]+$'), '')""",
            )
            assert bad == [(0,)], name
