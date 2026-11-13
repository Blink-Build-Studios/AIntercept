"""The stateless read path (docs/STREAMING_PIPELINE.md §5): tables read straight from the
stream processor's bucketed output, with nothing held but a cache.

The processor (§4) owns each key bucket in turn and commits it as a create-only log,
`stream/v1/buckets/NN/commit-<seq>.json`. A commit names, per table, the bucket's tiered
base (its segments oldest first; segments may overlap, so their order matters) and the run
files written since, oldest first. Every row carries the key whose
state produced it, its id within the table, and a tombstone flag; the newest file's row
for a (key, id) wins, and a tombstone removes it.

A request reads only what it needs. `scope(...)` fixes, for the request, which buckets
each table comes from (a session's, a person's sessions' and accounts', or every bucket
for the directory) and the commit of each; the tables are built from those files once
per commit and reused by every request that wants the same ones. Objects are immutable,
so the local cache never goes stale; freshness is listing each bucket's log for newer
commits, at most once a second per bucket.

Routing is the processor's: a key's bucket is the top bits of its FNV-1a 64 hash
(aintercept/internal/records/route.go), pinned on both sides by the same test vectors.
"""

import contextlib
import contextvars
import datetime as dt
import hashlib
import hmac
import importlib.resources
import json
import os
import pathlib
import re
import tempfile
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
import xml.etree.ElementTree as ET  # noqa: S405 (S3's own listing responses, over TLS)
from collections import OrderedDict
from collections.abc import Callable, Iterator, Sequence
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass, field
from typing import Any, Protocol, TypeVar, cast

import duckdb

from aintercept_api import state_sql
from aintercept_api import store as legacy
from aintercept_api.person_sql import DIMENSIONS, PERSON_TABLES
from aintercept_api.raw import RawSource

T = TypeVar("T")
BUCKET_BITS = 6
BUCKETS = 1 << BUCKET_BITS
ROOT = "stream/v1/"
_FNV_OFFSET = 0xCBF29CE484222325
_FNV_PRIME = 0x100000001B3
_MASK = (1 << 64) - 1


def bucket_of(key: str, bits: int = BUCKET_BITS) -> int:
    """A routing key's bucket: the top `bits` of its FNV-1a 64-bit hash (records.BucketOf)."""
    h = _FNV_OFFSET
    for b in key.encode():
        h = ((h ^ b) * _FNV_PRIME) & _MASK
    return h >> (64 - bits)


def connect(
    source: "Source", *, memory_limit: str | None = None, threads: int | None = None
) -> duckdb.DuckDBPyConnection:
    """A DuckDB connection that reads source's objects: never downloads an extension (httpfs is
    in the image, under AINTERCEPT_DUCKDB_EXTENSIONS), UTC, the memory and thread caps."""
    config: dict[str, Any] = {"autoinstall_known_extensions": False, "autoload_known_extensions": False}
    if os.environ.get("AINTERCEPT_DUCKDB_EXTENSIONS"):
        config["extension_directory"] = os.environ["AINTERCEPT_DUCKDB_EXTENSIONS"]
    con = duckdb.connect(config=config)
    con.execute("SET TimeZone='UTC'")
    if memory_limit:
        if not re.fullmatch(r"\d+(\.\d+)?\s*[KMGT]i?B", memory_limit):
            raise ValueError(f"memory_limit {memory_limit!r}: use a size like '512MB'")
        con.execute(f"SET memory_limit = '{memory_limit}'")
    if threads:
        con.execute(f"SET threads = {int(threads)}")
    source.configure(con)
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
    """A table from its files (CONTRACT.md): the newest row of each (key, id) across the files
    in commit order, tombstones dropped. With keys, one query that filters on key inside the
    scan, so DuckDB reads only the row groups (by range request, over S3) whose key statistics
    can hold them."""
    where = "" if keys is None else (f"WHERE key IN ({_in_list(keys)})" if keys else "WHERE false")
    drop = "tomb, _file, _r" if keep_keys else "key, id, tomb, _file, _r"
    sql = f"""SELECT * EXCLUDE ({drop}) FROM (
    SELECT *, row_number() OVER (PARTITION BY key, id ORDER BY list_position(?::VARCHAR[], _file) DESC) AS _r
    FROM read_parquet(?::VARCHAR[], filename = '_file', union_by_name = true, hive_partitioning = {str(hive).lower()})
    {where}
) WHERE _r = 1 AND NOT coalesce(tomb, false)"""  # noqa: S608 (keys quoted; the rest fixed)
    return con.sql(sql, params=[list(urls), list(urls)])


