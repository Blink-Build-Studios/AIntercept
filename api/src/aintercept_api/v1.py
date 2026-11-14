"""The read path over the v5 pipeline's folders (docs/PIPELINE.md §4).

Rollup workers commit each key's tables under `v1/keys/<key>/` (an output directory per run,
the newest row per (table, key, id) winning), and the merge folds the committed keys into
org-level partitions under `v1/merge/`: account keys' rows in the index, every other key's in
the day of its last activity, with `merge_keys` naming each key's day. Both use the stream
table codec, so a request reads them exactly as the tested stream-state SQL expects
(state_sql, buckets.BucketStore._build_state):

- a session's endpoints read the session's day partition, then its own key's newest commit
  on top (fresher than the merge by up to a merge period);
- a person's read their sessions' day partitions;
- the request drawer reads only its call's keys' own tables (the fact tables included): the keys
  come from the call's marks (calls/<id>.keys/<key>, written by ingest when a key first gets one
  of the call's records) or, for a reported call nothing captured, from v1/reported/<id>/<key>
  (written by the rollup that stores it). Its raw view reads the call's detail object
  (calls/<id>.ndjson.gz), written when the call ends;
- the directory reads the day partitions its window can touch (from its `from` day on: whatever
  overlaps the window was last active in it or after) and the index;
- /status reads no table: the merge commit's first request, earliest row, newest row and
  retention;
- tables that span keys (the dimensions, the transactions, the title calls, ...) are read from
  every partition.

DuckDB reads the objects in place (a local path, or S3 through httpfs): nothing is copied to
disk. Freshness is the newest merge commit, looked for at most once a second; /status's
newest time and retention mode are the merge commit's.
"""

import json
import re
import threading
import urllib.error
import urllib.parse
from collections.abc import Callable, Sequence
from dataclasses import dataclass
from datetime import UTC, datetime
from typing import Any

from aintercept_api import raw, state_sql
from aintercept_api import store as legacy
from aintercept_api.buckets import BucketStore, Commit, Scope, Source, _get_if_exists, _Loaded
from aintercept_api.raw import RawSource

V1 = "v1/"
_COMMIT_N = re.compile(r"/(\d{12})\.json$")


def key_dir(prefix: str, key: str) -> str:
    """A key's folder, as rollup.KeyDir writes it (Go's url.PathEscape)."""
    return f"{prefix}{V1}keys/{urllib.parse.quote(key, safe='$&+:=@')}/"


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


class _Newest:
    """The newest object under a commit/ folder, looked for at most every min_interval seconds,
    listing only after the newest one known."""

    def __init__(self, source: Source, folder: str, clock: Callable[[], float], min_interval: float = 1.0):
        self.source, self.folder, self.clock, self.min_interval = source, folder, clock, min_interval
        self.key: str | None = None
        self.body: bytes | None = None
        self._checked: float | None = None
        self._lock = threading.Lock()

    def latest(self) -> tuple[str, bytes] | None:
        with self._lock:
            now = self.clock()
            if self._checked is None or now - self._checked >= self.min_interval:
                self._checked = now
                newer = [k for k in self.source.list_after(self.folder, self.key or "") if _COMMIT_N.search(k)]
                if newer:
                    self.key = max(newer)
                    self.body = self.source.get(self.key)
            return None if self.key is None or self.body is None else (self.key, self.body)


