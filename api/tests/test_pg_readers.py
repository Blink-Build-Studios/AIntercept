"""Every Postgres table has a reader (docs/PIPELINE.md §3): an API statement or the workers' own reads
of it (their coordination, and the rollups' inputs). A table nothing reads costs the database its rows
for nothing: the analytics events were the largest table and had no reader (v8)."""

import pathlib
import re

from tests import pgdb

_SRC = pathlib.Path(__file__).resolve().parents[1] / "src" / "aintercept_api"
_GO = pathlib.Path(__file__).resolve().parents[2] / "aintercept" / "internal"
_API_READERS = (
    "reader.py",
    "pgstore.py",
    "drawer.py",
    "sessionpages.py",
    "personpages.py",
    "directorypages.py",
    "tools.py",
)
_WORKERS = ("ingest", "flush", "refresh")


def _tables() -> list[str]:
    return re.findall(r"^CREATE TABLE (\w+) \(", pgdb.SCHEMA.read_text(), re.M)


def test_every_table_has_a_reader() -> None:
    api = "\n".join((_SRC / p).read_text() for p in _API_READERS)
    go = "\n".join(p.read_text() for w in _WORKERS for p in sorted((_GO / w).glob("*.go")) if "_test" not in p.name)
    unread = [
        t
        for t in _tables()
        # v0.6.2's schema hash: kept so v0.6.2's API pods stay ready through the upgrade to migrations.
        if t != "schema_version"
        # Who each credential belongs to (issue #106): written now so its history is there when the
        # person page's credentials (the follow-up) read it.
        and t != "credentials"
        and not re.search(rf"\b(FROM|JOIN)\s+{t}\b", api)
        and not re.search(rf"\b(FROM|JOIN|UPDATE)\s+{t}\b", go)
    ]
    assert unread == [], f"tables nothing reads: {unread}"


def test_the_analytics_events_are_not_stored() -> None:
    assert "analytics" not in _tables()
