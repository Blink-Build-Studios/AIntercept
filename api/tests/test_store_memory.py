"""The store's memory stays bounded as data grows and commits churn its tables.

The cluster's API was evicted at 2.2 GB on 60 MB of Parquet: its tables were held in
memory uncompressed (about 19 times their Parquet size), 94% of the requests were the
observers' path probe, and each commit's inserts, compactions and snapshot swaps added
to it. These tests replay that shape at a small scale: the probe's rows never load,
the tables live on disk (where DuckDB compresses them and pages them under
`memory_limit`), and every load through the churn fits that limit."""

import itertools
import json
import pathlib

import duckdb
import pytest

from aintercept_api.app import duckdb_memory_limit
from aintercept_api.store import Store
from tests.conftest import make_client, run_processor
from tests.test_store import manifest, more_of, shaped

PREFIX = ""  # keys relative to the output root, as a local processor writes them


def test_probe_rows_arent_loaded_but_their_start_is(dataset):
    """The path probe's rows stay out of the tables, through new parts and a compaction,
    and the setup screen still knows when the probe started."""
    dataset.transaction("2026-11-04T17:40:00", path="/", method="GET", synthetic=True)
    dataset.transaction("2026-11-04T17:50:00")
    root = shaped(dataset)
    store = Store(root)
    client = make_client(store)
    for hour in ("18", "19", "20"):
        more = more_of(dataset)
        more._ids = itertools.count(int(hour) * 100)
        more.transaction(f"2026-11-04T{hour}:00:00", path="/", method="GET", synthetic=True)
        more.transaction(f"2026-11-04T{hour}:00:30")
        more.write()
        run_processor(root, root.parent / "shaped", fresh=False, compact_at=2)
        client.get("/api/v1/status")
    assert any("day" in k or "dt=" in k for k in manifest(root)["facts"]["requests"]), "no compaction ran"
    assert store.query("SELECT count(*), count(*) FILTER (WHERE synthetic) FROM requests")[0] == (4, 0)
    d = make_client(store).get("/api/v1/status").json()["data"]
    assert d["firstRequestAt"] == "2026-11-04T17:50:00Z"
    store.close()


def _write(con: duckdb.DuckDBPyConnection, path: pathlib.Path) -> None:
    """Table `part` as a Parquet file, in row groups about the size the processor writes."""
    con.execute(f"COPY part TO '{path}' (FORMAT parquet, ROW_GROUP_SIZE 8192)")


@pytest.mark.parametrize(
    ("base", "commits", "compact_every"),
    [pytest.param(300_000, 120, 30, marks=pytest.mark.full, id="full"), pytest.param(30_000, 12, 4, id="small")],
)
def test_memory_stays_under_the_limit_through_churn(dataset, tmp_path, base, commits, compact_every):
    """About 420k request rows (90% probes, 3 KB of headers each) and 120 commits with
    four compactions, under a 128 MB limit (the full run; make check runs a tenth of it)."""
    dataset.transaction("2026-11-04T17:00:00", path="/", method="GET", synthetic=True)
    dataset.transaction("2026-11-04T17:00:30")
    root = shaped(dataset)
    out = root.parent / "shaped"
    m = json.loads((out / "manifest.json").read_text())
    seed = out / m["facts"]["requests"][0]
    con = duckdb.connect()

    def part(name: str, rows: int, probes: float) -> str:
        """A requests part of `rows` copies of the seed's rows, `probes` of them synthetic."""
        key = f"{PREFIX}requests/{name}.parquet"
        con.execute(
            """CREATE OR REPLACE TABLE part AS
               SELECT * REPLACE (correlation_id || ? || i AS correlation_id, call_id || ? || i AS call_id,
                                 i < ? AS synthetic, repeat(md5(i::VARCHAR), 100) AS client_json)
               FROM read_parquet(?), range(?) t(i)""",
            [f"-{name}-", f"-{name}-", int(rows // 2 * probes), str(seed), rows // 2],
        )
        _write(con, out / key)
        return key

    m["facts"]["requests"] = [part("base", base, 0.9)]
    store = Store(root, memory_limit="128MB", threads=1, db_dir=tmp_path / "db")
    client = make_client(store)

    def commit(run: int) -> None:
        m["run"], m["committed"] = run, f"2026-11-04T18:00:{run % 60:02d}Z"
        (out / "manifest.json").write_text(json.dumps(m))
        assert client.get("/api/v1/status").status_code == 200
        assert client.get("/api/v1/overview").status_code == 200

    run = m["run"]
    fresh: list[str] = []
    for _ in range(commits):
        run += 1
        fresh.append(part(f"run-{run:012d}", 1_000, 0.9))
        m["facts"]["requests"].append(fresh[-1])
        if len(fresh) == compact_every:  # compaction: the day's parts become one file
            day = f"{PREFIX}requests/day-{run:012d}.parquet"
            files = [str(out / k) for k in fresh]
            con.execute("CREATE OR REPLACE TABLE part AS SELECT * FROM read_parquet(?, union_by_name = true)", [files])
            _write(con, out / day)
            m["facts"]["requests"] = [k for k in m["facts"]["requests"] if k not in fresh] + [day]
            fresh = []
        commit(run)
    real = store.query("SELECT count(*) FROM requests")[0][0]
    assert real == base // 10 + commits * 100  # the probes' 90% never loaded
    assert store.query("SELECT path FROM duckdb_databases() WHERE database_name = current_database()")[0][0]
    used = store.query("SELECT sum(memory_usage_bytes) FROM duckdb_memory()")[0][0]
    assert used <= 128 * 2**20
    store.close()
    assert not list((tmp_path / "db").iterdir())  # the store's files go with it


def test_duckdb_gets_a_share_of_the_container():
    """The chart passes the container's limit; DuckDB's follows it unless set outright."""
    assert duckdb_memory_limit({"AINTERCEPT_CONTAINER_MEMORY": str(1536 * 2**20)}) == "921MB"
    assert duckdb_memory_limit({"AINTERCEPT_CONTAINER_MEMORY": "1610612736", "AINTERCEPT_MEMORY_LIMIT": "2GB"}) == "2GB"
    assert duckdb_memory_limit({}) is None
    assert duckdb_memory_limit({"AINTERCEPT_CONTAINER_MEMORY": "lots"}) is None
