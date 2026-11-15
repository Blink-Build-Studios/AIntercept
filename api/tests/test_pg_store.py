"""The API's Postgres store (docs/PIPELINE.md v7 §5, v8 §5.7): per request one connection and one READ
ONLY, REPEATABLE READ snapshot, the page's statements in order. v8's reader (psycopg) serves /status
(the key rows and the pods' index times), the drawer and the session pages (v9: test_drawer.py and
test_session_pages.py hold what they read); v7's loader the rest, its rows as Arrow into the request's
DuckDB under today's table names. The startup refuses a database whose schema isn't this build's.

What a person page and the directory read (their statements, their count, their plans) is held here."""

import contextlib
import pathlib
import time

import duckdb
import pytest

from aintercept_api import pgstore, ranges
from aintercept_api.pgstore import PgStore, SchemaMismatch
from tests import pgdb
from tests.builders import Dataset, load_fixture
from tests.conftest import lay_out, make_client
from tests.oracle.harness import Layout, Oracle
from tests.test_codex import claude_code_account, codex_work

NS = 1_000_000_000
T0 = 1_762_250_000 * NS  # 2025-11-04T09:53:20Z


def _exec(dsn: str, *sql: str) -> None:
    conn = pgdb.connect(dsn)
    try:
        pgdb.execute(conn, *sql)
    finally:
        conn.close()


@pytest.fixture
def db(pg_dsn):
    """The session's fresh database, emptied of what a test writes (keys, pods, a session row)."""
    yield pg_dsn
    _exec(pg_dsn, "DELETE FROM keys", "DELETE FROM ingest_pods", "DELETE FROM session_rows", "DELETE FROM event_rows")


def test_the_schema_hash_is_this_builds(pg_dsn):
    # The API ships the hash of the schema it reads (it has no copy of schema.sql); a schema change
    # without it fails here.
    assert pgstore.SCHEMA_HASH == pgdb.schema_hash()
    PgStore(pg_dsn).close()


def test_a_database_with_another_schema_is_refused(pg_dsn):
    admin = pgdb.admin_dsn()
    assert admin is not None
    with pgdb.fresh_database(admin) as other:
        _exec(other, "UPDATE schema_version SET hash = 'not-this-build'")
        with pytest.raises(SchemaMismatch):
            PgStore(other)
        _exec(other, "DELETE FROM schema_version")
        with pytest.raises(SchemaMismatch):
            PgStore(other)


def _status(store: PgStore) -> dict:
    with contextlib.closing(store):
        data: dict = make_client(store).get("/api/v1/status").json()["data"]
        return data


def test_status_with_nothing_committed(db):
    assert _status(PgStore(db)) == {
        "proxy": None,
        "certIssued": None,
        "firstRequestAt": None,
        "waitingSince": None,
        "lastIngestAt": None,
        "contentRetained": None,
    }


def test_status_reads_the_keys_and_the_recent_pods(db):
    # As the merge's commit had it: the first request and earliest row are the least nonzero over
    # the keys, the newest the larger of the keys' newest row and the pods' newest upload (only the
    # index objects of the last 10 minutes), retention any key's.
    _exec(
        db,
        f"INSERT INTO keys (key, n, first_request_ns, earliest_ns, newest_row_ns, content_retained) VALUES"  # noqa: S608
        f" ('s:a', 1, 0, {T0 + 5 * NS}, {T0 + 9 * NS}, false),"
        f" ('s:b', 1, {T0 + 7 * NS}, {T0 + 3 * NS}, {T0 + 8 * NS}, true),"
        f" ('c:x', 2, {T0 + 6 * NS}, 0, 0, NULL)",  # noqa: S608
        f"INSERT INTO ingest_pods (incarnation, last_ns, at) VALUES ('p1', {T0 + 30 * NS}, now() - interval"  # noqa: S608
        f" '20 minutes'), ('p2', {T0 + 12 * NS}, now() - interval '1 minute')",
    )
    assert _status(PgStore(db)) == {
        "proxy": None,
        "certIssued": None,
        "firstRequestAt": "2025-11-04T09:53:26Z",
        "waitingSince": None,
        "lastIngestAt": "2025-11-04T09:53:32Z",
        "contentRetained": True,
    }


def test_status_before_the_first_request_waits_since_the_earliest_row(db):
    _exec(
        db,
        f"INSERT INTO keys (key, n, earliest_ns, newest_row_ns, content_retained) VALUES ('ip:x', 1, {T0}, 0, false)",  # noqa: S608
    )
    got = _status(PgStore(db))
    assert got["firstRequestAt"] is None and got["waitingSince"] == "2025-11-04T09:53:20Z"
    assert got["lastIngestAt"] is None and got["contentRetained"] is False


