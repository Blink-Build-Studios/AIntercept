"""Person header, summary and request log (handoff §5.2.3, §5.2.4, §5.2.7).

A person's requests are every transaction made with one of their credential
hashes (key_hash): model calls, count_tokens, analytics batches, flag fetches
and account calls with no session. key_hash isn't an identity on its own (OAuth
tokens rotate), but each hash seen on the person's own calls or batches is theirs.
"""

import re
from collections import Counter
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


# --- header -------------------------------------------------------------------------------------


@dataclass
class HeaderFacts:
    org: str | None
    plan: str | None
    devices: list[tuple[str, str | None, str | None, str | None]]
    limit_status: str | None
    utilization_7d: float | None
    overage: str | None
    sandbox: str | None
    proxy_detected: str | None


def header_facts(store: Store, person_id: str, session_ids: list[str], hashes: list[str]) -> HeaderFacts:
    org, plan = store.query(
        """SELECT any_value(organization_uuid) FILTER (WHERE organization_uuid IS NOT NULL),
                  arg_max(json_extract_string(md, '$.subscription_type'), ts)
                    FILTER (WHERE json_extract_string(md, '$.subscription_type') IS NOT NULL)
           FROM cc_events WHERE account_uuid = ?""",
        [person_id],
    )[0]
    devices = store.query(
        """SELECT device_id, arg_max(json_extract_string(env, '$.platform'), ts),
                  arg_max(json_extract_string(env, '$.terminal'), ts), arg_max(json_extract_string(env, '$.shell'), ts)
           FROM cc_events WHERE account_uuid = ? AND device_id IS NOT NULL GROUP BY 1 ORDER BY 1""",
        [person_id],
    )
    limits = store.query(
        """SELECT json_extract_string(provider_json, '$."anthropic-ratelimit-unified-status"'),
                  TRY_CAST(json_extract_string(provider_json, '$."anthropic-ratelimit-unified-7d-utilization"')
                           AS DOUBLE),
                  json_extract_string(provider_json, '$."anthropic-ratelimit-unified-overage-status"')
           FROM calls WHERE correlation_id IN (
               SELECT correlation_id FROM cc_calls WHERE session_id IN (SELECT unnest(?::VARCHAR[])))
             AND json_extract_string(provider_json, '$."anthropic-ratelimit-unified-status"') IS NOT NULL
           ORDER BY coalesce(response_start_ns, ts) DESC LIMIT 1""",
        [session_ids],
    )
    sandbox, proxy = store.query(
        """SELECT arg_max(json_extract_string(md, '$.sandbox_enabled'), ts)
                    FILTER (WHERE name = 'tengu_bash_tool_command_executed'),
                  max(json_extract_string(md, '$.gzipSkipReason')) FILTER (
                    WHERE name = 'tengu_api_success' AND json_extract_string(md, '$.gzipSkipReason') = 'custom_ca')
           FROM cc_events WHERE session_id IN (SELECT unnest(?::VARCHAR[]))""",
        [session_ids],
    )[0]
    status, util, overage = limits[0] if limits else (None, None, None)
    return HeaderFacts(
        org=org,
        plan=plan,
        devices=[tuple(d) for d in devices],
        limit_status=status,
        utilization_7d=util,
        overage=overage,
        sandbox={"true": "on", "false": "off"}.get(sandbox or ""),
        proxy_detected=proxy,
    )


# --- summary --------------------------------------------------------------------------------------


@dataclass
class Rollup:
    """Counts over a person's analytics events in a range."""

    events: int
    batches: int
    types: int
    names: Counter[str]
    tools: Counter[str]
    mcp_tools: Counter[str]
    words: Counter[str]
    destructive: Counter[str]
    outside_cwd: int
    git: Counter[str]
    prs: list[int]
    head_changes: int | None
    hooks: int
    hook_ms: float | None
    cache_5m: int | None
    cache_1h: int | None
    cost: float | None


# One pass per question, all over the same scope: the person's sessions in [lo, hi].
_TOTALS = """
SELECT count(*), count(DISTINCT correlation_id), count(DISTINCT name), count(DISTINCT head_sha),
       count(DISTINCT json_extract_string(md, '$.hookName')) FILTER (WHERE name = 'tengu_run_hook'),
       avg(TRY_CAST(json_extract_string(md, '$.preToolHookDurationMs') AS DOUBLE))
         FILTER (WHERE name = 'tengu_tool_use_success'),
       sum(TRY_CAST(json_extract_string(md, '$.cache_creation_5m_input_tokens') AS BIGINT))
         FILTER (WHERE name = 'tengu_api_success'),
       sum(TRY_CAST(json_extract_string(md, '$.cache_creation_1h_input_tokens') AS BIGINT))
         FILTER (WHERE name = 'tengu_api_success'),
       sum(TRY_CAST(json_extract_string(md, '$.costUSD') AS DOUBLE)) FILTER (WHERE name = 'tengu_api_success'),
       count(*) FILTER (WHERE name = 'tengu_bash_tool_command_executed'
                          AND json_extract_string(md, '$.destructive_target_scope') = 'outside_cwd')
FROM cc_events WHERE session_id IN (SELECT unnest(?::VARCHAR[])) AND ts BETWEEN ? AND ?
"""
# (kind, key) pairs for every counted dimension, in one grouped query.
_DIMENSIONS = """
SELECT 'name', name, count(*) FROM cc_events
WHERE session_id IN (SELECT unnest(?::VARCHAR[])) AND ts BETWEEN ? AND ? GROUP BY 1, 2
UNION ALL
SELECT CASE WHEN json_extract_string(md, '$.isMcp') = 'true' THEN 'mcp_tool' ELSE 'tool' END,
       json_extract_string(md, '$.toolName'), count(*) FROM cc_events
WHERE name = 'tengu_tool_use_success' AND session_id IN (SELECT unnest(?::VARCHAR[])) AND ts BETWEEN ? AND ?
GROUP BY 1, 2
UNION ALL
SELECT 'word', json_extract_string(md, '$.bash_argv0'), count(*) FROM cc_events
WHERE name = 'tengu_bash_tool_command_executed' AND session_id IN (SELECT unnest(?::VARCHAR[])) AND ts BETWEEN ? AND ?
GROUP BY 1, 2
UNION ALL
SELECT 'destructive', json_extract_string(md, '$.destructive_category'), count(*) FROM cc_events
WHERE name = 'tengu_bash_tool_command_executed' AND json_extract_string(md, '$.destructive_category') <> 'none'
  AND session_id IN (SELECT unnest(?::VARCHAR[])) AND ts BETWEEN ? AND ? GROUP BY 1, 2
UNION ALL
SELECT 'git', json_extract_string(md, '$.operation'), count(*) FROM cc_events
WHERE name = 'tengu_git_operation' AND session_id IN (SELECT unnest(?::VARCHAR[])) AND ts BETWEEN ? AND ?
GROUP BY 1, 2
UNION ALL
SELECT 'pr', json_extract_string(md, '$.prNumber'), count(*) FROM cc_events
WHERE name = 'tengu_session_linked_to_pr' AND session_id IN (SELECT unnest(?::VARCHAR[])) AND ts BETWEEN ? AND ?
GROUP BY 1, 2
"""


