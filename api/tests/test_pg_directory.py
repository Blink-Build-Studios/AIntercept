"""The directory on Postgres (docs/PIPELINE.md v7 build step 7): /people and /overview side by side
with the oracle.

- **Tables:** for no from (All) and a from on each day the merge has, the tables the directory reads,
  as PgStore loads them (the dimension rows of the keys whose day is from's day or later, plus every
  a: key's, through person_sql; those keys' sessions' rows, call-day folds and missed marks), equal
  the oracle's directory tables (quirk 1: exactly the rows v5's partitions held).
- **Responses:** test_directory.py's tests, each request of /people and /overview answered by both
  stores and held byte for byte: "old" the oracle's tables through the directory SQL as it was (every
  call mark: tests/oracle/directory_sql.py), "new" PgStore. test_v1_pipeline.py holds every from
  (presets, unaligned, none) on the mixed dataset.
"""

import contextlib
import json
import pathlib
from collections.abc import Iterator
from typing import Any

import pytest

from aintercept_api.buckets import LocalSource
from aintercept_api.pgstore import PgStore
from aintercept_api.raw import LocalRaw
from tests import pgdb
from tests.conftest import lay_out, make_client
from tests.oracle.directory_sql import todays_sql
from tests.oracle.harness import Layout, Oracle, Table, Tables, assert_same, extract
from tests.oracle.store import OracleStore
from tests.test_directory import (  # noqa: F401 (run here side by side; see SideBySide)
    test_a_range_without_an_offset_is_rejected,
    test_overview_counts_only_the_window,
    test_overview_coverage_gap,
    test_overview_demo,
    test_overview_empty,
    test_overview_many,
    test_overview_one,
    test_overview_unattributed,
    test_people_are_named_from_their_account_profile,
    test_people_demo,
    test_people_empty,
    test_people_filtered_agent,
    test_people_in_a_range,
    test_people_many_sorted_and_searched,
    test_people_one,
)
from tests.test_v6_session_rows import _datasets, _without


def _admin() -> str:
    admin = pgdb.admin_dsn()
    if admin is None:
        if pgdb.required():
            pytest.fail("AINTERCEPT_TEST_PG isn't set (CI's service container should set it)")
        pytest.skip("no test Postgres: set AINTERCEPT_TEST_PG (make check starts one: scripts/test-pg.sh)")
    return admin


# --- tables --------------------------------------------------------------------------------------

# The tables the directory reads whole, in today's order (accounts has none: a multiset).
_WHOLE = ("person_map", "person_sessions", "account_facts", "account_devices")


@pytest.fixture(scope="module")
def layouts(tmp_path_factory) -> Iterator[list[tuple[str, pathlib.Path, str]]]:
    admin = _admin()
    tmp = tmp_path_factory.mktemp("v7dir")
    with contextlib.ExitStack() as stack:
        out = []
        for name, raw in _datasets(tmp):
            dsn = stack.enter_context(pgdb.fresh_database(admin))
            out.append((name, lay_out(raw, pg=dsn), dsn))
        yield out


def _directory_tables(store: PgStore, day: str | None) -> Tables:
    loaded = store.load_for("/api/v1/people" + (f"?from={day}T00:00:00Z" if day else ""))
    assert loaded is not None
    try:
        con = loaded.con
        spec = {t: "true" for t in (*_WHOLE, "accounts", "session_rows", "session_marks", "session_call_days")}
        return extract(con, spec, None)
    finally:
        loaded.con.close()


def _project(t: Table, columns: tuple[str, ...], where: Any = None) -> Table:
    """t's rows (those where keeps) in columns' order."""
    idx = [t.columns.index(c) for c in columns]
    return Table(columns, [tuple(r[i] for i in idx) for r in t.rows if where is None or where(r)])


