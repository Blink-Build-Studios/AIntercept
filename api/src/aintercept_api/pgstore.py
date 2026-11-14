"""The read path over Postgres (docs/PIPELINE.md v7 §5): each request reads its rows from Postgres
and today's endpoint code runs over them in DuckDB.

For each request the store:
1. takes one pooled connection and opens one snapshot (READ ONLY, REPEATABLE READ), so every
   statement sees the same commits;
2. runs the endpoint's statements in order, one round trip each, each bounded by the page's entity
   (and its range, where one is given) through an index, each resolving the entity itself;
3. reads each result as Arrow (ADBC, binary COPY) straight into the request's in-memory DuckDB,
   rebuilding today's tables: the API's columns in today's order, the re-encodings filled back
   (prompt_rows.session_last_ns; session_marks keep their block keys, which sort as today's seq
   within a kind), and the values other keys hold joined in (sql/labels.sql: a Codex thread's alone
   title, children_json; a session's Requests tab's rows other keys hold). A table a page doesn't
   read is today's empty shape;
4. runs today's endpoint code over it unchanged. Nothing is cached between requests.

Statement parameters go in as a DuckDB relation's Arrow stream: the driver's own conversions of
Python values would need pyarrow, which the API doesn't ship.

Served so far: /status, the session pages, the request drawer and /events/{id} (build step 5; the
drawer's rows precomputed by the worker, §3.7: one statement each), and the directory, /people and
/overview (step 7: pgstore_directory), and the person pages (step 6: pgstore_person). The API's
default store (step 8); AINTERCEPT_STORE=v1 selects V1Store instead.
"""

import contextlib
import contextvars
import importlib.resources
import itertools
import pathlib
import threading
import time
import urllib.parse
from collections import OrderedDict
from collections.abc import Callable, Iterator, Sequence
from dataclasses import dataclass, field
from datetime import UTC, datetime
from typing import Any, TypeVar, cast

import adbc_driver_postgresql.dbapi as pgdbapi
import duckdb

from aintercept_api import raw, state_sql, stream_sql
from aintercept_api import store as legacy

T = TypeVar("T")

# The schema this build reads (aintercept/internal/pg/schema.sql's sha256, as pg.SchemaHash and
# schema_version record it). The API ships no copy of the file; tests/test_pg_store.py holds the
# two equal.
SCHEMA_HASH = "dfa6f4ee3911b2e5996290395138983ca54fe802343b7efd756932cadedc9fe0"

BEGIN = "BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY"
# Per connection, once (the API login sets the same; a DSN with another login still gets them).
_SESSION_SETTINGS = (
    "SET application_name = 'aintercept_api'",
    "SET statement_timeout = '30s'",
    "SET idle_in_transaction_session_timeout = '30s'",
)
POOL_SIZE = 8

_LABELS_SQL = importlib.resources.files("aintercept_api").joinpath("sql/labels.sql").read_text()


class SchemaMismatch(RuntimeError):
    """The database's schema isn't this build's (pg.ErrSchemaMismatch, pg.ErrNoSchema): the API
    refuses to start."""


# --- the API's tables, as today's store shapes them -----------------------------------------------


def _shapes() -> dict[str, list[tuple[str, str]]]:
    con = duckdb.connect()
    try:
        out = {}
        for table, ddl in legacy._EMPTY.items():
            con.execute(ddl)
            out[table] = [(c, t) for c, t, *_ in con.execute(f"DESCRIBE {table}").fetchall()]  # noqa: S608
        return out
    finally:
        con.close()


SHAPES = _shapes()
# What Postgres doesn't hold of a table the API loads (the oracle's file name; analytics.env_json,
# which no query reads).
_NOT_STORED = {"filename", "env_json"}


def _q(name: str) -> str:
    return '"' + name + '"'


def columns(table: str, *, prefix: str = "") -> str:
    """The API's columns of table that Postgres stores, quoted, for a SELECT list."""
    return ", ".join(prefix + _q(c) for c, _ in SHAPES[table] if c not in _NOT_STORED)


# Today's row order of each table (state_sql._ORDER, store._SNAPSHOT_SQL); the stored primary key
# for the rest.
_ORDER = {
    "session_rows": "session_id",
    "prompt_rows": "session_id, seq",
    "prompt_steps": "session_id, seq, step",
    "session_marks": "session_id, kind, seq",
    "event_rows": "session_id, seq",
    "presence_segments": "session_id, seq",
    "session_tools": "session_id, kind, server, tool",
    "call_tool_uses": "session_id, ts_ns, kind, server, tool",
    "session_request_rows": "scope_session_id, seq",
}

# --- the statements -------------------------------------------------------------------------------