# Each table's routing: the key prefix and the column whose value follows it. "s:" tables are a
# session's, "p:" a person's, "a:" an account's; "*" tables are read whole, from every bucket
# (the raw fact tables and snapshots the request drawer and /status still use: the inventory in
# the spec, §2.3), so their key doesn't matter here.
ROUTING: dict[str, tuple[str, str]] = {
    "prompt_rows": ("s:", "session_id"),
    "prompt_steps": ("s:", "session_id"),
    "session_rows": ("s:", "session_id"),
    "session_marks": ("s:", "session_id"),
    "presence_segments": ("s:", "session_id"),
    "event_rows": ("s:", "session_id"),
    "session_tools": ("s:", "session_id"),
    "session_request_rows": ("s:", "scope_session_id"),
    "call_tool_uses": ("s:", "session_id"),
    "request_rows": ("p:", "person_id"),
    "person_last": ("p:", "person_id"),
    "person_map": ("p:", "person_id"),
    "person_sessions": ("p:", "person_id"),
    "account_facts": ("a:", "account_id"),
    "account_devices": ("a:", "account_id"),
    "requests": ("*", ""),
    "analytics": ("*", ""),
    "ledger": ("*", ""),
    "probes": ("*", ""),
    "sessions": ("*", ""),
    "prompts": ("*", ""),
    "person_devices": ("*", ""),
    "people": ("*", ""),
    "pending": ("*", ""),
    "accounts": ("*", ""),
    # The dimension tables (§4.6): small, read whole; the person tables are built from them.
    **{t: ("*", "") for t in DIMENSIONS},
    # session_rows' inputs (sql/session_rows.sql): a session's own rows, and the small tables
    # that link sessions (calls' accounts, title calls, lineage edges), read whole.
    "session_prompt_ids": ("s:", "session_id"),
    "session_devices": ("s:", "session_id"),
    "session_missed": ("s:", "session_id"),
    "session_call_accounts": ("*", ""),
    "session_title_calls": ("*", ""),
    "session_edges": ("*", ""),
}

# The processor's per-session tables session_rows is rebuilt from: when they're in the layout,
# they're loaded as _s_<table> and the API's session_rows is the SQL over them.
SESSION_INPUTS = (
    "session_rows",
    "session_prompt_ids",
    "session_devices",
    "session_call_accounts",
    "session_missed",
    "session_edges",
    "session_title_calls",
)
_SESSION_SQL = importlib.resources.files("aintercept_api").joinpath("sql/session_rows.sql").read_text()
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
# The small inputs, when a layout has none of their rows.
_SESSION_EMPTY = {
    "session_prompt_ids": "CREATE TABLE _s_session_prompt_ids (session_id VARCHAR, prompt_id VARCHAR)",
    "session_devices": "CREATE TABLE _s_session_devices (session_id VARCHAR, device_id VARCHAR)",
    "session_call_accounts": "CREATE TABLE _s_session_call_accounts (session_id VARCHAR, account_id VARCHAR)",
    "session_missed": "CREATE TABLE _s_session_missed (session_id VARCHAR, ts_ns BIGINT, event_key VARCHAR)",
    "session_edges": "CREATE TABLE _s_session_edges (parent_session_id VARCHAR, child_session_id VARCHAR, "
    "kind VARCHAR, child_first_ns BIGINT)",
    "session_title_calls": "CREATE TABLE _s_session_title_calls (session_id VARCHAR, ts_ns BIGINT, call_id VARCHAR, "
    "account_id VARCHAR, title VARCHAR)",
}


@dataclass(frozen=True)
class TitleRun:
    """One run of session_rows' SQL: its sessions, and the accounts whose title calls it read."""

    sessions: frozenset[str]
    titles_accounts: frozenset[str]


_PERSON_RESOLUTION = ("person_sessions", "person_map")

_PERSON_SESSIONS = "SELECT DISTINCT session_id FROM person_sessions WHERE person_id = ? AND session_id IS NOT NULL"
_PERSON_ACCOUNTS = "SELECT DISTINCT account_id FROM person_map WHERE person_id = ? AND account_id IS NOT NULL"


# --- object sources --------------------------------------------------------------------------


class Source(Protocol):
    def list_after(self, prefix: str, start_after: str) -> list[str]:
        """Keys under prefix that sort after start_after, in order."""
        ...

    def get(self, key: str) -> bytes: ...

    def folders(self, prefix: str) -> list[str]:
        """The names of the "folders" directly under prefix (S3 common prefixes)."""
        ...

    def url(self, key: str) -> str:
        """The object as DuckDB reads it: a local path, or s3://bucket/key through httpfs."""
        ...

    def configure(self, con: duckdb.DuckDBPyConnection) -> None:
        """Ready con to read this source's urls (httpfs and its credentials, for S3)."""
        ...


class LocalSource:
    """A directory laid out like the bucket (tests, a local stack)."""

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

    def folders(self, prefix: str) -> list[str]:
        base = self.root / prefix
        return sorted(p.name for p in base.iterdir() if p.is_dir()) if base.is_dir() else []

    def url(self, key: str) -> str:
        return str(self.root / key)

    def configure(self, con: duckdb.DuckDBPyConnection) -> None:
        pass


