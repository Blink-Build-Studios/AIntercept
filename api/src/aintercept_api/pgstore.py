"""The read path over Postgres (docs/PIPELINE.md v7 §5, v8 §5.7): each page's reader (pages.py).

/status, the request drawer and /events/{id} read the worker's finished rows through v8's reader
(PgPages, on the psycopg reader in reader.py): one statement each. Every other page is still v7's
loader's, below, until build steps 9 to 11 move it: its rows from Postgres into an in-memory DuckDB,
and today's endpoint SQL (pages.SqlPages) over them.

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

v7's loader serves the session pages, the directory (/people and /overview: pgstore_directory) and
the person pages (pgstore_person). A range the router filters by is snapped to whole minutes
(ranges.py); the loader pushes down that same snapped range. The API's default store;
AINTERCEPT_STORE=v1 selects V1Store instead.
"""

import contextlib
import contextvars
import importlib.resources
import itertools
import json
import pathlib
import time
import urllib.parse
from collections import OrderedDict
from collections.abc import Callable, Iterator, Sequence
from dataclasses import dataclass, field
from typing import Any, TypeVar, cast

import adbc_driver_postgresql.dbapi as pgdbapi
import duckdb

from aintercept_api import pages, ranges, raw, state_sql, stream_sql
from aintercept_api import request_detail as rd
from aintercept_api import store as legacy
from aintercept_api.models.requests import EventDetail, RequestDetail, RequestDetails, RequestRaw
from aintercept_api.reader import (
    BEGIN,
    POOL_SIZE,
    SESSION_SETTINGS,
    Load,
    Pool,
    Reader,
    SchemaMismatch,
    Snapshot,
    Statement,
)

T = TypeVar("T")

# The schema this build reads (aintercept/internal/pg/schema.sql's sha256, as pg.SchemaHash and
# schema_version record it). The API ships no copy of the file; tests/test_pg_store.py holds the
# two equal.
SCHEMA_HASH = "6c398638f39a0175e60b6f838d0ecd314465558c134514c36dc0ae06e63eedd5"

__all__ = ["BEGIN", "POOL_SIZE", "PgStore", "SchemaMismatch"]

_LABELS_SQL = importlib.resources.files("aintercept_api").joinpath("sql/labels.sql").read_text()


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
# drawer's lastDataAt is global, quirk 7), in one statement. v8 stores each object as a template
# (drawer_templates, shared by every row of its shape) and its values, joined by \x1f, and the
# statement rebuilds it with format(); a row's stepCount is its prompt's step count, kept once per
# (session, prompt) in drawer_step_counts so a new step doesn't rewrite its prompt's rows: the row
# holds it as its one `"stepCount":null`, filled in here.
_STEP_COUNT = """replace({}, '"stepCount":null', '"stepCount":' || coalesce(c.n::text, 'null'))"""


def _drawer_sql(column: str) -> str:
    value = f"format(t.body, VARIADIC string_to_array(r.{column}_vals, E'\\x1f'))"
    if column == "data":
        value = _STEP_COUNT.format(value)
    return f"""WITH status AS ({_STATUS})
SELECT status.*, d.data FROM status LEFT JOIN LATERAL (
    SELECT {value} AS data FROM drawer_requests r
    JOIN drawer_templates t ON t.key = r.key AND t.id = r.{column}_tpl
    LEFT JOIN drawer_step_counts c ON c.session_id = r.step_session AND c.prompt_id = r.step_prompt AND c.key = r.key
    WHERE r.request_id = %(id)s ORDER BY r.key LIMIT 1) d ON true"""  # noqa: S608


# /raw: the row's existence, and the call's requests row (the view when its detail object can't be
# read: a WebSocket turn's, or one not found); request_detail formats the detail object as today.
_REQUEST_COLUMNS = [c for c, _ in SHAPES["requests"] if c not in _NOT_STORED]
_RAW = f"""WITH status AS ({_STATUS}),
d AS (SELECT request_id FROM drawer_requests WHERE request_id = %(id)s LIMIT 1)
SELECT status.*, (SELECT count(*) FROM d) AS found, r.* FROM status LEFT JOIN (
    SELECT {columns("requests")} FROM requests
    WHERE call_id = replace(%(id)s, '~', '#') AND NOT coalesce(synthetic, false) AND EXISTS (SELECT 1 FROM d)) r
  ON true"""  # noqa: S608
# /events/{id}: the event's row (its raw row comes from the batch's detail object, as today's).
_EVENT = f"""WITH status AS ({_STATUS})
SELECT status.*, e.data, e.correlation_id, e.event_key FROM status LEFT JOIN LATERAL (
    SELECT data, correlation_id, event_key FROM drawer_events WHERE event_id = %(id)s ORDER BY key LIMIT 1) e
  ON true"""  # noqa: S608

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


