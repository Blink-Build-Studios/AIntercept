"""The oracle's read path: today's (v6 step 2) v1.V1Store and buckets.BucketStore, copied, over a
local v1 layout (`aintercept v1layout`): which files a request reads (the merge's index and day
partitions, a session's own key commit on top, the drawer's own keys), the newest row per (key, id)
(`read_newest`), the session group and the Codex title runs (`_session_group`, `_session_rows`), and
the derivation into the API-shaped tables (`_build_state`, over this package's copies of state_sql,
stream_sql, person_sql and sql/*.sql). Only what reads S3, Redis and the bucketed (pre-v1) layout
is left out.

It serves the endpoints' Store interface, so the API's tests run on it (`AINTERCEPT_TEST_STORE=oracle`)
and the parity gate pins "old" to it. Both it and the harness run DuckDB with threads=1.
"""

import contextlib
import contextvars
import json
import pathlib
import re
import threading
import time
import urllib.parse
from collections import OrderedDict
from collections.abc import Callable, Iterator, Sequence
from dataclasses import dataclass, field
from datetime import UTC, datetime
from typing import Any, TypeVar, cast

import duckdb

from aintercept_api import raw
from aintercept_api import store as endpoints_store
from aintercept_api.raw import RawSource
from tests.oracle import state_sql
from tests.oracle import tables as legacy
from tests.oracle.person_sql import DIMENSIONS, PERSON_TABLES

T = TypeVar("T")
V1 = "v1/"
_COMMIT_N = re.compile(r"/(\d{12})\.json$")
# The merge's per-key Claude Code and Codex call times (rollup.TableMergeAgentCalls).
AGENT_CALLS = "merge_agent_calls"


class LocalSource:
    """A directory laid out like the bucket (buckets.LocalSource)."""

    def __init__(self, root: pathlib.Path) -> None:
        self.root = pathlib.Path(root)

    def list_after(self, prefix: str, start_after: str) -> list[str]:
        base = self.root / prefix
        if not base.is_dir():
            return []
        keys = sorted(prefix + p.relative_to(base).as_posix() for p in base.rglob("*") if p.is_file())
        return [k for k in keys if k > start_after]

    def get(self, key: str) -> bytes:
        return (self.root / key).read_bytes()

    def url(self, key: str) -> str:
        return str(self.root / key)


def _get_if_exists(source: LocalSource, key: str) -> bytes | None:
    try:
        return source.get(key)
    except FileNotFoundError:
        return None


def connect(*, memory_limit: str | None = None, threads: int = 1) -> duckdb.DuckDBPyConnection:
    """A DuckDB connection as buckets.connect makes one (UTC, the memory cap), with one thread."""
    config: dict[str, Any] = {"autoinstall_known_extensions": False, "autoload_known_extensions": False}
    con = duckdb.connect(config=config)
    con.execute("SET TimeZone='UTC'")
    if memory_limit:
        if not re.fullmatch(r"\d+(\.\d+)?\s*[KMGT]i?B", memory_limit):
            raise ValueError(f"memory_limit {memory_limit!r}: use a size like '512MB'")
        con.execute(f"SET memory_limit = '{memory_limit}'")
    con.execute(f"SET threads = {int(threads)}")
    return con


def _in_list(keys: Sequence[str]) -> str:
    return ", ".join("'" + k.replace("'", "''") + "'" for k in keys)


def read_newest(
    con: duckdb.DuckDBPyConnection,
    urls: Sequence[str],
    *,
    keys: Sequence[str] | None = None,
    keep_keys: bool = False,
    hive: bool = False,
) -> duckdb.DuckDBPyRelation:
    """A table from its files (buckets.read_newest): the newest row of each (key, id) across the
    files in commit order, tombstones dropped; with keys, only those keys' rows."""
    where = "" if keys is None else (f"WHERE key IN ({_in_list(keys)})" if keys else "WHERE false")
    drop = "tomb, _file, _r" if keep_keys else "key, id, tomb, _file, _r"
    sql = f"""SELECT * EXCLUDE ({drop}) FROM (
    SELECT *, row_number() OVER (PARTITION BY key, id ORDER BY list_position(?::VARCHAR[], _file) DESC) AS _r
    FROM read_parquet(?::VARCHAR[], filename = '_file', union_by_name = true, hive_partitioning = {str(hive).lower()})
    {where}
) WHERE _r = 1 AND NOT coalesce(tomb, false)"""  # noqa: S608 (keys quoted; the rest fixed)
    return con.sql(sql, params=[list(urls), list(urls)])


