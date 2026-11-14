"""Every Postgres table has a reader (docs/PIPELINE.md §3.1): an API statement (pgstore*.py) or the
worker's own reads of it (coordination, other keys' rows). A table nothing reads costs the database
its rows for nothing: the analytics events were the largest table and had no reader (v8)."""

import pathlib
import re

from tests import pgdb

_SRC = pathlib.Path(__file__).resolve().parents[1] / "src" / "aintercept_api"
_GO = pathlib.Path(__file__).resolve().parents[2] / "aintercept" / "internal"
# The tables only the worker reads, and the Go file whose statement does.
_WORKER_READS = {
    "dirty_keys": "worker/claim.go",
    "workers": "worker/claim.go",
    "tail": "tail/tail.go",
}


def _tables() -> list[str]:
    return re.findall(r"^CREATE TABLE (\w+) \(", pgdb.SCHEMA.read_text(), re.M)


def test_every_table_has_a_reader() -> None:
    api = "\n".join(p.read_text() for p in sorted(_SRC.glob("pgstore*.py")))
    unread = []
    for t in _tables():
        if t in _WORKER_READS:
            go = (_GO / _WORKER_READS[t]).read_text()
            assert re.search(rf"\b(FROM|JOIN|UPDATE)\s+{t}\b", go), f"{t}: no read in {_WORKER_READS[t]}"
            continue
        if not re.search(rf"\b{t}\b", api):
            unread.append(t)
    assert unread == [], f"tables no API statement reads: {unread}"


def test_the_analytics_events_are_not_stored() -> None:
    assert "analytics" not in _tables()
