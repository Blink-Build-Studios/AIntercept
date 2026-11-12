"""The request log's SQL over the streaming processor's stored tables (stream_sql), held to
the reference: the Go state tests write, per scenario, the tables the stream stores and the
single-pass reference's request_rows, person_last and session_request_rows; every person's
rows and every session's Requests tab must come out identical. With AINTERCEPT_RECORDS set
(a local records directory, never committed) the real capture is a scenario too."""

import json
import os
import pathlib
import subprocess
from typing import Any

import duckdb
import pytest

from aintercept_api import stream_sql

REPO = pathlib.Path(__file__).resolve().parents[2]


@pytest.fixture(scope="module")
def fixtures(tmp_path_factory: pytest.TempPathFactory) -> pathlib.Path:
    out = tmp_path_factory.mktemp("stream-sql")
    env = dict(os.environ, AINTERCEPT_SQL_FIXTURES=str(out))
    subprocess.run(  # noqa: S603
        ["go", "test", "./internal/stream/state", "-run", "^TestWriteRequestSQLFixtures$", "-count=1"],  # noqa: S607
        cwd=REPO / "aintercept",
        env=env,
        check=True,
    )
    return out


def _connect(scenario: pathlib.Path) -> duckdb.DuckDBPyConnection:
    con = duckdb.connect()
    for f in scenario.glob("*.parquet"):
        con.execute(f"CREATE VIEW {f.stem} AS SELECT * FROM read_parquet('{f}', file_row_number = true)")
    return con


def _want(con: duckdb.DuckDBPyConnection, sql: str, *params: object) -> list[dict[str, Any]]:
    cur = con.execute(sql, list(params))
    cols = [d[0] for d in cur.description]
    rows = [dict(zip(cols, r, strict=True)) for r in cur.fetchall()]
    for r in rows:
        r.pop("file_row_number", None)
    return rows


def _scenarios(root: pathlib.Path) -> list[pathlib.Path]:
    return sorted(p for p in root.iterdir() if p.is_dir())


def test_person_request_rows_equal_the_reference(fixtures: pathlib.Path) -> None:
    for scenario in _scenarios(fixtures):
        con = _connect(scenario)
        people = json.loads((scenario / "people.json").read_text())
        assert people
        for person in people:
            want = _want(con, "SELECT * FROM want_request_rows WHERE person_id = ? ORDER BY file_row_number", person)
            got = stream_sql.person_request_rows(con, person)
            assert got == want, f"{scenario.name}: {person}"
            last = _want(con, "SELECT last_ns FROM want_person_last WHERE person_id = ?", person)
            assert stream_sql.person_last(con, person) == last[0]["last_ns"], f"{scenario.name}: {person}"


def test_session_request_rows_equal_the_reference(fixtures: pathlib.Path) -> None:
    for scenario in _scenarios(fixtures):
        con = _connect(scenario)
        sessions = [r[0] for r in con.execute("SELECT session_id FROM session_rows ORDER BY session_id").fetchall()]
        assert sessions
        for s in sessions:
            want = _want(
                con,
                "SELECT seq, row.* FROM want_session_request_rows WHERE scope_session_id = ? ORDER BY file_row_number",
                s,
            )
            got = stream_sql.session_request_rows(con, s)
            assert got == want, f"{scenario.name}: {s}"