# --- session_rows (buckets._session_rows and its inputs) -------------------------------------

_SESSION_SQL = (pathlib.Path(__file__).parent / "sql" / "session_rows.sql").read_text()
SESSION_INPUTS = state_sql.SESSION_INPUTS
# Each input restricted to one run's sessions (and, for the title calls, the accounts behind
# them), under the name the SQL reads.
_RESTRICT = {
    "session_rows": "CREATE OR REPLACE TEMP TABLE session_rows AS "
    "SELECT * FROM _s_session_rows WHERE session_id IN (SELECT unnest(?::VARCHAR[]))",
    "session_prompt_ids": "CREATE OR REPLACE TEMP TABLE session_prompt_ids AS "
    "SELECT * FROM _s_session_prompt_ids WHERE session_id IN (SELECT unnest(?::VARCHAR[]))",
    "session_devices": "CREATE OR REPLACE TEMP TABLE session_devices AS "
    "SELECT * FROM _s_session_devices WHERE session_id IN (SELECT unnest(?::VARCHAR[]))",
    "session_call_accounts": "CREATE OR REPLACE TEMP TABLE session_call_accounts AS "
    "SELECT * FROM _s_session_call_accounts WHERE session_id IN (SELECT unnest(?::VARCHAR[]))",
    "session_missed": "CREATE OR REPLACE TEMP TABLE session_missed AS "
    "SELECT * FROM _s_session_missed WHERE session_id IN (SELECT unnest(?::VARCHAR[]))",
    "session_edges": "CREATE OR REPLACE TEMP TABLE session_edges AS "
    "SELECT * FROM _s_session_edges WHERE parent_session_id IN (SELECT unnest(?::VARCHAR[]))",
    "session_title_calls": "CREATE OR REPLACE TEMP TABLE session_title_calls AS "
    "SELECT * FROM _s_session_title_calls WHERE account_id IN (SELECT unnest(?::VARCHAR[]))",
}
_DROP_RESTRICTED = {t: f"DROP TABLE IF EXISTS temp.{t}" for t in SESSION_INPUTS}  # noqa: S608 (fixed names)


@dataclass(frozen=True)
class TitleRun:
    """One run of session_rows' SQL: its sessions, and the accounts whose title calls it read."""

    sessions: frozenset[str]
    titles_accounts: frozenset[str]


def session_rows(con: duckdb.DuckDBPyConnection, sessions: frozenset[str] | set[str] | None) -> list[TitleRun]:
    """The API's session_rows from the per-session tables (buckets.BucketStore._session_rows): the
    SQL run once per group of sessions linked by their calls' accounts that has title calls, and
    once for the rest, with no title calls. Returns the runs."""
    have = {s for (s,) in con.execute("SELECT session_id FROM _s_session_rows").fetchall()}
    if sessions is not None:
        have &= set(sessions)
    rows = [r for r in con.execute("SELECT session_id, account_id FROM _s_session_call_accounts").fetchall()]
    titled = {a for (a,) in con.execute("SELECT DISTINCT account_id FROM _s_session_title_calls").fetchall()}
    parent = {s: s for s in have}

    def find(x: str) -> str:
        while parent[x] != x:
            parent[x] = parent[parent[x]]
            x = parent[x]
        return x

    by_account: dict[str, str] = {}
    for s_, a in rows:
        if s_ not in parent:
            continue
        if a in by_account:
            parent[find(s_)] = find(by_account[a])
        else:
            by_account[a] = s_
    groups: dict[str, set[str]] = {}
    for s_ in have:
        groups.setdefault(find(s_), set()).add(s_)
    runs, rest = [], set()
    for g in groups.values():
        accts = {a for s_, a in rows if s_ in g}
        if accts & titled:
            runs.append(TitleRun(frozenset(g), frozenset(accts & titled)))
        else:
            rest |= g
    if rest or not runs:
        runs.append(TitleRun(frozenset(rest), frozenset()))
    first = True
    for run in runs:
        ids = sorted(run.sessions)
        for t, sql in _RESTRICT.items():
            con.execute(sql, [sorted(run.titles_accounts) if t == "session_title_calls" else ids])
        out = con.sql(_SESSION_SQL)
        if first:
            out.create("_api_session_rows")
            first = False
        else:
            out.insert_into("_api_session_rows")
        for sql in _DROP_RESTRICTED.values():
            con.execute(sql)
    con.sql("SELECT * FROM _api_session_rows ORDER BY session_id").create("session_rows")
    con.execute("DROP TABLE _api_session_rows")
    return runs


