"""The session render side by side with the oracle (docs/PIPELINE.md build step 3, its
format-agnostic part).

`aintercept v1layout` writes each session key's finished tables (state.RenderSession, from the
key's own rows) under v2/test-sessions/<sid>/<table>.parquet: TEST-ONLY, one Parquet file per
session per table, not the v6 storage format (under review). For every session of the API test
datasets, those tables equal the oracle's for that session, row for row, seq included.

What the session's own key can't know isn't rendered, so it isn't compared:
- children_json (the children's keys);
- a Codex thread's label from a title thread, another key. The datasets' Codex threads have none,
  so every label is compared;
- a Codex thread's Requests tab rows that no thread names (the credential's c: key);
- an analytics batch's call still in flight: its start names no session, so it sits in its
  credential's c: key until its ending brings the batch's session (records.Assembler.release).
  The builders' batches never end, so their rows are left out here; an ended batch's are compared.
"""

import pathlib
import urllib.parse
from collections.abc import Iterator
from typing import Any

import duckdb
import pytest

from tests.builders import ClaudeCode, Dataset, load_fixture
from tests.conftest import lay_out
from tests.oracle.harness import SESSION_TABLES, Layout, Oracle, Table, Tables, assert_same
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


def rendered_tables(layout: Layout, sid: str) -> Tables:
    """A session's rendered tables, as v1layout wrote them (test-only files)."""
    d = layout.v2 / "test-sessions" / urllib.parse.quote(sid, safe="$&+:=@")
    out: Tables = {}
    con = duckdb.connect()
    try:
        con.execute("SET threads = 1")
        for name in SESSION_TABLES:
            path = d / f"{name}.parquet"
            if path.exists():
                cur = con.execute("SELECT * FROM read_parquet(?)", [str(path)])
                out[name] = _flatten(Table(tuple(c[0] for c in cur.description or []), cur.fetchall()))
    finally:
        con.close()
    return out


def _flatten(t: Table) -> Table:
    """session_request_rows' Go row nests the request row under `row`: as the API's table, flat."""
    if "row" not in t.columns or not t.rows:
        return t
    i = t.columns.index("row")
    inner = tuple(t.rows[0][i].keys())
    rows = [r[:i] + tuple(r[i][k] for k in inner) + r[i + 1 :] for r in t.rows]
    return Table(t.columns[:i] + inner + t.columns[i + 1 :], rows)


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
def layouts(tmp_path_factory) -> list[tuple[str, pathlib.Path]]:
    tmp = tmp_path_factory.mktemp("v6s")
    return [(name, lay_out(raw)) for name, raw in _datasets(tmp)]


def test_every_sessions_render_equals_the_oracles_tables(layouts):
    compared: dict[str, int] = {}
    rows: dict[str, int] = {}
    for name, root in layouts:
        lay = Layout(root)
        with Oracle(lay) as oracle:
            sids = oracle.session_ids()
            assert sids, name
            for sid in sids:
                w, g = comparable(oracle.session(sid), rendered_tables(lay, sid), sid)
                assert_same(w, g, entity=f"{name}: session {sid}", unordered=_UNORDERED)
                compared[name] = compared.get(name, 0) + 1
                for t, v in w.items():
                    rows[t] = rows.get(t, 0) + len(v.rows)
    print("sessions compared:", compared, "rows:", rows)  # noqa: T201 (the gate's coverage, in -s output)
    assert compared["demo and builders"] >= 5 and compared["spanning batch"] == 2, compared
    # Not vacuous: every session table holds rows somewhere.
    assert all(rows[t] for t in SESSION_TABLES), rows
