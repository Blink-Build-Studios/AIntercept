"""Telemetry store: the shaped tables `aintercept process` writes, loaded into DuckDB.

The observer writes raw partitions; the processor (stage 2, docs/ui/BUILD_PLAN.md
§7.5) turns them into Parquet tables and commits each run with a manifest. This
store reads only what the manifest names: no JSON parsing at query time.
`ingest()` (called per request, at most every interval) re-reads the manifest
and, when there's a new commit, appends the new fact parts and replaces the
snapshot tables; a compaction (parts replaced) reloads that table.

Tables (columns as in aintercept/internal/process/tables.go):
- `requests`: every HTTP transaction and WebSocket turn, ended. `pending`:
  transactions still waiting for their ending (in flight).
- `analytics`: each analytics event a client reported.
- `sessions`, `prompts`, `person_devices`, `people`: snapshots.
- `probes`: the earliest time of each part's synthetic rows. The observers' path probe
  (`aintercept-proxyprobe`) is most of `requests` by row count and is never shown, so its
  rows aren't loaded; the setup screen's "waiting since" needs only when it started.

The tables live in a DuckDB file in `db_dir` (a fresh one per process), not in
memory: DuckDB compresses tables on disk (about a ninth of their in-memory size) and
pages them in and out under `memory_limit`, so memory stays bounded as data grows
and as commits replace parts.

Views:
- `calls`: requests plus the in-flight ones, with `ts` (start), `end_ts`.
- `cc_calls`: every coding agent's visible model calls (Claude Code, Codex: a `product`
  column); `hidden_calls`: the ones the user never sees (Codex prewarm, title).
- `cc_events`: Claude Code analytics events; `codex_events`: Codex's.

The raw rows of one transaction (the drawer's raw view) come from its lineage:
the raw files from `first_file` to `source_file` of its observer pod.
"""

import hashlib
import json
import os
import pathlib
import re
import shutil
import tempfile
import threading
import time
import weakref
from collections import OrderedDict
from collections.abc import Callable, Sequence
from typing import Any, TypeVar, cast

import duckdb

from aintercept_api import raw
from aintercept_api.raw import LocalRaw, RawSource

T = TypeVar("T")
FACTS = ("requests", "analytics", "ledger")
MEMO_SIZE = 64  # Store.memo entries (a few sessions' events, steps and calls)
SNAPSHOTS = ("sessions", "prompts", "person_devices", "people", "pending", "accounts")
_MANIFEST_TABLE = {"requests": "requests", "analytics": "events", "ledger": "ledger"}  # ours -> the manifest's

# Loading statements per table, written out (no SQL is built from strings).
# Fact rows carry the part they came from (`filename`), so a compaction swaps
# just the parts it replaced.
_INSERT = {
    "requests": (
        "INSERT INTO requests BY NAME SELECT * FROM read_parquet(?, filename = true, union_by_name = true)"
        " WHERE NOT coalesce(synthetic, false)"
    ),
    "analytics": "INSERT INTO analytics BY NAME SELECT * FROM read_parquet(?, filename = true, union_by_name = true)",
    "ledger": "INSERT INTO ledger BY NAME SELECT * FROM read_parquet(?, filename = true, union_by_name = true)",
}
_DELETE = {
    "requests": "DELETE FROM requests WHERE filename IN (SELECT unnest(?::VARCHAR[]))",
    "analytics": "DELETE FROM analytics WHERE filename IN (SELECT unnest(?::VARCHAR[]))",
    "ledger": "DELETE FROM ledger WHERE filename IN (SELECT unnest(?::VARCHAR[]))",
}
# The probe's rows: only each requests part's earliest (see the module docstring).
_PROBES_INSERT = """INSERT INTO probes
SELECT filename, min(coalesce(start_ns, end_ns)) FROM read_parquet(?, filename = true, union_by_name = true)
WHERE synthetic GROUP BY filename"""
_PROBES_DELETE = "DELETE FROM probes WHERE filename IN (SELECT unnest(?::VARCHAR[]))"
_PROBES_EMPTY = "CREATE OR REPLACE TABLE probes (filename VARCHAR, first_ns BIGINT)"
_REPLACE = {
    "requests": (
        "CREATE OR REPLACE TABLE requests AS SELECT * FROM read_parquet(?, filename = true, union_by_name = true)"
        " WHERE NOT coalesce(synthetic, false)"
    ),
    "analytics": (
        "CREATE OR REPLACE TABLE analytics AS SELECT * FROM read_parquet(?, filename = true, union_by_name = true)"
    ),
    "ledger": (
        "CREATE OR REPLACE TABLE ledger AS SELECT * FROM read_parquet(?, filename = true, union_by_name = true)"
    ),
    "sessions": "CREATE OR REPLACE TABLE sessions AS SELECT * FROM read_parquet(?)",
    "prompts": "CREATE OR REPLACE TABLE prompts AS SELECT * FROM read_parquet(?)",
    "person_devices": "CREATE OR REPLACE TABLE person_devices AS SELECT * FROM read_parquet(?)",
    "people": "CREATE OR REPLACE TABLE people AS SELECT * FROM read_parquet(?)",
    "pending": "CREATE OR REPLACE TABLE pending AS SELECT * FROM read_parquet(?)",
    "accounts": "CREATE OR REPLACE TABLE accounts AS SELECT * FROM read_parquet(?)",
}

