"""The worker's Postgres rows and every session's loaded tables side by side with the oracle
(docs/PIPELINE.md v7 build steps 2, 3 and 5).

Stored inputs (step 2): each §3.4 table equals the oracle's st_<state table> over every key, row for
row, keyed by (key, the state's row id): the oracle's nested structs flattened as the schema flattens
them (row_<column>, plan_v), its id as bytes. (The analytics events aren't stored: v8.)

Sessions (steps 3 and 5):

`aintercept v1layout -pg <dsn>` writes every key's rows into a fresh Postgres (internal/render:
state.RenderSession re-encoded, the folds); the API's loader (pgstore.PgStore) reads each session
back as its pages do, and its 9 tables must equal the oracle's for that session, row for row, seq by
order. The load fills back the two re-encodings (prompt_rows.session_last_ns from
session_rows.prompt_last_ns; session_marks.seq keyed per block, compared by order), drops the
columns the API doesn't load (own_title, prompt_last_ns, key_day, event_rows.batch), and joins what
other keys hold (§4): children_json (the children's edges), a Codex thread's alone label (a title
thread's calls) and the Requests tab's rows other keys hold (a Codex thread's unthreaded client
calls, an analytics batch's call still in flight). The folds are checked against the oracle's
tables too: session_call_days, agent_calls (and their hours) and batch_parts.

Not compared here: label_text and label_source, the Codex title over the account-connected
component, which only the person pages read (build step 6); every session page reads the alone label.
"""

import contextlib
import pathlib
import re
from collections.abc import Iterator
from typing import Any

import duckdb
import pytest

from aintercept_api.pgstore import PgStore
from tests import pgdb
from tests.builders import ClaudeCode, Codex, Dataset, load_fixture
from tests.conftest import lay_out
from tests.oracle.harness import SESSION_TABLES, Layout, Oracle, Table, Tables, assert_same, extract
from tests.oracle.store import LocalSource, OracleStore, Scope, read_newest
from tests.test_codex import CHILD, claude_code_account, codex_work
from tests.test_directory import OTHER, S1, person, unattributed
from tests.test_session import H, prompt
from tests.test_stream_pipeline import one_stream_one_id

SPAN_A, SPAN_B = "aaaaaaaa-0000-4000-8000-0000000000a1", "bbbbbbbb-0000-4000-8000-0000000000b2"
TITLE_THREAD = "01a1c1ec-3e49-71b3-b32b-55a748502bab"
# Kept in no particular order by today's store (sorted by session only, or with ties): multisets.
_UNORDERED = ("session_tools", "call_tool_uses")
# The component label (build step 6): no session page reads it.
_COMPONENT = ("label_text", "label_source")
# What a session's own key can't know, joined from other keys' rows at load (§4).
_CROSS_KEY = ("children_json", "label_text_alone", "label_source_alone")


def loaded_tables(store: PgStore, sid: str) -> Tables:
    """A session's 9 tables as the API loads them (everything any of its pages reads)."""
    loaded = store.session_entity(sid)
    try:
        return extract(loaded.con, SESSION_TABLES, sid)
    finally:
        loaded.con.close()


def _without(t: Table, cols: tuple[str, ...]) -> Table:
    keep = [i for i, c in enumerate(t.columns) if c not in cols]
    return Table(tuple(t.columns[i] for i in keep), [tuple(r[i] for i in keep) for r in t.rows])


def own_rows(want: Tables, sid: str) -> Tables:
    """The oracle's tables with the cross-key values left out: the session row's children and alone
    label, and the Requests tab's rows other keys hold (renumbered over what's kept, as the render
    numbers its own)."""
    w = dict(want)
    w["session_rows"] = _without(w["session_rows"], _CROSS_KEY)
    sr = want["session_rows"]
    codex = any(r[sr.columns.index("product")] == "codex" for r in sr.rows)
    t = w["session_request_rows"]
    col = {c: t.columns.index(c) for c in ("seq", "session_id", "kind", "status", "status_err", "duration_ms")}

    def own(r: tuple[Any, ...]) -> bool:
        if codex and r[col["session_id"]] != sid:  # a thread's unthreaded client calls: the c: key's
            return False
        in_flight = r[col["status"]] is None and not r[col["status_err"]] and r[col["duration_ms"]] is None
        return not (r[col["kind"]] == "analytics" and in_flight)

    kept = [r for r in t.rows if own(r)]
    if len(kept) != len(t.rows):
        s = col["seq"]
        kept = [(*r[:s], n, *r[s + 1 :]) for n, r in enumerate(kept)]
    w["session_request_rows"] = Table(t.columns, kept)
    return w