def test_a_request_reads_one_snapshot(db):
    # A commit landing between two of a request's statements isn't seen by the later one: every
    # statement reads the snapshot the first one took.
    sid = "snap-1"
    _exec(db, f"INSERT INTO session_rows (session_id, start_ns, last_ns) VALUES ('{sid}', {T0}, {T0 + NS})")  # noqa: S608
    store = PgStore(db, trace=True)

    def commit_meanwhile(i: int) -> None:
        if i == 0:
            _exec(
                db,
                "INSERT INTO event_rows (session_id, seq, event_id, ts_ns, name)"  # noqa: S608
                f" VALUES ('{sid}', 0, 'b:1', {T0}, 'x')",
            )

    store.between = commit_meanwhile
    with contextlib.closing(store):
        loaded = store.session_entity(sid)
        assert loaded.con.sql("SELECT count(*) FROM session_rows").fetchall() == [(1,)]
        assert loaded.con.sql("SELECT count(*) FROM event_rows").fetchall() == [(0,)]
        (load,) = store.trace
        assert load.begin == "BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY" and load.connections == 1
        store.between = None
        again = store.session_entity(sid)
        assert again.con.sql("SELECT count(*) FROM event_rows").fetchall() == [(1,)]


def test_connections_are_pooled_and_a_broken_one_is_replaced(db):
    store = PgStore(db, pool_size=2, trace=True)
    with contextlib.closing(store):
        opened = store.opened  # the schema check's
        for _ in range(5):
            assert store.session_entity("x") is not None
        assert store.opened == opened + 1
        # A connection the server dropped (a restart, a timeout) is discarded and the request retried.
        # Only this test's database: the test server is shared with concurrent runs.
        _exec(
            db,
            "SELECT pg_terminate_backend(pid) FROM pg_stat_activity"
            " WHERE application_name = 'aintercept_api' AND datname = current_database()",
        )
        time.sleep(0.2)
        assert store.session_entity("x") is not None
        assert store.opened == opened + 2
        # v8's reader's too: its pooled connection, dropped, is replaced before the request reads.
        client = make_client(store)
        assert client.get("/api/v1/status").status_code == 200
        before = store.reader.pool.opened
        assert client.get("/api/v1/status").status_code == 200
        assert store.reader.pool.opened == before  # reused
        _exec(
            db,
            "SELECT pg_terminate_backend(pid) FROM pg_stat_activity"
            " WHERE application_name = 'aintercept_api' AND datname = current_database()",
        )
        time.sleep(0.2)
        assert client.get("/api/v1/status").status_code == 200
        assert store.reader.pool.opened == before + 1


def test_the_pool_holds_at_most_its_size_and_explain_never_joins_it(db):
    # An explain's connection (sequential scans priced out) is its own, closed after; a fresh one
    # past the pool's size is closed, not kept idle: the server's connections stay bounded.
    store = PgStore(db, pool_size=2, trace=True)
    with contextlib.closing(store):
        assert make_client(store).get("/api/v1/status").status_code == 200
        assert store.session_entity("x") is not None
        statements = [s for load in store.trace for s in load.statements]
        assert {s.via for s in statements} == {"psycopg", "adbc"}
        for _ in range(5):
            for statement in statements:
                assert store.explain(statement)
        for pool in (store.pool, store.reader.pool):
            for _ in range(3):
                with pool.connection(fresh=True):
                    pass
            assert len(pool._idle) <= 2
        conn = pgdb.connect(db)
        cur = conn.cursor()
        try:
            cur.execute(
                "SELECT count(*) AS n FROM pg_stat_activity"
                " WHERE datname = current_database() AND application_name = 'aintercept_api'"
            )
            con = duckdb.connect()
            con.from_arrow(cur.fetch_arrow()).create("n")
            (n,) = con.execute("SELECT n FROM n").fetchall()[0]
        finally:
            cur.close()
            conn.close()
        assert n <= 4, n  # each driver's pool at most its size (v7's loader's goes in build step 11)
        # And a request after them plans as usual.
        assert make_client(store).get("/api/v1/status").status_code == 200


