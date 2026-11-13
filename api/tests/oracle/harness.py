"""The table-level harness (docs/PIPELINE.md, Testing): each entity's API-shaped tables, as the
oracle derives them from a dataset's v1 layout, and the comparison later steps hold the v6 views to.

An entity is what a page's id names:
- a session (`/sessions/{id}/*`): its session_rows row, prompts, steps, marks, events, presence,
  tools, call tool uses and its Requests tab's rows (session_request_rows), loaded as the session's
  endpoints load them (its account group, its own key's commit on top);
- a person (`/people/{id}/*`, any account id of a group, and "unattributed"): person_map,
  person_sessions, request_rows, person_last, the accounts' accounts/account_facts/account_devices
  rows, and the person's sessions' rows as a person page loads them;
- a request id (the drawer, `/requests/{id}*`): its keys' fact tables and the views over them
  (requests, pending, analytics, calls, agent_calls, cc_calls, hidden_calls, cc_events, codex_events);
- the directory from a day (`/people`, `/overview` with `from` on that day, or none): the tables
  the directory reads.

A later step plugs in by producing the same `Tables` for an entity from its own output (for a v6
view object, `read_view`; several objects, `concat`) and calling
`assert_same(oracle.tables(kind, id), got, entity=...)`. `seq` is compared by order: its values
are replaced by their rank within the session before comparing (prompt_steps by its prompt row's).
"""

import pathlib
import urllib.parse
from collections.abc import Iterable, Iterator, Sequence
from dataclasses import dataclass
from typing import Any, Protocol

import duckdb

from tests.oracle.store import LocalSource, OracleStore, Scope

# Each entity kind's tables: the rows of the loaded table that are the entity's ($id).
_SESSION_OWN = "session_id = $id"
_PERSONS_SESSIONS = "session_id IN (SELECT session_id FROM person_sessions WHERE person_id = $id)"
_PERSONS_ACCOUNTS = "account_id IN (SELECT account_id FROM person_map WHERE person_id = $id)"
SESSION_TABLES = {
    "session_rows": _SESSION_OWN,
    "prompt_rows": _SESSION_OWN,
    "prompt_steps": _SESSION_OWN,
    "session_marks": _SESSION_OWN,
    "event_rows": _SESSION_OWN,
    "presence_segments": _SESSION_OWN,
    "session_tools": _SESSION_OWN,
    "call_tool_uses": _SESSION_OWN,
    "session_request_rows": "scope_session_id = $id",
}
PERSON_TABLES = {
    "person_map": "person_id = $id",
    "person_sessions": "person_id = $id",
    "request_rows": "person_id = $id",
    "person_last": "person_id = $id",
    "accounts": _PERSONS_ACCOUNTS,
    "account_facts": _PERSONS_ACCOUNTS,
    "account_devices": _PERSONS_ACCOUNTS,
    "session_rows": _PERSONS_SESSIONS,
    "prompt_rows": _PERSONS_SESSIONS,
    "session_marks": _PERSONS_SESSIONS,
    "event_rows": _PERSONS_SESSIONS,
    "presence_segments": _PERSONS_SESSIONS,
    "call_tool_uses": _PERSONS_SESSIONS,
}
REQUEST_TABLES = {
    t: "true"
    for t in (
        "requests",
        "pending",
        "analytics",
        "calls",
        "agent_calls",
        "cc_calls",
        "hidden_calls",
        "cc_events",
        "codex_events",
    )
}
DIRECTORY_TABLES = {
    t: "true"
    for t in (
        "person_sessions",
        "person_map",
        "session_rows",
        "session_marks",
        "accounts",
        "account_facts",
        "account_devices",
    )
}
KINDS = {"session": SESSION_TABLES, "person": PERSON_TABLES, "request": REQUEST_TABLES, "directory": DIRECTORY_TABLES}


@dataclass(frozen=True)
class Table:
    columns: tuple[str, ...]
    rows: list[tuple[Any, ...]]


Tables = dict[str, Table]


class _Loads(Protocol):
    """A store whose request scopes load the API-shaped tables (the oracle's, or today's V1Store)."""

    def scope_for_path(self, path: str) -> Any: ...

    def _load(self, scope: Any, only: Sequence[str] | None = None) -> Any: ...


def _path(kind: str, ident: str | None) -> str:
    """The URL whose scope loads an entity's tables (its widest: a session's Requests tab)."""
    q = urllib.parse.quote(ident or "", safe="")
    return {
        "session": f"/api/v1/sessions/{q}/requests",
        "person": f"/api/v1/people/{q}",
        "request": f"/api/v1/requests/{q}",
        "directory": "/api/v1/people" + (f"?from={ident}T00:00:00Z" if ident else ""),
    }[kind]