def comparable(want: Tables, got: Tables, sid: str, *, cross_key: bool) -> tuple[Tables, Tables]:
    """The oracle's session tables and the loader's, without the component label; without the
    cross-key values too unless cross_key."""
    w = {name: want[name] for name in SESSION_TABLES}
    g = {name: got[name] for name in SESSION_TABLES}
    w["session_rows"], g["session_rows"] = (
        _without(w["session_rows"], _COMPONENT),
        _without(g["session_rows"], _COMPONENT),
    )
    if not cross_key:
        w = own_rows(w, sid)
        g["session_rows"] = _without(g["session_rows"], _CROSS_KEY)
    return w, g


def differs_across_keys(want: Tables, sid: str) -> bool:
    """Whether a session has cross-key values the oracle shows (children, a title thread's label,
    other keys' request rows)."""
    sr = want["session_rows"]
    children, source, product = (sr.columns.index(c) for c in ("children_json", "label_source_alone", "product"))
    titled = any(r[source] == "title" and r[product] == "codex" for r in sr.rows)
    others = len(own_rows(want, sid)["session_request_rows"].rows) != len(want["session_request_rows"].rows)
    return titled or others or any(r[children] is not None for r in sr.rows)


def _datasets(tmp: pathlib.Path) -> Iterator[tuple[str, pathlib.Path]]:
    ds = Dataset(tmp / "mixed" / "data")
    load_fixture(ds, "demo")
    claude_code_account(ds)
    codex_work(ds)
    person(ds, S1, account=OTHER, hour=11, calls=3)
    unattributed(ds)
    one_stream_one_id(ds)
    # A Codex thread titled by a title thread of its own (another key), bodies recorded.
    Codex(ds, CHILD).post("2026-11-04T16:00:00", "turn-x", {"input": []}, [])
    Codex(ds, TITLE_THREAD).post(
        "2026-11-04T16:00:01",
        "",
        {"input": []},
        [
            {
                "type": "response.output_item.done",
                "item": {"type": "message", "content": [{"type": "output_text", "text": "Fix flaky timer test"}]},
            }
        ],
        source="thread_title",
    )
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
    # An analytics batch spanning two sessions that ends: each session's Requests tab counts the
    # whole batch (every key's batch_parts, read at render time: test_v7_drawer.py).
    from tests.test_v7_drawer import ended_spanning_batch

    ds = Dataset(tmp / "spanning-ended" / "data")
    ended_spanning_batch(ds)
    yield "ended spanning batch", pathlib.Path(ds.write())


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


def _compare(layouts, *, cross_key: bool) -> dict[str, int]:
    compared: dict[str, int] = {}
    rows: dict[str, int] = {}
    crossing = 0
    for name, root, dsn in layouts:
        with Oracle(Layout(root)) as oracle, contextlib.closing(PgStore(dsn, threads=1)) as store:
            sids = oracle.session_ids()
            assert sids, name
            for sid in sids:
                want = oracle.session(sid)
                crossing += differs_across_keys(want, sid)
                w, g = comparable(want, loaded_tables(store, sid), sid, cross_key=cross_key)
                assert_same(w, g, entity=f"{name}: session {sid}", unordered=_UNORDERED)
                compared[name] = compared.get(name, 0) + 1
                for t, v in w.items():
                    rows[t] = rows.get(t, 0) + len(v.rows)
    print("sessions compared:", compared, "rows:", rows, "with cross-key values:", crossing)  # noqa: T201
    assert compared["demo and builders"] >= 5 and compared["spanning batch"] == 2, compared
    assert compared["ended spanning batch"] == 2, compared
    # Not vacuous: every session table holds rows somewhere, and the datasets hold cross-key values.
    assert all(rows[t] for t in SESSION_TABLES), rows
    assert crossing >= 3, crossing
    return compared