def test_a_path_no_page_serves_reads_nothing(db):
    with contextlib.closing(PgStore(db)) as store:
        assert store.load_for("/ui/index.html") is None
        # v8's reader's pages load nothing ahead: they read in their own snapshot.
        for path in (
            "/api/v1/status",
            "/api/v1/requests/x",
            "/api/v1/requests/x/raw",
            "/api/v1/events/b:1",
            "/api/v1/sessions/x",
            "/api/v1/sessions/x/timeline",
            "/api/v1/sessions/x/requests?page=2",
        ):
            assert store.load_for(path) is None, path
        # Paths no person page serves read nothing (the router 404s or 422s them).
        assert store.load_for("/api/v1/people/x/nothing") is None
        assert store.load_for("/api/v1/coverage") is None


# --- what each page reads ------------------------------------------------------------------------


@pytest.fixture(scope="module")
def demo(tmp_path_factory):
    """The demo fixture with Codex work, laid out into a fresh Postgres; (oracle layout, dsn)."""
    admin = pgdb.admin_dsn()
    if admin is None:
        if pgdb.required():
            pytest.fail("AINTERCEPT_TEST_PG isn't set")
        pytest.skip("no test Postgres: set AINTERCEPT_TEST_PG (make check starts one: scripts/test-pg.sh)")
    tmp = tmp_path_factory.mktemp("pgstore")
    ds = Dataset(tmp / "data")
    load_fixture(ds, "demo")
    claude_code_account(ds)
    codex_work(ds)
    with pgdb.fresh_database(admin) as dsn:
        yield lay_out(pathlib.Path(ds.write()), pg=dsn), dsn


# The statements each person page runs (BEGIN and COMMIT aside), whatever the person's size: the
# scope's dimension rows, its sessions' rows, the labels' component (the sessions list and the
# timeline's bands), the ranged session tables, and the request log's inputs (one tagged statement).
PERSON_STATEMENTS = {
    "": ["dims", "sessions", "session_call_days"],
    "/sessions": ["dims", "sessions", "labels"],
    "/timeline": ["dims", "sessions", "labels", "event_rows", "session_marks", "prompt_rows"],
    "/summary": ["dims", "sessions", "event_rows", "session_marks", "call_tool_uses", "request_log"],
    "/requests": ["dims", "request_log"],
    "coverage": ["dims", "sessions", "session_marks", "request_log"],
}
_RANGES = ("", "?from=2026-11-04T00:00:00.000Z", "?from=2026-11-03T21:13:07.250Z&to=2026-11-04T16:41:59.875Z")


def _person_urls(pid: str) -> dict[str, list[str]]:
    out = {}
    for sub in PERSON_STATEMENTS:
        if sub == "coverage":
            out[sub] = [f"/api/v1/coverage?personId={pid}" + q.replace("?", "&") for q in _RANGES]
        else:
            out[sub] = [f"/api/v1/people/{pid}{sub}{q}" for q in _RANGES]
    return out


def test_every_person_page_runs_a_fixed_set_of_statements_on_one_connection(demo):
    layout, dsn = demo
    with Oracle(Layout(layout)) as oracle:
        pids = [*oracle.person_ids(), "unattributed", "nobody"]
    assert len(pids) >= 5
    store = PgStore(dsn, trace=True)
    with contextlib.closing(store):
        for pid in pids:
            for sub, urls in _person_urls(pid).items():
                for url in urls:
                    store.trace.clear()
                    assert store.load_for(url) is not None
                    (load,) = store.trace
                    assert load.connections == 1, url
                    assert [s.name for s in load.statements] == PERSON_STATEMENTS[sub], url
                    assert len(load.statements) <= 8


# /status's statement: v8's finished rows, with the recent pods'. (The drawer's are v9's: test_drawer.py.)
STATUS_STATEMENTS = {"/api/v1/status": ["status"]}


# The directory's statements (build step 7): the dimension rows of the keys whose day is from's day
# or later (and every a: key's; no from: every key's), those days' sessions' rows, their call-day
# folds and missed marks; /overview's request count too. Whatever the from, the people and the keys.
DIRECTORY = ["dimensions", "session_rows", "session_call_days", "missed_marks"]
DIRECTORY_FROMS = ["", "?from=2026-11-04T00:00:00.000Z", "?from=2026-11-03T07:41:13.517Z&to=2026-11-04T09:00:00Z",
                   "?to=2026-11-04T12:00:00Z", "?from=2030-01-01T00:00:00Z"]  # fmt: skip
DIRECTORY_STATEMENTS = {
    **{f"/api/v1/people{q}": DIRECTORY for q in DIRECTORY_FROMS},
    **{f"/api/v1/overview{q}": [*DIRECTORY, "agent_calls"] for q in DIRECTORY_FROMS},
    "/api/v1/people?q=codex&sort=name&page=2&pageSize=1": DIRECTORY,
}


