"""The test Postgres (docs/PIPELINE.md §8): a fresh database with the schema applied, per
test session (each xdist worker its own), on the server AINTERCEPT_TEST_PG names (an admin DSN: CI's
service container, or the container `make check` starts with scripts/test-pg.sh).

The schema is the workers' (`aintercept/internal/pg/schema.sql`: what their migrations build, which
their tests hold equal), its statements in one transaction, then every migration recorded applied in
goose's table, as `pg.Apply` leaves it."""

import contextlib
import os
import pathlib
import re
import secrets
import urllib.parse
from collections.abc import Iterator
from typing import Any

import psycopg

REPO = pathlib.Path(__file__).resolve().parents[2]
SCHEMA = REPO / "aintercept" / "internal" / "pg" / "schema.sql"
MIGRATIONS = REPO / "aintercept" / "internal" / "pg" / "migrations"


def admin_dsn() -> str | None:
    return os.environ.get("AINTERCEPT_TEST_PG") or None


def required() -> bool:
    """CI sets AINTERCEPT_TEST_PG_REQUIRED=1: a missing server fails the tests instead of skipping."""
    return os.environ.get("AINTERCEPT_TEST_PG_REQUIRED") == "1"


def with_database(dsn: str, name: str) -> str:
    u = urllib.parse.urlsplit(dsn)
    return urllib.parse.urlunsplit(u._replace(path="/" + name))


def connect(dsn: str, *, autocommit: bool = True) -> Any:
    return psycopg.connect(dsn, autocommit=autocommit)


def execute(conn: Any, *sql: str) -> None:
    """Runs statements that return no rows."""
    for s in sql:
        conn.execute(s)


def statements(sql: str) -> list[str]:
    """schema.sql's statements: the file is plain DDL (no function bodies), one statement per `;` at a
    line's end."""
    out = []
    for chunk in sql.split(";\n"):
        body = "\n".join(line for line in chunk.splitlines() if not line.lstrip().startswith("--")).strip()
        if body:
            out.append(body)
    return out


def newest_migration() -> int:
    """The workers' newest migration's version (migrations/NNNN_<name>.sql)."""
    return max(int(m[1]) for p in MIGRATIONS.iterdir() if (m := re.fullmatch(r"(\d{4})_\w+\.sql", p.name)))


def apply_schema(dsn: str) -> None:
    """schema.sql at the newest migration's version."""
    v = newest_migration()
    conn = connect(dsn, autocommit=False)
    try:
        sql = f"INSERT INTO goose_db_version (version_id, is_applied) SELECT n, true FROM generate_series(0, {v}) n"  # noqa: S608 (an int)
        execute(conn, *statements(SCHEMA.read_text()), sql)
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
