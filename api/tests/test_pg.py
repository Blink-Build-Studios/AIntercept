"""Postgres integration (docs/PIPELINE.md v7, Testing; build step 1): the schema the workers apply,
the built-in C.UTF-8 locale against DuckDB (sort order and lower()), and every column type through
Postgres -> Arrow (ADBC, binary COPY) -> DuckDB, equal to the oracle's types."""

import importlib.util
import math
import struct
import unicodedata
from datetime import UTC, datetime

import duckdb
import pytest

from tests import pgdb
from tests.oracle import tables as oracle_tables


@pytest.fixture
def pg(pg_dsn):
    conn = pgdb.connect(pg_dsn)
    yield conn
    conn.close()


def one(pg, sql: str):
    """sql's one row, read through DuckDB (the driver's own fetchone needs pyarrow)."""
    return pgdb.load(duckdb.connect(), pg, "one", sql).fetchall()[0]


def test_schema_is_applied_with_its_hash(pg):
    assert one(pg, "SELECT array_agg(hash) FROM schema_version")[0] == [pgdb.schema_hash()]
    assert one(pg, "SELECT datlocprovider::text, datlocale FROM pg_database WHERE datname = current_database()") == (
        "b",
        "C.UTF-8",
    )
    assert pgdb.statements("-- a comment\nCREATE TABLE a (x int);\n\n-- b\nCREATE INDEX ON a (x);\n") == [
        "CREATE TABLE a (x int)",
        "CREATE INDEX ON a (x)",
    ]


def test_lower_matches_duckdb_on_every_code_point(pg):
    """An email group found in Postgres (`lower(email)`) is the group person_sql finds in DuckDB:
    lower() agrees on every code point (Unicode's simple case mapping in both)."""
    con = duckdb.connect()
    pgdb.load(
        con,
        pg,
        "cps",
        "SELECT c::bigint AS c, chr(c) AS x, lower(chr(c)) AS l FROM generate_series(1, 1114111) c "
        "WHERE c NOT BETWEEN 55296 AND 57343",
    )
    counts = con.execute("SELECT count(*), count(*) FILTER (WHERE l IS DISTINCT FROM lower(x)) FROM cps")
    n, differ = counts.fetchall()[0]
    assert n == 1114111 - 2048
    assigned = sum(1 for c in range(1, 0x110000) if unicodedata.category(chr(c)) not in ("Cn", "Cs"))
    assert assigned > 280_000  # every assigned code point is among them
    sample = con.execute("SELECT c, l, lower(x) FROM cps WHERE l IS DISTINCT FROM lower(x) LIMIT 20").fetchall()
    assert differ == 0, sample


EMAILS = [
    "Ada@Example.com",
    "ADA@EXAMPLE.COM",
    "İlkay@örnek.com.tr",
    "STRASSE@ẞ.de",
    "Σοφία@ΠΑΡΆΔΕΙΓΜΑ.gr",
    "ǅemal@example.ba",
    "ÅSA@EXAMPLE.SE",
    "Ωmega@ohm.example",
    "KELVIN@example.com",
    "Ǆ@example.com",
    "ΐ@example.com",
    "@example.com",
    "",
    "日本@例え.jp",
    "😀@emoji.example",
    "\ufffd@replaced.example",
]


def test_lower_and_order_match_duckdb_on_emails(pg):
    con = duckdb.connect()
    literal = ", ".join("'" + e.replace("'", "''") + "'" for e in EMAILS)
    pgdb.load(
        con,
        pg,
        "emails",
        f"SELECT e, lower(e) AS l, row_number() OVER (ORDER BY e) AS pg_rank,"  # noqa: S608 (literals above)
        f" row_number() OVER (ORDER BY lower(e), e) AS pg_lower_rank FROM unnest(ARRAY[{literal}]) e",
    )
    rows = con.execute(
        "SELECT e, l, lower(e), pg_rank, row_number() OVER (ORDER BY e), "
        "pg_lower_rank, row_number() OVER (ORDER BY lower(e), e) FROM emails"
    ).fetchall()
    assert len(rows) == len(EMAILS)
    for e, pg_lower, duck_lower, pg_rank, duck_rank, pg_lrank, duck_lrank in rows:
        assert pg_lower == duck_lower, e
        assert pg_rank == duck_rank, e  # code point order in both
        assert pg_lrank == duck_lrank, e
    # Python's sort is code point order too (and Go's is UTF-8 byte order: the same).
    assert [r[0] for r in sorted(rows, key=lambda r: r[3])] == sorted(EMAILS)


FLOATS = [0.0, -0.0, 0.1, -1.5, 5e-324, 2.2250738585072014e-308, 1.7976931348623157e308, math.inf, -math.inf, math.nan]


def bits(x: float) -> bytes:
    return struct.pack("<d", x)


def float_literal(x: float) -> str:
    """x exactly, as Postgres reads it: repr is the shortest decimal that rounds back to it."""
    if math.isnan(x):
        return "'NaN'"
    if math.isinf(x):
        return "'Infinity'" if x > 0 else "'-Infinity'"
    return f"'{x!r}'"