# Columns newer processors write: added (as nulls) to tables loaded from older parts, so
# the views work on data from before them.
_ADD_COLUMNS = {
    "requests": (
        "ALTER TABLE requests ADD COLUMN IF NOT EXISTS product VARCHAR",
        "ALTER TABLE requests ADD COLUMN IF NOT EXISTS client VARCHAR",
        "ALTER TABLE requests ADD COLUMN IF NOT EXISTS client_version VARCHAR",
        "ALTER TABLE requests ADD COLUMN IF NOT EXISTS hidden_kind VARCHAR",
        "ALTER TABLE requests ADD COLUMN IF NOT EXISTS ext_json VARCHAR",
        "ALTER TABLE requests ADD COLUMN IF NOT EXISTS effort VARCHAR",
        "ALTER TABLE requests ADD COLUMN IF NOT EXISTS detail_json VARCHAR",
    ),
    "sessions": (
        "ALTER TABLE sessions ADD COLUMN IF NOT EXISTS product VARCHAR",
        "ALTER TABLE sessions ADD COLUMN IF NOT EXISTS client VARCHAR",
        "ALTER TABLE sessions ADD COLUMN IF NOT EXISTS client_version VARCHAR",
        "ALTER TABLE sessions ADD COLUMN IF NOT EXISTS hidden_calls BIGINT",
        "ALTER TABLE sessions ADD COLUMN IF NOT EXISTS origin_kind VARCHAR",
        "ALTER TABLE sessions ADD COLUMN IF NOT EXISTS origin_session_id VARCHAR",
        "ALTER TABLE sessions ADD COLUMN IF NOT EXISTS origin_turn_id VARCHAR",
        "ALTER TABLE sessions ADD COLUMN IF NOT EXISTS origin_turn BIGINT",
        "ALTER TABLE sessions ADD COLUMN IF NOT EXISTS init VARCHAR",
        "ALTER TABLE sessions ADD COLUMN IF NOT EXISTS agent_name VARCHAR",
    ),
}