# A session's row, with the two columns the load needs and the API doesn't keep.
_SESSION = f"SELECT {columns('session_rows')}, own_title, prompt_last_ns FROM session_rows WHERE session_id = $1"  # noqa: S608
# The alone label's title calls: the session's accounts' (its rendered call_accounts) in its first
# five minutes, when it's a Codex thread with no title of its own (sql/labels.sql's `cand`).
_TITLES = """SELECT t.session_id, t.ts_ns, t.call_id, t.account_id, t.title
FROM session_rows s JOIN session_title_calls t
  ON t.account_id = ANY (s.call_accounts) AND t.ts_ns BETWEEN s.start_ns AND s.start_ns + 300000000000
WHERE s.session_id = $1 AND s.product = 'codex' AND s.own_title IS NULL AND s.model_calls > 0"""
_EDGES = """SELECT parent_session_id, child_session_id, kind, child_first_ns FROM session_edges
WHERE parent_session_id = $1"""


def _own(table: str, *, ranged: bool = False) -> str:
    """A session's rows of a rendered table (by its primary key's session prefix); ranged, those in
    [$2, $3] too (either bound NULL: open)."""
    where = "session_id = $1"
    if ranged:
        where += " AND ($2::bigint IS NULL OR ts_ns >= $2) AND ($3::bigint IS NULL OR ts_ns <= $3)"
    return f"SELECT {columns(table)} FROM {table} WHERE {where}"  # noqa: S608 (fixed names)


_REQUEST_ROWS = f"SELECT {columns('session_request_rows')} FROM session_request_rows WHERE scope_session_id = $1"  # noqa: S608

# The Requests tab's rows other keys hold (§4): the transactions made with the session's credentials
# that its own key can't see. A Codex thread's client calls no thread names sit in the credential's
# c: key (candidates: its credentials' transactions from its start to a minute after its end); an
# analytics batch's call still in flight sits there too, its session unknown until it ends
# (candidates: the transactions of the batches the session has parts of). stream_sql's session
# builder keeps the ones today's tab shows. All of them are non-model calls, so the session's
# reports (ttft, cache writes, step numbers: model calls only) never touch them; their prompt
# numbers can (the session's prompt_index and codex_turns).
_TX_COLUMNS = [
    "call_id", "correlation_id", "key_hash", "own_session", "prompt_id", "req_id", "client_req_id", "kind",
    "hidden_kind", "product", "is_tx",
]  # fmt: skip
_CANDIDATES_CTE = """window_ AS (
    SELECT CASE WHEN product = 'codex' THEN start_ns / 1000 * 1000 END AS lo,
           CASE WHEN product = 'codex' THEN ((last_ns + 60000000000) / 1000 + 1) * 1000 END AS hi
    FROM session_rows WHERE session_id = $1),
cands AS (
    SELECT t.* FROM request_txs t
    WHERE t.key_hash IN (SELECT key_hash FROM dim_session_credential WHERE session_id = $1 AND key_hash <> '')
      AND t.is_tx AND t.key <> 's:' || $1
      AND (t.call_id IN (SELECT batch_id FROM batch_parts WHERE key = 's:' || $1)
           OR (t.row_ts_ns >= (SELECT lo FROM window_) AND t.row_ts_ns < (SELECT hi FROM window_))))"""
_PG_TYPES = {"VARCHAR": "text", "BIGINT": "bigint", "DOUBLE": "double precision", "BOOLEAN": "boolean",
             "VARCHAR[]": "text[]"}  # fmt: skip
_REQUEST_ROW_TYPES = dict(SHAPES["request_rows"])
# The one statement for all of it, each part tagged (src) in one shape: the candidates ('tx'), the
# parts of their batches ('part', all keys'), and the session's prompt numbers ('cc': prompt_index;
# 'codex': codex_turns), so the tab and the state readout stay within a handful of round trips.
_OTHER_PARTS: dict[str, list[tuple[str, str]]] = {
    "tx": [(c, "boolean" if c == "is_tx" else "text") for c in _TX_COLUMNS]
    + [("row_" + c, _PG_TYPES[_REQUEST_ROW_TYPES[c]]) for c in stream_sql.COLUMNS],
    "part": [("part_" + c, t) for c, t in (("batch_id", "text"), ("key", "text"), ("n", "bigint"),
             ("min_name", "text"), ("first_ns", "bigint"), ("first_session", "text"))],
    "num": [("num_" + c, t) for c, t in (("session_id", "text"), ("prompt_id", "text"), ("prompt_index", "bigint"),
            ("ts_ns", "bigint"), ("event_key", "text"), ("first_ns", "bigint"))],
}  # fmt: skip


def _tagged(src: str, part: str, values: dict[str, str], source: str) -> str:
    cols = []
    for p, spec in _OTHER_PARTS.items():
        for c, t in spec:
            cols.append(f"{values[c]} AS {c}" if p == part and c in values else f"NULL::{t} AS {c}")
    return f"SELECT '{src}' AS src, {', '.join(cols)} FROM {source}"  # noqa: S608