def entity_tables(store: _Loads, kind: str, ident: str | None) -> Tables:
    """An entity's tables from store, loaded under the scope its pages load."""
    con = store._load(store.scope_for_path(_path(kind, ident))).con
    return extract(con, KINDS[kind], ident)


def extract(con: duckdb.DuckDBPyConnection, spec: dict[str, str], ident: str | None) -> Tables:
    """Each table's rows that spec names, in the table's stored order."""
    out: Tables = {}
    cur = con.cursor()
    try:
        for table, where in spec.items():
            sql = f"SELECT * FROM {table} WHERE {where}"  # noqa: S608 (fixed names and clauses)
            cur.execute(sql, {"id": ident} if "$id" in where else {})
            out[table] = Table(tuple(d[0] for d in cur.description or []), cur.fetchall())
    finally:
        cur.close()
    return out


@dataclass(frozen=True)
class Layout:
    """A dataset laid out by `aintercept v1layout` (tests/conftest.py lay_out): v1/ and the merge
    under root/prefix, the v6 views beside them under v2/."""

    root: pathlib.Path
    prefix: str = "sim/"
    raw: pathlib.Path | None = None

    @property
    def v2(self) -> pathlib.Path:
        return self.root / self.prefix / "v2"


class Oracle:
    """The oracle over one layout: its entities and each one's tables."""

    def __init__(self, layout: Layout) -> None:
        self.layout = layout
        self.store = OracleStore(LocalSource(layout.root), prefix=layout.prefix)
        self._memo: dict[tuple[str, str | None], Tables] = {}
        self._all: Tables | None = None
        self._requests: list[str] | None = None

    def close(self) -> None:
        self.store.close()

    def __enter__(self) -> "Oracle":
        return self

    def __exit__(self, *exc: object) -> None:
        self.close()

    def tables(self, kind: str, ident: str | None) -> Tables:
        if (kind, ident) not in self._memo:
            self._memo[kind, ident] = entity_tables(self.store, kind, ident)
        return self._memo[kind, ident]

    def session(self, sid: str) -> Tables:
        return self.tables("session", sid)

    def person(self, pid: str) -> Tables:
        return self.tables("person", pid)

    def request(self, rid: str) -> Tables:
        return self.tables("request", rid)

    def directory(self, day: str | None = None) -> Tables:
        return self.tables("directory", day)

    def _everything(self) -> Tables:
        if self._all is None:
            con = self.store._load(Scope.everything()).con
            spec = {"session_rows": "true", "person_sessions": "true", "person_map": "true"}
            self._all = extract(con, spec, None)
        return self._all

    def session_ids(self) -> list[str]:
        t = self._everything()
        return sorted({r[0] for r in t["session_rows"].rows} | {r[1] for r in t["person_sessions"].rows})

    def person_ids(self) -> list[str]:
        t = self._everything()
        return sorted({r[0] for r in t["person_map"].rows} | {r[0] for r in t["person_sessions"].rows})

    def request_ids(self) -> list[str]:
        """Every request id a person's or a session's request log lists."""
        if self._requests is None:
            found: set[str] = set()
            for pid in self.person_ids():
                t = self.person(pid)["request_rows"]
                found |= {r[t.columns.index("request_id")] for r in t.rows}
            for sid in self.session_ids():
                t = self.session(sid)["session_request_rows"]
                found |= {r[t.columns.index("request_id")] for r in t.rows}
            self._requests = sorted(found)
        return self._requests

    def request_groups(self) -> dict[tuple[str, ...], list[str]]:
        """The request ids by the keys their drawer reads: a request's tables are its keys' (one
        entry per key set stands for all of its ids)."""
        out: dict[tuple[str, ...], list[str]] = {}
        for rid in self.request_ids():
            out.setdefault(tuple(self.store.request_keys(rid)), []).append(rid)
        return out

    def request_cover(self) -> list[str]:
        """A request id per key set, fewest keys first, until every key's facts are in one: the
        drawer entities a quick run compares (every key's rows, without each big key again)."""
        out, seen = [], set[str]()
        for keys, rids in sorted(self.request_groups().items(), key=lambda kv: (len(kv[0]), kv[0])):
            if set(keys) - seen:
                out.append(rids[0])
                seen |= set(keys)
        return out

    def directory_days(self) -> list[str | None]:
        """No window, and a `from` on each day the merge has a partition for."""
        return [None, *self.store.merge_days()]

    def entities(self, requests: bool = True) -> Iterator[tuple[str, str | None]]:
        for sid in self.session_ids():
            yield "session", sid
        for pid in self.person_ids():
            yield "person", pid
        for day in self.directory_days():
            yield "directory", day
        if requests:
            for rids in self.request_groups().values():
                yield "request", rids[0]


# --- comparing ---------------------------------------------------------------------------------


def _partition(columns: tuple[str, ...]) -> int | None:
    for c in ("scope_session_id", "session_id"):
        if c in columns:
            return columns.index(c)
    return None