@dataclass
class _Loaded:
    con: duckdb.DuckDBPyConnection
    n: int
    values: dict[str, Any] = field(default_factory=dict)  # what a plan computed for the endpoint
    memo: OrderedDict[tuple[Any, ...], Any] = field(default_factory=OrderedDict)


_current: contextvars.ContextVar[_Loaded | None] = contextvars.ContextVar("pg_tables", default=None)


class _Broken(Exception):
    """A pooled connection that failed before its snapshot began (the server dropped it)."""


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
        ms = (time.perf_counter() - t0) * 1000
        self.load.statements.append(Statement(name, sql, list(params), n, ms, via="adbc"))
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


# A range pushed down (the router's, snapped: ranges.of_query) is widened by this much each side.
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
        self.dsn = dsn
        self.trace: list[Load] = []
        # v8's reader (psycopg), and v7's loader's connections (ADBC) until build step 11 removes it.
        self.reader = Reader(dsn, pool_size=pool_size, trace=self.trace if trace else None)
        self.pool = Pool(self._adbc_connect, pool_size)
        self.source, self.prefix = source, prefix  # calls/<id>.ndjson.gz (the drawer's raw view)
        self.memory_limit, self.threads = memory_limit, threads
        self.data_dir = pathlib.Path(".")
        self.skipped: list[str] = []
        self._tracing = trace
        self.between: Callable[[int], None] | None = None  # tests: called after each loader statement
        self._n = itertools.count(1)
        self.reader.check_schema(SCHEMA_HASH)

    def _adbc_connect(self) -> Any:
        conn = pgdbapi.connect(self.dsn, autocommit=True)
        cur = conn.cursor()
        try:
            for s in SESSION_SETTINGS:
                cur.execute(s)
        finally:
            cur.close()
        return conn

    @property
    def opened(self) -> int:
        """Connections opened so far, by either driver."""
        return self.reader.pool.opened + self.pool.opened

    def close(self) -> None:
        self.reader.close()
        self.pool.close()

    # --- each page's reader (pages.py) -------------------------------------------------------------

    def page_reader(self, page: str, path: str) -> contextlib.AbstractContextManager[pages.Pages]:
        """v8's reader for the pages it serves (PgPages.PAGES), in one snapshot; v7's loader's tables
        (which load_for read in the request's snapshot) under today's SQL for the rest."""
        if page in PgPages.PAGES:
            return self._v8(path)
        return contextlib.nullcontext(pages.SqlPages(cast(legacy.Store, self)))

    @contextlib.contextmanager
    def _v8(self, path: str) -> Iterator[pages.Pages]:
        with self.reader.snapshot(path) as snap:
            yield cast(pages.Pages, PgPages(self, snap))

    # --- loading -----------------------------------------------------------------------------------

    def load_for(self, path: str) -> _Loaded | None:
        """The request's tables, read in one snapshot; None for a path that reads none."""
        bare, _, query = path.partition("?")
        if pages.page_of(bare) in PgPages.PAGES:
            return None  # v8's reader reads it, in the page's own snapshot
        params = urllib.parse.parse_qs(query)
        plan: Callable[[_Run], None]
        if bare.startswith("/api/v1/sessions/"):
            sid, _, sub = bare.removeprefix("/api/v1/sessions/").partition("/")
            sid = urllib.parse.unquote(sid)
            if sub not in SESSION_PAGES or sub == ENTITY:
                return None

            def plan(run: _Run) -> None:
                self._session(run, sid, sub, params)

        elif bare in ("/api/v1/people", "/api/v1/overview"):
            from aintercept_api import pgstore_directory

            def plan(run: _Run) -> None:
                pgstore_directory.load(run, overview=bare == "/api/v1/overview", params=params)

        elif bare.startswith("/api/v1/people/") or bare == "/api/v1/coverage":
            from aintercept_api import pgstore_person  # it builds on this module's loader

            page = pgstore_person.page_of(bare, params)
            if page is None:
                return None
            pid, sub = page
            lo, hi = ranges.of_query(params)

            def plan(run: _Run) -> None:
                pgstore_person.load(self, run, pid, sub, lo, hi)

        else:
            return None
        return self._read(path, plan)

    def session_entity(self, sid: str) -> _Loaded:
        """A whole session's tables, everything any of its pages reads (the table-level harness)."""

        def plan(run: _Run) -> None:
            self._session(run, sid, ENTITY, {})

        return self._read(f"entity:{sid}", plan)

    def _read(self, path: str, plan: Callable[[_Run], None]) -> _Loaded:
        try:
            return self._read_once(path, plan, fresh=False)
        except _Broken:
            return self._read_once(path, plan, fresh=True)

    def _read_once(self, path: str, plan: Callable[[_Run], None], *, fresh: bool) -> _Loaded:
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
                    plan(run)
                    cur.execute("COMMIT")
                finally:
                    cur.close()
            self._finish(con)
        except BaseException:
            con.close()
            raise
        if self._tracing:
            self.trace.append(load)
        return _Loaded(con, next(self._n), run.values)

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
                lo, hi = ranges.of_query(params)
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

    # --- tests: a statement's plan -----------------------------------------------------------------

    def explain(self, statement: Statement) -> list[str]:
        """statement's plan with sequential scans priced out (what an index can serve), on a connection
        of its own: its setting never reaches a pooled one."""
        if statement.via != "adbc":
            return self.reader.explain(statement)
        with contextlib.closing(self._adbc_connect()) as conn:
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