def test_every_column_type_round_trips_into_duckdb(pg_dsn):
    """Each type the schema uses, through ADBC's Arrow stream into DuckDB: the oracle's DuckDB types,
    floats bit for bit, NULL apart from an empty list and from an empty string."""
    conn = pgdb.connect(pg_dsn)
    pgdb.execute(
        conn,
        "CREATE TEMP TABLE every_type (i int, t text, b bigint, d double precision, ok boolean, ts text[], bs bigint[],"
        " ints integer[], raw bytea, j jsonb, stamp timestamptz)",
        "INSERT INTO every_type VALUES"
        " (0, 'plain', 9223372036854775807, NULL, true, ARRAY['a', 'b'], ARRAY[1, -2]::bigint[], ARRAY[3],"
        " '\\x00ff'::bytea,"
        " '{\"z\": 1, \"a\": [true]}', '2026-11-04T18:05:00Z'),"
        " (1, '', -9223372036854775808, NULL, false, '{}', '{}', '{}', ''::bytea, 'null', NULL),"
        " (2, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL),"
        " (3, 'é日本😀\ufffd', 0, NULL, NULL, ARRAY['', NULL, 'x'], ARRAY[NULL, 5]::bigint[], NULL, '\\x'::bytea,"
        " NULL, NULL)",
        "INSERT INTO every_type (i, d) SELECT 10 + n, d FROM unnest(ARRAY["  # noqa: S608 (float literals)
        + ", ".join(float_literal(x) for x in FLOATS)
        + "]::float8[]) WITH ORDINALITY AS u(d, n)",
    )
    con = duckdb.connect()
    rel = pgdb.load(con, conn, "every_type", "SELECT * FROM every_type ORDER BY i")
    assert dict(zip(rel.columns, map(str, rel.types), strict=True)) == {
        "i": "INTEGER",
        "t": "VARCHAR",
        "b": "BIGINT",
        "d": "DOUBLE",
        "ok": "BOOLEAN",
        "ts": "VARCHAR[]",
        "bs": "BIGINT[]",
        "ints": "INTEGER[]",
        "raw": "BLOB",
        "j": "JSON",
        "stamp": "TIMESTAMP WITH TIME ZONE",
    }
    read = con.execute("SELECT i, t, b, ok, ts, bs, ints, raw, j::VARCHAR, epoch_us(stamp) FROM every_type")
    rows = {r[0]: r for r in read.fetchall()}
    assert rows[0][1:] == (
        "plain",
        9223372036854775807,
        True,
        ["a", "b"],
        [1, -2],
        [3],
        b"\x00\xff",
        '{"a": [true], "z": 1}',  # jsonb's own key order: today's JSON columns are text
        int(datetime(2026, 11, 4, 18, 5, tzinfo=UTC).timestamp()) * 1_000_000,
    )
    assert rows[1][1:] == ("", -9223372036854775808, False, [], [], [], b"", "null", None)
    assert rows[2][1:] == (None,) * 9
    assert rows[3][1:5] == ("é日本😀\ufffd", 0, None, ["", None, "x"])
    assert rows[3][5] == [None, 5]
    got = dict(con.execute("SELECT i, d FROM every_type WHERE i >= 10").fetchall())
    for k, x in enumerate(FLOATS, start=11):
        assert bits(got[k]) == bits(x) or (math.isnan(x) and math.isnan(got[k])), (x, got[k])
    conn.close()


# The rendered tables' extra columns (§3.3), and the stored inputs' owner and row id: never part of
# an API table. The oracle's `filename` is its own (which file a row came from); env_json isn't stored.
EXTRAS = {"key", "id", "own_title", "prompt_last_ns", "key_day", "batch"}
ORACLE_ONLY = {"filename", "env_json"}


@pytest.mark.parametrize(
    "table",
    [
        "session_rows",
        "prompt_rows",
        "prompt_steps",
        "session_marks",
        "event_rows",
        "presence_segments",
        "session_tools",
        "call_tool_uses",
        "session_request_rows",
        "requests",
        "analytics",
    ],
)
def test_tables_load_with_the_oracles_columns_and_types(pg, table):
    """Each table the API loads, read from Postgres into DuckDB, has the oracle's columns and types."""
    con = duckdb.connect()
    rel = pgdb.load(con, pg, "pg_" + table, f"SELECT * FROM {table} LIMIT 0")  # noqa: S608 (a table name above)
    got = {c: str(t) for c, t in zip(rel.columns, rel.types, strict=True) if c not in EXTRAS}
    con.execute(oracle_tables._EMPTY[table])
    want = {c: t for c, t, *_ in con.execute(f"DESCRIBE {table}").fetchall() if c not in ORACLE_ONLY}  # noqa: S608
    assert got == want


def test_parameters_bind_from_duckdb_without_pyarrow(pg):
    """The API loader's path (§5) needs no pyarrow: statement parameters go in as a DuckDB relation's
    Arrow stream, rows come out as ADBC's. (The driver's own conversions of Python values and rows
    would need pyarrow, which isn't installed.)"""
    assert importlib.util.find_spec("pyarrow") is None
    con = duckdb.connect()
    params = duckdb.connect().sql("SELECT 's1\ufffd' AS sid, 5::BIGINT AS lo, ['a', 'b']::VARCHAR[] AS names")
    cur = pg.cursor()
    try:
        cur.execute("SELECT $1::text AS sid, $2::bigint + 1 AS lo, $3::text[] AS names", params)
        con.from_arrow(cur.fetch_arrow()).create("out")
    finally:
        cur.close()
    assert con.sql("SELECT * FROM out").fetchall() == [("s1\ufffd", 6, ["a", "b"])]
