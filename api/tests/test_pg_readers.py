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
    "dirty_inputs": "worker/keyrow.go",
    "workers": "worker/claim.go",
    "tail": "tail/tail.go",
}

# The v9 fact tables, the dirty list and the raw files' list (docs/PIPELINE.md §2, §3, §5.1),
# written by the flush (internal/flush) and ingest from steps 2 and 3, and the rollups the
# refresher (internal/refresh) writes from step 4: read by the pages from step 5.
_V9_UNTIL_ROLLUPS = {
    "calls",
    "events",
    "event_envs",
    "accounts",
    "session_accounts",
    "session_credentials",
    "account_devices",
    "session_parents",
    "dirty",
    "ingest_files",
    "session_minutes",
    "session_hours",
    "session_keys",
    "missed_reports",
    "cred_minutes",
    "cred_hours",
}


def _tables() -> list[str]:
    return re.findall(r"^CREATE TABLE (\w+) \(", pgdb.SCHEMA.read_text(), re.M)


def test_every_table_has_a_reader() -> None:
    api = "\n".join(p.read_text() for p in sorted(_SRC.glob("pgstore*.py")))
    unread = []
    for t in _tables():
        if t in _V9_UNTIL_ROLLUPS:
            continue
        if t in _WORKER_READS:
            go = (_GO / _WORKER_READS[t]).read_text()
            assert re.search(rf"\b(FROM|JOIN|UPDATE)\s+{t}\b", go), f"{t}: no read in {_WORKER_READS[t]}"
            continue
        if not re.search(rf"\b{t}\b", api):
            unread.append(t)
    assert unread == [], f"tables no API statement reads: {unread}"


def test_the_analytics_events_are_not_stored() -> None:
    assert "analytics" not in _tables()