def derive(
    con: duckdb.DuckDBPyConnection,
    sessions: frozenset[str] | set[str] | None,
    people: frozenset[str] | set[str] | None,
    *,
    partial: bool = False,
) -> list[TitleRun]:
    """The API-shaped tables from the stored ones (st_<table>, every STATE_TABLES table present):
    buckets.BucketStore._build_state's derivation. Returns session_rows' title runs."""
    state_sql.stored_views(con)
    for sql in PERSON_TABLES.values():
        con.execute(sql)
    if partial:  # a resolution's load: the dimensions and the person tables are enough
        return []
    runs = session_rows(con, sessions)
    state_sql.session_tables(con)
    state_sql.event_tables(con)
    state_sql.accounts(con)
    state_sql.request_tables(con, people, sessions)
    state_sql.fact_tables(con)
    # What the drawer, /status and the directory still read of the raw history: not in the
    # stream's layout, so empty here.
    have = {r[0] for r in con.execute("SELECT table_name FROM duckdb_tables()").fetchall()}
    for table, ddl in legacy._EMPTY.items():
        if table not in have:
            con.execute(ddl)
    if "probes" not in have:
        con.execute(legacy._PROBES_EMPTY)
    con.execute(legacy.VIEWS)
    return runs


def empty_state(con: duckdb.DuckDBPyConnection, table: str) -> None:
    """An empty stored table (st_<table>) in the processor's columns."""
    con.execute(f"CREATE TABLE st_{table} ({state_sql.STATE_SCHEMA[table]})")


# --- scopes (buckets.Scope) ------------------------------------------------------------------


@dataclass(frozen=True)
class Scope:
    """Which keys a request reads, per kind: None is every key."""

    sessions: frozenset[str] | set[str] | None = None
    people: frozenset[str] | set[str] | None = None
    accounts: frozenset[str] | set[str] | None = None
    skip: frozenset[str] = frozenset()
    own: frozenset[str] | None = None
    days_from: str | None = None

    @classmethod
    def everything(cls) -> "Scope":
        return cls()

    def reads(self, prefix: str) -> bool:
        return (
            prefix != "f:"
            or self.own is not None
            or (self.sessions is None and self.people is None and self.accounts is None)
        )

    def keys(self, prefix: str) -> list[str] | None:
        keys = {"s:": self.sessions, "p:": self.people, "a:": self.accounts}.get(prefix)
        return None if keys is None else sorted(prefix + k for k in keys)

    def signature(self) -> tuple[Any, ...]:
        def f(x: Any) -> Any:
            return None if x is None else tuple(sorted(x))

        return (
            f(self.sessions),
            f(self.people),
            f(self.accounts),
            tuple(sorted(self.skip)),
            f(self.own),
            self.days_from,
        )


_SESSION_PATH = re.compile(r"^/api/v1/sessions/([^/]+)")
_PERSON_PATH = re.compile(r"^/api/v1/people/([^/]+)")
_REQUEST_INPUTS = frozenset({"request_txs", "batch_members"})
_DIMENSION_INPUTS = frozenset(DIMENSIONS)
_EVERYTHING_PATHS = ("/api/v1/status", "/api/v1/overview", "/api/v1/people")
_EVERYTHING_PREFIXES = ("/api/v1/requests/", "/api/v1/events/")
_PERSON_SESSIONS = "SELECT DISTINCT session_id FROM person_sessions WHERE person_id = ? AND session_id IS NOT NULL"
_PERSON_ACCOUNTS = "SELECT DISTINCT account_id FROM person_map WHERE person_id = ? AND account_id IS NOT NULL"