def test_the_directory_runs_a_fixed_set_of_statements(demo, pg_dsn):
    # The same statements on the demo and on an empty database: never one per person, key or session.
    _, dsn = demo
    for d in (dsn, pg_dsn):
        store = PgStore(d, trace=True)
        with contextlib.closing(store):
            for url, names in DIRECTORY_STATEMENTS.items():
                store.trace.clear()
                assert store.load_for(url) is not None
                (load,) = store.trace
                assert load.connections == 1 and [s.name for s in load.statements] == names, url
                assert make_client(store).get(url).status_code == 200, url


def test_status_runs_one_statement(demo):
    # On v8's reader: one statement, on one connection, in one snapshot.
    _, dsn = demo
    store = PgStore(dsn, trace=True)
    with contextlib.closing(store):
        client = make_client(store)
        for url, names in STATUS_STATEMENTS.items():
            store.trace.clear()
            client.get(url)
            (load,) = store.trace
            assert load.connections == 1 and [s.name for s in load.statements] == names, url
            assert (
                load.begin == "BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY" and load.statements[0].via == "psycopg"
            )


# The tables a page's statements may read whole (tiny): every other read must be able to use an index.
_SMALL = {"schema_version", "tail", "ingest_pods", "workers"}


def test_every_statement_can_use_an_index(demo):
    """Each statement's plan, with sequential scans priced out (the test tables are tiny, so the
    planner would scan them anyway), reads every big table through an index: none needs a full scan."""
    layout, dsn = demo
    store = PgStore(dsn, trace=True)
    plans: list[tuple[str, str]] = []
    with contextlib.closing(store):
        client = make_client(store)
        for url in STATUS_STATEMENTS:
            store.trace.clear()
            client.get(url)
            plans += [(s.name, p) for s in store.trace[0].statements for p in store.explain(s)]
        for url in DIRECTORY_STATEMENTS:
            store.trace.clear()
            store.load_for(url)
            plans += [(s.name, p) for s in store.trace[0].statements for p in store.explain(s)]
        with Oracle(Layout(layout)) as oracle:
            busiest = max(oracle.person_ids(), key=lambda p: len(oracle.person(p)["person_sessions"].rows))
        for pid in (busiest, "unattributed"):
            for urls in _person_urls(pid).values():
                for url in urls:
                    store.trace.clear()
                    store.load_for(url)
                    for s in store.trace[0].statements:
                        for p in store.explain(s):
                            # "unattributed" is every visible session no account names (§4): it reads
                            # dim_session_visible whole, one row per session; the anti-join uses an index.
                            if pid == "unattributed" and "Seq Scan on dim_session_visible " in p:
                                continue
                            plans.append((s.name, p))
    scans = [(n, p) for n, p in plans if "Seq Scan on" in p and p.split("Seq Scan on ")[1].split()[0] not in _SMALL]
    assert plans and not scans, scans


# --- /overview's request count (agent_calls_in) ---------------------------------------------------

HOUR = 3600 * NS
US = 1000  # the finest a from= or to= can say
H0 = T0 // HOUR * HOUR  # 2025-11-04T09:00:00Z