_OTHER_KEYS = f"""WITH {_CANDIDATES_CTE}
{_tagged("tx", "tx", {c: c for c, _ in _OTHER_PARTS["tx"]}, "cands")}
UNION ALL
{
    _tagged(
        "part",
        "part",
        {"part_" + c: c for c in ("batch_id", "key", "n", "min_name", "first_ns", "first_session")},
        "batch_parts WHERE batch_id IN (SELECT call_id FROM cands)",
    )
}
UNION ALL
{
    _tagged(
        "cc",
        "num",
        {"num_" + c: c for c in ("session_id", "prompt_id", "prompt_index", "ts_ns", "event_key")},
        "prompt_index WHERE session_id = $1",
    )
}
UNION ALL
{
    _tagged(
        "codex",
        "num",
        {"num_session_id": "session_id", "num_prompt_id": "prompt_id", "num_first_ns": "first_ns"},
        "codex_turns WHERE session_id = $1",
    )
}"""  # noqa: S608 (fixed names)
# The Requests tab's statements after the session's row, in order.
REQUESTS_TAB = ("session_request_rows", "other_keys")

_STATUS = """SELECT
    (SELECT min(first_request_ns) FROM keys WHERE first_request_ns > 0) AS first_request_ns,
    (SELECT min(earliest_ns) FROM keys WHERE earliest_ns > 0) AS earliest_ns,
    (SELECT max(newest_row_ns) FROM keys) AS newest_row_ns,
    (SELECT max(last_ns) FROM ingest_pods WHERE at > now() - interval '10 minutes') AS pods_ns,
    EXISTS (SELECT 1 FROM keys) AS any_key,
    EXISTS (SELECT 1 FROM keys WHERE content_retained) AS retained"""


# The request drawer (§3.7): the worker's finished row, by its primary key (request_id, key; a
# call another key held a moment ago, its copy's: the first key), with /status's values (the
# drawer's lastDataAt is global, quirk 7), in one statement.
def _drawer_sql(column: str) -> str:
    return f"""WITH status AS ({_STATUS})
SELECT status.*, d.{column} AS data FROM status LEFT JOIN LATERAL (
    SELECT {column} FROM drawer_requests WHERE request_id = $1 ORDER BY key LIMIT 1) d ON true"""  # noqa: S608


# /raw: the row's existence, and the call's requests row (the view when its detail object can't be
# read: a WebSocket turn's, or one not found); request_detail formats the detail object as today.
_RAW = f"""WITH status AS ({_STATUS}),
d AS (SELECT request_id FROM drawer_requests WHERE request_id = $1 LIMIT 1)
SELECT status.*, (SELECT count(*) FROM d) AS found, r.* FROM status LEFT JOIN (
    SELECT {columns("requests")} FROM requests
    WHERE call_id = replace($1, '~', '#') AND NOT coalesce(synthetic, false) AND EXISTS (SELECT 1 FROM d)) r ON true"""  # noqa: S608
# /events/{id}: the event's row (its raw row comes from the batch's detail object, as today's).
_EVENT = f"""WITH status AS ({_STATUS})
SELECT status.*, e.data, e.correlation_id, e.event_key FROM status LEFT JOIN LATERAL (
    SELECT data, correlation_id, event_key FROM drawer_events WHERE event_id = $1 ORDER BY key LIMIT 1) e ON true"""  # noqa: S608
_STATUS_COLUMNS = ("first_request_ns", "earliest_ns", "newest_row_ns", "pods_ns", "any_key", "retained")

# What each session page reads after its row: the tables its endpoint code queries.
SESSION_PAGES: dict[str, tuple[str, ...]] = {
    "": ("titles", "edges"),
    "timeline": ("titles", "prompt_rows", "session_marks", "event_rows", "presence_segments"),
    "prompts": ("prompt_rows", "prompt_steps", "session_marks"),
    "events": ("event_rows",),
    "state": ("prompt_rows", "session_marks", "event_rows", "session_tools", *REQUESTS_TAB),
    "requests": REQUESTS_TAB,
}
# A whole session as the table-level harness compares it: everything any of its pages reads.
ENTITY = "*"
SESSION_PAGES[ENTITY] = (
    "titles",
    "edges",
    "prompt_rows",
    "prompt_steps",
    "session_marks",
    "event_rows",
    "presence_segments",
    "session_tools",
    "call_tool_uses",
    *REQUESTS_TAB,
)
_SESSION_TABLES = (
    "prompt_rows",
    "prompt_steps",
    "session_marks",
    "event_rows",
    "presence_segments",
    "session_tools",
    "call_tool_uses",
)


_EXPLAIN_FN = """CREATE OR REPLACE FUNCTION pg_temp.explain_plan(q text) RETURNS SETOF text LANGUAGE plpgsql AS $f$
DECLARE r text;
BEGIN
  FOR r IN EXECUTE 'EXPLAIN ' || q LOOP RETURN NEXT r; END LOOP;
END $f$"""

# --- tracing (tests: what a request read) -----------------------------------------------------------


@dataclass
class Statement:
    name: str
    sql: str
    params: list[tuple[object, str]]
    rows: int = 0
    ms: float = 0.0


@dataclass
class Load:
    path: str
    begin: str = BEGIN
    connections: int = 0
    statements: list[Statement] = field(default_factory=list)


@dataclass
class Status:
    first_request_ns: int | None
    earliest_ns: int | None
    newest_ns: int | None
    content_retained: bool | None


@dataclass
class _Loaded:
    con: duckdb.DuckDBPyConnection
    n: int
    status: Status | None = None
    values: dict[str, Any] = field(default_factory=dict)  # what a plan computed for the endpoint
    memo: OrderedDict[tuple[Any, ...], Any] = field(default_factory=OrderedDict)