def _ranks(table: Table) -> dict[tuple[Any, Any], int]:
    """(session, seq) -> the seq's rank among the session's seqs."""
    i, p = table.columns.index("seq"), _partition(table.columns)
    by: dict[Any, set[Any]] = {}
    for r in table.rows:
        by.setdefault(None if p is None else r[p], set()).add(r[i])
    return {(s, v): n for s, vs in by.items() for n, v in enumerate(sorted(vs, key=lambda x: (x is None, x)))}


def _reseq(table: Table, ranks: dict[tuple[Any, Any], int]) -> Table:
    i, p = table.columns.index("seq"), _partition(table.columns)
    rows = []
    for r in table.rows:
        k = (None if p is None else r[p], r[i])
        rows.append((*r[:i], ranks.get(k, ("unmatched", r[i])), *r[i + 1 :]))
    return Table(table.columns, rows)


def normalize(tables: Tables) -> Tables:
    """`seq` by order: each value replaced by its rank within its session (prompt_steps' by its
    prompt row's, so a step still names its prompt). Other tables unchanged."""
    out = dict(tables)
    for name, t in tables.items():
        if "seq" not in t.columns:
            continue
        if name == "prompt_steps" and "prompt_rows" in tables:
            out[name] = _reseq(t, _ranks(tables["prompt_rows"]))
        else:
            out[name] = _reseq(t, _ranks(t))
    return out


def _key(row: tuple[Any, ...]) -> str:
    return repr(row)


def differences(
    want: Tables, got: Tables, *, only: Iterable[str] | None = None, unordered: Iterable[str] = ()
) -> list[str]:
    """What differs between two entities' tables (normalized), table by table: a missing table,
    other columns, rows only one side has, or the same rows in another order (tables in
    unordered are compared as multisets)."""
    want, got = normalize(want), normalize(got)
    loose = set(unordered)
    out = []
    for name in sorted(set(only) if only is not None else set(want) | set(got)):
        if name not in want or name not in got:
            out.append(f"{name}: only on the {'oracle' if name in want else 'other'} side")
            continue
        a, b = want[name], got[name]
        if a.columns != b.columns:
            out.append(f"{name}: columns {a.columns} != {b.columns}")
            continue
        if a.rows == b.rows:
            continue
        sa, sb = sorted(a.rows, key=_key), sorted(b.rows, key=_key)
        if sa == sb:
            if name not in loose:
                i = next(i for i, (x, y) in enumerate(zip(a.rows, b.rows, strict=True)) if x != y)
                out.append(f"{name}: same rows, other order (first at row {i}: {a.rows[i]!r} vs {b.rows[i]!r})")
            continue
        ra, rb = {_key(r) for r in a.rows}, {_key(r) for r in b.rows}
        only_a = [r for r in a.rows if _key(r) not in rb][:2]
        only_b = [r for r in b.rows if _key(r) not in ra][:2]
        out.append(
            f"{name}: {len(a.rows)} rows vs {len(b.rows)}; oracle only {only_a!r}; other only {only_b!r}"
            if only_a or only_b
            else f"{name}: {len(a.rows)} rows vs {len(b.rows)} (duplicates differ)"
        )
    return out


def assert_same(
    want: Tables,
    got: Tables,
    *,
    entity: str = "",
    only: Iterable[str] | None = None,
    unordered: Iterable[str] = (),
) -> None:
    """The oracle's tables (want) and another side's (got) are the same, row for row, seq by order."""
    diff = differences(want, got, only=only, unordered=unordered)
    assert not diff, f"{entity}: " + "; ".join(diff)


# --- v6 view objects (docs/PIPELINE.md §3.1) ---------------------------------------------------


def read_view(path: pathlib.Path | str) -> Tables:
    """A view object: one Parquet row, each table one column, a list of structs in its row type."""
    con = duckdb.connect()
    try:
        con.execute("SET threads = 1")
        cols = [r[0] for r in con.execute("DESCRIBE SELECT * FROM read_parquet(?)", [str(path)]).fetchall()]
        out: Tables = {}
        for c in cols:
            q = '"' + c.replace('"', '""') + '"'
            cur = con.execute(f"SELECT unnest({q}, max_depth := 2) FROM read_parquet(?)", [str(path)])  # noqa: S608
            out[c] = Table(tuple(d[0] for d in cur.description or []), cur.fetchall())
        return out
    finally:
        con.close()


def concat(*parts: Tables) -> Tables:
    """Several objects' tables as one entity's: each table's rows in the order given."""
    out: Tables = {}
    for part in parts:
        for name, t in part.items():
            if name in out:
                assert out[name].columns == t.columns, (name, out[name].columns, t.columns)
                out[name] = Table(t.columns, out[name].rows + t.rows)
            else:
                out[name] = Table(t.columns, list(t.rows))
    return out
