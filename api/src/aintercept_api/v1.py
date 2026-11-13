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
- the directory, /status and the drawer read every partition;
- tables that span keys (the dimensions, the transactions, the title calls, ...) are read from
  every partition.

Nothing is copied but the immutable objects a request reads; freshness is the newest merge
commit, looked for at most once a second.
"""

import json
import re
import threading
import urllib.error
import urllib.parse
from collections.abc import Callable, Sequence
from dataclasses import dataclass
from typing import Any

import duckdb

from aintercept_api import state_sql
from aintercept_api.buckets import BucketStore, Commit, Scope, Source, _Loaded
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
            path = str(self.cache.path(self._merge_file(merge.index, "merge_keys")))
            rows = duckdb.execute(
                "SELECT key, day FROM read_parquet(?) WHERE NOT tomb", [path]
            ).fetchall()
            days = {k: d for k, d in rows}
        with self._lock:
            self._days = (merge.n, days)
        return days

    def _merge_file(self, out: Out, table: str) -> str:
        return f"{self.prefix}{V1}merge/{out.dir}{table}.parquet"

    # --- loading -------------------------------------------------------------------------------

    def _load(self, scope: Scope, only: Sequence[str] | None = None) -> _Loaded:
        routing = state_sql.STATE_ROUTING
        names = list(only) if only is not None else list(routing)
        single = scope.sessions is not None and len(scope.sessions) == 1 and scope.people is None
        if scope.sessions is not None and only is None:
            scope = Scope(sessions=self._session_group(scope.sessions), people=scope.people, accounts=scope.accounts)
        merge = self.merge()
        parts = merge.parts() if merge else []
        days = self._key_days(merge) if merge else {}
        wanted: set[str] | None = None
        if scope.sessions is not None:
            wanted = {"index"} | {days[k] for s in scope.sessions if (k := "s:" + s) in days}
        plan: dict[str, list[str]] = {}
        for t in names:
            every = routing[t][0] == "*" or wanted is None
            plan[t] = [
                self._merge_file(out, t) for name, out in parts if t in out.tables and (every or name in wanted)
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
            {0: Commit(bucket=0, seq=merge.n, key=merge.key, base={}, runs={}, newest_ns=merge.newest_ns)}
            if merge
            else {}
        )
        loaded = _Loaded(self._build_state(plan, scope, partial=only is not None), commits)
        with self._lock:
            self.loads += 1
            self._loaded[sig] = loaded
            while len(self._loaded) > self._loaded_max:
                _, old = self._loaded.popitem(last=False)
                old.con.close()
        return loaded


class SourceRaw:
    """The raw capture read straight from the object store (the drawer's raw view)."""

    def __init__(self, source: Source) -> None:
        self.source = source

    def folder(self, folder: str) -> list[str]:
        names = [k[len(folder) :] for k in self.source.list_after(folder, "")]
        return sorted(n for n in names if "/" not in n and n.endswith(".ndjson.gz"))

    def read(self, key: str) -> bytes | None:
        try:
            return self.source.get(key)
        except FileNotFoundError:
            return None
        except urllib.error.HTTPError as e:
            if e.code == 404:
                return None
            raise
