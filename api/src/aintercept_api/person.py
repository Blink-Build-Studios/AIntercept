"""A person's transactions, as the request drawer reads them (handoff §5.2.7), and the reference
the processor's request_rows are tested against. The header, summary and request log are SQL
over the processor's tables (rollups.py; docs/architecture.md, Decision 4).

A person's requests are every transaction made with one of their credential
hashes (key_hash): model calls, count_tokens, analytics batches, flag fetches
and account calls with no session. key_hash isn't an identity on its own (OAuth
tokens rotate), but each hash seen on the person's own calls or batches is theirs.
"""

import re
from collections.abc import Callable
from dataclasses import dataclass

from aintercept_api import claude_code as cc
from aintercept_api.store import Store

BACKGROUND_CLASSES = {"auxiliary", "compaction"}


def key_hashes(store: Store, session_ids: list[str]) -> list[str]:
    return [
        r[0]
        for r in store.query(
            """SELECT key_hash FROM cc_calls WHERE session_id IN (SELECT unnest(?::VARCHAR[])) AND key_hash IS NOT NULL
               UNION
               SELECT c.key_hash FROM calls c JOIN (SELECT DISTINCT correlation_id FROM cc_events
                 WHERE session_id IN (SELECT unnest(?::VARCHAR[]))) b USING (correlation_id)
               WHERE c.key_hash IS NOT NULL""",
            [session_ids, session_ids],
        )
    ]


def kind_of(path: str) -> str:
    p = path.split("?", 1)[0]
    # Codex's channels (handoff §11L.6): model calls, analytics events, OTel metrics,
    # usage & account (wham/*, accounts), catalog (plugins, models).
    if p == "/backend-api/codex/responses":
        return "model"
    if p.startswith("/backend-api/codex/analytics-events"):
        return "analytics"
    if p.startswith("/otlp/"):
        return "metrics"
    if p.startswith(("/backend-api/wham/", "/backend-api/accounts/")):
        return "usage"
    if p.startswith(("/backend-api/ps/", "/backend-api/plugins/", "/backend-api/codex/models")):
        return "catalog"
    if p.startswith("/v1/messages/count_tokens"):
        return "count"
    if p.startswith("/v1/messages"):
        return "model"
    if p.startswith("/api/event_logging"):
        return "analytics"
    if p.startswith("/api/eval/"):
        return "flags"
    return "account"


def model_family(model: str | None) -> str:
    if not model:
        return "—"
    m = re.match(r"claude-(opus|sonnet|haiku)", model)
    return m.group(1) if m else model


@dataclass
class Tx:
    correlation_id: str
    ts: int
    method: str | None
    path: str
    session_id: str | None
    prompt_id: str | None
    request_class: str | None
    status: int | None
    end_ts: int | None
    model: str | None
    stop_reason: str | None
    bytes_up: int | None
    bytes_down: int | None
    batch_events: int | None
    batch_session: str | None
    request_id: str | None
    client_request_id: str | None
    content_encoding: str | None
    error: str | None
    input: int | None
    output: int | None
    cache_read: int | None
    cache_write: int | None
    product: str | None = None
    client: str | None = None
    hidden_kind: str | None = None
    effort: str | None = None
    reasoning: int | None = None
    tool_uses_json: str | None = None
    ws_turn: int | None = None
    batch_name: str | None = None
    detail_json: str | None = None

    @property
    def kind(self) -> str:
        return kind_of(self.path)

    @property
    def session(self) -> str | None:
        return self.session_id or self.batch_session

    @property
    def background(self) -> bool:
        return self.kind in ("count", "analytics") or (self.request_class or "") in BACKGROUND_CLASSES

    def detail(self) -> str:
        k = self.kind
        if k == "model":
            return f"{self.request_class or '—'} · {model_family(self.model)} · {self.stop_reason or '—'}"
        if k == "analytics":
            n = self.batch_events or 0
            if self.product == "codex" and self.batch_name:  # its event, by name (handoff §11L.6)
                return self.batch_name if n == 1 else f"{self.batch_name} · {n} events"
            return f"{n} events"
        if k == "flags":
            return "feature flags"
        if k == "count":
            return "count_tokens"
        p = self.path.split("?", 1)[0]
        if k in ("usage", "catalog", "metrics"):  # Codex's endpoints, by name: wham/usage, ps/plugins/list
            return p.removeprefix("/backend-api/").lstrip("/") or p
        return p.rsplit("/", 1)[-1] or self.path


def transactions(store: Store, hashes: list[str], session_id: str | None = None) -> list[Tx]:
    """Every transaction made with these credentials; with `session_id`, only that
    session's (its own calls, and batches whose events are its). Shared: don't mutate it."""
    if not hashes:
        return []
    # Once per commit: a person's summary and requests both need them, every poll.
    return store.memo(("transactions", tuple(hashes), session_id), lambda: _transactions(store, hashes, session_id))