_current: contextvars.ContextVar[_Loaded | None] = contextvars.ContextVar("pg_tables", default=None)


class _Broken(Exception):
    """A pooled connection that failed before its snapshot began (the server dropped it)."""


# --- the connection pool --------------------------------------------------------------------------


class _Pool:
    """At most size connections; idle ones are reused, a failed one is closed, never returned."""

    def __init__(self, dsn: str, size: int) -> None:
        self.dsn, self.size = dsn, size
        self._idle: list[Any] = []
        self._slots = threading.BoundedSemaphore(size)
        self._lock = threading.Lock()
        self.opened = 0

    def open(self) -> Any:
        conn = pgdbapi.connect(self.dsn, autocommit=True)
        cur = conn.cursor()
        try:
            for s in _SESSION_SETTINGS:
                cur.execute(s)
        finally:
            cur.close()
        with self._lock:
            self.opened += 1
        return conn

    @contextlib.contextmanager
    def connection(self, *, fresh: bool = False) -> Iterator[tuple[Any, bool]]:
        """(a connection, whether it was reused)."""
        self._slots.acquire()
        try:
            with self._lock:
                conn = None if fresh or not self._idle else self._idle.pop()
            reused = conn is not None
            if conn is None:
                conn = self.open()
            ok = False
            try:
                yield conn, reused
                ok = True
            finally:
                keep = False
                if ok:
                    with self._lock:
                        keep = len(self._idle) < self.size
                        if keep:
                            self._idle.append(conn)
                if not keep:
                    with contextlib.suppress(Exception):
                        conn.close()
        finally:
            self._slots.release()

    def close(self) -> None:
        with self._lock:
            idle, self._idle = self._idle, []
        for conn in idle:
            with contextlib.suppress(Exception):
                conn.close()


# --- one request's reads --------------------------------------------------------------------------


class _Run:
    """One request's snapshot: its statements, each result into the request's DuckDB."""

    def __init__(self, cur: Any, con: duckdb.DuckDBPyConnection, load: Load, between: Callable[[int], None] | None):
        self.cur, self.con, self.load, self.between = cur, con, load, between
        self.values: dict[str, Any] = {}  # handed to the request (_Loaded.values)
        self._params = con.cursor()  # its own connection: the relation's open result can't block con

    def fetch(self, name: str, sql: str, params: Sequence[tuple[object, str]] = ()) -> str:
        """sql's rows as the DuckDB table _pg_<name>; params are (value, DuckDB type)."""
        t0 = time.perf_counter()
        if params:
            # Parameters bind from a relation's Arrow stream ($1, $2, … by position).
            select = ", ".join(f"${i + 1}::{t} AS p{i + 1}" for i, (_, t) in enumerate(params))
            self.cur.execute(sql, self._params.sql(f"SELECT {select}", params=[v for v, _ in params]))
        else:
            self.cur.execute(sql)
        table = "_pg_" + name
        self.con.from_arrow(self.cur.fetch_arrow()).create(table)
        n = int(self.con.execute(f"SELECT count(*) FROM {table}").fetchall()[0][0])  # noqa: S608
        self.load.statements.append(Statement(name, sql, list(params), n, (time.perf_counter() - t0) * 1000))
        if self.between is not None:
            self.between(len(self.load.statements) - 1)
        return table


def _sid(value: str) -> list[tuple[object, str]]:
    return [(value, "VARCHAR")]


def _event_id(eid: str) -> str:
    """An event id as the worker writes it, <batch>:<rank>: request_detail.event reads the rank as
    int(n), so "b:007" is b:7's (one that doesn't parse looks up as given, and 404s)."""
    cid, _, n = eid.rpartition(":")
    if cid and n.isdigit():
        with contextlib.suppress(ValueError):
            return f"{cid}:{int(n)}"
    return eid


def _instant_ns(value: str | None) -> int | None:
    """A from=/to= instant in ns, as the endpoint will parse it (None: absent or not one)."""
    if not value:
        return None
    try:
        t = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        return None
    if t.tzinfo is None:
        return None
    t = t.astimezone(UTC)
    return int(t.timestamp()) * 1_000_000_000 + t.microsecond * 1000


# A range pushed down is widened by this much each side: a superset of the endpoint's exact filter
# whatever its parsing rounds.
_WIDEN_NS = 1_000_000