class V1Store(BucketStore):
    """The store the endpoints query, over v1/keys and v1/merge."""

    def __init__(self, source: Source, cache_dir: Any, *, raw: RawSource | None = None, **kw: Any) -> None:
        super().__init__(source, cache_dir, raw=raw, **kw)
        self._merge_log = _Newest(source, f"{self.prefix}{V1}merge/commit/", self.clock)
        self._key_logs: dict[str, _Newest] = {}
        self._days: tuple[int, dict[str, str]] | None = None

    @property
    def stream_state(self) -> bool:
        return True

    # --- scopes ------------------------------------------------------------------------------

    def scope_for_path(self, path: str) -> Scope | None:
        """As BucketStore's, except: /status reads no table; the drawer reads its call's keys;
        the directory reads the day partitions its window can touch."""
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
        return super().scope_for_path(path)

    def call_keys(self, correlation_id: str) -> list[str]:
        """The keys a call's records went to: its marks, calls/<id>.keys/<escaped key>."""
        if not correlation_id or "/" in correlation_id:
            return []
        folder = f"{self.prefix}calls/{correlation_id}.keys/"
        return sorted(urllib.parse.unquote(k[len(folder) :]) for k in self.source.list_after(folder, ""))

    def request_keys(self, request_id: str) -> list[str]:
        """A request id's keys: a captured call's (its connection's, for a WebSocket turn,
        <connection>~<turn>), or a reported call's from v1/reported/<id>/<key>."""
        if request_id.startswith("reported:"):
            folder = f"{self.prefix}{V1}reported/{_escape(request_id.removeprefix('reported:'))}/"
            return sorted(urllib.parse.unquote(k[len(folder) :]) for k in self.source.list_after(folder, ""))
        return self.call_keys(request_id.split("~", 1)[0])

    def agent_calls_in(self, lo: int | None, hi: int | None) -> int:
        """/overview's requests: the Claude Code and Codex calls in [lo, hi], from the merge's
        per-key call times (merge_agent_calls) in the day partitions from lo's day on (a key is
        in the day of its last activity, so an earlier one has none in the window)."""
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

    # --- /status -------------------------------------------------------------------------------

    def status_stats(self) -> tuple[int | None, int | None]:
        """/status's first request and earliest row, from the merge commit."""
        m = self.merge()
        return (m.first_request_ns, m.earliest_ns) if m else (None, None)

    @property
    def newest_ns(self) -> int | None:
        """The merge's newest row."""
        m = self.merge()
        return (m.newest_ns or None) if m else None

    @property
    def content_retained(self) -> bool | None:
        m = self.merge()
        return m.content_retained if m else None

    # --- what's committed ----------------------------------------------------------------------

    def merge(self) -> Merge | None:
        got = self._merge_log.latest()
        if got is None:
            return None
        key, body = got
        m = json.loads(body)
        return Merge(
            n=int(m["n"]),
            key=key,
            days={d: _out(o) for d, o in (m.get("days") or {}).items()},
            index=_out(m.get("index")),
            newest_ns=m.get("newest_ns") or None,
            content_retained=m.get("content_retained"),
            first_request_ns=m.get("first_request_ns") or None,
            earliest_ns=m.get("earliest_ns") or None,
        )

    def _key_commit(self, key: str) -> dict[str, Any] | None:
        with self._lock:
            log = self._key_logs.get(key)
            if log is None:
                log = self._key_logs[key] = _Newest(self.source, key_dir(self.prefix, key) + "commit/", self.clock)
        got = log.latest()
        return None if got is None else json.loads(got[1])

    def _key_days(self, merge: Merge) -> dict[str, str]:
        """Each merged key's day (merge_keys, in the index): once per merge commit."""
        with self._lock:
            if self._days is not None and self._days[0] == merge.n:
                return self._days[1]
        days: dict[str, str] = {}
        if "merge_keys" in merge.index.tables:
            con = self._connect()
            try:
                url = self.source.url(self._merge_file(merge.index, "merge_keys"))
                days = dict(con.execute("SELECT key, day FROM read_parquet(?) WHERE NOT tomb", [url]).fetchall())
            finally:
                con.close()
        with self._lock:
            self._days = (merge.n, days)
        return days

    def _merge_file(self, out: Out, table: str) -> str:
        return f"{self.prefix}{V1}merge/{out.dir}{table}.parquet"

    # --- loading -------------------------------------------------------------------------------

    def _load(self, scope: Scope, only: Sequence[str] | None = None) -> _Loaded:
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
        # Within a partition, a session table's rows are filtered on key in the scan (the files
        # are in key order, so only the row groups that can hold the keys are read).
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
        sig = (
            scope.signature(),
            tuple(names),
            tuple(overlay),
            tuple((t, tuple(fs)) for t, fs in sorted(plan.items())),
        )
        with self._lock:
            if sig in self._loaded:
                self._loaded.move_to_end(sig)
                return self._loaded[sig]
        commits = (
            {
                0: Commit(
                    bucket=0,
                    seq=merge.n,
                    key=merge.key,
                    base={},
                    runs={},
                    newest_ns=merge.newest_ns,
                    content_retained=merge.content_retained,
                )
            }
            if merge
            else {}
        )
        loaded = _Loaded(self._build_state(plan, scope, partial=only is not None, keys=keys), commits)
        with self._lock:
            self.loads += 1
            self._loaded[sig] = loaded
            while len(self._loaded) > self._loaded_max:
                _, old = self._loaded.popitem(last=False)
                old.con.close()
        return loaded

    def _load_own(self, scope: Scope, names: list[str], merge: Merge | None) -> _Loaded:
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
        with self._lock:
            if sig in self._loaded:
                self._loaded.move_to_end(sig)
                return self._loaded[sig]
        commits = {0: self._merge_commit(merge)} if merge else {}
        keys: dict[str, list[str] | None] = {t: None for t in names}
        loaded = _Loaded(self._build_state(plan, scope, partial=False, keys=keys), commits)
        with self._lock:
            self.loads += 1
            self._loaded[sig] = loaded
            while len(self._loaded) > self._loaded_max:
                _, old = self._loaded.popitem(last=False)
                old.con.close()
        return loaded

    def _merge_commit(self, merge: Merge) -> Commit:
        return Commit(
            bucket=0,
            seq=merge.n,
            key=merge.key,
            base={},
            runs={},
            newest_ns=merge.newest_ns,
            content_retained=merge.content_retained,
        )

    # --- the raw view --------------------------------------------------------------------------

    def _detail(self, correlation_id: str) -> list[bytes]:
        """A call's rows as the observer wrote them: its detail object (calls/<id>.ndjson.gz),
        read from the bucket when the drawer opens."""
        return raw.lines(_get_if_exists(self.source, f"{self.prefix}calls/{correlation_id}.ndjson.gz"))

    def raw_rows(self, correlation_id: str) -> list[dict[str, Any]]:
        return legacy.rows_of(self._detail(correlation_id), correlation_id)

    def raw_event(self, correlation_id: str, event_key: str, source_file: str) -> dict[str, Any] | None:
        return legacy.event_row(self._detail(correlation_id), correlation_id, event_key)


# The merge's per-key Claude Code and Codex call times (rollup.TableMergeAgentCalls).
AGENT_CALLS = "merge_agent_calls"


def _escape(s: str) -> str:
    """As Go's url.PathEscape names a path segment (rollup.ReportedMark)."""
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
