"""The API's Postgres store (docs/PIPELINE.md §6): per request one connection and one READ ONLY,
REPEATABLE READ snapshot, the page's statements in order (test_drawer.py, test_session_pages.py and
test_person_pages.py hold what each page reads). On a database at a schema version this build can't read
the API is up but not ready, until it can.

/status (the first call, the probes, the pods) and the overview's request count are held here."""

import contextlib
import pathlib
import time
from collections.abc import Iterator

import pytest
from fastapi.testclient import TestClient

from aintercept_api import pgstore, ranges
from aintercept_api.pgstore import PgStore
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
    """The session's fresh database, emptied of what a test writes (pods, calls)."""
    yield pg_dsn
    _exec(pg_dsn, "DELETE FROM ingest_pods", "DELETE FROM calls")


def test_the_newest_schema_version_is_the_workers(pg_dsn):
    # The API ships the newest schema version it knows (schema_version.py, which `make regen` writes from
    # the workers' migrations; it has no copy of them). The database pgdb builds from schema.sql is at it.
    assert pgstore.OLDEST <= pgstore.NEWEST == pgdb.newest_migration()
    with contextlib.closing(PgStore(pg_dsn)) as store:
        assert store.ready()


# The hash v0.6.2 recorded in schema_version (its schema.sql's sha256), the last before versions.
V062 = "79b14d4e95fdfafe7a2e1f60b76432e39e25bbe551e63572b346c09313a19bbe"


def _version(dsn: str, version: int | None, readers_from: int = 1) -> None:
    """Sets the database's schema version and the oldest build that reads it (None: no row)."""
    _exec(dsn, "DELETE FROM schema_version")
    if version is not None:
        _exec(dsn, f"INSERT INTO schema_version (version, readers_from) VALUES ({version}, {readers_from})")  # noqa: S608


@contextlib.contextmanager
def _before_versions(dsn: str) -> Iterator[None]:
    """schema_version as it was before versions (v0.6.2's hash) until the block ends."""
    _exec(
        dsn,
        "ALTER TABLE schema_version RENAME TO versions_aside",
        "CREATE TABLE schema_version (hash text NOT NULL)",
        f"INSERT INTO schema_version (hash) VALUES ('{V062}')",  # noqa: S608
    )
    try:
        yield
    finally:
        _exec(dsn, "DROP TABLE schema_version", "ALTER TABLE versions_aside RENAME TO schema_version")


def _unready(c: TestClient, store: PgStore) -> None:
    """Up but unready: 503 on the API (its readiness probe fails), and alive."""
    assert not store.ready()
    r = c.get("/api/v1/status")
    assert r.status_code == 503 and r.json()["error"]["code"] == "schema_not_ready"
    assert c.get("/api/v1/people").status_code == 503
    assert c.get("/healthz").status_code == 200


def test_an_older_schema_is_unready_until_the_workers_migrate_it(monkeypatch):
    # An upgrade: the new pod starts before the workers have migrated the database to a version it reads
    # (this build reads 5 on). It answers 503 and stays alive, then is ready without a restart.
    monkeypatch.setattr(pgstore, "RECHECK_SECONDS", 0.0)
    monkeypatch.setattr(pgstore, "OLDEST", 5)
    monkeypatch.setattr(pgstore, "NEWEST", 6)
    admin = pgdb.admin_dsn()
    assert admin is not None
    with pgdb.fresh_database(admin) as other:
        for older in ("none", "before versions", 4):
            aside: contextlib.AbstractContextManager[None] = contextlib.nullcontext()
            if older == "before versions":
                aside = _before_versions(other)
            with aside:
                if older != "before versions":
                    _version(other, None if older == "none" else 4)
                store = PgStore(other)
                c = make_client(store)
                _unready(c, store)
            with contextlib.closing(store):
                _version(other, 5)
                assert c.get("/api/v1/status").status_code == 200 and store.ready()