class S3Source:
    """An S3-compatible bucket, read with SigV4-signed GETs (no SDK: two calls, ListObjectsV2
    and GetObject). Credentials from AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY (or the
    arguments); endpoint for S3-compatible stores (path style)."""

    def __init__(
        self,
        bucket: str,
        region: str,
        *,
        endpoint: str | None = None,
        access_key: str | None = None,
        secret_key: str | None = None,
        session_token: str | None = None,
        timeout: float = 30.0,
    ) -> None:
        self.bucket, self.region, self.timeout = bucket, region, timeout
        self.endpoint = (endpoint or f"https://s3.{region}.amazonaws.com").rstrip("/")
        self.access_key = access_key or os.environ.get("AWS_ACCESS_KEY_ID", "")
        self.secret_key = secret_key or os.environ.get("AWS_SECRET_ACCESS_KEY", "")
        self.session_token = session_token or os.environ.get("AWS_SESSION_TOKEN")

    def list_after(self, prefix: str, start_after: str) -> list[str]:
        out: list[str] = []
        token: str | None = None
        while True:
            q = {"list-type": "2", "prefix": prefix}
            if start_after:
                q["start-after"] = start_after
            if token:
                q["continuation-token"] = token
            root = ET.fromstring(self._request("/" + self.bucket, q))  # noqa: S314 (S3's response)
            ns = root.tag.split("}")[0] + "}" if root.tag.startswith("{") else ""
            for c in root.findall(ns + "Contents"):
                out += [k.text or "" for k in c.findall(ns + "Key")]
            more = [e.text for e in root.findall(ns + "IsTruncated")]
            nxt = [e.text or "" for e in root.findall(ns + "NextContinuationToken")]
            if more != ["true"] or not nxt:
                return out
            token = nxt[0]

    def get(self, key: str) -> bytes:
        return self._request("/" + self.bucket + "/" + urllib.parse.quote(key, safe="/~"), {})

    def folders(self, prefix: str) -> list[str]:
        out: list[str] = []
        token: str | None = None
        while True:
            q = {"list-type": "2", "prefix": prefix, "delimiter": "/"}
            if token:
                q["continuation-token"] = token
            root = ET.fromstring(self._request("/" + self.bucket, q))  # noqa: S314 (S3's response)
            ns = root.tag.split("}")[0] + "}" if root.tag.startswith("{") else ""
            for c in root.findall(ns + "CommonPrefixes"):
                out += [(p.text or "")[len(prefix) :].rstrip("/") for p in c.findall(ns + "Prefix")]
            more = [e.text for e in root.findall(ns + "IsTruncated")]
            nxt = [e.text or "" for e in root.findall(ns + "NextContinuationToken")]
            if more != ["true"] or not nxt:
                return out
            token = nxt[0]

    def url(self, key: str) -> str:
        return f"s3://{self.bucket}/{key}"

    def configure(self, con: duckdb.DuckDBPyConnection) -> None:
        """httpfs (installed in the image, never downloaded here) with this source's credentials
        as a DuckDB secret: the same keys or, with none, the default chain (IRSA, Pod Identity)
        through the aws extension. The values stay in the process: no command line, no file."""
        con.execute("LOAD httpfs")
        u = urllib.parse.urlparse(self.endpoint)
        opts = {"TYPE": "s3", "REGION": self.region}
        if not u.netloc.endswith("amazonaws.com"):
            opts |= {"ENDPOINT": u.netloc, "URL_STYLE": "path", "USE_SSL": "true" if u.scheme == "https" else "false"}
        if self.access_key and self.secret_key:
            opts |= {"KEY_ID": self.access_key, "SECRET": self.secret_key}
            if self.session_token:
                opts["SESSION_TOKEN"] = self.session_token
        else:
            con.execute("LOAD aws")
            opts["PROVIDER"] = "credential_chain"

        def lit(v: str) -> str:
            return v if v in ("true", "false") else "'" + v.replace("'", "''") + "'"

        body = ", ".join(f"{k} {v if k in ('TYPE', 'PROVIDER') else lit(v)}" for k, v in opts.items())
        con.execute(f"CREATE OR REPLACE SECRET aintercept_s3 ({body})")

    def _request(self, path: str, query: dict[str, str]) -> bytes:
        now = dt.datetime.now(dt.UTC)
        amz, day = now.strftime("%Y%m%dT%H%M%SZ"), now.strftime("%Y%m%d")
        host = urllib.parse.urlparse(self.endpoint).netloc
        qs = "&".join(
            f"{urllib.parse.quote(k, safe='~')}={urllib.parse.quote(v, safe='~')}" for k, v in sorted(query.items())
        )
        payload = hashlib.sha256(b"").hexdigest()
        headers = {"host": host, "x-amz-content-sha256": payload, "x-amz-date": amz}
        if self.session_token:
            headers["x-amz-security-token"] = self.session_token
        signed = ";".join(sorted(headers))
        canonical = "\n".join(["GET", path, qs, *(f"{k}:{headers[k]}" for k in sorted(headers)), "", signed, payload])
        scope = f"{day}/{self.region}/s3/aws4_request"
        to_sign = "\n".join(["AWS4-HMAC-SHA256", amz, scope, hashlib.sha256(canonical.encode()).hexdigest()])
        k = ("AWS4" + self.secret_key).encode()
        for part in (day, self.region, "s3", "aws4_request"):
            k = hmac.new(k, part.encode(), hashlib.sha256).digest()
        sig = hmac.new(k, to_sign.encode(), hashlib.sha256).hexdigest()
        auth = f"AWS4-HMAC-SHA256 Credential={self.access_key}/{scope}, SignedHeaders={signed}, Signature={sig}"
        url = self.endpoint + path + ("?" + qs if qs else "")
        if not url.startswith(("https://", "http://")):
            raise ValueError(f"S3 endpoint {self.endpoint!r}: use an http(s) URL")
        req = urllib.request.Request(url, headers={**headers, "Authorization": auth})  # noqa: S310 (checked above)
        with urllib.request.urlopen(req, timeout=self.timeout) as r:  # noqa: S310
            return cast(bytes, r.read())


