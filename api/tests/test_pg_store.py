"""The API's Postgres loader (docs/PIPELINE.md v7 §5, build step 5): per request one connection and
one READ ONLY, REPEATABLE READ snapshot, the endpoint's statements in order, the rows as Arrow into
the request's DuckDB under today's table names. /status reads the key rows and the pods' index times;
the startup refuses a database whose schema isn't this build's.

What a session page reads (its statements, their count, their plans) is held here; what it serves is
held to the oracle in test_v1_pipeline.py (responses) and test_v6_session_rows.py (tables)."""

import contextlib
import pathlib
import time

import duckdb
import pytest

from aintercept_api import pgstore
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
        loaded = store.load_for(f"/api/v1/sessions/{sid}/events")
        assert loaded is not None
        assert loaded.con.sql("SELECT count(*) FROM session_rows").fetchall() == [(1,)]
        assert loaded.con.sql("SELECT count(*) FROM event_rows").fetchall() == [(0,)]
        (load,) = store.trace
        assert load.begin == "BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY" and load.connections == 1
        store.between = None
        again = store.load_for(f"/api/v1/sessions/{sid}/events")
        assert again is not None and again.con.sql("SELECT count(*) FROM event_rows").fetchall() == [(1,)]


def test_connections_are_pooled_and_a_broken_one_is_replaced(db):
    store = PgStore(db, pool_size=2, trace=True)
    with contextlib.closing(store):
        for _ in range(5):
            assert store.load_for("/api/v1/status") is not None
        assert store.opened == 1
        # A connection the server dropped (a restart, a timeout) is discarded and the request retried.
        # Only this test's database: the test server is shared with concurrent runs.
        _exec(
            db,
            "SELECT pg_terminate_backend(pid) FROM pg_stat_activity"
            " WHERE application_name = 'aintercept_api' AND datname = current_database()",
        )
        time.sleep(0.2)
        assert store.load_for("/api/v1/status") is not None
        assert store.opened == 2


def test_the_pool_holds_at_most_its_size_and_explain_never_joins_it(db):
    # An explain's connection (sequential scans priced out) is its own, closed after; a fresh one
    # past the pool's size is closed, not kept idle: the server's connections stay bounded.
    store = PgStore(db, pool_size=2, trace=True)
    with contextlib.closing(store):
        assert store.load_for("/api/v1/status") is not None
        (statement,) = store.trace[0].statements
        for _ in range(5):
            assert store.explain(statement)
        for _ in range(3):
            with store.pool.connection(fresh=True):
                pass
        assert len(store.pool._idle) <= 2
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
        assert n <= 2, n
        # And a request after them plans as usual.
        assert store.load_for("/api/v1/status") is not None


def test_a_path_no_page_serves_reads_nothing(db):
    with contextlib.closing(PgStore(db)) as store:
        assert store.load_for("/ui/index.html") is None
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


# The statements each session page runs (BEGIN and COMMIT aside), whatever the session's size.
SESSION_STATEMENTS = {
    "": ["session", "titles", "edges"],
    "/timeline": ["session", "titles", "prompt_rows", "session_marks", "event_rows", "presence_segments"],
    "/prompts": ["session", "prompt_rows", "prompt_steps", "session_marks"],
    "/events": ["session", "event_rows"],
    "/state": ["session", "prompt_rows", "session_marks", "event_rows", "session_tools", *pgstore.REQUESTS_TAB],
    "/requests": ["session", *pgstore.REQUESTS_TAB],
}


def test_every_session_page_runs_a_fixed_set_of_statements_on_one_connection(demo):
    layout, dsn = demo
    with Oracle(Layout(layout)) as oracle:
        sids = oracle.session_ids()
    assert len(sids) >= 5
    store = PgStore(dsn, trace=True)
    with contextlib.closing(store):
        for sid in sids:
            for sub, names in SESSION_STATEMENTS.items():
                store.trace.clear()
                assert store.load_for(f"/api/v1/sessions/{sid}{sub}?from=2026-11-04T00:00:00Z") is not None
                (load,) = store.trace
                assert load.connections == 1, (sid, sub)
                assert [s.name for s in load.statements] == names, (sid, sub)
                assert len(load.statements) <= 8
        # An unknown session stops at its row: every page 404s.
        store.trace.clear()
        store.load_for("/api/v1/sessions/no-such-session/timeline")
        assert [s.name for s in store.trace[0].statements] == ["session"]