def _call_days(marks: Table) -> Table:
    """The oracle's call marks folded per session and day, as _ACTIVITY counted them."""
    s, k, t = (marks.columns.index(c) for c in ("session_id", "kind", "t_ns"))
    days: dict[tuple[str, int], list[int]] = {}
    for r in marks.rows:
        if r[k] == "call":
            day = int((r[t] / 1e9 / 86400) // 1)
            days.setdefault((r[s], day), []).append(r[t])
    cols = ("session_id", "day", "n", "first_ns", "last_ns")
    return Table(cols, sorted((sid, d, len(ts), min(ts), max(ts)) for (sid, d), ts in days.items()))


def test_the_directory_tables_equal_the_oracles_for_every_day(layouts):
    """The directory's tables for no from and a from on every day: the person tables (person_sql over
    the dimension rows of the day's keys), the accounts, the sessions' rows in the directory's
    columns, their missed marks, and the call-day folds (the oracle's call marks, folded)."""
    compared, rows = 0, dict[str, int]()
    for name, root, dsn in layouts:
        with Oracle(Layout(root)) as oracle, contextlib.closing(PgStore(dsn, threads=1)) as store:
            days = oracle.directory_days()
            assert len(days) >= 2, (name, days)
            for day in days:
                want, got = oracle.directory(day), _directory_tables(store, day)
                entity = f"{name}: directory from {day or 'All'}"
                cols = got["session_rows"].columns
                assert_same(
                    {
                        **{t: want[t] for t in _WHOLE},
                        "accounts": want["accounts"],
                        "session_rows": _project(want["session_rows"], cols),
                        "session_marks": _without(
                            _project(want["session_marks"], got["session_marks"].columns, lambda r: r[2] == "missed"),
                            ("seq",),
                        ),
                        "session_call_days": _call_days(want["session_marks"]),
                    },
                    {
                        **{t: got[t] for t in _WHOLE},
                        "accounts": got["accounts"],
                        "session_rows": got["session_rows"],
                        "session_marks": _without(got["session_marks"], ("seq",)),
                        "session_call_days": Table(
                            got["session_call_days"].columns, sorted(got["session_call_days"].rows)
                        ),
                    },
                    entity=entity,
                    unordered=("accounts",),
                )
                compared += 1
                for t, v in got.items():
                    rows[t] = rows.get(t, 0) + len(v.rows)
                oracle._memo.clear()
    print("directory days compared:", compared, "rows:", rows)  # noqa: T201
    assert compared >= 6, compared
    assert all(rows.values()), rows  # not vacuous: every table holds rows somewhere


# --- test_directory.py's tests, side by side -----------------------------------------------------


def _directory(url: str) -> bool:
    return url.split("?")[0] in ("/api/v1/people", "/api/v1/overview")


class SideBySide:
    """A client whose /people and /overview requests are answered by both stores and must agree
    byte for byte (the Postgres answer is returned); anything else is the oracle's alone."""

    def __init__(self, old: Any, new: Any) -> None:
        self.old, self.new = old, new
        self.app = new.app
        self.held = 0

    def get(self, url: str, **kwargs: Any) -> Any:
        if not _directory(url):
            return self.old.get(url, **kwargs)
        with todays_sql():
            a = self.old.get(url, **kwargs)
        b = self.new.get(url, **kwargs)
        assert b.status_code == a.status_code, (url, kwargs, a.status_code, b.status_code)
        assert json.dumps(b.json(), sort_keys=True) == json.dumps(a.json(), sort_keys=True), (url, kwargs)
        self.held += 1
        return b


@pytest.fixture
def client_for():
    """client_for(dataset): a SideBySide client over the dataset laid out once for each store."""
    admin = _admin()
    with contextlib.ExitStack() as stack:
        clients: list[SideBySide] = []

        def make(ds: Any, **kwargs: Any) -> SideBySide:
            root = pathlib.Path(ds.write())
            root.mkdir(parents=True, exist_ok=True)
            dsn = stack.enter_context(pgdb.fresh_database(admin))
            layout = lay_out(root, pg=dsn)
            old = OracleStore(layout, prefix="sim/", raw=LocalRaw(root))
            new = PgStore(dsn, source=LocalSource(layout), prefix="sim/", threads=1)
            stack.callback(old.close)
            stack.callback(new.close)
            clients.append(SideBySide(make_client(old, **kwargs), make_client(new, **kwargs)))
            return clients[-1]

        yield make
        assert sum(c.held for c in clients), "no directory request was held side by side"


@pytest.fixture
def write_sample():
    """The samples are test_directory.py's to write: here only the status is checked."""

    def check(client: Any, response: Any, scenario: str, status: int = 200) -> None:
        assert response.status_code == status, response.text

    return check