# --- the object cache ------------------------------------------------------------------------


class ObjectCache:
    """Immutable objects on local disk, least recently used evicted past max_bytes."""

    def __init__(self, source: Source, directory: pathlib.Path, max_bytes: int) -> None:
        self.source, self.dir, self.max_bytes = source, pathlib.Path(directory), max_bytes
        self.dir.mkdir(parents=True, exist_ok=True)
        self._lru: OrderedDict[str, tuple[pathlib.Path, int]] = OrderedDict()
        self._lock = threading.Lock()
        self.size = self.hits = self.misses = 0

    def path(self, key: str) -> pathlib.Path:
        with self._lock:
            if key in self._lru:
                self._lru.move_to_end(key)
                self.hits += 1
                return self._lru[key][0]
        body = self.source.get(key)
        p = self.dir / hashlib.sha256(key.encode()).hexdigest()
        fd, tmp = tempfile.mkstemp(dir=self.dir)
        with os.fdopen(fd, "wb") as f:
            f.write(body)
        os.replace(tmp, p)
        with self._lock:
            self.misses += 1
            if key not in self._lru:
                self._lru[key] = (p, len(body))
                self.size += len(body)
            while self.size > self.max_bytes and len(self._lru) > 1:
                old, (op, n) = self._lru.popitem(last=False)
                if old == key:  # never evict what was just asked for
                    self._lru[old] = (op, n)
                    self._lru.move_to_end(old, last=False)
                    break
                op.unlink(missing_ok=True)
                self.size -= n
        return p


# --- each bucket's newest commit -------------------------------------------------------------

_COMMIT = re.compile(r"commit-(\d{12})\.json$")


@dataclass(frozen=True)
class FileRef:
    key: str
    min_key: str = ""
    max_key: str = ""

    def holds_any(self, keys: Sequence[str]) -> bool:
        """Whether the file's key range can hold any of keys (no range recorded: it might)."""
        if not self.min_key and not self.max_key:
            return True
        return any(self.min_key <= k <= self.max_key for k in keys)


@dataclass(frozen=True)
class Commit:
    bucket: int
    seq: int
    key: str
    base: dict[str, list[FileRef]]
    runs: dict[str, list[FileRef]]
    newest_ns: int | None = None
    content_retained: bool | None = None
    layout: str = ""

    def files(self, table: str, keys: Sequence[str] | None = None) -> list[str]:
        """The table's files, oldest first: the base, then the runs since; with keys, only the
        files whose key range can hold one of them (CONTRACT.md: a file holds only keys in its
        [min_key, max_key])."""
        refs = [*self.base.get(table, []), *self.runs.get(table, [])]
        return [r.key for r in refs if keys is None or r.holds_any(keys)]


def _parse_commit(key: str, body: bytes, get: Callable[[str], bytes] | None = None) -> Commit:
    """A commit, its base read from its base manifest when it names one (the base's
    parts grow with history, so they're written once per generation, not per commit)."""
    m = json.loads(body)

    def refs(by_table: dict[str, list[dict[str, Any]] | None] | None) -> dict[str, list[FileRef]]:
        return {
            t: [FileRef(r["key"], r.get("min_key") or "", r.get("max_key") or "") for r in rs or []]
            for t, rs in (by_table or {}).items()
        }

    base = m.get("base")
    if m.get("base_manifest"):
        if get is None:
            raise ValueError(f"commit {key} names a base manifest and nothing can read it")
        base = json.loads(get(m["base_manifest"])).get("base")
    return Commit(
        bucket=int(m["bucket"]),
        seq=int(m["seq"]),
        key=key,
        base=refs(base),
        runs=refs(m.get("runs")),
        newest_ns=m.get("newest_ns"),
        content_retained=m.get("content_retained"),
        layout=m.get("layout") or "",
    )


