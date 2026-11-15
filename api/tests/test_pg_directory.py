"""The directory on Postgres (docs/PIPELINE.md v7 build step 7): /people and /overview side by side
with the oracle.

- **Tables:** for no from (All) and a from on each day the merge has, the tables the directory reads,
  as PgStore loads them (the dimension rows of the keys whose day is from's day or later, plus every
  a: key's, through person_sql; those keys' sessions' rows, call-day folds and missed marks), equal
  the oracle's directory tables (quirk 1: exactly the rows v5's partitions held).

The pages read v9 now (docs/PIPELINE.md §6.4): test_person_pages.py holds them to a from-scratch
computation. This holds v7's loader until step 8 deletes it.
"""

import contextlib
import pathlib
from collections.abc import Iterator
from typing import Any

import pytest

from aintercept_api.pgstore import PgStore
from tests import pgdb
from tests.conftest import lay_out
from tests.oracle.harness import Layout, Oracle, Table, Tables, assert_same, extract
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
    from aintercept_api import pgstore_directory

    params = {"from": [f"{day}T00:00:00Z"]} if day else {}
    loaded = store._read("/api/v1/people", lambda run: pgstore_directory.load(run, overview=False, params=params))
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