def test_every_sessions_tables_equal_the_oracles(layouts):
    """Every session's tables as the API loads them, the cross-key values joined (§4): children_json,
    the alone label from a title thread, and the Requests tab's rows other keys hold."""
    _compare(layouts, cross_key=True)


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


# --- stored inputs (step 2) -------------------------------------------------------------------

# The stored tables whose state table is named otherwise (§3.4); the rest share their names.
_STATE_OF = {"requests": "fact_requests"}
_NOT_STORED: dict[str, tuple[str, ...]] = {}


def stored_tables() -> list[str]:
    """The §3.4 tables: schema.sql's tables keyed by (key, id bytea)."""
    return re.findall(r"CREATE TABLE (\w+) \(\n    key text NOT NULL,\n    id bytea NOT NULL,", pgdb.SCHEMA.read_text())


def _flattened(rel: duckdb.DuckDBPyRelation, drop: tuple[str, ...]) -> str:
    """A select list flattening rel's structs as the schema does (c_f), with its id as bytes."""
    out = []
    for name, typ in zip(rel.columns, rel.types, strict=True):
        if name in drop:
            continue
        if name == "id":
            out.append("encode(id) AS id")
        elif typ.id == "struct":
            out += [f'"{name}"."{f}" AS "{name}_{f}"' for f, _ in typ.children]
        else:
            out.append(f'"{name}"')
    return ", ".join(out)


def test_every_datasets_stored_inputs_equal_the_oracles(layouts):
    tables = stored_tables()
    assert len(tables) == 19, tables  # nine dimensions, six request-log inputs, three labels and edges, one fact
    rows: dict[str, int] = {}
    for name, root, dsn in layouts:
        oracle = OracleStore(LocalSource(root), prefix="sim/")
        merge = oracle.merge()
        assert merge is not None, name
        con, pg = duckdb.connect(), pgdb.connect(dsn)
        try:
            for table in tables:
                state = _STATE_OF.get(table, table)
                got = pgdb.load(con, pg, "pg_" + table, f"SELECT * FROM {table}")  # noqa: S608 (schema names)
                urls = [oracle.source.url(oracle._merge_file(o, state)) for _, o in merge.parts() if state in o.tables]
                if urls:
                    st = read_newest(con, urls, keep_keys=True)
                    want = con.sql(f"SELECT {_flattened(st, ('tomb', *_NOT_STORED.get(table, ())))} FROM st")  # noqa: S608
                else:  # the oracle has none
                    want = con.sql(f"SELECT * FROM pg_{table} WHERE false")  # noqa: S608
                assert sorted(want.columns) == sorted(got.columns), (name, table, set(want.columns) ^ set(got.columns))
                want.create("want_" + table)
                cols = ", ".join(f'"{c}"' for c in got.columns)
                diff = con.sql(
                    f"(SELECT 'oracle only', {cols} FROM want_{table}"  # noqa: S608 (schema names)
                    f" EXCEPT ALL SELECT 'oracle only', {cols} FROM pg_{table})"
                    f" UNION ALL (SELECT 'stored only', {cols} FROM pg_{table}"
                    f" EXCEPT ALL SELECT 'stored only', {cols} FROM want_{table})"
                ).fetchall()
                assert not diff, f"{name}: {table}: {len(diff)} rows differ, e.g. {diff[:4]}"
                rows[table] = rows.get(table, 0) + con.sql(f"SELECT count(*) FROM pg_{table}").fetchall()[0][0]  # noqa: S608
        finally:
            pg.close()
            con.close()
    print("stored rows compared:", rows)  # noqa: T201 (the gate's coverage, in -s output)
    # Not vacuous: the tables the datasets feed hold rows.
    empty = sorted(t for t in tables if not rows[t])
    assert len(empty) <= 4, empty