class CommitLog:
    """One bucket's newest commit, looked for at most every min_interval seconds. The first
    look lists the bucket's log; after that, commits being create-only with zero-padded
    sequence numbers, it GETs commit-(seq+1), then the next, until one isn't there: no listing
    (a store's listing can lag, and never moves the newest backwards). A listing every
    relist seconds still finds a newer commit past a gap (one collected, or never written)."""

    def __init__(
        self,
        source: Source,
        prefix: str,
        bucket: int,
        *,
        clock: Callable[[], float] = time.monotonic,
        min_interval: float = 1.0,
        relist: float = 60.0,
    ) -> None:
        self.source, self.bucket, self.clock, self.min_interval = source, bucket, clock, min_interval
        self.relist = relist
        self._listed: float | None = None
        self.prefix = f"{prefix}{ROOT}buckets/{bucket:02d}/"
        self._latest: Commit | None = None
        self._checked: float | None = None
        self._lock = threading.Lock()

    def latest(self) -> Commit | None:
        with self._lock:
            now = self.clock()
            if self._checked is not None and now - self._checked < self.min_interval:
                return self._latest
            self._checked = now
            if self._latest is None or self._listed is None or now - self._listed >= self.relist:
                self._listed = now
                after = self._latest.key if self._latest else ""
                newer = [k for k in self.source.list_after(self.prefix, after) if _COMMIT.search(k) and k > after]
                if newer:
                    key = max(newer)
                    self._latest = _parse_commit(key, self.source.get(key), self.source.get)
                if self._latest is None:
                    return None
            while True:
                key = f"{self.prefix}commit-{self._latest.seq + 1:012d}.json"
                body = _get_if_exists(self.source, key)
                if body is None:
                    return self._latest
                self._latest = _parse_commit(key, body, self.source.get)


def _get_if_exists(source: Source, key: str) -> bytes | None:
    try:
        return source.get(key)
    except FileNotFoundError:
        return None
    except urllib.error.HTTPError as e:
        if e.code == 404:
            return None
        raise


# --- scopes ----------------------------------------------------------------------------------


@dataclass(frozen=True)
class Scope:
    """Which keys a request reads, per kind: None is every bucket."""

    sessions: frozenset[str] | set[str] | None = None
    people: frozenset[str] | set[str] | None = None
    accounts: frozenset[str] | set[str] | None = None
    # Tables the endpoint never reads (left empty): the ones read from every bucket, when an
    # endpoint doesn't need them.
    skip: frozenset[str] = frozenset()
    # v1 only: exactly these keys' own tables, the fact tables included (the request drawer:
    # its call's keys, from the call's marks), and nothing merged.
    own: frozenset[str] | None = None
    # v1 only: the merge's day partitions from this day (YYYY-MM-DD) on, and its index (the
    # directory over a window: whatever overlaps it was last active in it or after).
    days_from: str | None = None

    @classmethod
    def everything(cls) -> "Scope":
        return cls()

    def reads(self, prefix: str) -> bool:
        """Whether a table routed by prefix is read at all: "f:" tables (the raw facts) only by a
        scope of everything."""
        return (
            prefix != "f:"
            or self.own is not None
            or (self.sessions is None and self.people is None and self.accounts is None)
        )

    def buckets(self, prefix: str) -> frozenset[int] | None:
        """The buckets a table routed by prefix comes from (None: all of them)."""
        keys = {"s:": self.sessions, "p:": self.people, "a:": self.accounts}.get(prefix)
        return None if keys is None else frozenset(bucket_of(prefix + k) for k in keys)

    def keys(self, prefix: str) -> list[str] | None:
        """The routing keys a table routed by prefix is filtered on (None: no filter)."""
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
# What some endpoints never read (state tables read from every bucket).
_REQUEST_INPUTS = frozenset({"request_txs", "batch_members"})
_DIMENSION_INPUTS = frozenset(DIMENSIONS)
# The endpoints that read every bucket (their own change narrows them: the spec's §2.3).
_EVERYTHING_PATHS = ("/api/v1/status", "/api/v1/overview", "/api/v1/people")
_EVERYTHING_PREFIXES = ("/api/v1/requests/", "/api/v1/events/")
_PERSON_PATH = re.compile(r"^/api/v1/people/([^/]+)")


@dataclass
class _Loaded:
    con: duckdb.DuckDBPyConnection
    commits: dict[int, Commit]
    memo: OrderedDict[tuple[Any, ...], Any] = field(default_factory=OrderedDict)


_current: contextvars.ContextVar[_Loaded | None] = contextvars.ContextVar("bucket_tables", default=None)


