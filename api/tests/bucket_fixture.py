"""Writes the stream processor's bucketed table layout (docs/STREAMING_PIPELINE.md §4.2) for tests:
per bucket, a commit naming base and run files whose rows are `key, id, tomb` plus the table's
own columns. `from_store` lays out every table a Store has loaded, split across a base, two
runs and a ghost row the second run tombstones, so the reader's newest-wins fold is exercised
on real endpoint data."""

import json
import pathlib
from typing import Any

import duckdb

from aintercept_api.buckets import BUCKET_BITS, ROUTING, bucket_of
from aintercept_api.store import FACTS, SNAPSHOTS, Store

ROOT = "stream/v1/"


def table_key(prefix: str, table: str, bucket: int, name: str) -> str:
    return f"{prefix}{ROOT}tables/{table}/bucket={bucket:02d}/{name}.parquet"


def commit_key(prefix: str, bucket: int, seq: int) -> str:
    return f"{prefix}{ROOT}buckets/{bucket:02d}/commit-{seq:012d}.json"


def write_rows(root: pathlib.Path, key: str, con: duckdb.DuckDBPyConnection, select: str, params: list[Any]) -> int:
    """Rows of `select` (which yields key, id, tomb and the table's columns) as a Parquet file at key."""
    path = root / key
    path.parent.mkdir(parents=True, exist_ok=True)
    rel = con.sql(select, params=params)
    rel.write_parquet(str(path))
    return int(con.execute("SELECT count(*) FROM read_parquet(?)", [str(path)]).fetchall()[0][0])


def write_commit(
    root: pathlib.Path,
    prefix: str,
    bucket: int,
    seq: int,
    base: dict[str, list[str]],
    runs: dict[str, list[str]],
    **meta: Any,
) -> str:
    def refs(files: dict[str, list[str]]) -> dict[str, list[dict[str, Any]]]:
        return {t: [{"key": k, "min_key": "", "max_key": "￿", "rows": 0} for k in ks] for t, ks in files.items()}

    body = {"bucket": bucket, "seq": seq, "epoch": 1, "base": refs(base), "runs": refs(runs), **meta}
    key = commit_key(prefix, bucket, seq)
    path = root / key
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(body))
    return key


# Each table's rows, with the routing key (ROUTING's column, prefixed) and a row id unique within the key.
_ROWS = """SELECT ? || coalesce(CAST(t.{col} AS VARCHAR), '') AS key,
       CAST(row_number() OVER (PARTITION BY t.{col}) AS VARCHAR) AS id, false AS tomb, t.*
FROM {table} t"""
# A table read whole: one key, the row's position its id.
_WHOLE = """SELECT ? AS key, CAST(row_number() OVER () AS VARCHAR) AS id, false AS tomb, t.* FROM {table} t"""

_HALF = "SELECT * EXCLUDE (_bucket) FROM src WHERE _bucket = ? AND CAST(id AS INTEGER) % 2 = 1"
_REST = """SELECT * EXCLUDE (_bucket) FROM src WHERE _bucket = ? AND CAST(id AS INTEGER) % 2 = 0
UNION ALL SELECT * FROM (SELECT * EXCLUDE (_bucket) REPLACE ('ghost' AS id) FROM src WHERE _bucket = ? LIMIT 1)"""
_GHOST = """SELECT * FROM (
    SELECT * EXCLUDE (_bucket) REPLACE ('ghost' AS id, true AS tomb) FROM src WHERE _bucket = ? LIMIT 1)"""


def from_store(store: Store, root: pathlib.Path, prefix: str = "") -> None:
    """Every table the store has loaded, bucketed: per bucket a base file with half its rows,
    a first run with the rest plus a ghost row, and a second run that tombstones the ghost."""
    store.ingest()
    con = duckdb.connect()
    files: dict[int, tuple[dict[str, list[str]], dict[str, list[str]]]] = {}
    # request_txs as the processor writes it (every non-synthetic call: its product, and its
    # request-log row's time), the directory's source for /overview's request count.
    store._con.execute(
        "CREATE OR REPLACE TEMP TABLE request_txs AS SELECT call_id, product, {'ts_ns': ts} AS row "
        "FROM calls WHERE NOT synthetic"
    )
    for table in (*FACTS, *SNAPSHOTS, "probes", "request_txs"):
        how, col = ROUTING[table]
        stage = root / ".stage" / f"{table}.parquet"
        stage.parent.mkdir(parents=True, exist_ok=True)
        store._con.sql((_ROWS if col else _WHOLE).format(col=col, table=table), params=[how]).write_parquet(str(stage))  # noqa: S608
        con.execute("DROP TABLE IF EXISTS src")
        con.execute("CREATE TABLE src AS SELECT *, 0 AS _bucket FROM read_parquet(?)", [str(stage)])
        if not con.execute("SELECT count(*) FROM src").fetchall()[0][0]:
            continue
        for (k,) in con.execute("SELECT DISTINCT key FROM src").fetchall():
            con.execute("UPDATE src SET _bucket = ? WHERE key = ?", [bucket_of(k), k])
        for (b,) in con.execute("SELECT DISTINCT _bucket FROM src ORDER BY 1").fetchall():
            base, runs = files.setdefault(b, ({}, {}))
            k0 = table_key(prefix, table, b, "base-000000000001-0000000001-000000000001-0000")
            k1 = table_key(prefix, table, b, "run-0000000001-000000000002")
            k2 = table_key(prefix, table, b, "run-0000000001-000000000003")
            write_rows(root, k0, con, _HALF, [b])
            write_rows(root, k1, con, _REST, [b, b])  # the other half, and one row copied under a ghost id
            write_rows(root, k2, con, _GHOST, [b])  # which this run tombstones
            base[table] = [k0]
            runs[table] = [k1, k2]
    # /status's stats, as the processor's commits carry them (every bucket's the same here: the
    # reader takes the newest, earliest and any across them).
    first, earliest = store._con.execute(
        """SELECT min(ts) FILTER (WHERE NOT synthetic), min(ts)
           FROM (SELECT ts, synthetic FROM calls UNION ALL SELECT row_ns, false FROM analytics
                 UNION ALL SELECT first_ns, true FROM probes)"""
    ).fetchall()[0]
    for b, (base, runs) in files.items():
        write_commit(
            root,
            prefix,
            b,
            3,
            base,
            runs,
            newest_ns=store.newest_ns,
            content_retained=store.content_retained,
            first_call_ns=first or 0,
            earliest_ns=earliest or 0,
        )


__all__ = ["BUCKET_BITS", "commit_key", "from_store", "table_key", "write_commit", "write_rows"]