class PgStore:
    """The store the endpoints query (store.Store's interface), over Postgres."""

    # The person summary counts analytics batches by event_rows.batch, stored (§5), not by a regex
    # over event_id (routers/people.py).
    event_batch = "batch"

    def __init__(
        self,
        dsn: str,
        *,
        source: Any = None,
        prefix: str = "",
        pool_size: int = POOL_SIZE,
        memory_limit: str | None = None,
        threads: int | None = None,
        trace: bool = False,
    ) -> None:
        self.pool = _Pool(dsn, pool_size)
        self.source, self.prefix = source, prefix  # calls/<id>.ndjson.gz (the drawer's raw view)
        self.memory_limit, self.threads = memory_limit, threads
        self.data_dir = pathlib.Path(".")
        self.skipped: list[str] = []
        self.trace: list[Load] = []
        self._tracing = trace
        self.between: Callable[[int], None] | None = None  # tests: called after each statement
        self._n = itertools.count(1)
        self.check_schema()

    @property
    def opened(self) -> int:
        return self.pool.opened

    def check_schema(self) -> None:
        """Refuse a database whose schema isn't this build's (pg.Check)."""
        with self.pool.connection() as (conn, _):
            cur = conn.cursor()
            con = duckdb.connect()
            try:
                cur.execute("SELECT to_regclass('schema_version')::text AS reg")
                con.from_arrow(cur.fetch_arrow()).create("reg")
                if con.execute("SELECT reg FROM reg").fetchall() != [("schema_version",)]:
                    raise SchemaMismatch("the database has no schema (the workers apply it)")
                cur.execute("SELECT hash FROM schema_version")
                con.from_arrow(cur.fetch_arrow()).create("h")
                found = [h for (h,) in con.execute("SELECT hash FROM h").fetchall()]
            finally:
                cur.close()
                con.close()
        if found != [SCHEMA_HASH]:
            raise SchemaMismatch(
                f"the database's schema {found} isn't this build's {SCHEMA_HASH}"
                " (a schema change needs a fresh database)"
            )

    def close(self) -> None:
        self.pool.close()

    # --- loading -----------------------------------------------------------------------------------

    def load_for(self, path: str) -> _Loaded | None:
        """The request's tables, read in one snapshot; None for a path that reads none."""
        bare, _, query = path.partition("?")
        params = urllib.parse.parse_qs(query)
        plan: Callable[[_Run], Status | None]
        if bare == "/api/v1/status":
            plan = self._status
        elif bare.startswith("/api/v1/sessions/"):
            sid, _, sub = bare.removeprefix("/api/v1/sessions/").partition("/")
            sid = urllib.parse.unquote(sid)
            if sub not in SESSION_PAGES or sub == ENTITY:
                return None

            def plan(run: _Run) -> Status | None:
                self._session(run, sid, sub, params)
                return None

        elif bare.startswith("/api/v1/requests/"):
            rid, _, sub = bare.removeprefix("/api/v1/requests/").partition("/")
            rid = urllib.parse.unquote(rid)

            def plan(run: _Run) -> Status | None:
                return self._drawer(run, rid, sub)

        elif bare.startswith("/api/v1/events/"):
            eid = urllib.parse.unquote(bare.removeprefix("/api/v1/events/").split("/", 1)[0])

            def plan(run: _Run) -> Status | None:
                return self._event(run, eid)

        elif bare in ("/api/v1/people", "/api/v1/overview"):
            from aintercept_api import pgstore_directory

            def plan(run: _Run) -> Status | None:
                pgstore_directory.load(run, overview=bare == "/api/v1/overview", params=params)
                return None

        elif bare.startswith("/api/v1/people/") or bare == "/api/v1/coverage":
            from aintercept_api import pgstore_person  # it builds on this module's loader

            page = pgstore_person.page_of(bare, params)
            if page is None:
                return None
            pid, sub = page
            lo, hi = pgstore_person.range_of(params)

            def plan(run: _Run) -> Status | None:
                return pgstore_person.load(self, run, pid, sub, lo, hi)

        else:
            return None
        return self._read(path, plan)

    def session_entity(self, sid: str) -> _Loaded:
        """A whole session's tables, everything any of its pages reads (the table-level harness)."""

        def plan(run: _Run) -> Status | None:
            self._session(run, sid, ENTITY, {})
            return None

        return self._read(f"entity:{sid}", plan)

    def _read(self, path: str, plan: Callable[[_Run], Status | None]) -> _Loaded:
        try:
            return self._read_once(path, plan, fresh=False)
        except _Broken:
            return self._read_once(path, plan, fresh=True)

    def _read_once(self, path: str, plan: Callable[[_Run], Status | None], *, fresh: bool) -> _Loaded:
        load = Load(path)
        con = self._duckdb()
        try:
            with self.pool.connection(fresh=fresh) as (conn, reused):
                load.connections += 1
                cur = conn.cursor()
                try:
                    try:
                        cur.execute(BEGIN)
                    except Exception as e:
                        if reused:
                            raise _Broken() from e
                        raise
                    run = _Run(cur, con, load, self.between)
                    status = plan(run)
                    cur.execute("COMMIT")
                finally:
                    cur.close()
            self._finish(con)
        except BaseException:
            con.close()
            raise
        if self._tracing:
            self.trace.append(load)
        return _Loaded(con, next(self._n), status, run.values)

    def _duckdb(self) -> duckdb.DuckDBPyConnection:
        config: dict[str, Any] = {"autoinstall_known_extensions": False, "autoload_known_extensions": False}
        con = duckdb.connect(config=config)
        con.execute("SET TimeZone='UTC'")
        if self.memory_limit:
            con.execute(f"SET memory_limit = '{self.memory_limit}'")
        if self.threads:
            con.execute(f"SET threads = {int(self.threads)}")
        return con

    @staticmethod
    def _finish(con: duckdb.DuckDBPyConnection) -> None:
        """Today's empty shape for every table the page didn't load, the views, and the loader's
        own tables dropped."""
        have = {r[0] for r in con.execute("SELECT table_name FROM duckdb_tables()").fetchall()}
        for table, ddl in legacy._EMPTY.items():
            if table not in have:
                con.execute(ddl)
        con.execute(legacy._PROBES_EMPTY)
        con.execute(legacy.VIEWS)
        for t in sorted(have):
            if t.startswith("_"):
                con.execute(f"DROP TABLE {t}")

    @staticmethod
    def _into(con: duckdb.DuckDBPyConnection, table: str, select: str, params: Sequence[object] = ()) -> None:
        """Today's table from select's rows (by name), in today's order."""
        con.execute(legacy._EMPTY[table])
        order = f" ORDER BY {_ORDER[table]}" if table in _ORDER else ""
        con.execute(f"INSERT INTO {table} BY NAME SELECT * FROM ({select}){order}", list(params))  # noqa: S608

    # --- /status -----------------------------------------------------------------------------------

    def _status(self, run: _Run) -> Status:
        t = run.fetch("status", _STATUS)
        first, earliest, newest, pods, any_key, retained = run.con.execute(f"SELECT * FROM {t}").fetchall()[0]  # noqa: S608
        found = [n for n in (newest, pods) if n]
        return Status(first or None, earliest or None, max(found) if found else None, retained if any_key else None)

    # --- session pages -----------------------------------------------------------------------------

    def _session(self, run: _Run, sid: str, sub: str, params: dict[str, list[str]]) -> None:
        con = run.con
        row = run.fetch("session", _SESSION, _sid(sid))
        if sub != ENTITY and not con.execute(f"SELECT count(*) FROM {row}").fetchall()[0][0]:  # noqa: S608
            return  # no such session: every page 404s on its row
        wants = SESSION_PAGES[sub]
        loaded: dict[str, str] = {}
        for name in wants:
            if name == "titles":
                loaded[name] = run.fetch(name, _TITLES, _sid(sid))
            elif name == "edges":
                loaded[name] = run.fetch(name, _EDGES, _sid(sid))
            elif name == "event_rows" and sub == "events":
                lo, hi = (_instant_ns(params[k][0] if params.get(k) else None) for k in ("from", "to"))
                ranged = [
                    (sid, "VARCHAR"),
                    (None if lo is None else lo - _WIDEN_NS, "BIGINT"),
                    (None if hi is None else hi + _WIDEN_NS, "BIGINT"),
                ]
                loaded[name] = run.fetch(name, _own(name, ranged=True), ranged)
            elif name in _SESSION_TABLES:
                loaded[name] = run.fetch(name, _own(name), _sid(sid))
            elif name == "session_request_rows":
                loaded[name] = run.fetch(name, _REQUEST_ROWS, _sid(sid))
            elif name == "other_keys":
                loaded[name] = run.fetch(name, _OTHER_KEYS, _sid(sid))
        # session_rows: today's columns, the labels other keys' rows give it.
        api_cols = ", ".join(_q(c) for c, _ in SHAPES["session_rows"])
        self._into(con, "session_rows", f"SELECT {api_cols} FROM {row}")  # noqa: S608
        if "titles" in loaded or "edges" in loaded:
            con.execute(
                f"""CREATE TABLE _lbl_sessions AS
                    SELECT session_id, start_ns, product, own_title, model_calls, call_accounts FROM {row}"""  # noqa: S608
            )
            con.execute(
                "CREATE TABLE _lbl_title_calls AS SELECT * FROM "  # noqa: S608 (fixed SQL)
                + loaded.get(
                    "titles",
                    "(SELECT ''::VARCHAR AS session_id, 0::BIGINT AS ts_ns, ''::VARCHAR AS call_id,"
                    " ''::VARCHAR AS account_id, ''::VARCHAR AS title WHERE false)",
                )  # noqa: S608
            )
            con.execute(
                "CREATE TABLE _lbl_edges AS SELECT * FROM "  # noqa: S608 (fixed SQL)
                + loaded.get(
                    "edges",
                    "(SELECT ''::VARCHAR AS parent_session_id, ''::VARCHAR AS child_session_id,"
                    " ''::VARCHAR AS kind, 0::BIGINT AS child_first_ns WHERE false)",
                )  # noqa: S608
            )
            con.execute(
                f"""UPDATE session_rows s SET children_json = l.children_json, label_text_alone = l.label_text_alone,
                       label_source_alone = l.label_source_alone
                    FROM ({_LABELS_SQL}) l WHERE l.session_id = s.session_id"""  # noqa: S608 (our SQL file)
            )
        for name in _SESSION_TABLES:
            if name not in loaded:
                continue
            if name == "prompt_rows":
                # session_last_ns is kept once per session (session_rows.prompt_last_ns, §3.3).
                cols = ", ".join(
                    "coalesce(s.prompt_last_ns, p.session_last_ns) AS session_last_ns"
                    if c == "session_last_ns"
                    else "p." + _q(c)
                    for c, _ in SHAPES[name]
                )
                select = f"SELECT {cols} FROM {loaded[name]} p LEFT JOIN {row} s USING (session_id)"  # noqa: S608
            else:
                select = f"SELECT * FROM {loaded[name]}"  # noqa: S608
            self._into(con, name, select)
        if "session_request_rows" in loaded:
            self._request_rows(con, sid, loaded)

    def _request_rows(self, con: duckdb.DuckDBPyConnection, sid: str, loaded: dict[str, str]) -> None:
        """The Requests tab: the session's rendered rows, and the rows other keys hold (stream_sql's
        session builder over the candidates), numbered together in today's order. Request ids
        differ between the two, so the rendered seq never decides between them."""
        schema = state_sql.STATE_SCHEMA
        other = loaded["other_keys"]
        row = "struct_pack(" + ", ".join(f"{_q(c)} := row_{c}" for c in stream_sql.COLUMNS) + ")"
        txs = f"(SELECT * FROM {other} WHERE src = 'tx')"  # noqa: S608
        builder = {
            "request_txs": f"SELECT {', '.join(_TX_COLUMNS)}, {row} AS row FROM {txs}",  # noqa: S608
            "dim_session_credential": f"SELECT DISTINCT $1::VARCHAR AS session_id, key_hash FROM {txs}",  # noqa: S608
            "batch_parts": "SELECT part_batch_id AS batch_id, part_key AS key, part_n AS n, part_min_name AS min_name,"  # noqa: S608
            f" part_first_ns AS first_ns, part_first_session AS first_session FROM {other} WHERE src = 'part'",  # noqa: S608
            "prompt_index": "SELECT num_session_id AS session_id, num_prompt_id AS prompt_id,"  # noqa: S608
            " num_prompt_index AS prompt_index, num_ts_ns AS ts_ns, num_event_key AS event_key"
            f" FROM {other} WHERE src = 'cc'",  # noqa: S608
            "codex_turns": "SELECT num_session_id AS session_id, num_prompt_id AS prompt_id, num_first_ns AS first_ns"  # noqa: S608
            f" FROM {other} WHERE src = 'codex'",  # noqa: S608
        }
        for t in (
            "request_txs",
            "dim_session_credential",
            "prompt_index",
            "codex_turns",
            "request_reports",
            "request_missed",
        ):
            con.execute(f"CREATE TABLE {t} ({schema[t]})")
            if t in builder:
                con.execute(f"INSERT INTO {t} BY NAME {builder[t]}", [sid] if "$1" in builder[t] else [])  # noqa: S608
        con.execute(f"CREATE TABLE batch_parts AS {builder['batch_parts']}")
        sql, p = stream_sql.session_request_rows_sql(con, sid, parts=True)
        con.execute(f"CREATE TABLE _other_rows AS SELECT * EXCLUDE (seq) FROM ({sql})", p)  # noqa: S608
        for t in ("request_txs", "dim_session_credential", "prompt_index", "codex_turns", "request_reports",
                  "request_missed", "batch_parts"):  # fmt: skip
            con.execute(f"DROP TABLE {t}")
        rendered = loaded["session_request_rows"]
        self._into(
            con,
            "session_request_rows",
            f"""SELECT * EXCLUDE (_o, _seq), row_number() OVER (ORDER BY ts_ns, request_id, _o, _seq) - 1 AS seq FROM (
                  SELECT * EXCLUDE (seq), 1 AS _o, seq AS _seq FROM {rendered}
                  UNION ALL BY NAME
                  SELECT *, $1::VARCHAR AS scope_session_id, 0 AS _o, -1 AS _seq FROM _other_rows)""",  # noqa: S608
            [sid],
        )

    # --- the drawer and one event (§3.7: the worker's rows) ---------------------------------------

    @staticmethod
    def _status_of_row(run: _Run, table: str) -> Status:
        sql = f"SELECT {', '.join(_STATUS_COLUMNS)} FROM {table} LIMIT 1"  # noqa: S608
        first, earliest, newest, pods, any_key, retained = run.con.execute(sql).fetchall()[0]
        found = [n for n in (newest, pods) if n]
        return Status(first or None, earliest or None, max(found) if found else None, retained if any_key else None)

    def _drawer(self, run: _Run, rid: str, sub: str) -> Status:
        if sub == "raw":
            t = run.fetch("drawer", _RAW, _sid(rid))
            run.values["drawer"] = run.con.execute(f"SELECT found FROM {t} LIMIT 1").fetchall()[0][0] > 0  # noqa: S608
            req = ", ".join(_q(c) for c, _ in SHAPES["requests"] if c not in _NOT_STORED)
            self._into(run.con, "requests", f"SELECT {req} FROM {t} WHERE call_id IS NOT NULL")  # noqa: S608
        else:
            t = run.fetch("drawer", _drawer_sql("details" if sub == "details" else "data"), _sid(rid))
            run.values["drawer"] = run.con.execute(f"SELECT data FROM {t}").fetchall()[0][0]  # noqa: S608
        return self._status_of_row(run, t)

    def _event(self, run: _Run, eid: str) -> Status:
        t = run.fetch("event", _EVENT, _sid(_event_id(eid)))
        run.values["event"] = run.con.execute(f"SELECT data, correlation_id, event_key FROM {t}").fetchall()[0]  # noqa: S608
        return self._status_of_row(run, t)

    # The drawer's endpoints read the worker's rows (routers/requests.py).
    precomputed = True

    def drawer_row(self) -> str | None:
        """/requests/{id}'s or /details' finished JSON (None: no such request)."""
        return cast(str | None, self._tables().values["drawer"])

    def drawer_found(self) -> bool:
        """/raw: whether the request has a row."""
        return bool(self._tables().values["drawer"])

    def event_row(self) -> tuple[str | None, str | None, str | None]:
        """/events/{id}: the event's JSON (but its raw row), its batch and its event key."""
        return cast(tuple[str | None, str | None, str | None], self._tables().values["event"])

    # --- tests: a statement's plan -----------------------------------------------------------------

    def explain(self, statement: Statement) -> list[str]:
        """statement's plan with sequential scans priced out (what an index can serve), on a connection
        of its own: its setting never reaches a pooled one."""
        with contextlib.closing(self.pool.open()) as conn:
            cur = conn.cursor()
            con = duckdb.connect()
            try:
                cur.execute("SET enable_seqscan = off")
                run = _Run(cur, con, Load("explain"), None)
                if statement.params:
                    t = run.fetch("plan", "EXPLAIN " + statement.sql, statement.params)
                else:  # a statement without parameters is read through COPY, which can't wrap EXPLAIN
                    cur.execute(_EXPLAIN_FN)
                    t = run.fetch("plan", "SELECT * FROM pg_temp.explain_plan($1)", [(statement.sql, "VARCHAR")])
                return [str(r[0]) for r in con.execute(f"SELECT * FROM {t}").fetchall()]  # noqa: S608
            finally:
                cur.close()
                con.close()

    # --- the Store interface the endpoints use -----------------------------------------------------

    def enter(self, loaded: object) -> contextvars.Token[_Loaded | None] | None:
        return None if loaded is None else _current.set(cast(_Loaded, loaded))

    def leave(self, token: contextvars.Token[_Loaded | None] | None) -> None:
        if token is not None:
            _current.reset(token)

    def _tables(self) -> _Loaded:
        loaded = _current.get()
        if loaded is None:
            raise RuntimeError("no tables loaded for this request (load_for didn't plan this path)")
        return loaded

    def maybe_ingest(self, min_interval_s: float) -> None:
        """Nothing to load ahead: each request reads its own snapshot."""

    @property
    def commit(self) -> object:
        return self._tables().n

    def _status_of(self) -> Status:
        status = self._tables().status
        if status is None:
            raise RuntimeError("this page's load didn't read /status's statement")
        return status

    def status_stats(self) -> tuple[int | None, int | None]:
        s = self._status_of()
        return s.first_request_ns, s.earliest_ns

    @property
    def newest_ns(self) -> int | None:
        return self._status_of().newest_ns

    @property
    def content_retained(self) -> bool | None:
        return self._status_of().content_retained

    def agent_calls_in(self, lo: int | None, hi: int | None) -> int:
        """/overview's requests: the Claude Code and Codex calls in [lo, hi], counted in the request's
        snapshot (pgstore_directory)."""
        from aintercept_api import pgstore_directory

        return pgstore_directory.agent_calls_in(self._tables().values, lo, hi)

    def memo(self, key: tuple[Any, ...], build: Callable[[], T]) -> T:
        loaded = self._tables()
        if key in loaded.memo:
            return cast(T, loaded.memo[key])
        value = build()
        loaded.memo[key] = value
        while len(loaded.memo) > legacy.MEMO_SIZE:
            loaded.memo.popitem(last=False)
        return value

    def query(self, sql: str, params: Sequence[object] | None = None) -> list[tuple[Any, ...]]:
        cur = self._tables().con.cursor()
        try:
            return cur.execute(sql, params or []).fetchall()
        finally:
            cur.close()

    def query_dicts(self, sql: str, params: Sequence[object] | None = None) -> list[dict[str, Any]]:
        cur = self._tables().con.cursor()
        try:
            cur.execute(sql, params or [])
            names = [d[0] for d in cur.description or []]
            return [dict(zip(names, r, strict=True)) for r in cur.fetchall()]
        finally:
            cur.close()

    # The raw view: the call's detail object (calls/<id>.ndjson.gz), written when the call ends.
    def _detail(self, correlation_id: str) -> list[bytes]:
        if self.source is None or not correlation_id or "/" in correlation_id:
            return []
        from aintercept_api.buckets import _get_if_exists

        return raw.lines(_get_if_exists(self.source, f"{self.prefix}calls/{correlation_id}.ndjson.gz"))

    def raw_rows(self, correlation_id: str) -> list[dict[str, Any]]:
        return legacy.rows_of(self._detail(correlation_id), correlation_id)

    def raw_event(self, correlation_id: str, event_key: str, source_file: str) -> dict[str, Any] | None:
        return legacy.event_row(self._detail(correlation_id), correlation_id, event_key)
