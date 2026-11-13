"""The test Postgres (docs/PIPELINE.md v7, Testing): a fresh database with the schema applied, per
test session (each xdist worker its own), on the server AINTERCEPT_TEST_PG names (an admin DSN: CI's
service container, or the container `make check` starts with scripts/test-pg.sh).

The schema is the workers' (`aintercept/internal/pg/schema.sql`), applied as `pg.Apply` applies it:
its statements in one transaction, then its hash into schema_version."""

import contextlib
import hashlib
import os
import pathlib
import re
import secrets
import urllib.parse
from collections.abc import Iterator
from typing import Any

import adbc_driver_postgresql.dbapi as pgdbapi
import duckdb

REPO = pathlib.Path(__file__).resolve().parents[2]
SCHEMA = REPO / "aintercept" / "internal" / "pg" / "schema.sql"


def admin_dsn() -> str | None:
    return os.environ.get("AINTERCEPT_TEST_PG") or None


def required() -> bool:
    """CI sets AINTERCEPT_TEST_PG_REQUIRED=1: a missing server fails the tests instead of skipping."""
    return os.environ.get("AINTERCEPT_TEST_PG_REQUIRED") == "1"


def with_database(dsn: str, name: str) -> str:
    u = urllib.parse.urlsplit(dsn)
    return urllib.parse.urlunsplit(u._replace(path="/" + name))


def connect(dsn: str, *, autocommit: bool = True) -> Any:
    return pgdbapi.connect(dsn, autocommit=autocommit)


def execute(conn: Any, *sql: str) -> None:
    """Runs statements that return no rows, closing their cursor (a connection with one open can't close)."""
    cur = conn.cursor()
    try:
        for s in sql:
            cur.execute(s)
    finally:
        cur.close()


def statements(sql: str) -> list[str]:
    """schema.sql's statements. ADBC prepares each statement, and a prepared statement is one
    command; the file is plain DDL (no function bodies), one statement per `;` at a line's end."""
    out = []
    for chunk in sql.split(";\n"):
        body = "\n".join(line for line in chunk.splitlines() if not line.lstrip().startswith("--")).strip()
        if body:
            out.append(body)
    return out


def schema_hash() -> str:
    return hashlib.sha256(SCHEMA.read_bytes()).hexdigest()


def apply_schema(dsn: str) -> None:
    digest = schema_hash()
    assert re.fullmatch(r"[0-9a-f]{64}", digest)
    conn = connect(dsn, autocommit=False)
    try:
        execute(conn, *statements(SCHEMA.read_text()), f"INSERT INTO schema_version (hash) VALUES ('{digest}')")  # noqa: S608 (a hex digest)
        conn.commit()
    finally:
        conn.close()


@contextlib.contextmanager
def fresh_database(admin: str) -> Iterator[str]:
    """A new database with the built-in C.UTF-8 locale and the schema applied, dropped afterwards;
    yields its DSN."""
    name = "t_" + secrets.token_hex(6)
    conn = connect(admin)
    try:
        locale = "LOCALE_PROVIDER builtin BUILTIN_LOCALE 'C.UTF-8'"
        execute(conn, f"CREATE DATABASE {name} TEMPLATE template0 ENCODING 'UTF8' {locale}")
        dsn = with_database(admin, name)
        apply_schema(dsn)
        yield dsn
    finally:
        execute(conn, f"DROP DATABASE IF EXISTS {name} WITH (FORCE)")
        conn.close()


def load(con: duckdb.DuckDBPyConnection, pg: Any, name: str, sql: str) -> duckdb.DuckDBPyRelation:
    """sql's rows from Postgres, as DuckDB table name: ADBC's Arrow stream (binary COPY), read by DuckDB
    directly (`__arrow_c_stream__`), with no pyarrow. The stream is read once, while its statement is
    open, so the rows are copied into the table."""
    cur = pg.cursor()
    try:
        cur.execute(sql)
        con.from_arrow(cur.fetch_arrow()).create(name)
    finally:
        cur.close()
    return con.table(name)
