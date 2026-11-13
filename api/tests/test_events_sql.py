"""The event rows' SQL (sql/events.sql) over the streaming processor's stored tables equals the
reference: the Go state tests write, per scenario, the stored event tables (event_rows, their
request links, prompt numbers and Codex turns, and call_tool_uses) and the single-pass
reference's rows; the SQL's event_rows and call_tool_uses must come out identical, row for row.
With AINTERCEPT_RECORDS set (a local records directory, never committed) the real capture is
a scenario too."""

import math
import os
import pathlib
import subprocess
from typing import Any

import duckdb
import pytest

from aintercept_api import state_sql

REPO = pathlib.Path(__file__).resolve().parents[2]
STORED = ("event_rows", "session_request_links", "prompt_index", "codex_turns", "call_tool_uses")


@pytest.fixture(scope="module")
def fixtures(tmp_path_factory: pytest.TempPathFactory) -> pathlib.Path:
    out = tmp_path_factory.mktemp("events-sql")
    env = dict(os.environ, EVENTS_SQL_OUT=str(out))
    subprocess.run(  # noqa: S603
        ["go", "test", "./internal/stream/state", "-run", "^TestWriteEventsSQLFixtures$", "-count=1"],  # noqa: S607
        cwd=REPO / "aintercept",
        env=env,
        check=True,
    )
    return out


def _rows(con: duckdb.DuckDBPyConnection, sql: str) -> list[dict[str, Any]]:
    cur = con.execute(sql)
    cols = [d[0] for d in cur.description or []]
    return [dict(zip(cols, r, strict=True)) for r in cur.fetchall()]


def _same(a: Any, b: Any) -> bool:
    if isinstance(a, float) and isinstance(b, float):
        return a == b or (math.isnan(a) and math.isnan(b))
    return bool(a == b)


def _check(got: list[dict[str, Any]], want: list[dict[str, Any]], what: str) -> None:
    assert len(got) == len(want), f"{what}: {len(got)} rows, want {len(want)}"
    for i, (g, w) in enumerate(zip(got, want, strict=True)):
        assert list(g) == list(w), f"{what}: columns {list(g)}, want {list(w)}"
        diff = {k: (g[k], w[k]) for k in w if not _same(g[k], w[k])}
        assert not diff, f"{what} row {i}: {diff}"


def test_event_rows_and_call_tool_uses_equal_the_reference(fixtures: pathlib.Path) -> None:
    scenarios = sorted(p for p in fixtures.iterdir() if p.is_dir())
    assert scenarios
    for scenario in scenarios:
        con = duckdb.connect()
        for t in STORED:
            con.read_parquet(str(scenario / f"{t}.parquet")).create("st_" + t)
        state_sql.event_tables(con)
        _check(
            _rows(con, "SELECT * FROM event_rows ORDER BY session_id, seq"),
            _rows(con, f"SELECT * FROM read_parquet('{scenario / 'ref_event_rows.parquet'}') ORDER BY session_id, seq"),  # noqa: S608
            f"{scenario.name} event_rows",
        )
        _check(
            _rows(con, "SELECT * FROM call_tool_uses ORDER BY session_id, ts_ns, kind, server, tool"),
            _rows(
                con,
                f"SELECT * FROM read_parquet('{scenario / 'ref_call_tool_uses.parquet'}') "  # noqa: S608
                "ORDER BY session_id, ts_ns, kind, server, tool",
            ),
            f"{scenario.name} call_tool_uses",
        )