def _iso_us(ns: int) -> str:
    from datetime import UTC, datetime, timedelta

    return (datetime(1970, 1, 1, tzinfo=UTC) + timedelta(microseconds=ns // US)).strftime("%Y-%m-%dT%H:%M:%S.%fZ")


@pytest.fixture
def calls(pg_dsn):
    """Agent calls in two keys over three hours, on and around the hour boundaries, and one with no
    time (neither a start nor an end); agent_call_hours as the worker folds them."""
    times = [H0 - US, H0, H0 + US, H0 + HOUR // 2, H0 + HOUR - US, H0 + HOUR, H0 + HOUR + 7 * US,
             H0 + 2 * HOUR - US, H0 + 2 * HOUR, H0 + 3 * HOUR + 5 * US]  # fmt: skip
    rows = [(f"s:k{i % 2}", f"call-{i}", t) for i, t in enumerate(times)] + [("c:x:d", "untimed", None)]
    values = ", ".join(f"('{k}', '{c}', {'NULL' if t is None else t})" for k, c, t in rows)
    _exec(
        pg_dsn,
        f"INSERT INTO agent_calls (key, call_id, ts_ns) VALUES {values}",  # noqa: S608
        f"""INSERT INTO agent_call_hours (hour, key, n)
            SELECT ts_ns - ((ts_ns % {HOUR}) + {HOUR}) % {HOUR}, key, count(*) FROM agent_calls
            WHERE ts_ns IS NOT NULL GROUP BY 1, 2""",  # noqa: S608
    )
    yield pg_dsn, [t for _, _, t in rows]
    _exec(pg_dsn, "DELETE FROM agent_calls", "DELETE FROM agent_call_hours")


def test_overviews_request_count_is_exact_on_and_off_the_hours(calls):
    # Whole hours from agent_call_hours, the partial ones from agent_calls: the count of calls in
    # [lo, hi] either way, the range snapped to whole minutes as the router snaps it; no range counts
    # every call, one with no time too (as the merge's did).
    dsn, times = calls
    bounds = [None, H0 - US, H0, H0 + US, H0 + HOUR - US, H0 + HOUR, H0 + HOUR + 7 * US, H0 + 2 * HOUR,
              H0 + 4 * HOUR]  # fmt: skip
    store = PgStore(dsn)
    with contextlib.closing(store):
        for lo in bounds:
            for hi in bounds:
                q = "&".join(f"{k}={_iso_us(v)}" for k, v in (("from", lo), ("to", hi)) if v is not None)
                loaded = store.load_for(f"/api/v1/overview?{q}")
                token = store.enter(loaded)
                a, b = ranges.snap(lo, hi)
                try:
                    got = store.agent_calls_in(a, b)
                finally:
                    store.leave(token)
                want = sum(1 for t in times if (a is None and b is None) or (
                    t is not None and (a is None or t >= a) and (b is None or t <= b)))  # fmt: skip
                assert got == want, (lo, hi)


def test_the_app_reads_postgres_by_default(pg_dsn, tmp_path, monkeypatch):
    # Postgres is the API's store (build step 8): its DSN given outright or in a mounted file, and
    # without one the API refuses to start. V1Store stays selectable (AINTERCEPT_STORE=v1) for the
    # tests and the oracle.
    from aintercept_api.app import create_app
    from aintercept_api.buckets import LocalSource

    monkeypatch.setenv("AINTERCEPT_BUCKETS_DIR", str(tmp_path))
    monkeypatch.setenv("AINTERCEPT_CACHE_DIR", str(tmp_path / "cache"))
    for name in ("AINTERCEPT_STORE", "AINTERCEPT_PG_DSN", "AINTERCEPT_PG_DSN_FILE"):
        monkeypatch.delenv(name, raising=False)
    with pytest.raises(ValueError, match="AINTERCEPT_PG_DSN.*AINTERCEPT_STORE=v1"):
        create_app()
    monkeypatch.setenv("AINTERCEPT_STORE", "v1")
    assert type(create_app().state.store).__name__ == "V1Store"
    monkeypatch.setenv("AINTERCEPT_STORE", "redis")
    with pytest.raises(ValueError, match="AINTERCEPT_STORE"):
        create_app()
    monkeypatch.delenv("AINTERCEPT_STORE")
    (tmp_path / "dsn").write_text(pg_dsn + "\n")
    monkeypatch.setenv("AINTERCEPT_PG_DSN_FILE", str(tmp_path / "dsn"))
    store = create_app().state.store
    try:
        assert isinstance(store, PgStore) and isinstance(store.source, LocalSource)
        assert make_client(store).get("/api/v1/status").status_code == 200
    finally:
        store.close()
    monkeypatch.delenv("AINTERCEPT_PG_DSN_FILE")
    monkeypatch.setenv("AINTERCEPT_PG_DSN", pg_dsn)
    create_app().state.store.close()


def test_the_app_refuses_to_start_on_another_schema(pg_dsn, tmp_path, monkeypatch):
    # The schema-hash check runs when the app starts (the API is up only on this build's schema).
    from aintercept_api.app import create_app

    monkeypatch.setenv("AINTERCEPT_BUCKETS_DIR", str(tmp_path))
    monkeypatch.delenv("AINTERCEPT_STORE", raising=False)
    monkeypatch.delenv("AINTERCEPT_PG_DSN_FILE", raising=False)
    admin = pgdb.admin_dsn()
    assert admin is not None
    with pgdb.fresh_database(admin) as other:
        _exec(other, "UPDATE schema_version SET hash = 'not-this-build'")
        monkeypatch.setenv("AINTERCEPT_PG_DSN", other)
        with pytest.raises(SchemaMismatch):
            create_app()
