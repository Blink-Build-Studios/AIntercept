"""Writes the stream processor's bucketed table layout (docs/STREAMING_PIPELINE.md §4.2) for tests:
per bucket, a commit naming base and run files whose rows are `key, id, tomb` plus the table's
own columns."""

import json
import pathlib
from typing import Any

import duckdb

from aintercept_api.buckets import BUCKET_BITS

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
__all__ = ["BUCKET_BITS", "commit_key", "table_key", "write_commit", "write_rows"]