# An empty table of each shape, for before the first commit (columns the views use).
_EMPTY = {
    "requests": """CREATE OR REPLACE TABLE requests (
        call_id VARCHAR, correlation_id VARCHAR, source_file VARCHAR, first_file VARCHAR, ws_turn BIGINT,
        transport VARCHAR, start_ns BIGINT, response_start_ns BIGINT, end_ns BIGINT, host VARCHAR, method VARCHAR,
        path VARCHAR, status BIGINT, synthetic BOOLEAN, ip VARCHAR, key_hash VARCHAR, key_hint VARCHAR, error VARCHAR,
        session_id VARCHAR, prompt_id VARCHAR, person_id VARCHAR, device_id VARCHAR, org_id VARCHAR,
        request_class VARCHAR, client_request_id VARCHAR, request_id VARCHAR, agent_id VARCHAR, agent_type VARCHAR,
        user_agent VARCHAR, model VARCHAR, stop_reason VARCHAR, message_id VARCHAR, previous_message_id VARCHAR,
        thread_type VARCHAR, stream BOOLEAN, service_tier VARCHAR, input_tokens BIGINT, output_tokens BIGINT,
        cache_read_tokens BIGINT, cache_write_tokens BIGINT, reasoning_tokens BIGINT, request_bytes BIGINT,
        response_bytes BIGINT, content_encoding VARCHAR, client_json VARCHAR, provider_json VARCHAR,
        tool_uses_json VARCHAR, product VARCHAR, client VARCHAR, client_version VARCHAR, hidden_kind VARCHAR,
        effort VARCHAR, detail_json VARCHAR,
        ext_json VARCHAR, filename VARCHAR)""",
    "analytics": """CREATE OR REPLACE TABLE analytics (
        event_key VARCHAR, correlation_id VARCHAR, source_file VARCHAR, row_ns BIGINT, ns BIGINT, seq BIGINT,
        source VARCHAR, name VARCHAR, type VARCHAR, client_event_id VARCHAR, session_id VARCHAR, prompt_id VARCHAR,
        person_id VARCHAR, org_id VARCHAR, device_id VARCHAR, agent_id VARCHAR, agent_type VARCHAR, model VARCHAR,
        request_id VARCHAR, client_request_id VARCHAR, tool_use_id VARCHAR, prompt_index BIGINT,
        permission_mode VARCHAR, head_sha VARCHAR, metadata_json VARCHAR, env_json VARCHAR, filename VARCHAR)""",
    "ledger": """CREATE OR REPLACE TABLE ledger (raw_key VARCHAR, size BIGINT, rows BIGINT, bad BIGINT, run BIGINT,
        filename VARCHAR)""",
    "sessions": """CREATE OR REPLACE TABLE sessions (session_id VARCHAR, person_id VARCHAR, org_id VARCHAR,
        device_id VARCHAR, first_ns BIGINT, last_ns BIGINT, calls BIGINT, events BIGINT, prompts BIGINT,
        product VARCHAR, client VARCHAR, client_version VARCHAR, hidden_calls BIGINT, origin_kind VARCHAR,
        origin_session_id VARCHAR, origin_turn_id VARCHAR, origin_turn BIGINT, init VARCHAR, agent_name VARCHAR)""",
    "prompts": """CREATE OR REPLACE TABLE prompts (prompt_id VARCHAR, session_id VARCHAR, prompt_index BIGINT,
        first_ns BIGINT, last_ns BIGINT, calls BIGINT, events BIGINT)""",
    "person_devices": """CREATE OR REPLACE TABLE person_devices (person_id VARCHAR, device_id VARCHAR,
        first_ns BIGINT, last_ns BIGINT)""",
    "people": """CREATE OR REPLACE TABLE people (person_id VARCHAR, first_ns BIGINT, last_ns BIGINT,
        sessions BIGINT, calls BIGINT, events BIGINT, devices BIGINT)""",
    "pending": """CREATE OR REPLACE TABLE pending (correlation_id VARCHAR, source_file VARCHAR, seen_ns BIGINT,
        done BOOLEAN, end_ns BIGINT, error VARCHAR, end_file VARCHAR, start_ns BIGINT, response_start_ns BIGINT,
        host VARCHAR, method VARCHAR, path VARCHAR, status BIGINT, synthetic BOOLEAN, ip VARCHAR, key_hash VARCHAR,
        key_hint VARCHAR, request_bytes BIGINT, response_bytes BIGINT, websocket BOOLEAN, client_json VARCHAR,
        request_id VARCHAR, content_encoding VARCHAR, provider_json VARCHAR)""",
    "accounts": """CREATE OR REPLACE TABLE accounts (account_id VARCHAR, email VARCHAR, name VARCHAR, org VARCHAR,
        seen_ns BIGINT)""",
}