def key_dir(prefix: str, key: str) -> str:
    """A key's folder, as rollup.KeyDir writes it (Go's url.PathEscape)."""
    return f"{prefix}{V1}keys/{urllib.parse.quote(key, safe='$&+:=@')}/"


def _escape(s: str) -> str:
    return urllib.parse.quote(s, safe="$&+:=@")


def _day_of(value: str | None) -> str | None:
    """A from= instant's UTC day (YYYY-MM-DD), or None for none or one that doesn't parse."""
    if not value:
        return None
    try:
        t = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        return None
    if t.tzinfo is None:
        t = t.replace(tzinfo=UTC)
    return t.astimezone(UTC).strftime("%Y-%m-%d")


@dataclass(frozen=True)
class Out:
    dir: str
    tables: tuple[str, ...]


@dataclass(frozen=True)
class Merge:
    n: int
    key: str
    days: dict[str, Out]
    index: Out
    newest_ns: int | None
    content_retained: bool | None = None
    first_request_ns: int | None = None
    earliest_ns: int | None = None

    def parts(self) -> list[tuple[str, Out]]:
        out = [("index", self.index)] if self.index.dir else []
        return out + [(d, self.days[d]) for d in sorted(self.days)]


def _out(o: dict[str, Any] | None) -> Out:
    o = o or {}
    return Out(dir=o.get("dir") or "", tables=tuple(o.get("tables") or ()))


@dataclass
class Loaded:
    con: duckdb.DuckDBPyConnection
    merge: Merge | None
    runs: list[TitleRun] = field(default_factory=list)
    memo: OrderedDict[tuple[Any, ...], Any] = field(default_factory=OrderedDict)


_current: contextvars.ContextVar[Loaded | None] = contextvars.ContextVar("oracle_tables", default=None)


