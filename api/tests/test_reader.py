"""The API's psycopg reader (docs/PIPELINE.md v8 §5.1, §5.7; build step 1): the pool, one READ ONLY,
REPEATABLE READ snapshot per request, its statements, the trace; and the router's page-reader seam
that picks each page's reader."""

import re
import time

import psycopg
import pytest

from aintercept_api import pages
from aintercept_api.app import create_app
from aintercept_api.pgstore import PgPages
from aintercept_api.reader import BEGIN, Load, Reader
from tests.side_by_side import PAGES


def _exec(dsn: str, *sql: str) -> None:
    with psycopg.connect(dsn, autocommit=True) as conn:
        for s in sql:
            conn.execute(s)


@pytest.fixture
def db(pg_dsn):
    yield pg_dsn
    _exec(pg_dsn, "DELETE FROM keys")


def _key(dsn: str, key: str) -> None:
    _exec(dsn, f"INSERT INTO keys (key, n) VALUES ('{key}', 1)")  # noqa: S608 (a test's literal)


def test_a_request_reads_one_snapshot(db):
    # A commit landing between two of a request's statements isn't seen by the later one.
    trace: list[Load] = []
    reader = Reader(db, trace=trace)
    reader.between = lambda i: _key(db, "s:late") if i == 0 else None
    try:
        with reader.snapshot("/x") as snap:
            assert snap.rows("before", "SELECT count(*) FROM keys") == [(0,)]
            assert snap.rows("after", "SELECT count(*) FROM keys WHERE key = %s", ["s:late"]) == [(0,)]
        (load,) = trace
        assert load.path == "/x" and load.begin == BEGIN and load.connections == 1
        assert [(s.name, s.rows, s.params) for s in load.statements] == [("before", 1, []), ("after", 1, ["s:late"])]
        reader.between = None
        with reader.snapshot("/y") as snap:
            assert snap.dicts("again", "SELECT count(*) AS n FROM keys WHERE key = %(k)s", {"k": "s:late"}) == [
                {"n": 1}
            ]
        assert trace[-1].statements[0].params == {"k": "s:late"}
    finally:
        reader.close()


def test_the_snapshot_is_read_only(db):
    reader = Reader(db)
    try:
        with pytest.raises(psycopg.errors.ReadOnlySqlTransaction), reader.snapshot("/x") as snap:
            snap.rows("write", "INSERT INTO keys (key, n) VALUES ('s:x', 1) RETURNING key")
    finally:
        reader.close()


def test_a_page_that_reads_nothing_takes_no_connection(db):
    trace: list[Load] = []
    reader = Reader(db, trace=trace)
    try:
        with reader.snapshot("/x"):
            pass
        assert reader.pool.opened == 0 and trace == []
    finally:
        reader.close()


def test_a_failed_request_rolls_back_and_returns_its_connection(db):
    # A 404 after a read (an exception out of the page) and a statement that fails both end the
    # transaction; the connection goes back to the pool and the next request reuses it.
    reader = Reader(db, pool_size=1)
    try:
        with pytest.raises(LookupError), reader.snapshot("/x") as snap:
            snap.rows("one", "SELECT 1")
            raise LookupError("no such session")
        with pytest.raises(psycopg.errors.UndefinedTable), reader.snapshot("/x") as snap:
            snap.rows("bad", "SELECT * FROM no_such_table")
        with reader.snapshot("/x") as snap:
            assert snap.rows("fine", "SELECT 2") == [(2,)]
        assert reader.pool.opened == 1
    finally:
        reader.close()


def test_a_dropped_pooled_connection_is_replaced_before_the_request_reads(db):
    reader = Reader(db, pool_size=2)
    try:
        with reader.snapshot("/x") as snap:
            snap.rows("one", "SELECT 1")
        # Only this test's database: the test server is shared with concurrent runs.
        _exec(
            db,
            "SELECT pg_terminate_backend(pid) FROM pg_stat_activity"
            " WHERE application_name = 'aintercept_api' AND datname = current_database()",
        )
        time.sleep(0.2)
        with reader.snapshot("/x") as snap:
            assert snap.rows("two", "SELECT 2") == [(2,)]
            assert snap.load.connections == 1
        assert reader.pool.opened == 2
    finally:
        reader.close()


def test_explain_reads_a_statements_plan_on_its_own_connection(db):
    trace: list[Load] = []
    reader = Reader(db, trace=trace)
    try:
        with reader.snapshot("/x") as snap:
            snap.rows("by key", "SELECT n FROM keys WHERE key = %(k)s", {"k": "s:a"})
        plan = reader.explain(trace[0].statements[0])
        assert any("keys_pkey" in line for line in plan), plan
        assert reader.pool.opened == 1  # the explain's connection was its own, and closed
    finally:
        reader.close()


# --- the seam ---------------------------------------------------------------------------------------


def test_every_route_is_a_page():
    """Each API route maps to one reader page (pages.page_of), and the harness walks every page."""

    class _Store:
        memory_limit = None

    app = create_app(store=_Store(), auth=None)  # type: ignore[arg-type]
    routes = [r for r in app.openapi()["paths"] if r.startswith("/api/v1/")]
    names: list[str] = []
    for route in routes:
        path = re.sub(r"\{[^}]+\}", "x", route)
        name = pages.page_of(path)
        assert name is not None, route
        names.append(name)
    assert sorted(names) == sorted(pages.PAGE_NAMES), (names, pages.PAGE_NAMES)
    assert sorted(p.name for p in PAGES) == sorted(pages.PAGE_NAMES)
    assert PgPages.PAGES <= set(pages.PAGE_NAMES)
    assert pages.page_of("/api/v1/nothing") is None and pages.page_of("/ui/index.html") is None


def test_a_store_without_a_picker_reads_every_page_with_todays_sql():
    class _App:
        class state:  # noqa: N801
            store = object()

    class _Request:
        app = _App
        url = None

    with pages.read(_Request(), "status") as reader:  # type: ignore[arg-type]
        assert isinstance(reader, pages.SqlPages)
