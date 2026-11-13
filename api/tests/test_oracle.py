"""The v6 oracle (tests/oracle/, docs/PIPELINE.md Testing): today's derivation, frozen, and the
table-level harness later steps compare the Go views with.

The oracle equals today's code (V1Store) on every entity of the API test datasets: the builders'
and the demo fixture's (test_v1_pipeline.py, where the parity gate's "old" side is the oracle's
store), and here the SQL fixture seeds (session_rows' and the person tables' stored inputs). The
harness's comparison is held to its rules: `seq` by order, rows in order, every table."""

import pathlib

import duckdb
import pytest

from aintercept_api import buckets
from aintercept_api.v1 import V1Store
from tests.oracle import state_sql
from tests.oracle.harness import (
    DIRECTORY_TABLES,
    PERSON_TABLES,
    SESSION_TABLES,
    Table,
    assert_same,
    concat,
    differences,
    extract,
    normalize,
    read_view,
)
from tests.oracle.store import OracleStore, Scope
from tests.test_person_sql import SEEDS as PERSON_SEEDS
from tests.test_person_sql import _load as load_dims
from tests.test_session_sql import SEEDS as SESSION_SEEDS

# Every API-shaped table the seeds' stored inputs reach, whole.
_ALL = {t: "true" for t in {**SESSION_TABLES, **PERSON_TABLES, **DIRECTORY_TABLES}}


def _stored(root: pathlib.Path, con: duckdb.DuckDBPyConnection, tables: dict[str, str]) -> dict[str, list[str]]:
    """Each stored table (its SELECT over con) as one file in the processor's codec (key, id,
    tomb, the columns); the plan that reads them."""
    plan: dict[str, list[str]] = {}
    for table, select in tables.items():
        rel = f"st/{table}.parquet"
        (root / "st").mkdir(parents=True, exist_ok=True)
        sql = (
            "SELECT 'k' AS key, CAST(row_number() OVER () AS VARCHAR) AS id, false AS tomb, * "  # noqa: S608 (fixed)
            f"FROM ({select})"
        )
        con.sql(sql).write_parquet(str(root / rel))
        plan[table] = [rel]
    return plan


def _both(root: pathlib.Path, plan: dict[str, list[str]]):
    """The API-shaped tables both ways from the same stored files: the oracle's and today's."""
    oracle = OracleStore(root)._build_state(plan, Scope.everything(), None, partial=False, keys={})
    today = V1Store(buckets.LocalSource(root), root / ".cache", threads=1)
    return extract(oracle.con, _ALL, None), extract(today._build_state(plan, buckets.Scope.everything()), _ALL, None)


@pytest.mark.parametrize("seed", SESSION_SEEDS, ids=lambda p: p.name)
def test_the_oracle_derives_todays_session_rows_on_the_seeds(seed, tmp_path):
    con = duckdb.connect()
    inputs = {t: f"SELECT * FROM read_parquet('{seed / (t + '.parquet')}')" for t in state_sql.SESSION_INPUTS}  # noqa: S608
    want, got = _both(tmp_path, _stored(tmp_path, con, inputs))
    assert_same(want, got, entity=seed.name)
    assert want["session_rows"].rows


@pytest.mark.parametrize("seed", PERSON_SEEDS, ids=lambda p: p.stem)
def test_the_oracle_derives_todays_person_tables_on_the_seeds(seed, tmp_path):
    con, _ = load_dims(seed)
    from tests.oracle.person_sql import DIMENSIONS

    want, got = _both(tmp_path, _stored(tmp_path, con, {t: f"SELECT * FROM {t}" for t in DIMENSIONS}))  # noqa: S608
    assert_same(want, got, entity=seed.stem)
    assert want["person_map"].rows and want["person_sessions"].rows


# --- the comparison's rules ---------------------------------------------------------------------

_PROMPTS = Table(("session_id", "seq", "t"), [("a", 10, 1), ("a", 30, 2), ("b", 7, 1)])
_STEPS = Table(("session_id", "seq", "step"), [("a", 30, 1), ("a", 30, 2), ("b", 7, 1)])


def test_seq_compares_by_order_within_its_session():
    # The v6 views' seq is an order key ((hour, rank)): only its order within a session counts.
    other = Table(_PROMPTS.columns, [("a", 1_000_000, 1), ("a", 2_000_005, 2), ("b", 3, 1)])
    steps = Table(_STEPS.columns, [("a", 2_000_005, 1), ("a", 2_000_005, 2), ("b", 3, 1)])
    want = {"prompt_rows": _PROMPTS, "prompt_steps": _STEPS}
    assert differences(want, {"prompt_rows": other, "prompt_steps": steps}) == []
    assert normalize(want)["prompt_steps"].rows == [("a", 1, 1), ("a", 1, 2), ("b", 0, 1)]
    # A step names its prompt by the prompt row's seq, so a step on another prompt differs.
    moved = Table(_STEPS.columns, [("a", 1_000_000, 1), ("a", 2_000_005, 2), ("b", 3, 1)])
    assert differences(want, {"prompt_rows": other, "prompt_steps": moved})


def test_the_comparison_names_each_difference():
    t = Table(("session_id", "x"), [("a", 1), ("a", 2)])
    flipped = Table(t.columns, list(reversed(t.rows)))
    assert differences({"t": t}, {"t": flipped})[0].startswith("t: same rows, other order")
    assert differences({"t": t}, {"t": flipped}, unordered=["t"]) == []
    assert "oracle only [('a', 2)]" in differences({"t": t}, {"t": Table(t.columns, [("a", 1)])})[0]
    assert "columns" in differences({"t": t}, {"t": Table(("session_id", "y"), t.rows)})[0]
    assert differences({"t": t}, {}) == ["t: only on the oracle side"]
    assert differences({"t": t, "u": t}, {"t": t}, only=["t"]) == []
    with pytest.raises(AssertionError, match="session s1: t: only on the other side"):
        assert_same({}, {"t": t}, entity="session s1")


def test_a_view_object_reads_as_tables(tmp_path):
    # docs/PIPELINE.md §3.1: one row, each table a column holding a list of structs in its row type.
    path = tmp_path / "h.parquet"
    duckdb.connect().execute(
        "COPY (SELECT [{'session_id': 's', 'seq': 5, 'tools': ['a', 'b']}, {'session_id': 's', 'seq': 9, 'tools': []}]"
        " AS event_rows, []::STRUCT(session_id VARCHAR, kind VARCHAR)[] AS session_tools)"
        " TO '" + str(path) + "' (FORMAT parquet, COMPRESSION zstd)"
    )
    got = read_view(path)
    assert got["event_rows"] == Table(("session_id", "seq", "tools"), [("s", 5, ["a", "b"]), ("s", 9, [])])
    assert got["session_tools"] == Table(("session_id", "kind"), [])
    both = concat(got, read_view(path))
    assert [r[1] for r in both["event_rows"].rows] == [5, 9, 5, 9] and both["session_tools"].rows == []