class OracleStore:
    """Today's V1Store over a local v1 layout, with the oracle's derivation."""

    def __init__(
        self,
        source: LocalSource | pathlib.Path,
        cache_dir: Any = None,
        *,
        prefix: str = "",
        raw: RawSource | None = None,
        memory_limit: str | None = None,
        threads: int | None = None,
        loaded_max: int = 16,
        clock: Callable[[], float] = time.monotonic,
    ) -> None:
        self.source = source if isinstance(source, LocalSource) else LocalSource(source)
        self.prefix, self.raw, self.memory_limit = prefix, raw, memory_limit
        self.data_dir = pathlib.Path(".")
        self.skipped: list[str] = []
        self.loads = 0
        self.title_runs: list[TitleRun] = []
        self._loaded: OrderedDict[tuple[Any, ...], Loaded] = OrderedDict()
        self._loaded_max = loaded_max
        self._lock = threading.Lock()
        self._merge: Merge | None = None
        self._merge_read = False
        self._key_commits: dict[str, dict[str, Any] | None] = {}
        self._days: dict[str, str] | None = None

    # --- what's committed (a layout at rest: read once) ----------------------------------------

    def merge(self) -> Merge | None:
        with self._lock:
            if self._merge_read:
                return self._merge
        folder = f"{self.prefix}{V1}merge/commit/"
        keys = [k for k in self.source.list_after(folder, "") if _COMMIT_N.search(k)]
        merge = None
        if keys:
            key = max(keys)
            m = json.loads(self.source.get(key))
            merge = Merge(
                n=int(m["n"]),
                key=key,
                days={d: _out(o) for d, o in (m.get("days") or {}).items()},
                index=_out(m.get("index")),
                newest_ns=m.get("newest_ns") or None,
                content_retained=m.get("content_retained"),
                first_request_ns=m.get("first_request_ns") or None,
                earliest_ns=m.get("earliest_ns") or None,
            )
        with self._lock:
            self._merge, self._merge_read = merge, True
        return merge

    def _key_commit(self, key: str) -> dict[str, Any] | None:
        with self._lock:
            if key in self._key_commits:
                return self._key_commits[key]
        folder = key_dir(self.prefix, key) + "commit/"
        keys = [k for k in self.source.list_after(folder, "") if _COMMIT_N.search(k)]
        c = json.loads(self.source.get(max(keys))) if keys else None
        with self._lock:
            self._key_commits[key] = c
        return c

    def _key_days(self, merge: Merge) -> dict[str, str]:
        """Each merged key's day (merge_keys, in the index)."""
        with self._lock:
            if self._days is not None:
                return self._days
        days: dict[str, str] = {}
        if "merge_keys" in merge.index.tables:
            con = self._connect()
            try:
                url = self.source.url(self._merge_file(merge.index, "merge_keys"))
                days = dict(con.execute("SELECT key, day FROM read_parquet(?) WHERE NOT tomb", [url]).fetchall())
            finally:
                con.close()
        with self._lock:
            self._days = days
        return days

    def _merge_file(self, out: Out, table: str) -> str:
        return f"{self.prefix}{V1}merge/{out.dir}{table}.parquet"

    def merge_days(self) -> list[str]:
        """The merge's day partitions, oldest first."""
        m = self.merge()
        return sorted(m.days) if m else []

    # --- scopes --------------------------------------------------------------------------------

    def scope_for_path(self, path: str) -> Scope | None:
        bare, _, query = path.partition("?")
        if bare == "/api/v1/status":
            return None
        if bare.startswith("/api/v1/requests/"):
            rid = urllib.parse.unquote(bare.removeprefix("/api/v1/requests/").split("/", 1)[0])
            return Scope(own=frozenset(self.request_keys(rid)))
        if bare.startswith("/api/v1/events/"):
            eid = urllib.parse.unquote(bare.removeprefix("/api/v1/events/").split("/", 1)[0])
            return Scope(own=frozenset(self.call_keys(eid.rpartition(":")[0])))
        if bare in ("/api/v1/people", "/api/v1/overview"):
            day = _day_of(urllib.parse.parse_qs(query).get("from", [None])[0])
            return Scope(days_from=day, skip=state_sql.RAW_ONLY)
        if bare == "/api/v1/coverage":
            person = urllib.parse.parse_qs(query).get("personId")
            return self.resolve_person(person[0]) if person else None
        if m := _SESSION_PATH.match(bare):
            rest = bare[m.end() :]
            skip = frozenset() if rest in ("/requests", "/state") else _REQUEST_INPUTS | _DIMENSION_INPUTS
            return Scope(sessions={urllib.parse.unquote(m.group(1))}, skip=skip)
        if m := _PERSON_PATH.match(bare):
            return self.resolve_person(urllib.parse.unquote(m.group(1)))
        if bare in _EVERYTHING_PATHS or bare.startswith(_EVERYTHING_PREFIXES):
            return Scope.everything()
        return None

    def resolve_person(self, person_id: str) -> Scope:
        loaded = self._load(Scope.everything(), only=tuple(DIMENSIONS))
        cur = loaded.con.cursor()
        try:
            sessions = {r[0] for r in cur.execute(_PERSON_SESSIONS, [person_id]).fetchall()}
            accounts = {r[0] for r in cur.execute(_PERSON_ACCOUNTS, [person_id]).fetchall()}
        finally:
            cur.close()
        return Scope(sessions=sessions, people={person_id}, accounts=accounts)

    def call_keys(self, correlation_id: str) -> list[str]:
        if not correlation_id or "/" in correlation_id:
            return []
        folder = f"{self.prefix}calls/{correlation_id}.keys/"
        return sorted(urllib.parse.unquote(k[len(folder) :]) for k in self.source.list_after(folder, ""))

    def request_keys(self, request_id: str) -> list[str]:
        if request_id.startswith("reported:"):
            folder = f"{self.prefix}{V1}reported/{_escape(request_id.removeprefix('reported:'))}/"
            return sorted(urllib.parse.unquote(k[len(folder) :]) for k in self.source.list_after(folder, ""))
        return self.call_keys(request_id.split("~", 1)[0])

    def _session_group(self, sessions: frozenset[str] | set[str]) -> frozenset[str]:
        """The sessions linked to these through their calls' accounts (transitively)."""
        loaded = self._load(Scope.everything(), only=("session_call_accounts",))
        rows = loaded.con.execute("SELECT session_id, account_id FROM session_call_accounts").fetchall()
        group, accounts = set(sessions), set[str]()
        while True:
            more = {a for s, a in rows if s in group} - accounts
            if not more:
                return frozenset(group)
            accounts |= more
            group |= {s for s, a in rows if a in more}

    # --- loading (V1Store._load / _load_own) ---------------------------------------------------

    def _connect(self) -> duckdb.DuckDBPyConnection:
        return connect(memory_limit=self.memory_limit, threads=1)

    def load(self, scope: Scope) -> Loaded:
        """scope's tables, built (or reused)."""
        return self._load(scope)

    def _load(self, scope: Scope, only: Sequence[str] | None = None) -> Loaded:
        routing = state_sql.STATE_ROUTING
        names = [
            t
            for t in (list(only) if only is not None else list(routing))
            if t not in scope.skip and scope.reads(routing[t][0])
        ]
        single = scope.sessions is not None and len(scope.sessions) == 1 and scope.people is None
        if scope.sessions is not None and only is None:
            scope = Scope(
                sessions=self._session_group(scope.sessions),
                people=scope.people,
                accounts=scope.accounts,
                skip=scope.skip,
            )
        merge = self.merge()
        if scope.own is not None:
            return self._load_own(scope, names, merge)
        parts = merge.parts() if merge else []
        wanted: set[str] | None = None
        if scope.sessions is not None:
            days = self._key_days(merge) if merge else {}
            wanted = {"index"} | {days[k] for s in scope.sessions if (k := "s:" + s) in days}
        if scope.days_from is not None:
            parts = [(name, out) for name, out in parts if name == "index" or name >= scope.days_from]
        plan: dict[str, list[str]] = {}
        keys = {t: None if routing[t][0] == "*" else scope.keys(routing[t][0]) for t in names}
        for t in names:
            want = None if routing[t][0] == "*" else wanted
            plan[t] = [
                self._merge_file(out, t) for name, out in parts if t in out.tables and (want is None or name in want)
            ]
        overlay: list[str] = []
        if single and only is None and scope.sessions:
            for session in sorted(scope.sessions):
                key = "s:" + session
                c = self._key_commit(key)
                if c is None:
                    continue
                overlay.append(f"{key}@{c['n']}")
                for o in c.get("out") or []:
                    for t in o.get("tables") or []:
                        if t in plan:
                            plan[t].append(key_dir(self.prefix, key) + o["dir"] + t + ".parquet")
        sig = (scope.signature(), tuple(names), tuple(overlay), tuple((t, tuple(fs)) for t, fs in sorted(plan.items())))
        return self._cached(sig, lambda: self._build_state(plan, scope, merge, partial=only is not None, keys=keys))

    def _load_own(self, scope: Scope, names: list[str], merge: Merge | None) -> Loaded:
        """Exactly scope.own's keys' own tables, each key's newest commit (no merge partitions)."""
        plan: dict[str, list[str]] = {t: [] for t in names}
        overlay: list[str] = []
        for key in sorted(scope.own or ()):
            c = self._key_commit(key)
            if c is None:
                continue
            overlay.append(f"{key}@{c['n']}")
            for o in c.get("out") or []:
                for t in o.get("tables") or []:
                    if t in plan:
                        plan[t].append(key_dir(self.prefix, key) + o["dir"] + t + ".parquet")
        sig = (scope.signature(), tuple(names), tuple(overlay))
        keys: dict[str, list[str] | None] = {t: None for t in names}
        return self._cached(sig, lambda: self._build_state(plan, scope, merge, partial=False, keys=keys))

    def _cached(self, sig: tuple[Any, ...], build: Callable[[], Loaded]) -> Loaded:
        with self._lock:
            if sig in self._loaded:
                self._loaded.move_to_end(sig)
                return self._loaded[sig]
        loaded = build()
        with self._lock:
            self.loads += 1
            self._loaded[sig] = loaded
            while len(self._loaded) > self._loaded_max:
                _, old = self._loaded.popitem(last=False)
                old.con.close()
        return loaded

    def _build_state(
        self,
        plan: dict[str, list[str]],
        scope: Scope,
        merge: Merge | None,
        *,
        partial: bool,
        keys: dict[str, list[str] | None],
    ) -> Loaded:
        con = self._connect()
        for table in state_sql.STATE_TABLES:
            urls = [self.source.url(k) for k in plan.get(table, [])]
            if urls:
                read_newest(con, urls, keys=keys.get(table)).create("st_" + table)
            else:
                empty_state(con, table)
        runs = derive(con, scope.sessions, scope.people, partial=partial)
        if not partial:
            self.title_runs = runs
        return Loaded(con, merge, runs)

    # --- the endpoints' Store interface --------------------------------------------------------

    def maybe_ingest(self, min_interval_s: float) -> None:
        """A layout at rest: nothing to look for."""

    def load_for(self, path: str) -> object:
        scope = self.scope_for_path(path)
        return None if scope is None else self._load(scope)

    def enter(self, loaded: object) -> contextvars.Token[Loaded | None] | None:
        return None if loaded is None else _current.set(cast(Loaded, loaded))

    def leave(self, token: contextvars.Token[Loaded | None] | None) -> None:
        if token is not None:
            _current.reset(token)

    @contextlib.contextmanager
    def scope(self, scope: Scope) -> Iterator[None]:
        token = _current.set(self._load(scope))
        try:
            yield
        finally:
            _current.reset(token)

    def _tables(self) -> Loaded:
        loaded = _current.get()
        return loaded if loaded is not None else self._load(Scope.everything())

    @property
    def commit(self) -> object:
        m = self.merge()
        return ((0, m.n),) if m else ()

    @property
    def newest_ns(self) -> int | None:
        m = self.merge()
        return m.newest_ns if m else None

    @property
    def content_retained(self) -> bool | None:
        m = self.merge()
        return m.content_retained if m else None

    def status_stats(self) -> tuple[int | None, int | None]:
        m = self.merge()
        return (m.first_request_ns, m.earliest_ns) if m else (None, None)

    def agent_calls_in(self, lo: int | None, hi: int | None) -> int:
        """/overview's requests: the merge's per-key call times in the day partitions from lo's day on."""
        merge = self.merge()
        if merge is None:
            return 0
        start = None if lo is None else datetime.fromtimestamp(lo / 1e9, UTC).strftime("%Y-%m-%d")
        urls = [
            self.source.url(self._merge_file(out, AGENT_CALLS))
            for name, out in merge.parts()
            if name != "index" and AGENT_CALLS in out.tables and (start is None or name >= start)
        ]
        if not urls:
            return 0
        con = self._connect()
        try:
            (n,) = con.execute(
                """SELECT count(*) FROM read_parquet(?) WHERE NOT tomb
                   AND (?::BIGINT IS NULL OR ts_ns >= ?) AND (?::BIGINT IS NULL OR ts_ns <= ?)""",
                [urls, lo, lo, hi, hi],
            ).fetchone() or (0,)
        finally:
            con.close()
        return int(n)

    def memo(self, key: tuple[Any, ...], build: Callable[[], T]) -> T:
        loaded = self._tables()
        with self._lock:
            if key in loaded.memo:
                loaded.memo.move_to_end(key)
                return cast(T, loaded.memo[key])
        value = build()
        with self._lock:
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

    # The drawer's raw view: the call's detail object (calls/<id>.ndjson.gz), as V1Store reads it.
    def _detail(self, correlation_id: str) -> list[bytes]:
        return raw.lines(_get_if_exists(self.source, f"{self.prefix}calls/{correlation_id}.ndjson.gz"))

    def raw_rows(self, correlation_id: str) -> list[dict[str, Any]]:
        return endpoints_store.rows_of(self._detail(correlation_id), correlation_id)

    def raw_event(self, correlation_id: str, event_key: str, source_file: str) -> dict[str, Any] | None:
        return endpoints_store.event_row(self._detail(correlation_id), correlation_id, event_key)

    def close(self) -> None:
        with self._lock:
            for loaded in self._loaded.values():
                loaded.con.close()
            self._loaded.clear()