VIEWS = """
CREATE OR REPLACE VIEW calls AS
SELECT call_id, correlation_id, ws_turn, coalesce(start_ns, end_ns) AS ts, end_ns AS end_ts, response_start_ns,
       host, method, path, status, coalesce(synthetic, false) AS synthetic, ip, key_hash, error,
       session_id, prompt_id, person_id, device_id, org_id, request_class, client_request_id, request_id,
       agent_id, agent_type, user_agent, model, stop_reason, message_id, previous_message_id,
       input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, request_bytes, response_bytes,
       content_encoding, provider_json, client_json, source_file, first_file, false AS in_flight,
       coalesce(product, CASE WHEN host = 'api.anthropic.com' THEN 'claude_code' END) AS product,
       client, client_version, hidden_kind, ext_json, reasoning_tokens, tool_uses_json, effort, detail_json
FROM requests
UNION ALL BY NAME
SELECT correlation_id AS call_id, correlation_id, coalesce(start_ns, seen_ns) AS ts, NULL::BIGINT AS end_ts,
       response_start_ns, host, method, path, status, coalesce(synthetic, false) AS synthetic, ip, key_hash,
       json_extract_string(client_json, '$."x-claude-code-session-id"') AS session_id,
       json_extract_string(client_json, '$."x-claude-code-prompt-id"') AS prompt_id,
       json_extract_string(client_json, '$."x-organization-uuid"') AS org_id,
       json_extract_string(client_json, '$."x-claude-code-request-class"') AS request_class,
       json_extract_string(client_json, '$."x-client-request-id"') AS client_request_id, request_id,
       json_extract_string(client_json, '$."x-claude-code-agent-id"') AS agent_id,
       json_extract_string(client_json, '$."x-claude-code-agent-type"') AS agent_type,
       json_extract_string(client_json, '$."user-agent"') AS user_agent,
       request_bytes, response_bytes, content_encoding, provider_json,
       client_json, source_file, source_file AS first_file, true AS in_flight,
       CASE WHEN host = 'api.anthropic.com' THEN 'claude_code'
            WHEN json_extract_string(client_json, '$.originator') LIKE 'codex%' THEN 'codex' END AS product
FROM pending WHERE NOT done AND start_ns IS NOT NULL;

-- Every coding agent's model calls (docs/harness-parity.md), the ones the user sees: Claude
-- Code's /v1/messages calls, Codex's response.create turns. `hidden_calls` are the rest
-- (Codex prewarm and title calls). `correlation_id` is the call's id: a WebSocket turn's
-- call_id, written connection~turn (URL-safe; the processor's is connection#turn), so each
-- turn opens on its own.
CREATE OR REPLACE VIEW agent_calls AS
SELECT replace(call_id, '#', '~') AS correlation_id, ts, end_ts, ip, key_hash, session_id, prompt_id,
       request_class, client_request_id, agent_id, request_id, status, person_id AS account_uuid, device_id,
       model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, stop_reason, agent_type,
       product, client, client_version, hidden_kind, ext_json, provider_json, ws_turn,
       reasoning_tokens, tool_uses_json, effort, response_bytes, method, detail_json
FROM calls
WHERE NOT synthetic AND session_id IS NOT NULL
  AND ((product = 'claude_code' AND host = 'api.anthropic.com'
        AND path LIKE '/v1/messages%' AND path NOT LIKE '/v1/messages/count_tokens%')
    OR (product = 'codex' AND host = 'chatgpt.com' AND path = '/backend-api/codex/responses'
        AND (ws_turn IS NOT NULL OR method = 'POST')));

CREATE OR REPLACE VIEW cc_calls AS SELECT * FROM agent_calls WHERE hidden_kind IS NULL;
CREATE OR REPLACE VIEW hidden_calls AS SELECT * FROM agent_calls WHERE hidden_kind IS NOT NULL;

-- Claude Code views (handoff §1.1): field names verified against captured traffic.

CREATE OR REPLACE VIEW cc_events AS
SELECT correlation_id, row_ns AS row_ts, ns AS ts, name, session_id, person_id AS account_uuid,
       org_id AS organization_uuid, device_id, metadata_json::JSON AS md, env_json::JSON AS env, head_sha, seq,
       prompt_id, event_key, source_file, request_id, client_request_id, tool_use_id
FROM analytics WHERE source = 'claude_code';

-- Codex's own analytics (codex_turn_event, codex_thread_initialized, …): the params are `md`.
CREATE OR REPLACE VIEW codex_events AS
SELECT correlation_id, ns AS ts, name, session_id, prompt_id, metadata_json::JSON AS md, event_key
FROM analytics WHERE source = 'codex';
"""


