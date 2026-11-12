"""The person tables from the dimension tables (docs/STREAMING_PIPELINE.md §4.6): the API's SQL
gives exactly what the processor's reference rebuild (personTables, dims_test.go) does, on every
generated seed in fixtures/person_dims."""

import json
import pathlib

import duckdb
import pytest

from aintercept_api.person_sql import DIMENSIONS, PERSON_TABLES

SEEDS = sorted((pathlib.Path(__file__).parent / "fixtures" / "person_dims").glob("seed-*.json"))
_PICK = "STRUCT(v VARCHAR, ts BIGINT, ok BOOLEAN)"
_SHAPES = {
    "dim_account": "account_id VARCHAR, email VARCHAR, name VARCHAR, org VARCHAR, seen_ns BIGINT, "
    "email_ns BIGINT, name_ns BIGINT, org_ns BIGINT",
    "dim_session_account": "session_id VARCHAR, account_id VARCHAR",
    "dim_session_visible": "session_id VARCHAR",
    "dim_session_person": "session_id VARCHAR, person_id VARCHAR",
    "dim_person_device": "person_id VARCHAR, device_id VARCHAR",
    "dim_session_credential": "session_id VARCHAR, key_hash VARCHAR",
    "dim_account_events": f"account_id VARCHAR, org VARCHAR, org_ns BIGINT, plan {_PICK}",
    "dim_account_device": f"account_id VARCHAR, device_id VARCHAR, on_events BOOLEAN, platform {_PICK}, "
    f"terminal {_PICK}, shell {_PICK}",
    "dim_codex_plan": f"account_id VARCHAR, plan {_PICK}",
}


def _load(seed: pathlib.Path) -> tuple[duckdb.DuckDBPyConnection, dict]:
    data = json.loads(seed.read_text())
    con = duckdb.connect()
    assert set(DIMENSIONS) == set(_SHAPES)
    for table, shape in _SHAPES.items():
        con.execute(f"CREATE TABLE {table} ({shape})")  # noqa: S608 (fixed test schema)
        cols = [c.split()[0] for c in shape.split(", ") if not c.startswith(("v ", "ts ", "ok "))]
        cols = [c for c in cols if c not in ("ts", "ok", "v")]
        for r in data.get(table) or []:
            con.execute(
                f"INSERT INTO {table} BY NAME SELECT * FROM (SELECT {', '.join('?::' + _type(shape, c) + ' AS ' + c for c in cols)})",  # noqa: S608,E501
                [r.get(c) for c in cols],
            )
    return con, data["want"]


def _type(shape: str, col: str) -> str:
    for part in shape.replace(f"{_PICK}", "PICK").split(", "):
        name, typ = part.split(" ", 1)
        if name == col:
            return _PICK if typ == "PICK" else typ
    raise KeyError(col)


@pytest.mark.parametrize("seed", SEEDS, ids=lambda p: p.stem)
def test_person_tables_equal_the_reference(seed):
    con, want = _load(seed)
    for table, sql in PERSON_TABLES.items():
        con.execute(sql)
        cur = con.execute(f"SELECT * FROM {table}")  # noqa: S608
        names = [d[0] for d in cur.description]
        got = [dict(zip(names, r, strict=True)) for r in cur.fetchall()]
        assert got == (want.get(table) or []), table


def test_there_are_seeds():
    assert len(SEEDS) >= 10
