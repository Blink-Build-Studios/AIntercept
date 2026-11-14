"""The API's Postgres reader (docs/PIPELINE.md v8 §5.1, §5.7; build step 1), on psycopg 3.

For each request a page reads in one snapshot:
1. it takes one pooled connection and opens one READ ONLY, REPEATABLE READ transaction at its first
   statement, so every statement after it sees the same commits;
2. it runs the page's statements in order, one round trip each, and gets their rows as Python values;
3. it commits and returns the connection when the page is done (a failed one is closed, never
   returned).

A pooled connection the server dropped (a restart, an idle timeout) fails at BEGIN, before any of the
request's statements ran: it's closed and the snapshot opens on a fresh one. Each request's
statements (their SQL, parameters, rows and time) can be traced, for the tests' statement counts and
plans.

v7's loader (pgstore.py, which reads Arrow into DuckDB through ADBC) uses this module's pool and
trace too, until build step 11 removes it.
"""

import contextlib
import threading
import time
from collections.abc import Callable, Iterator, Sequence
from dataclasses import dataclass, field
from typing import Any

import psycopg
from psycopg.rows import dict_row

BEGIN = "BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY"
# Per connection, once (the API login sets the same; a DSN with another login still gets them).
SESSION_SETTINGS = (
    "SET application_name = 'aintercept_api'",
    "SET statement_timeout = '30s'",
    "SET idle_in_transaction_session_timeout = '30s'",
)
POOL_SIZE = 8


class SchemaMismatch(RuntimeError):
    """The database's schema isn't this build's (pg.ErrSchemaMismatch, pg.ErrNoSchema): the API
    refuses to start."""


# --- tracing (tests: what a request read) -----------------------------------------------------------


@dataclass
class Statement:
    name: str
    sql: str
    params: Any  # psycopg's: a list or a dict; v7's loader's: (value, DuckDB type) pairs
    rows: int = 0
    ms: float = 0.0
    via: str = "psycopg"  # or "adbc": v7's loader


@dataclass
class Load:
    path: str
    begin: str = BEGIN
    connections: int = 0
    statements: list[Statement] = field(default_factory=list)


# --- the connection pool --------------------------------------------------------------------------


class Pool:
    """At most size connections in use; idle ones are reused, a failed one is closed, never returned."""

    def __init__(self, connect: Callable[[], Any], size: int) -> None:
        self.connect, self.size = connect, size
        self._idle: list[Any] = []
        self._slots = threading.BoundedSemaphore(size)
        self._lock = threading.Lock()
        self.opened = 0

    def open(self) -> Any:
        conn = self.connect()
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


class Broken(Exception):
    """A pooled connection that failed before its snapshot began (the server dropped it)."""


# --- one request's snapshot -------------------------------------------------------------------------


class Snapshot:
    """One request's statements, on one connection in one READ ONLY, REPEATABLE READ transaction,
    opened at the first statement (a page that 404s before reading anything takes no connection)."""

    def __init__(self, reader: "Reader", path: str) -> None:
        self.reader = reader
        self.load = Load(path)
        self._stack: contextlib.ExitStack | None = None
        self._conn: Any = None

    def _open(self) -> Any:
        if self._conn is not None:
            return self._conn
        for fresh in (False, True):
            stack = contextlib.ExitStack()
            conn, reused = stack.enter_context(self.reader.pool.connection(fresh=fresh))
            try:
                conn.execute(BEGIN)
            except Exception as e:
                stack.__exit__(type(e), e, e.__traceback__)  # the pool closes it
                if reused:
                    continue  # dropped while idle: nothing of the request ran on it yet
                raise
            self._stack, self._conn = stack, conn
            self.load.connections += 1
            return conn
        raise Broken("no connection")  # unreachable: a fresh connection either begins or raises

    def _run(self, name: str, sql: str, params: Sequence[Any] | dict[str, Any], *, dicts: bool) -> list[Any]:
        conn = self._open()
        t0 = time.perf_counter()
        with conn.cursor(row_factory=dict_row) if dicts else conn.cursor() as cur:
            cur.execute(sql, params)
            rows: list[Any] = cur.fetchall()
        kept = dict(params) if isinstance(params, dict) else list(params)
        self.load.statements.append(Statement(name, sql, kept, len(rows), (time.perf_counter() - t0) * 1000))
        if self.reader.between is not None:
            self.reader.between(len(self.load.statements) - 1)
        return rows

    def rows(self, name: str, sql: str, params: Sequence[Any] | dict[str, Any] = ()) -> list[tuple[Any, ...]]:
        """sql's rows, as tuples; params bind %s (or %(name)s) placeholders."""
        return self._run(name, sql, params, dicts=False)

    def dicts(self, name: str, sql: str, params: Sequence[Any] | dict[str, Any] = ()) -> list[dict[str, Any]]:
        """sql's rows, as dicts by column name."""
        return self._run(name, sql, params, dicts=True)

    def close(self, *, ok: bool) -> None:
        """Ends the transaction (COMMIT, or ROLLBACK after a failure: a 404 is one) and returns the
        connection; one that can't end it is closed, never returned."""
        if self._stack is None:
            return
        stack, self._stack, conn, self._conn = self._stack, None, self._conn, None
        try:
            conn.execute("COMMIT" if ok else "ROLLBACK")
        except BaseException as e:
            stack.__exit__(type(e), e, e.__traceback__)
            if ok:
                raise
            return
        stack.close()


# --- the reader -------------------------------------------------------------------------------------


class Reader:
    """psycopg connections to the API's Postgres: a pool, a snapshot per request, the trace."""

    def __init__(self, dsn: str, *, pool_size: int = POOL_SIZE, trace: list[Load] | None = None) -> None:
        self.dsn = dsn
        self.pool = Pool(self.connect, pool_size)
        self.trace = trace  # each request's Load, when tracing
        self.between: Callable[[int], None] | None = None  # tests: called after each statement

    def connect(self) -> psycopg.Connection[Any]:
        conn = psycopg.connect(self.dsn, autocommit=True)
        for s in SESSION_SETTINGS:
            conn.execute(s)
        return conn

    @contextlib.contextmanager
    def snapshot(self, path: str) -> Iterator[Snapshot]:
        snap = Snapshot(self, path)
        ok = False
        try:
            yield snap
            ok = True
        finally:
            snap.close(ok=ok)
            if self.trace is not None and snap.load.statements:
                self.trace.append(snap.load)

    def check_schema(self, want: str) -> None:
        """Refuse a database whose schema isn't this build's (pg.Check)."""
        with self.pool.connection() as (conn, _):
            (reg,) = conn.execute("SELECT to_regclass('schema_version')::text").fetchone() or (None,)
            if reg != "schema_version":
                raise SchemaMismatch("the database has no schema (the workers apply it)")
            found = [h for (h,) in conn.execute("SELECT hash FROM schema_version").fetchall()]
        if found != [want]:
            raise SchemaMismatch(
                f"the database's schema {found} isn't this build's {want} (a schema change needs a fresh database)"
            )

    def explain(self, statement: Statement) -> list[str]:
        """statement's plan with sequential scans priced out (what an index can serve), on a connection
        of its own: its setting never reaches a pooled one."""
        with contextlib.closing(self.connect()) as conn:
            conn.execute("SET enable_seqscan = off")
            return [str(r[0]) for r in conn.execute("EXPLAIN " + statement.sql, statement.params).fetchall()]

    def close(self) -> None:
        self.pool.close()