class BucketStore:
    """The store the endpoints query (store.Store's interface), over the bucketed output."""

    def __init__(
        self,
        source: Source,
        cache_dir: pathlib.Path,
        *,
        prefix: str = "",
        tables: dict[str, tuple[str, str]] | None = None,
        empty: dict[str, str] | None = None,
        views: str | None = None,
        raw: RawSource | None = None,
        keep_keys: bool = False,
        clock: Callable[[], float] = time.monotonic,
        cache_bytes: int = 2 << 30,
        loaded_max: int = 16,
        memory_limit: str | None = None,
        threads: int | None = None,
    ) -> None:
        self.source, self.prefix = source, prefix
        self.tables = tables if tables is not None else ROUTING
        self.empty = empty if empty is not None else (legacy._EMPTY if tables is None else {})
        self.views = views if views is not None else (legacy.VIEWS if tables is None else "")
        self.raw = raw
        self.cache = ObjectCache(source, pathlib.Path(cache_dir), cache_bytes)
        self.clock, self.keep_keys = clock, keep_keys
        self._logs: dict[int, CommitLog] = {}
        self._buckets: frozenset[int] = frozenset()
        self._listed: float | None = None
        self.memory_limit, self.threads = memory_limit, threads
        self.data_dir = pathlib.Path(".")
        self.shaped_dir = pathlib.Path(prefix or ".")
        self.skipped: list[str] = []
        self.loads = 0  # table sets built (one per new commit of a scope): the polling-cost counter
        self._loaded: OrderedDict[tuple[Any, ...], _Loaded] = OrderedDict()
        self._read_keys: set[str] = set()
        self._loaded_max = loaded_max
        self._lock = threading.Lock()
        self._state: bool | None = None
        self._state_listed: float | None = None
        self.title_runs: list[TitleRun] = []  # the newest table build's session_rows runs (for tests)
        self.title_all_sessions: frozenset[str] = frozenset()

    # --- scopes --------------------------------------------------------------------------------

    def scope_for_path(self, path: str) -> Scope | None:
        """A request path's scope: a session's endpoints read its bucket, a person's (and
        /coverage's, for its personId) their sessions' and accounts'; the directory, /status and
        the drawer all. None for anything else (the UI's files, unknown paths, /coverage without
        a personId): those read nothing."""
        path, _, query = path.partition("?")
        if path == "/api/v1/coverage":
            person = urllib.parse.parse_qs(query).get("personId")
            return self.resolve_person(person[0]) if person else None
        if m := _SESSION_PATH.match(path):
            rest = path[m.end() :]
            # Only a session's Requests tab and its state readout (which counts that tab's rows)
            # read the request log's inputs (and the credentials, a dimension, they're found by).
            skip = frozenset() if rest in ("/requests", "/state") else _REQUEST_INPUTS | _DIMENSION_INPUTS
            return Scope(sessions={urllib.parse.unquote(m.group(1))}, skip=skip)
        if m := _PERSON_PATH.match(path):
            return self.resolve_person(urllib.parse.unquote(m.group(1)))
        if path in _EVERYTHING_PATHS or path.startswith(_EVERYTHING_PREFIXES):
            return Scope.everything()
        return None

    def resolve_person(self, person_id: str) -> Scope:
        """A person's sessions and accounts, from the dimension tables (every bucket's)."""
        if self.stream_state:
            need: list[str] = list(DIMENSIONS)
        else:
            need = [*_PERSON_RESOLUTION, *(DIMENSIONS if self._has_dimensions() else ())]
        loaded = self._load(Scope.everything(), only=tuple(t for t in need if t in self._routing()))
        cur = loaded.con.cursor()
        try:
            sessions = {r[0] for r in cur.execute(_PERSON_SESSIONS, [person_id]).fetchall()}
            accounts = {r[0] for r in cur.execute(_PERSON_ACCOUNTS, [person_id]).fetchall()}
        finally:
            cur.close()
        return Scope(sessions=sessions, people={person_id}, accounts=accounts)

    @contextlib.contextmanager
    def scope(self, scope: Scope) -> Iterator[None]:
        """Within: queries read this scope's tables, at the commits current on entry."""
        token = _current.set(self._load(scope))
        try:
            yield
        finally:
            _current.reset(token)

    # For the app's middleware: `load_for` (blocking: run it off the event loop) finds and
    # loads the request's tables; `enter`, in the request's own context, makes them current.
    def load_for(self, path: str) -> object:
        scope = self.scope_for_path(path)
        return None if scope is None else self._load(scope)

    def enter(self, loaded: object) -> contextvars.Token[_Loaded | None] | None:
        return None if loaded is None else _current.set(cast(_Loaded, loaded))

    def leave(self, token: contextvars.Token[_Loaded | None] | None) -> None:
        if token is not None:
            _current.reset(token)

    # --- loading -------------------------------------------------------------------------------

    def _load(self, scope: Scope, only: Sequence[str] | None = None) -> _Loaded:
        routing = self._routing()
        names = [
            t
            for t in (list(only) if only is not None else list(routing))
            if t not in scope.skip and scope.reads(routing[t][0])
        ]
        if scope.sessions is not None and self._has_session_inputs() and only is None:
            scope = Scope(
                sessions=self._session_group(scope.sessions),
                people=scope.people,
                accounts=scope.accounts,
                skip=scope.skip,
            )
        wants = {t: None if routing[t][0] == "*" else scope.buckets(routing[t][0]) for t in names}
        keys = {t: None if routing[t][0] == "*" else scope.keys(routing[t][0]) for t in names}
        needed: set[int] = set()
        for want in wants.values():
            needed |= set(range(1 << 16)) if want is None else want
        commits = self._latest(sorted(needed & self._discover()))
        plan: dict[str, list[str]] = {}
        for t, want in wants.items():
            plan[t] = [k for b in sorted(commits) if want is None or b in want for k in commits[b].files(t, keys[t])]
        sig = (scope.signature(), tuple(names), tuple((t, tuple(fs)) for t, fs in sorted(plan.items())))
        with self._lock:
            if sig in self._loaded:
                self._loaded.move_to_end(sig)
                return self._loaded[sig]
        with self._lock:
            self._read_keys.update(k for fs in plan.values() for k in fs)
        build = self._build_state if self.stream_state else self._build
        loaded = _Loaded(build(plan, scope, partial=only is not None, keys=keys), commits)
        with self._lock:
            self.loads += 1
            self._loaded[sig] = loaded
            while len(self._loaded) > self._loaded_max:
                _, old = self._loaded.popitem(last=False)
                old.con.close()
        return loaded

    def _latest(self, bucket_ids: Sequence[int]) -> dict[int, Commit]:
        """Each bucket's newest commit, looked up in parallel (a GET each, once warm)."""
        logs = [self._log(b) for b in bucket_ids]
        if len(logs) <= 1:
            found = [log.latest() for log in logs]
        else:
            with ThreadPoolExecutor(max_workers=min(16, len(logs))) as pool:
                found = list(pool.map(lambda log: log.latest(), logs))
        return {log.bucket: c for log, c in zip(logs, found, strict=True) if c is not None}

    def _connect(self) -> duckdb.DuckDBPyConnection:
        return connect(self.source, memory_limit=self.memory_limit, threads=self.threads)

    def _build_state(
        self,
        plan: dict[str, list[str]],
        scope: Scope | None = None,
        *,
        partial: bool = False,
        keys: dict[str, list[str] | None] | None = None,
    ) -> duckdb.DuckDBPyConnection:
        """The API's tables from the stream state's (state_sql): each stored table folded as
        st_<table> (empty when the scope has none of its files), then rebuilt into today's."""
        con = self._connect()
        for table in state_sql.STATE_TABLES:
            urls = [self.source.url(k) for k in plan.get(table, [])]
            if urls:
                read_newest(con, urls, keys=(keys or {}).get(table)).create("st_" + table)
            else:
                con.execute(f"CREATE TABLE st_{table} ({state_sql.STATE_SCHEMA[table]})")
        state_sql.stored_views(con)
        for sql in PERSON_TABLES.values():
            con.execute(sql)
        if partial:  # a resolution's load: the dimensions and the person tables are enough
            return con
        sessions = None if scope is None else scope.sessions
        self._session_rows(con, sessions)
        state_sql.session_tables(con)
        state_sql.event_tables(con)
        state_sql.accounts(con)
        state_sql.request_tables(con, None if scope is None else scope.people, sessions)
        state_sql.fact_tables(con)
        # What the drawer, /status and the directory still read of the raw history: not in the
        # stream's layout (another change moves them onto it), so empty here.
        have = {r[0] for r in con.execute("SELECT table_name FROM duckdb_tables()").fetchall()}
        for table, ddl in legacy._EMPTY.items():
            if table not in have:
                con.execute(ddl)
        if "probes" not in have:
            con.execute(legacy._PROBES_EMPTY)
        con.execute(legacy.VIEWS)
        return con

    def _build(
        self,
        plan: dict[str, list[str]],
        scope: Scope | None = None,
        *,
        partial: bool = False,
        keys: dict[str, list[str] | None] | None = None,
    ) -> duckdb.DuckDBPyConnection:
        con = self._connect()
        sessions_shape = self._has_session_inputs() and self._stored_sessions(plan.get("session_rows") or [])
        for table, files in plan.items():
            urls = [self.source.url(k) for k in files]
            name = "_s_" + table if sessions_shape and table in SESSION_INPUTS else table
            if urls:
                read_newest(con, urls, keys=(keys or {}).get(table), keep_keys=self.keep_keys).create(name)
            elif sessions_shape and table in _SESSION_EMPTY:
                con.execute(_SESSION_EMPTY[table])
            elif table in self.empty:
                con.execute(self.empty[table])
            elif table == "probes":
                con.execute(legacy._PROBES_EMPTY)
        # With the dimension tables in the layout, the person tables are theirs (the stored ones,
        # from the full-rebuild processor, are the fallback until it's gone).
        if sessions_shape:
            self._session_rows(con, None if scope is None else scope.sessions)
        if all(plan.get(t) for t in DIMENSIONS):
            for sql in PERSON_TABLES.values():
                con.execute(sql)
        if self.views and all(t in plan for t in self.tables):
            con.execute(self.views)
        return con

    @property
    def stream_state(self) -> bool:
        """Whether the layout is the stream state's tables (request_txs, prompt_items, …) rather
        than finished API-shaped tables: what the commits say (their layout), looked at most
        every 30 seconds; never read from a table file. A store given its tables isn't."""
        if self.tables is not ROUTING:
            return False
        with self._lock:
            now = self.clock()
            if self._state is not None and self._state_listed is not None and now - self._state_listed < 30.0:
                return self._state
        found = False
        for b in sorted(self._discover()):
            c = self._log(b).latest()
            if c is not None:
                found = c.layout.startswith("stream-state/")
                break
        with self._lock:
            self._state, self._state_listed = found, now
        return found

    def _routing(self) -> dict[str, tuple[str, str]]:
        return state_sql.STATE_ROUTING if self.stream_state else self.tables

    def _discover(self) -> frozenset[int]:
        """The buckets that have a commit log: the folders under stream/v1/buckets/, listed at most
        every 30 seconds (buckets appear when the processor first commits one)."""
        with self._lock:
            now = self.clock()
            if self._listed is not None and now - self._listed < 30.0:
                return self._buckets
            self._listed = now
        found = {int(n) for n in self.source.folders(f"{self.prefix}{ROOT}buckets/") if n.isdigit()}
        with self._lock:
            self._buckets = frozenset(found)
            return self._buckets

    def _log(self, bucket: int) -> CommitLog:
        with self._lock:
            if bucket not in self._logs:
                self._logs[bucket] = CommitLog(self.source, self.prefix, bucket, clock=self.clock)
            return self._logs[bucket]

    def cache_keys(self) -> list[str]:
        """The objects read so far (for tests): the table files the loads named, and what the
        local cache holds."""
        with self._lock:
            return sorted(self._read_keys | set(self.cache._lru))

    def _stored_sessions(self, keys: list[str]) -> bool:
        """Whether session_rows' files are the processor's per-session rows (with own_title) rather
        than the full-rebuild processor's finished rows."""
        if not keys:
            return False
        cur = self._connect().execute("SELECT * FROM read_parquet(?) LIMIT 0", [self.source.url(keys[0])])
        return "own_title" in [d[0] for d in cur.description or []]

    def _has_session_inputs(self) -> bool:
        return all(t in self._routing() for t in SESSION_INPUTS)

    def _session_group(self, sessions: frozenset[str] | set[str]) -> frozenset[str]:
        """The sessions linked to these through their calls' accounts (transitively): the ones
        whose Codex titles can affect theirs."""
        loaded = self._load(Scope.everything(), only=("session_call_accounts",))
        found = loaded.con.execute(
            "SELECT count(*) FROM information_schema.tables WHERE table_name = 'session_call_accounts'"
        )
        if not found.fetchall()[0][0]:  # a layout without the per-session tables
            return frozenset(sessions)
        rows = loaded.con.execute("SELECT session_id, account_id FROM session_call_accounts").fetchall()
        group, accounts = set(sessions), set[str]()
        while True:
            more = {a for s, a in rows if s in group} - accounts
            if not more:
                return frozenset(group)
            accounts |= more
            group |= {s for s, a in rows if a in more}

    def _session_rows(self, con: duckdb.DuckDBPyConnection, sessions: frozenset[str] | set[str] | None) -> None:
        """The API's session_rows from the per-session tables: the SQL run once per group of
        sessions linked by their calls' accounts that has title calls (the recursive titles
        query only ever sees one group), and once for the rest, with no title calls."""
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
        self.title_runs, self.title_all_sessions = runs, frozenset(have)

    def _has_dimensions(self) -> bool:
        return all(t in self._routing() for t in DIMENSIONS)

    def _tables(self) -> _Loaded:
        loaded = _current.get()
        return loaded if loaded is not None else self._load(Scope.everything())

    # --- the Store interface the endpoints use ----------------------------------------------------

    def maybe_ingest(self, min_interval_s: float) -> None:
        """Nothing to load ahead: each request's scope finds its buckets' newest commits."""

    @property
    def commit(self) -> object:
        return tuple(sorted((b, c.seq) for b, c in self._tables().commits.items()))

    @property
    def newest_ns(self) -> int | None:
        found = [c.newest_ns for c in self._tables().commits.values() if c.newest_ns]
        return max(found) if found else None

    @property
    def content_retained(self) -> bool | None:
        found = [c.content_retained for c in self._tables().commits.values() if c.content_retained is not None]
        return any(found) if found else None

    def memo(self, key: tuple[Any, ...], build: Callable[[], T]) -> T:
        """build(), once per loaded table set (its commits) for this key."""
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

    # The drawer's raw view reads the raw log as the old store does (the spec's §4.9 moves it
    # onto per-call detail objects).
    def raw_rows(self, correlation_id: str) -> list[dict[str, Any]]:
        if self.raw is None:
            return []
        return legacy.Store.raw_rows(cast(legacy.Store, self), correlation_id)

    def raw_event(self, correlation_id: str, event_key: str, source_file: str) -> dict[str, Any] | None:
        if self.raw is None:
            return None
        return legacy.Store.raw_event(cast(legacy.Store, self), correlation_id, event_key, source_file)

    def _read(self, keys: list[str], correlation_id: str) -> list[dict[str, Any]]:
        return legacy.Store._read(cast(legacy.Store, self), keys, correlation_id)

    def close(self) -> None:
        with self._lock:
            for loaded in self._loaded.values():
                loaded.con.close()
            self._loaded.clear()