def _transactions(store: Store, hashes: list[str], session_id: str | None) -> list[Tx]:
    rows = store.query(
        """WITH c AS (
             -- Each coding agent's transactions: a WebSocket's turns, not the connection itself.
             SELECT * REPLACE (replace(call_id, '#', '~') AS correlation_id) FROM calls
             WHERE product IN ('claude_code', 'codex') AND NOT synthetic
               AND NOT (ws_turn IS NULL AND method = 'GET' AND path = '/backend-api/codex/responses')
               AND key_hash IN (SELECT unnest(?::VARCHAR[]))
           ), batch AS (
             -- An analytics batch's events (Claude Code's or Codex's) name its session.
             SELECT correlation_id, count(*) AS n, any_value(session_id) AS sid, min(name) AS first_name
             FROM (SELECT correlation_id, session_id, name FROM cc_events
                   UNION ALL SELECT correlation_id, session_id, name FROM codex_events)
             WHERE correlation_id IN (SELECT correlation_id FROM c) GROUP BY 1
           )
           SELECT c.correlation_id, c.ts, c.method, c.path, c.session_id, c.prompt_id, c.request_class,
                  c.status, c.end_ts, c.model, c.stop_reason, c.request_bytes, c.response_bytes, batch.n, batch.sid,
                  c.request_id, c.client_request_id, c.content_encoding, c.error,
                  c.input_tokens, c.output_tokens, c.cache_read_tokens, c.cache_write_tokens,
                  c.product, c.client, c.hidden_kind, c.effort, c.reasoning_tokens, c.tool_uses_json, c.ws_turn,
                  batch.first_name, c.detail_json
           FROM c LEFT JOIN batch USING (correlation_id)
           WHERE ?::VARCHAR IS NULL OR coalesce(c.session_id, batch.sid) = ?
           ORDER BY c.ts""",
        [hashes, session_id, session_id],
    )
    return [Tx(*r) for r in rows]


@dataclass
class Reported:
    """What a tengu_api_success says about its call."""

    ttft_ms: float | None
    cache_5m: int | None
    cache_1h: int | None


def reports(store: Store, session_ids: list[str]) -> dict[str, Reported]:
    """requestId and clientRequestId -> the call's tengu_api_success facts."""
    out: dict[str, Reported] = {}
    for rid, crid, ttft, c5, c1 in store.query(
        """SELECT json_extract_string(md, '$.requestId'), json_extract_string(md, '$.clientRequestId'),
                  TRY_CAST(json_extract_string(md, '$.ttftMs') AS DOUBLE),
                  TRY_CAST(json_extract_string(md, '$.cache_creation_5m_input_tokens') AS BIGINT),
                  TRY_CAST(json_extract_string(md, '$.cache_creation_1h_input_tokens') AS BIGINT)
           FROM cc_events WHERE name = 'tengu_api_success' AND session_id IN (SELECT unnest(?::VARCHAR[]))
           ORDER BY ts""",
        [session_ids],
    ):
        for key in (rid, crid):
            if key:
                out.setdefault(key, Reported(ttft, c5, c1))
    return out


RETRY_AFTER_NS = 30 * cc.NS  # tengu_api_retry is logged within seconds of the failed attempt's end


def nearest_retry[T](
    retries: list[T], key: Callable[[T], tuple[int, str | None]], ts: int, end: int | None, prompt: str | None
) -> T | None:
    """The tengu_api_retry for a failed attempt. It carries no request id, so: same prompt (when both have
    one), logged between the attempt's start and 30 s after its end; the nearest to its end."""
    stop = end or ts
    near = []
    for r in retries:
        at, pid = key(r)
        if ts <= at <= stop + RETRY_AFTER_NS and (prompt is None or pid in (None, prompt)):
            near.append((abs(at - stop), at, r))
    return min(near, key=lambda x: (x[0], x[1]))[2] if near else None


@dataclass
class Retry:
    ts: int
    session_id: str
    prompt_id: str | None
    attempt: int | None
    delay_ms: float | None


def retries(store: Store, session_ids: list[str]) -> dict[str, list[Retry]]:
    """session id -> its tengu_api_retry events, oldest first."""
    out: dict[str, list[Retry]] = {}
    for ts, sid, pid, attempt, delay in store.query(
        """SELECT ts, session_id, json_extract_string(md, '$.cc_prompt_id'),
                  TRY_CAST(json_extract_string(md, '$.attempt') AS INT),
                  TRY_CAST(json_extract_string(md, '$.delayMs') AS DOUBLE)
           FROM cc_events WHERE name = 'tengu_api_retry' AND session_id IN (SELECT unnest(?::VARCHAR[]))
           ORDER BY ts""",
        [session_ids],
    ):
        out.setdefault(sid, []).append(Retry(ts, sid, pid, attempt, delay))
    return out


def prompt_indexes(store: Store, session_ids: list[str]) -> dict[str, int]:
    """Prompt id -> the prompt's number in its session: Claude Code's from tengu_input_prompt, a
    Codex turn's from its place in the thread."""
    return {
        pid: idx
        for pid, idx in store.query(
            """SELECT json_extract_string(md, '$.cc_prompt_id'),
                      TRY_CAST(json_extract_string(md, '$.prompt_index') AS INT)
               FROM cc_events WHERE name = 'tengu_input_prompt' AND session_id IN (SELECT unnest(?::VARCHAR[]))
               UNION ALL
               SELECT prompt_id, row_number() OVER (PARTITION BY session_id ORDER BY min(ts), prompt_id)
               FROM cc_calls WHERE product = 'codex' AND prompt_id IS NOT NULL
                 AND session_id IN (SELECT unnest(?::VARCHAR[]))
               GROUP BY session_id, prompt_id""",
            [session_ids, session_ids],
        )
        if pid and idx is not None
    }