_COPY = re.compile(r"^(h[0-9a-f]{16})-\d+$")


def _remove(con: duckdb.DuckDBPyConnection, db_dir: str) -> None:
    con.close()
    shutil.rmtree(db_dir, ignore_errors=True)


class Store:
    def __init__(
        self,
        data_dir: pathlib.Path,
        shaped_dir: pathlib.Path | None = None,
        *,
        memory_limit: str | None = None,
        threads: int | None = None,
        raw: RawSource | None = None,
        db_dir: pathlib.Path | None = None,
    ):
        """`data_dir` holds the raw partitions (read only for the drawer's raw
        view; `raw` reads them from elsewhere, the cluster's mirror sidecar);
        `shaped_dir` the processor's output (default: data_dir/../shaped).
        `memory_limit` / `threads` bound DuckDB (default: DuckDB's own). The tables
        go in a new directory under `db_dir` (default: the system's temp directory),
        removed when the store closes or is collected."""
        self.data_dir = pathlib.Path(data_dir)
        self.raw: RawSource = raw or LocalRaw(self.data_dir)
        self.shaped_dir = pathlib.Path(shaped_dir) if shaped_dir else self.data_dir.parent / "shaped"
        if db_dir:
            pathlib.Path(db_dir).mkdir(parents=True, exist_ok=True)
        self._db_dir = tempfile.mkdtemp(prefix="aintercept-store-", dir=db_dir)
        self._con = duckdb.connect(os.path.join(self._db_dir, "store.duckdb"))
        self._cleanup = weakref.finalize(self, _remove, self._con, self._db_dir)
        self._con.execute("SET TimeZone='UTC'")
        self.memory_limit = memory_limit
        if memory_limit:
            if not re.fullmatch(r"\d+(\.\d+)?\s*[KMGT]i?B", memory_limit):
                raise ValueError(f"memory_limit {memory_limit!r}: use a size like '512MB'")
            self._con.execute(f"SET memory_limit = '{memory_limit}'")
        if threads:
            self._con.execute(f"SET threads = {int(threads)}")
        for sql in _EMPTY.values():
            self._con.execute(sql)
        self._con.execute(_PROBES_EMPTY)
        self._con.execute(VIEWS)
        self._lock = threading.Lock()
        self._memo: OrderedDict[tuple[Any, ...], Any] = OrderedDict()
        self._memo_lock = threading.Lock()
        self._memo_commit: object = None
        self._ingesting = threading.Lock()  # maybe_ingest: one load at a time; the rest don't queue
        self._run: tuple[int, str | None] | None = None
        self._parts: dict[str, list[str]] = {t: [] for t in FACTS}
        self._snaps: dict[str, str] = {}
        self._unread: list[str] = []
        self._last_ingest: float | None = None
        self.newest_ns: int | None = None
        self.content_retained: bool | None = None  # the deployment's retention mode, once there's a commit
        self.parts_read = 0  # Parquet files loaded by this process: the polling-cost counter

    # --- loading -----------------------------------------------------------------------------

    def _manifest(self) -> dict[str, Any] | None:
        try:
            m = json.loads((self.shaped_dir / "manifest.json").read_text())
        except FileNotFoundError:
            return None
        return m if isinstance(m, dict) else None

    def ingest(self) -> int:
        """Load the processor's newest commit, if there is one. Returns the parts loaded."""
        with self._lock:
            self._last_ingest = time.monotonic()
            m = self._manifest()
            commit = (m.get("run", 0), m.get("committed")) if m else None
            if m is None or commit is None or commit == self._run:
                return 0
            loaded = 0
            # A run number that didn't increase is a rebuilt output: reload everything.
            # What's loaded changes only once the load commits: a failed load leaves
            # the tables and this bookkeeping as they were, and is retried next time.
            rebuilt = self._run is not None and commit[0] <= self._run[0]
            parts = {t: [] for t in FACTS} if rebuilt else dict(self._parts)
            snaps: dict[str, str] = {} if rebuilt else dict(self._snaps)
            cur = self._con.cursor()
            try:
                cur.execute("BEGIN")
                for table in FACTS:
                    want = list(m.get("facts", {}).get(_MANIFEST_TABLE[table], []))
                    have = parts[table]
                    if not have:  # first load: the table is its parts
                        added = want
                        self._replace(cur, table, want)
                    else:  # the parts that went (a compaction) out, the new ones in
                        removed = [k for k in have if k not in set(want)]
                        added = [k for k in want if k not in set(have)]
                        if removed:
                            cur.execute(_DELETE[table], [self._paths(removed)])
                            if table == "requests":
                                cur.execute(_PROBES_DELETE, [self._paths(removed)])
                        if added:
                            cur.execute(_INSERT[table], [self._paths(added)])
                            if table == "requests":
                                cur.execute(_PROBES_INSERT, [self._paths(added)])
                    loaded += len(added)
                    parts[table] = want
                for table in SNAPSHOTS:
                    key = m.get("snapshots", {}).get(table)
                    if key and key != snaps.get(table):
                        self._replace(cur, table, [key])
                        snaps[table] = key
                        loaded += 1
                cur.execute("COMMIT")
            except duckdb.Error:
                cur.execute("ROLLBACK")  # a part went away mid-read: the old tables stay
                raise
            finally:
                cur.close()
            self._parts, self._snaps = parts, snaps
            self._run = commit
            self.newest_ns = m.get("newest_ns") or None
            self.content_retained = bool(m.get("content_retained"))
            self._unread = list(m.get("unread") or [])
            self.parts_read += loaded
            return loaded

    def _replace(self, cur: duckdb.DuckDBPyConnection, table: str, keys: list[str]) -> None:
        """Make the table exactly these Parquet files (their columns, as the processor wrote them)."""
        if keys:
            cur.execute(_REPLACE[table], [self._paths(keys)])
            for sql in _ADD_COLUMNS.get(table, ()):
                cur.execute(sql)
        else:
            cur.execute(_EMPTY[table])
        if table == "requests":
            cur.execute(_PROBES_EMPTY)
            if keys:
                cur.execute(_PROBES_INSERT, [self._paths(keys)])

    def _paths(self, keys: list[str]) -> list[str]:
        """Manifest keys are relative to the processor's output root; this store reads
        that root as shaped_dir (the keys may carry the bucket's prefix)."""
        out = []
        for k in keys:
            p = self.shaped_dir / k
            if not p.exists() and "/" in k:
                p = self.shaped_dir / k.split("/", 1)[1]  # <prefix>/shaped/... synced as shaped/...
                if not p.exists():
                    p = self.shaped_dir / k.split("shaped/", 1)[-1]
            out.append(str(p))
        return out

    def maybe_ingest(self, min_interval_s: float) -> None:
        """Ingest at most every `min_interval_s` (0 = on every call). Called per request,
        off the event loop. While one load runs, other requests serve what's loaded
        rather than wait; only the first load is waited for (there's nothing yet)."""
        if self._last_ingest is not None and time.monotonic() - self._last_ingest < min_interval_s:
            return
        if not self._ingesting.acquire(blocking=self._run is None):
            return
        try:
            self.ingest()
        finally:
            self._ingesting.release()

    @property
    def commit(self) -> object:
        """The processor commit loaded (changes when new data is)."""
        return self._run

    def memo(self, key: tuple[Any, ...], build: Callable[[], T]) -> T:
        """`build()`, computed once per commit for this key: a page's endpoints (and
        its polls until new data lands) share a session's events, steps and calls
        instead of each rebuilding them. Callers must not mutate the result."""
        with self._memo_lock:
            if self._memo_commit != self._run:
                self._memo.clear()
                self._memo_commit = self._run
            if key in self._memo:
                self._memo.move_to_end(key)
                return cast(T, self._memo[key])
        value = build()
        with self._memo_lock:
            if self._memo_commit == self._run:
                self._memo[key] = value
                while len(self._memo) > MEMO_SIZE:
                    self._memo.popitem(last=False)
        return value

    @property
    def skipped(self) -> list[str]:
        """Raw files the processor couldn't read (retried when they change)."""
        return sorted(self._unread)

    def close(self) -> None:
        self._cleanup()

    # --- queries -----------------------------------------------------------------------------

    def query(self, sql: str, params: Sequence[object] | None = None) -> list[tuple[Any, ...]]:
        # A cursor per query: DuckDB runs them in parallel and isolates them from loading.
        cur = self._con.cursor()
        try:
            return cur.execute(sql, params or []).fetchall()
        finally:
            cur.close()

    # --- raw rows ----------------------------------------------------------------------------

    def raw_rows(self, correlation_id: str) -> list[dict[str, Any]]:
        """Every raw row of one transaction, in the order the observer wrote them,
        read from its lineage (the pod's files from first_file to source_file; a
        few earlier ones too when its headers aren't there)."""
        found = self.query(
            "SELECT min(first_file), max(source_file) FROM calls WHERE correlation_id = ?", [correlation_id]
        )
        first, last = found[0] if found else (None, None)
        if not first:
            found = self.query(
                "SELECT min(source_file), max(source_file) FROM analytics WHERE correlation_id = ?", [correlation_id]
            )
            first, last = found[0] if found else (None, None)
        if not first:
            return []
        last = last or first
        a, b = raw.folder_of(first), raw.folder_of(last)
        first_name, last_name = first.rsplit("/", 1)[1], last.rsplit("/", 1)[1]
        # The pod's folder for every hour from the first file's to the last's (a
        # long stream or WebSocket connection spans several).
        keys = []
        for d in raw.pod_hours(a, b):
            keys += [d + n for n in self.raw.folder(d) if (d != a or n >= first_name) and (d != b or n <= last_name)]
        rows = self._read(keys, correlation_id)
        if not any(r.get("phase") == "request_headers" for r in rows):  # first_file is best effort
            earlier = [a + n for n in self.raw.folder(a) if n < first_name][-3:]
            rows = self._read(earlier, correlation_id) + rows
        return rows

    def raw_event(self, correlation_id: str, event_key: str, source_file: str) -> dict[str, Any] | None:
        """The raw client_event row an analytics event came from."""
        want = event_key.split("/", 1)[1] if "/" in event_key else ""
        # A repeat of an identical row is keyed <hash>-2, -3 (the processor's eventCopies):
        # any of the identical rows is it.
        want = _COPY.sub(r"\1", want)
        for line in raw.lines(self.raw.read(source_file)):
            try:
                r = json.loads(line)
            except ValueError:
                continue
            if r.get("correlation_id") != correlation_id or r.get("phase") != "client_event":
                continue
            ce = r.get("client_event") or {}
            ident = ce.get("id") or ("h" + hashlib.sha256(line).hexdigest()[:16])
            if ident == want and isinstance(r, dict):
                return r
        return None

    def _read(self, keys: list[str], correlation_id: str) -> list[dict[str, Any]]:
        out = []
        for k in keys:
            for line in raw.lines(self.raw.read(k)):
                if correlation_id.encode() not in line:
                    continue
                try:
                    r = json.loads(line)
                except ValueError:
                    continue
                if r.get("correlation_id") == correlation_id:
                    out.append(r)
        return out