def test_a_newer_schema_older_builds_read_keeps_a_pod_ready(monkeypatch):
    # A rolling update: the workers migrate past this build's newest version while its pods still serve.
    # An additive migration leaves them ready; one that says older builds can't read it (readers_from
    # after this build's newest) takes them out of the Service until they're replaced.
    monkeypatch.setattr(pgstore, "RECHECK_SECONDS", 0.0)
    admin = pgdb.admin_dsn()
    assert admin is not None
    newest = pgstore.NEWEST
    with pgdb.fresh_database(admin) as other, contextlib.closing(PgStore(other)) as store:
        c = make_client(store)
        assert c.get("/api/v1/status").status_code == 200
        _version(other, newest + 1, readers_from=newest)
        assert c.get("/api/v1/status").status_code == 200 and store.ready()
        _version(other, newest + 2, readers_from=newest + 2)
        _unready(c, store)


def test_the_schema_is_checked_every_few_seconds(pg_dsn, monkeypatch):
    # Between checks a pod answers from the last one: no query per request.
    with contextlib.closing(PgStore(pg_dsn)) as store:
        assert store.ready()
        checks: list[tuple[int, int]] = []

        def check(oldest: int, newest: int) -> int:
            checks.append((oldest, newest))
            return newest

        monkeypatch.setattr(store.reader, "check_schema", check)
        assert store.ready() and checks == []
        store._checked -= pgstore.RECHECK_SECONDS  # the interval passes
        assert store.ready() and checks == [(pgstore.OLDEST, pgstore.NEWEST)]


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


def test_connections_are_pooled_and_a_broken_one_is_replaced(db):
    # A pooled connection the server dropped (a restart, a timeout) is replaced before the request reads.
    store = PgStore(db, pool_size=2, trace=True)
    with contextlib.closing(store):
        client = make_client(store)
        assert client.get("/api/v1/status").status_code == 200
        before = store.reader.pool.opened
        assert client.get("/api/v1/status").status_code == 200
        assert store.reader.pool.opened == before  # reused
        # Only this test's database: the test server is shared with concurrent runs.
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
        statements = [s for load in store.trace for s in load.statements]
        for _ in range(5):
            for statement in statements:
                assert store.reader.explain(statement)
        for _ in range(3):
            with store.reader.pool.connection(fresh=True):
                pass
        assert len(store.reader.pool._idle) <= 2
        with pgdb.connect(db) as conn:
            (n,) = conn.execute(
                "SELECT count(*) FROM pg_stat_activity"
                " WHERE datname = current_database() AND application_name = 'aintercept_api'"
            ).fetchone()
        assert n <= 2, n  # the pool at most its size
        # And a request after them plans as usual.
        assert make_client(store).get("/api/v1/status").status_code == 200


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
        assert load.begin == "BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY"
        plans = [p for s in load.statements for p in store.reader.explain(s)]
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


def test_the_app_reads_postgres(pg_dsn, tmp_path, monkeypatch):
    # Postgres is the API's store: its DSN given outright or in a mounted file, and without one the API
    # refuses to start. The drawer's raw members come from the observer's local directory, or the bucket.
    from aintercept_api.app import create_app
    from aintercept_api.objects import LocalSource

    monkeypatch.setenv("AINTERCEPT_BUCKETS_DIR", str(tmp_path))
    for name in ("AINTERCEPT_PG_DSN", "AINTERCEPT_PG_DSN_FILE", "AINTERCEPT_S3_BUCKET"):
        monkeypatch.delenv(name, raising=False)
    with pytest.raises(ValueError, match="AINTERCEPT_PG_DSN"):
        create_app()
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


def test_the_app_starts_unready_on_another_schema(pg_dsn, tmp_path, monkeypatch):
    # The schema check never stops the app starting (a crash loop would back off for minutes):
    # it's up, unready, and ready once the workers have applied this build's schema.
    from aintercept_api.app import create_app

    monkeypatch.setattr(pgstore, "RECHECK_SECONDS", 0.0)
    monkeypatch.setenv("AINTERCEPT_BUCKETS_DIR", str(tmp_path))
    monkeypatch.delenv("AINTERCEPT_PG_DSN_FILE", raising=False)
    admin = pgdb.admin_dsn()
    assert admin is not None
    with pgdb.fresh_database(admin) as other:
        _version(other, None)
        monkeypatch.setenv("AINTERCEPT_PG_DSN", other)
        app = create_app()
        with contextlib.closing(app.state.store):
            c = TestClient(app)
            assert c.get("/api/v1/status").status_code == 503
            _version(other, pgstore.NEWEST)
            assert c.get("/api/v1/status").status_code == 200
