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


def test_the_store_builds_the_person_tables_from_bucketed_dimensions(tmp_path):
    """A layout holding the dimension tables (as the processor writes them, each row under its
    key): the store derives the person tables from them, and resolves a person through them."""
    from aintercept_api.buckets import ROUTING, BucketStore, LocalSource, Scope, bucket_of
    from tests.bucket_fixture import table_key, write_commit, write_rows
    from tests.test_buckets import Clock

    seed = SEEDS[0]
    con, want = _load(seed)
    data = json.loads(seed.read_text())
    commits: dict[int, dict[str, list[str]]] = {}
    for table in DIMENSIONS:
        rows = data.get(table) or []
        for b in sorted({bucket_of(r["key"]) for r in rows}):
            k = table_key("", table, b, "base-1")
            # one row per (key, id), in the fixture's order
            con.execute("DROP TABLE IF EXISTS one")
            con.execute(f"CREATE TABLE one AS SELECT *, ''::VARCHAR AS key, ''::VARCHAR AS id, false AS tomb FROM {table} LIMIT 0")  # noqa: S608,E501
            for r in rows:
                if bucket_of(r["key"]) != b:
                    continue
                cols = [c for c in r if c not in ("key", "id")]
                con.execute(
                    f"INSERT INTO one BY NAME SELECT ? AS key, ? AS id, {', '.join('?::' + _type(_SHAPES[table], c) + ' AS ' + c for c in cols)}",  # noqa: S608,E501
                    [r["key"], r["id"], *[r[c] for c in cols]],
                )
            write_rows(tmp_path, k, con, "SELECT * FROM one", [])
            commits.setdefault(b, {})[table] = [k]
    for b, base in commits.items():
        write_commit(tmp_path, "", b, 1, base, {})
    tables = {t: ROUTING[t] for t in (*DIMENSIONS, "person_map", "person_sessions", "account_facts", "account_devices")}
    store = BucketStore(LocalSource(tmp_path), tmp_path / ".cache", tables=tables, clock=Clock())
    with store.scope(Scope.everything()):
        for table in PERSON_TABLES:
            cur_rows = store.query_dicts(f"SELECT * FROM {table}")  # noqa: S608
            assert cur_rows == (want.get(table) or []), table
    pid = want["person_sessions"][0]["person_id"]
    sessions = {r["session_id"] for r in want["person_sessions"] if r["person_id"] == pid}
    assert store.resolve_person(pid).sessions == sessions