# The statements each person page runs (BEGIN and COMMIT aside), whatever the person's size: the
# scope's dimension rows, its sessions' rows, the labels' component (the sessions list and the
# timeline's bands), the ranged session tables, and the request log's inputs (one tagged statement).
PERSON_STATEMENTS = {
    "": ["dims", "sessions", "session_call_days"],
    "/sessions": ["dims", "sessions", "labels"],
    "/timeline": ["dims", "sessions", "labels", "event_rows", "session_marks", "prompt_rows", "presence_segments"],
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


# The drawer's and /events' statement (§3.7): the worker's finished row by its primary key, with
# /status's values (the drawer's lastDataAt is global, quirk 7), in one statement; /raw's also reads
# the call's requests row (the view when its detail object can't be read).
DRAWER_STATEMENTS = {
    "/api/v1/requests/demo-000001": ["drawer"],
    "/api/v1/requests/demo-000001/details": ["drawer"],
    "/api/v1/requests/conn-1~2/raw": ["drawer"],
    "/api/v1/requests/reported:req_1": ["drawer"],
    "/api/v1/events/batch-1:3": ["event"],
    "/api/v1/status": ["status"],
}


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


def test_the_drawer_and_status_run_a_fixed_set_of_statements(demo):
    _, dsn = demo
    store = PgStore(dsn, trace=True)
    with contextlib.closing(store):
        for url, names in DRAWER_STATEMENTS.items():
            store.trace.clear()
            assert store.load_for(url) is not None
            (load,) = store.trace
            assert load.connections == 1 and [s.name for s in load.statements] == names, url


def test_the_events_tab_reads_only_its_range(demo):
    # A range given is pushed down, widened by a millisecond each side (a superset of the tab's exact
    # filter, whatever its parsing rounds); without one the tab is the session's whole span.
    _, dsn = demo
    store = PgStore(dsn, trace=True)
    with contextlib.closing(store):
        sid = _busiest(dsn)
        everything = store.load_for(f"/api/v1/sessions/{sid}/events")
        assert everything is not None
        ts = sorted(t for (t,) in everything.con.sql("SELECT ts_ns FROM event_rows").fetchall())
        assert len(ts) >= 4
        lo, hi = ts[1], ts[-2]
        some = store.load_for(f"/api/v1/sessions/{sid}/events?from={_iso(lo)}&to={_iso(hi)}")
        assert some is not None
        got = sorted(t for (t,) in some.con.sql("SELECT ts_ns FROM event_rows").fetchall())
        a, b = lo // 1000 * 1000 - pgstore._WIDEN_NS, hi // 1000 * 1000 + pgstore._WIDEN_NS
        assert got == [t for t in ts if a <= t <= b] and len(got) < len(ts)


def _iso(ns: int) -> str:
    from datetime import UTC, datetime

    return datetime.fromtimestamp(ns // 1000 / 1e6, UTC).strftime("%Y-%m-%dT%H:%M:%S.%fZ")


def _busiest(dsn: str) -> str:
    con, pg = duckdb.connect(), pgdb.connect(dsn)
    try:
        q = "SELECT session_id FROM event_rows GROUP BY 1 ORDER BY count(*) DESC, 1 LIMIT 1"
        return str(pgdb.load(con, pg, "b", q).fetchall()[0][0])
    finally:
        con.close()
        pg.close()


# The tables a page's statements may read whole (tiny): every other read must be able to use an index.
_SMALL = {"schema_version", "tail", "ingest_pods", "workers"}


def test_every_statement_can_use_an_index(demo):
    """Each statement's plan, with sequential scans priced out (the test tables are tiny, so the
    planner would scan them anyway), reads every big table through an index: none needs a full scan."""
    layout, dsn = demo
    store = PgStore(dsn, trace=True)
    plans: list[tuple[str, str]] = []
    with contextlib.closing(store):
        for sid in [_busiest(dsn), "no-such-session"]:
            for sub in SESSION_STATEMENTS:
                store.trace.clear()
                store.load_for(f"/api/v1/sessions/{sid}{sub}")
                plans += [(s.name, p) for s in store.trace[0].statements for p in store.explain(s)]
        for url in [*DRAWER_STATEMENTS, *DIRECTORY_STATEMENTS]:
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
    # [lo, hi] either way; no range counts every call, one with no time too (as the merge's did).
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
                try:
                    got = store.agent_calls_in(lo, hi)
                finally:
                    store.leave(token)
                want = sum(1 for t in times if (lo is None and hi is None) or (
                    t is not None and (lo is None or t >= lo) and (hi is None or t <= hi)))  # fmt: skip
                assert got == want, (lo, hi)


# --- what other keys hold, joined at load (§4) ---------------------------------------------------
# The datasets' cross-key values need build step 2's stored inputs (test_v6_session_rows.py holds
# them to the oracle once they're written); these rows, written by hand, hold the joins meanwhile.

CODEX, CC = "cx-thread-0001", "cc-session-0002"


@pytest.fixture
def joined(pg_dsn):
    """A Codex thread with a title thread's calls and two children, and a Claude Code session with
    an analytics batch in flight in its credential's c: key."""
    _exec(
        pg_dsn,
        f"""INSERT INTO session_rows (session_id, start_ns, last_ns, product, client, own_title, model_calls,
              call_accounts, label_text_alone, label_source_alone) VALUES
            ('{CODEX}', {T0}, {T0 + 10 * NS}, 'codex', 'exec', NULL, 2, ARRAY['acct-1'], 'Session cx-threa', 'none'),
            ('{CC}', {T0}, {T0 + 30 * NS}, 'claude_code', 'cli', NULL, 1, ARRAY['acct-2'],
             'Session cc-sessi', 'none')""",  # noqa: S608
        f"""INSERT INTO session_title_calls (key, id, session_id, ts_ns, call_id, account_id, title) VALUES
            ('s:title-thread', '\\x01', 'title-thread', {T0 + 2 * NS}, 'call-b', 'acct-1', 'Second'),
            ('s:title-thread', '\\x02', 'title-thread', {T0 + 1 * NS}, 'call-a', 'acct-1', 'First'),
            ('s:title-thread', '\\x03', 'title-thread', {T0 + 400 * NS}, 'call-c', 'acct-1', 'Too late'),
            ('s:other', '\\x04', 'other', {T0 + 1 * NS - 1}, 'call-0', 'acct-9', 'Another account')""",  # noqa: S608
        f"""INSERT INTO session_edges (key, id, parent_session_id, child_session_id, kind, child_first_ns) VALUES
            ('s:kid-b', '\\x01', '{CODEX}', 'kid-b', 'review', {T0 + 5 * NS}),
            ('s:kid-a', '\\x01', '{CODEX}', 'kid-a', 'subagent', {T0 + 3 * NS})""",  # noqa: S608
        f"""INSERT INTO dim_session_credential (key, id, session_id, key_hash) VALUES
            ('s:{CODEX}', '\\x01', '{CODEX}', 'kh-1'), ('s:{CC}', '\\x01', '{CC}', 'kh-2')""",  # noqa: S608
        # The CC session's own rendered row: a model call at T0 + 1 s.
        f"""INSERT INTO session_request_rows (scope_session_id, seq, person_id, request_id, ts_ns, kind, agent,
              client, session_id, tool_calls, method, path, background, captured, detail)
            VALUES ('{CC}', 0, '', 'own-call', {T0 + NS}, 'model', 'claude-code', 'cli', '{CC}', '{{}}', 'POST',
                    '/v1/messages', false, true, 'a call')""",  # noqa: S608
        f"""INSERT INTO batch_parts (batch_id, key, n, min_name, first_ns, first_session) VALUES
            ('batch-1', 's:{CC}', 2, 'tengu_b', {T0 + 2 * NS}, '{CC}'),
            ('batch-1', 's:elsewhere', 3, 'tengu_a', {T0 + 3 * NS}, 'elsewhere')""",  # noqa: S608
        # In the credentials' c: keys: the thread's client calls (one in its window, one a minute and
        # more after its end, one model call) and the batch's call, in flight.
        f"""INSERT INTO request_txs (key, id, call_id, correlation_id, key_hash, own_session, prompt_id, req_id,
              client_req_id, kind, hidden_kind, product, is_tx, row_request_id, row_ts_ns, row_kind, row_agent,
              row_client, row_session_id, row_detail, row_tool_calls,
              row_method, row_path, row_background, row_captured) VALUES
            ('c:kh-1:d', '\\x01', 'flags-1', 'flags-1', 'kh-1', '', '', '', '', 'flags', '', 'codex', true,
             'flags-1', {T0 + 5 * NS}, 'flags', 'codex', 'exec', NULL, 'GET /flags', '{{}}', 'GET', '/x', false, true),
            ('c:kh-1:d', '\\x02', 'flags-2', 'flags-2', 'kh-1', '', '', '', '', 'flags', '', 'codex', true,
             'flags-2', {T0 + 71 * NS}, 'flags', 'codex', 'exec', NULL, 'GET /flags', '{{}}', 'GET', '/x', false, true),
            ('c:kh-1:d', '\\x03', 'model-1', 'model-1', 'kh-1', '', '', '', '', 'model', '', 'codex', true,
             'model-1', {T0 + 6 * NS}, 'model', 'codex', 'exec', NULL, 'responses', '{{}}', 'GET', '/x', false, true),
            ('c:kh-2:d', '\\x01', 'batch-1', 'batch-1', 'kh-2', '', '', '', '', 'analytics', '', 'claude_code', true,
             'batch-1', {T0 + 20 * NS}, 'analytics', 'claude-code', 'cli', NULL, NULL, '{{}}', 'POST', '/x',
             true, true)""",  # noqa: S608
    )
    yield pg_dsn
    _exec(
        pg_dsn,
        *(
            f"DELETE FROM {t}"  # noqa: S608
            for t in (
                "session_rows",
                "session_title_calls",
                "session_edges",
                "dim_session_credential",
                "session_request_rows",
                "batch_parts",
                "request_txs",
            )
        ),
    )


def test_a_threads_alone_label_and_children_come_from_other_keys(joined):
    with contextlib.closing(PgStore(joined)) as store:
        loaded = store.load_for(f"/api/v1/sessions/{CODEX}")
        assert loaded is not None
        got = loaded.con.execute(
            "SELECT label_text_alone, label_source_alone, children_json FROM session_rows"
        ).fetchall()
    assert got == [("First", "title", '{"subagent":1,"review":1,"sessionIds":["kid-a","kid-b"]}')]


def test_a_requests_tab_joins_the_rows_other_keys_hold(joined):
    with contextlib.closing(PgStore(joined)) as store:
        codex = store.load_for(f"/api/v1/sessions/{CODEX}/requests")
        assert codex is not None
        # Its client's call in [start, last + 60 s] that no thread names; not the later one, not a model call.
        assert codex.con.execute(
            "SELECT seq, request_id, session_id, scope_session_id, person_id FROM session_request_rows"
        ).fetchall() == [(0, "flags-1", None, CODEX, "")]
        cc = store.load_for(f"/api/v1/sessions/{CC}/requests")
        assert cc is not None
        # The batch in flight: its session is its earliest part's, its count and name the parts'.
        assert cc.con.execute(
            "SELECT seq, request_id, session_id, detail FROM session_request_rows ORDER BY seq"
        ).fetchall() == [(0, "own-call", CC, "a call"), (1, "batch-1", CC, "5 events")]
        client = make_client(store)
        body = client.get(f"/api/v1/sessions/{CC}/requests?order=desc").json()
        assert [r["requestId"] for r in body["data"]] == ["batch-1", "own-call"] and body["meta"]["total"] == 2


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