def event_rollup(store: Store, session_ids: list[str], lo: int, hi: int) -> Rollup:
    args = [session_ids, lo, hi]
    events, batches, types, heads, hooks, hook_ms, c5, c1, cost, outside = store.query(_TOTALS, args)[0]
    dims: dict[str, Counter[str]] = {
        k: Counter() for k in ("name", "tool", "mcp_tool", "word", "destructive", "git", "pr")
    }
    for kind, key, n in store.query(_DIMENSIONS, args * 6):
        if key is not None:
            dims[kind][key] += n
    tools = dims["tool"] + dims["mcp_tool"]
    return Rollup(
        events=events,
        batches=batches,
        types=types,
        names=dims["name"],
        tools=tools,
        mcp_tools=dims["mcp_tool"],
        words=dims["word"],
        destructive=dims["destructive"],
        outside_cwd=outside,
        git=dims["git"],
        prs=sorted(int(k) for k in dims["pr"] if k.isdigit()),
        head_changes=max(heads - 1, 0) if heads else None,
        hooks=hooks,
        hook_ms=hook_ms,
        cache_5m=c5,
        cache_1h=c1,
        cost=cost,
    )


def call_tokens(
    store: Store, session_ids: list[str], lo: int, hi: int
) -> tuple[int, int | None, int | None, int | None, int | None, int]:
    """(calls, input, cacheRead, cacheWrite, output, subagent calls) over captured calls in [lo, hi]."""
    r = store.query(
        """SELECT count(*), sum(input_tokens), sum(cache_read_tokens), sum(cache_write_tokens), sum(output_tokens),
                  count(*) FILTER (WHERE request_class = 'subagent')
           FROM cc_calls WHERE session_id IN (SELECT unnest(?::VARCHAR[])) AND ts BETWEEN ? AND ?""",
        [session_ids, lo, hi],
    )[0]
    return r[0], r[1], r[2], r[3], r[4], r[5]


def sessions_in_range(sessions: dict[str, cc.Session], lo: int, hi: int, now: int) -> list[cc.Session]:
    return [s for s in sessions.values() if s.start <= hi and (now if s.live(now) else s.last) >= lo]


# --- capabilities -------------------------------------------------------------------------------


def tool_uses(store: Store, session_ids: list[str], lo: int, hi: int) -> tuple[dict[str, set[str]], set[str]]:
    """The tools the model invoked in these sessions' calls, from their responses' tool_use
    blocks (the processor's tool_uses_json): MCP server -> tools, and the built-in tools.
    Claude Code's analytics redact most MCP tool names to "mcp_tool"; the responses don't."""
    mcp: dict[str, set[str]] = {}
    builtin: set[str] = set()
    for server, tool, kind in store.query(
        """SELECT u.server, u.tool, u.kind FROM requests r,
             unnest(from_json(r.tool_uses_json, '[{"server":"VARCHAR","tool":"VARCHAR","kind":"VARCHAR"}]')) AS x(u)
           WHERE r.session_id IN (SELECT unnest(?::VARCHAR[])) AND r.tool_uses_json IS NOT NULL
             AND coalesce(r.start_ns, r.end_ns) BETWEEN ? AND ?""",
        [session_ids, lo, hi],
    ):
        if kind == "mcp" and server and tool:
            mcp.setdefault(server, set()).add(tool)
        elif tool:
            builtin.add(tool)
    return mcp, builtin


def latest_md(store: Store, session_ids: list[str], event: str, key: str, hi: int) -> int | None:
    """The latest value of an event's numeric field up to `hi` (Claude Code's own counts:
    the skills it synced, the MCP tools in its catalog)."""
    rows = store.query(
        """SELECT arg_max(TRY_CAST(json_extract_string(md, '$.' || ?) AS BIGINT), ts) FROM cc_events
           WHERE name = ? AND session_id IN (SELECT unnest(?::VARCHAR[])) AND ts <= ?""",
        [key, event, session_ids, hi],
    )
    return rows[0][0] if rows and rows[0][0] is not None else None
