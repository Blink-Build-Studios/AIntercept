"""The API's Postgres store (docs/PIPELINE.md v7 §5, v8 §5.7): per request one connection and one READ
ONLY, REPEATABLE READ snapshot, the page's statements in order. The reader (psycopg) serves every page
from v9's rows (test_drawer.py, test_session_pages.py and test_person_pages.py hold what they read); v7's
loader stays, unread by any page, until step 8 deletes it. The startup refuses a database whose schema
isn't this build's.

/status (the first call, the probes, the pods) and the overview's request count are held here."""

import contextlib
import pathlib
import time

import duckdb
import pytest

from aintercept_api import pgstore, ranges
from aintercept_api.pgstore import PgStore, SchemaMismatch
from tests import pgdb
from tests.builders import Dataset, load_fixture
from tests.conftest import make_client
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
    _exec(
        pg_dsn,
        "DELETE FROM keys",
        "DELETE FROM ingest_pods",
        "DELETE FROM session_rows",
        "DELETE FROM event_rows",
        "DELETE FROM calls",
    )


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


def _call(call_id: str, at: int, *, synthetic: bool = False, detail: str | None = None) -> str:
    """A calls row: a call at `at`, a probe when synthetic, its detail when the content was kept."""
    d = "NULL" if detail is None else f"'{detail}'"
    return (
        "INSERT INTO calls (call_id, correlation_id, kind, stream, synthetic, start_ns, detail)"  # noqa: S608
        f" VALUES ('{call_id}', '{call_id}', 'model', false, {synthetic}, {at}, {d})"
    )


def test_status_reads_the_first_call_and_the_pods(db):
    # §6.4: the first captured call (not a probe) by index; the pods' newest record; content kept when
    # any call kept its detail.
    _exec(
        db,
        _call("probe", T0 + 3 * NS, synthetic=True),
        _call("a", T0 + 7 * NS),
        _call("b", T0 + 9 * NS, detail='{"text": "x"}'),
        f"INSERT INTO ingest_pods (incarnation, last_ns, at) VALUES ('p1', {T0 + 30 * NS}, now() - interval"  # noqa: S608
        f" '20 minutes'), ('p2', {T0 + 12 * NS}, now() - interval '1 minute')",
    )
    assert _status(PgStore(db)) == {
        "proxy": None,
        "certIssued": None,
        "firstRequestAt": "2025-11-04T09:53:27Z",
        "waitingSince": None,
        "lastIngestAt": "2025-11-04T09:53:50Z",
        "contentRetained": True,
    }


def test_status_before_the_first_request_waits_since_the_first_probe(db):
    _exec(db, _call("probe-2", T0 + NS, synthetic=True), _call("probe-1", T0, synthetic=True))
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
            "/api/v1/people",
            "/api/v1/overview?from=2026-11-04T00:00:00Z",
            "/api/v1/people/x",
            "/api/v1/people/x/requests",
        ):
            assert store.load_for(path) is None, path
        assert store.load_for("/api/v1/people/x/nothing") is None


# --- what /status reads ----------------------------------------------------------------------------


@pytest.fixture(scope="module")
def demo(tmp_path_factory):
    """The demo fixture with Codex work in a fresh Postgres (conftest.PgDataset)."""
    from tests.conftest import PgDataset, pg_admin

    tmp = tmp_path_factory.mktemp("pgstore")
    ds = Dataset(tmp / "data")
    load_fixture(ds, "demo")
    claude_code_account(ds)
    codex_work(ds)
    pg = PgDataset(pg_admin(), pathlib.Path(ds.write()))
    yield pg.dsn
    pg.close()


def test_status_runs_one_statement_through_indexes(demo):
    # One statement, on one connection, in one snapshot; each of its reads through an index.
    store = PgStore(demo, trace=True)
    with contextlib.closing(store):
        store.trace.clear()
        make_client(store).get("/api/v1/status")
        (load,) = store.trace
        assert load.connections == 1 and [s.name for s in load.statements] == ["status"]
        assert load.begin == "BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY" and load.statements[0].via == "psycopg"
        plans = [p for s in load.statements for p in store.explain(s)]
    scans = [p for p in plans if "Seq Scan on" in p and p.split("Seq Scan on ")[1].split()[0] != "ingest_pods"]
    assert plans and not scans, scans


# --- /overview's request count (cred_hours, cred_minutes) -------------------------------------------

HOUR = 3600 * NS
MINUTE = 60 * NS
US = 1000  # the finest a from= or to= can say
H0 = T0 // HOUR * HOUR  # 2025-11-04T09:00:00Z


def _iso_us(ns: int) -> str:
    from datetime import UTC, datetime, timedelta

    return (datetime(1970, 1, 1, tzinfo=UTC) + timedelta(microseconds=ns // US)).strftime("%Y-%m-%dT%H:%M:%S.%fZ")


@pytest.fixture
def calls(pg_dsn):
    """Coding agents' calls of two credentials over three hours, on and around the hour boundaries, as
    the refresher rolls them up (cred_minutes, and cred_hours their sums)."""
    times = [H0 - US, H0, H0 + US, H0 + HOUR // 2, H0 + HOUR - US, H0 + HOUR, H0 + HOUR + 7 * US,
             H0 + 2 * HOUR - US, H0 + 2 * HOUR, H0 + 3 * HOUR + 5 * US]  # fmt: skip
    rows = [(f"k{i % 2}", t) for i, t in enumerate(times)]
    values = ", ".join(f"('{k}', {t - t % MINUTE}, 'model', false, 'claude_code', 1, 1)" for k, t in rows)
    _exec(
        pg_dsn,
        f"""INSERT INTO cred_minutes (key_hash, minute_ns, kind, hidden, agent, n, n_tx)
            SELECT key_hash, minute_ns, kind, hidden, agent, sum(n), sum(n_tx) FROM (VALUES {values})
              v(key_hash, minute_ns, kind, hidden, agent, n, n_tx)
            GROUP BY 1, 2, 3, 4, 5""",  # noqa: S608
        f"""INSERT INTO cred_hours (key_hash, hour_ns, kind, hidden, agent, n, n_tx)
            SELECT key_hash, minute_ns - minute_ns % {HOUR}, kind, hidden, agent, sum(n), sum(n_tx) FROM cred_minutes
            GROUP BY 1, 2, 3, 4, 5""",  # noqa: S608
    )
    yield pg_dsn, times
    _exec(pg_dsn, "DELETE FROM cred_minutes", "DELETE FROM cred_hours")


def test_overviews_request_count_is_exact_on_and_off_the_hours(calls):
    # Whole hours from cred_hours, the partial ones from cred_minutes: the count of calls in [lo, hi],
    # the range snapped to whole minutes as the router snaps it; no range counts every call.
    dsn, times = calls
    bounds = [None, H0 - US, H0, H0 + US, H0 + HOUR - US, H0 + HOUR, H0 + HOUR + 7 * US, H0 + 2 * HOUR,
              H0 + 4 * HOUR]  # fmt: skip
    store = PgStore(dsn)
    with contextlib.closing(store):
        client = make_client(store)
        for lo in bounds:
            for hi in bounds:
                q = "&".join(f"{k}={_iso_us(v)}" for k, v in (("from", lo), ("to", hi)) if v is not None)
                got = client.get(f"/api/v1/overview?{q}").json()["data"]["requests"]
                a, b = ranges.snap(lo, hi)
                want = sum(1 for t in times if (a is None or t >= a) and (b is None or t <= b))
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