# --- v8's reader (docs/PIPELINE.md v8 §5.6, §5.7) -------------------------------------------------------


class PgPages:
    """PgStore's pages that read finished rows through the psycopg reader, in one snapshot: /status, the
    request drawer and /events/{id}, one statement each. A page moves here (build steps 9 to 11) by
    adding its name to PAGES and its method, with pages.SqlPages' signature."""

    PAGES = frozenset({"status", "request", "request/raw", "request/details", "event"})

    def __init__(self, store: PgStore, snap: Snapshot) -> None:
        self.store, self.snap = store, snap
        self._status: pages.StatusValues | None = None

    def _with_status(self, name: str, sql: str, params: dict[str, Any] | None = None) -> dict[str, Any]:
        """A statement's one row, which carries /status's values (the drawer's lastDataAt is global,
        quirk 7)."""
        (row,) = self.snap.dicts(name, sql, params or {})
        found = [n for n in (row["newest_row_ns"], row["pods_ns"]) if n]
        self._status = pages.StatusValues(
            row["first_request_ns"] or None,
            row["earliest_ns"] or None,
            max(found) if found else None,
            row["retained"] if row["any_key"] else None,
        )
        return row

    def status(self) -> pages.StatusValues:
        if self._status is None:
            self._with_status("status", _STATUS)
        return cast(pages.StatusValues, self._status)

    def newest_ns(self) -> int | None:
        return self.status().newest_ns

    def _drawer(self, rid: str, column: str) -> Any:
        data = self._with_status("drawer", _drawer_sql(column), {"id": rid})["data"]
        return None if data is None else json.loads(data)

    def request(self, rid: str) -> RequestDetail | None:
        d = self._drawer(rid, "data")
        return None if d is None else RequestDetail.model_validate(d)

    def request_details(self, rid: str) -> RequestDetails | None:
        d = self._drawer(rid, "details")
        return None if d is None else RequestDetails.model_validate(d)

    def request_raw(self, rid: str) -> RequestRaw | None:
        row = self._with_status("drawer", _RAW, {"id": rid})
        if not row["found"]:
            return None
        if rid.startswith(rd.REPORTED):
            return rd.reported_raw()
        with contextlib.closing(_OneCall(self.store, row)) as call:
            return rd.wire_raw(cast(legacy.Store, call), rid)

    def event(self, eid: str) -> EventDetail | None:
        """The worker's row of the event, and its raw row from the batch's detail object."""
        row = self._with_status("event", _EVENT, {"id": _event_id(eid)})
        cid, _, n = eid.rpartition(":")
        if not cid or not n.isdigit() or row["data"] is None:
            return None
        body = self.store.raw_event(row["correlation_id"], row["event_key"], "") or {}
        ev = body.get("client_event") if isinstance(body, dict) else None
        d = json.loads(row["data"])
        d["eventId"], d["raw"] = eid, rd._scrub(ev if isinstance(ev, dict) else {})
        return EventDetail.model_validate(d)


class _OneCall:
    """What /raw's formatter (request_detail.wire_raw) reads of one call: its detail object (S3), and
    its requests row as today's `calls` view has it (a WebSocket turn's view, or one whose object is
    missing), in a DuckDB of its own, made only when asked (build step 8 renders that view instead)."""

    def __init__(self, store: PgStore, row: dict[str, Any]) -> None:
        self.store, self.row = store, row
        self.commit = next(store._n)
        self._con: duckdb.DuckDBPyConnection | None = None

    def raw_rows(self, correlation_id: str) -> list[dict[str, Any]]:
        return self.store.raw_rows(correlation_id)

    def query(self, sql: str, params: Sequence[object] | None = None) -> list[tuple[Any, ...]]:
        if self._con is None:
            con = self.store._duckdb()
            con.execute(legacy._EMPTY["requests"])
            if self.row.get("call_id") is not None:
                cols = ", ".join(_q(c) for c in _REQUEST_COLUMNS)
                marks = ", ".join("?" for _ in _REQUEST_COLUMNS)
                con.execute(
                    f"INSERT INTO requests ({cols}) VALUES ({marks})",  # noqa: S608 (fixed names)
                    [self.row[c] for c in _REQUEST_COLUMNS],
                )
            PgStore._finish(con)
            self._con = con
        return self._con.execute(sql, params or []).fetchall()

    def close(self) -> None:
        if self._con is not None:
            self._con.close()
