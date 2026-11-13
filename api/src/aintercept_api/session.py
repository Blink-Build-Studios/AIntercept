"""The session page (handoff §5.2.8-5.2.9): one Claude Code session's steps,
prompts, between rows, events, timeline lanes and range state.

Joins (docs/ui/handoff/context/DATA_ANSWERS.md §1):
- a step is a captured model call (cc_calls); its analytics are the
  tengu_api_success with the same requestId (else clientRequestId);
- a step's tool calls are the tengu_tool_use_* events carrying its requestId;
  a Bash call's exit code and destructive category come from
  tengu_bash_tool_command_executed by tool_use_id = toolUseID;
- a prompt is a cc_prompt_id: its tengu_input_prompt, the calls carrying it as
  x-claude-code-prompt-id, and the events carrying it;
- a compaction is tengu_reactive_compact_attempt → (the compaction call) →
  tengu_reactive_compact_succeeded; it is idle when tengu_idle_compact_fired
  started it. Token counts before and after come from the context series, since
  the compaction events' own metadata isn't registered yet (M7).

Content (prompt text, recaps) is never read: content retention is off by
default (docs/ui/BUILD_PLAN.md §2) and bodies aren't ingested.
"""

import json
from collections import defaultdict
from collections.abc import Sequence
from dataclasses import dataclass, field
from functools import cached_property
from typing import Any

from aintercept_api import claude_code as cc
from aintercept_api import person as pp
from aintercept_api import rows
from aintercept_api.models.people import McpServer, PromptText, TimelinePrompt, TokenCount
from aintercept_api.models.sessions import (
    BetweenData,
    BetweenItem,
    CapabilitiesAt,
    DestructiveMark,
    EventLink,
    EventRow,
    PromptData,
    PromptFriction,
    PromptItem,
    RangeCounts,
    Recap,
    RepoInRange,
    SessionState,
    StepData,
    ToolCallData,
    ToolTally,
    TurnReport,
)
from aintercept_api.store import Store

STREAMING_WITHIN_NS = 10 * 60 * cc.NS  # an unfinished call older than this never completed (aborted)

# Client internals hidden by default (handoff §5.2.9), matched without the tengu_ prefix.
INTERNAL_NAMES = {
    "declared_tool_set_held",
    "render_glyph_cardinality",
    "tree_sitter_parse_abort",
    "bash_ast_too_complex",
}
INTERNAL_PREFIXES = ("tether_", "wire_", "reminder_fold_", "spinner_")
TICK = "tengu_tool_use_progress"
# Ids and constants left out of an event row's summary: links carry the ids.
SUMMARY_SKIP = {
    "requestId",
    "clientRequestId",
    "cc_prompt_id",
    "toolUseID",
    "tool_use_id",
    "messageID",
    "message_id",
    "previousRequestId",
    "invokingRequestId",
    "queryChainId",
    "subscription_type",
    "rh",
}
SUMMARY_PAIRS = 6
SUMMARY_VALUE_CHARS = 40


# --- loading ----------------------------------------------------------------------------------


@dataclass
class Step:
    """One captured model call of the session."""

    correlation_id: str
    ts: int
    end_ts: int | None
    status: int | None
    prompt_id: str | None
    request_class: str | None
    agent_type: str | None
    request_id: str | None
    client_request_id: str | None
    model: str | None
    input: int | None
    output: int | None
    cache_read: int | None
    cache_write: int | None
    stop_reason: str | None
    ip: str | None
    tool_uses_json: str | None
    ws_turn: int | None
    response_bytes: int | None
    detail_json: str | None

    @property
    def context(self) -> int | None:
        """The context the call sent: input + cache read + cache write."""
        parts = (self.input, self.cache_read, self.cache_write)
        return None if all(p is None for p in parts) else sum(p or 0 for p in parts)

    def detail(self) -> str:
        return f"{self.request_class or '—'} · {pp.model_family(self.model)} · {self.stop_reason or '—'}"


def steps(store: Store, session_id: str) -> list[Step]:
    return store.memo(("steps", session_id), lambda: _steps(store, session_id))


def _steps(store: Store, session_id: str) -> list[Step]:
    return [
        Step(*r)
        for r in store.query(
            """SELECT correlation_id, ts, end_ts, status, prompt_id, request_class, agent_type, request_id,
                      client_request_id, model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
                      stop_reason, ip, tool_uses_json, ws_turn, response_bytes, detail_json
               FROM cc_calls WHERE session_id = ? ORDER BY ts, correlation_id""",
            [session_id],
        )
    ]


@dataclass
class Event:
    id: str
    """<batch correlation id>:<position in the whole batch, across its sessions>: unique and stable."""
    ts: int
    name: str
    device: str | None
    head: str | None
    md_json: str | None
    """The metadata as the client sent it; parsed only when read (`md`), since a list of
    events mostly needs names and times."""
    prompt_id: str | None

    @cached_property
    def md(self) -> dict[str, Any]:
        parsed = json.loads(self.md_json) if self.md_json else {}
        return parsed if isinstance(parsed, dict) else {}


def events(
    store: Store,
    session_id: str,
    names: Sequence[str] | None = None,
    *,
    about: tuple[str | None, Sequence[str], Sequence[str]] | None = None,
) -> list[Event]:
    """The session's analytics events, oldest first; all of them, or only `names`.
    `about` = (prompt id, request ids, tool use ids) keeps only the events about one
    call: its prompt's, those naming one of its ids, those of its tool uses, and the
    session's few stall summaries (matched by the caller: their request id is only in
    the JSON). A session can hold 100k events; one call needs a handful. Ids are the
    same either way."""
    if about is None:  # whole-session reads repeat across a page's endpoints: once per commit
        return store.memo(
            ("events", session_id, tuple(names) if names is not None else None),
            lambda: _events(store, session_id, names, None),
        )
    return _events(store, session_id, names, about)


def _events(
    store: Store,
    session_id: str,
    names: Sequence[str] | None,
    about: tuple[str | None, Sequence[str], Sequence[str]] | None,
) -> list[Event]:
    prompt, ids, tools = about or (None, [], [])
    want = [names is None, list(names or []), about is None, prompt, list(ids), list(ids), list(tools)]
    out = []
    for eid, ts, name, dev, head, md, prompt_id in store.query(
        """SELECT correlation_id || ':' || n, ts, name, device_id, head_sha, md::VARCHAR, prompt_id FROM (
             SELECT correlation_id, ts, coalesce(name, '(unnamed)') AS name, device_id, head_sha, md, prompt_id,
                    session_id, request_id, client_request_id, tool_use_id,
                    row_number() OVER (PARTITION BY correlation_id ORDER BY seq, row_ts, ts, name, md::VARCHAR, event_key) AS n
             FROM cc_events
             -- Number only the batches holding a wanted event (numbering is per batch).
             WHERE correlation_id IN (
               SELECT correlation_id FROM cc_events
               WHERE session_id = ? AND (? OR name IN (SELECT unnest(?::VARCHAR[])))
                 AND (? OR prompt_id = ?
                      OR request_id IN (SELECT unnest(?::VARCHAR[]))
                      OR client_request_id IN (SELECT unnest(?::VARCHAR[]))
                      OR tool_use_id IN (SELECT unnest(?::VARCHAR[]))
                      OR name = 'tengu_streaming_stall_summary')))
           WHERE session_id = ? AND (? OR name IN (SELECT unnest(?::VARCHAR[])))
             AND (? OR prompt_id = ?
                  OR request_id IN (SELECT unnest(?::VARCHAR[]))
                  OR client_request_id IN (SELECT unnest(?::VARCHAR[]))
                  OR tool_use_id IN (SELECT unnest(?::VARCHAR[]))
                  OR name = 'tengu_streaming_stall_summary')
           ORDER BY ts, correlation_id, n""",
        [session_id, *want, session_id, *want],
    ):
        out.append(Event(eid, ts, name, dev, head, md, prompt_id))
    return out


def _str(v: Any) -> str | None:
    return None if v is None or isinstance(v, dict | list) else str(v)


def _int(v: Any) -> int | None:
    try:
        return int(float(v)) if v is not None and not isinstance(v, bool) else None
    except (TypeError, ValueError):
        return None


def _float(v: Any) -> float | None:
    try:
        return float(v) if v is not None and not isinstance(v, bool) else None
    except (TypeError, ValueError):
        return None


def _true(v: Any) -> bool:
    return v is True or v == "true"


def _destructive(e: Event) -> str | None:
    cat = _str(e.md.get("destructive_category"))
    return cat if cat not in (None, "none") else None


# --- header -----------------------------------------------------------------------------------


# --- timeline lanes ---------------------------------------------------------------------------


# The marks of the sessions in [lo, hi] (None: all), from the processor's session_marks.
MARKS = """FROM session_marks WHERE kind = ? AND session_id IN (SELECT unnest(?::VARCHAR[]))
            AND (? OR t_ns BETWEEN ? AND ?)"""


def marks_params(kind: str, sids: list[str], lo: int | None, hi: int | None) -> list[object]:
    return [kind, sids, lo is None or hi is None, lo or 0, hi or 0]


def gap_windows(sids: list[str], lo: int | None, hi: int | None) -> tuple[str, list[object]]:
    """A `windows` CTE: the not-captured calls in [lo, hi] grouped into (a, b, n) windows, split at
    gaps of 15 minutes or more."""
    return (
        f"""windows AS (
              SELECT min(t_ns) AS a, max(t_ns) AS b, count(*) AS n FROM (
                SELECT t_ns, count_if(prev IS NULL OR t_ns - prev >= {cc.GAP_SPLIT_NS})
                              OVER (ORDER BY t_ns ROWS UNBOUNDED PRECEDING) AS w
                FROM (SELECT t_ns, lag(t_ns) OVER (ORDER BY t_ns) AS prev {MARKS}))
              GROUP BY w)""",  # noqa: S608 (fixed text; values are parameters)
        marks_params("missed", sids, lo, hi),
    )


def compactions(store: Store, session_id: str) -> list[dict[str, Any]]:
    """The session's compactions (the prompt list's compaction rows), oldest first."""
    return store.query_dicts(
        "SELECT * FROM session_marks WHERE session_id = ? AND kind = 'compaction' ORDER BY seq", [session_id]
    )


def context_points(store: Store, session_id: str) -> list[dict[str, Any]]:
    """The context each main-thread call sent."""
    return store.query_dicts(
        """SELECT * FROM session_marks WHERE session_id = ? AND kind = 'call' AND request_class = 'main'
             AND context IS NOT NULL ORDER BY seq""",
        [session_id],
    )


def permission(store: Store, session_id: str, end: int) -> list[dict[str, Any]]:
    """permissionMode as a state band: a segment per run of the same mode; the last one ends at `end`."""
    return store.query_dicts(
        """SELECT t_ns AS a, coalesce(to_ns, greatest(?, t_ns)) AS b, mode, prev_mode FROM session_marks
           WHERE session_id = ? AND kind = 'permission' ORDER BY seq""",
        [end, session_id],
    )


def networks(store: Store, session_id: str, end: int) -> list[dict[str, Any]]:
    """A segment per run of calls from the same client IP; the last one ends at `end`. Bypassed: a
    not-captured window overlaps it; notCaptured: the not-captured calls in it (segments share their
    boundaries: the last one includes its end)."""
    cte, params = gap_windows([session_id], None, None)
    return store.query_dicts(
        f"""WITH {cte},
            s AS (SELECT seq, t_ns AS a, coalesce(to_ns, greatest(?, t_ns)) AS b, to_ns IS NULL AS last, ip, ipv6
                  FROM session_marks WHERE session_id = ? AND kind = 'network')
            SELECT a, b, ip, ipv6,
                   EXISTS (SELECT 1 FROM windows w WHERE w.a <= s.b AND w.b >= s.a) AS bypassed,
                   (SELECT count(*) FROM session_marks m WHERE m.session_id = ? AND m.kind = 'missed'
                      AND m.t_ns >= s.a AND (m.t_ns < s.b OR s.last AND m.t_ns = s.b)) AS not_captured
            FROM s ORDER BY seq""",  # noqa: S608 (fixed text; values are parameters)
        [*params, end, session_id, session_id],
    )


def _count(store: Store, session_id: str, kind: str, lo: int, hi: int) -> int:
    return int(
        store.query(
            "SELECT count(*) FROM session_marks WHERE session_id = ? AND kind = ? AND t_ns BETWEEN ? AND ?",
            [session_id, kind, lo, hi],
        )[0][0]
    )


# --- prompts and between rows -----------------------------------------------------------------


@dataclass
class _Joins:
    """The session's events indexed for the step and prompt builders."""

    success: dict[str, Event]  # requestId or clientRequestId -> tengu_api_success
    tools: dict[str, list[Event]]  # requestId -> tool_use success/error
    ticks: dict[str, list[Event]]  # requestId -> tool_use_progress
    bash: dict[str, Event]  # tool_use_id -> bash_tool_command_executed
    by_prompt: dict[str, list[Event]]
    last_event: int | None
    results: dict[str, list[int]] = field(default_factory=dict)
    """Each tool call's exit codes, by call id (recorded bodies only)."""


def _joins(evs: list[Event]) -> _Joins:
    j = _Joins({}, defaultdict(list), defaultdict(list), {}, defaultdict(list), max((e.ts for e in evs), default=None))
    for e in evs:
        rid = _str(e.md.get("requestId"))
        if e.name == "tengu_api_success":
            for key in (rid, _str(e.md.get("clientRequestId"))):
                if key:
                    j.success.setdefault(key, e)
        elif e.name in ("tengu_tool_use_success", "tengu_tool_use_error") and rid:
            j.tools[rid].append(e)
        elif e.name == TICK and rid:
            j.ticks[rid].append(e)
        elif e.name == "tengu_bash_tool_command_executed" and e.md.get("tool_use_id"):
            j.bash[str(e.md["tool_use_id"])] = e
        if e.prompt_id:
            j.by_prompt[e.prompt_id].append(e)
    return j


def _mcp_server(e: Event) -> str | None:
    name = _str(e.md.get("toolName")) or ""
    parts = name.split("__")
    return _str(e.md.get("mcpServerName")) or (parts[1] if name.startswith("mcp__") and len(parts) >= 3 else None)


def _tool(e: Event, j: _Joins) -> ToolCallData:
    tid = _str(e.md.get("toolUseID"))
    bash = j.bash.get(tid) if tid else None
    cat = _destructive(bash) if bash else None
    return ToolCallData(
        toolUseId=tid,
        name=_str(e.md.get("toolName")) or "—",
        mcpServer=_mcp_server(e),
        target=None,  # from the tool input: M7 extraction
        command=_str(bash.md.get("bash_argv0")) if bash else None,
        status="ok" if e.name == "tengu_tool_use_success" else "error",
        durationMs=_float(e.md.get("durationMs")),
        exitCode=_int(bash.md.get("exit_code")) if bash else None,
        resultBytes=_int(e.md.get("toolResultSizeBytes")),
        destructive=DestructiveMark(category=cat, scope=_str(bash.md.get("destructive_target_scope")))
        if bash and cat
        else None,
    )


# --- prompts and between rows (the processor's prompt_rows, prompt_steps) ---------------------

# What depends on the time of the request, from the inputs the processor stored (its
# promptrows.go). `c.now` is the request's time.
_LIVE = f"(c.now - r.session_last_ns <= {cc.LIVE_WITHIN_NS})"
_IN_FLIGHT = f"""({_LIVE} AND (r.open_tail
                OR coalesce(c.now - r.unfinished_max_ns <= {STREAMING_WITHIN_NS}, false)))"""
_DURATION = f"""CASE WHEN r.duration_turn_ms IS NOT NULL AND NOT {_IN_FLIGHT}
                     THEN r.duration_turn_ms ELSE r.duration_report_ms END"""
_PROMPT_COLUMNS = f"r.*, {_IN_FLIGHT} AS in_flight, {_DURATION} AS duration_ms"


def _text(r: dict[str, Any]) -> PromptText:
    return PromptText(
        retained=r["text_retained"],
        text=r["text"],
        chars=r["text_chars"],
        pastedBytes=r["pasted_bytes"],
        images=r["images"],
    )


def _step_from_table(r: dict[str, Any]) -> StepData:
    """A prompt_steps row as the API's StepData: field copies."""
    return StepData(
        requestId=r["request_id"],
        t=rows.dt(r["t_ns"]),
        kind=r["kind"],
        model=r["model"],
        requestClass=r["request_class"],
        tokens=TokenCount(
            input=r["tokens_input"],
            cacheRead=r["tokens_cache_read"],
            cacheWrite=r["tokens_cache_write"],
            cacheWrite5m=r["tokens_cache_write_5m"],
            cacheWrite1h=r["tokens_cache_write_1h"],
            output=r["tokens_output"],
            accuracy=r["tokens_accuracy"],
        ),
        durationMs=r["duration_ms"],
        ttftMs=r["ttft_ms"],
        stopReason=r["stop_reason"],
        costUsd=r["cost_usd"],
        status=r["status"],
        eventsPending=r["events_pending"],
        agentType=r["agent_type"],
        tools=json.loads(r["tools"]),
        does=r["does"],
        commands=list(r["commands"]),
        patches=json.loads(r["patches_json"]),
        text=r["text"],
        exitCodes=list(r["exit_codes"]),
        usageMissing=r["usage_missing"],
    )


def _item_from_table(r: dict[str, Any], steps: list[StepData]) -> PromptItem | BetweenItem:
    """A prompt_rows row (with its steps) as the API's PromptItem or BetweenItem: field copies."""
    if r["type"] == "between":
        return BetweenItem(
            type="between",
            between=BetweenData(
                kind=r["between_kind"],
                t=rows.dt(r["t_ns"]),
                detail=r["detail"],
                requestId=r["request_id"],
                tokensFrom=r["tokens_from"],
                tokensTo=r["tokens_to"],
                waitMs=r["wait_ms"],
            ),
        )
    report = r["turn_report_json"]
    prompt = PromptData(
        promptId=r["prompt_id"],
        index=r["prompt_index"],
        t=rows.dt(r["t_ns"]),
        durationMs=r["duration_ms"],
        text=_text(r),
        steps=r["steps"],
        tokensIn=r["tokens_in"],
        tokensOut=r["tokens_out"],
        toolCounts=json.loads(r["tool_counts_json"]),
        friction=PromptFriction(frustrated=r["frustrated"], interrupted=r["interrupted"], destructive=r["destructive"]),
        inFlight=r["in_flight"],
        turnReport=TurnReport.model_validate_json(report) if report else None,
        hidden=json.loads(r["hidden_json"]),
    )
    return PromptItem(type="prompt", prompt=prompt, steps=steps)


def prompt_items(
    store: Store, session_id: str, now: int, lo: int, hi: int, page: int, size: int, prompt: int | None
) -> tuple[list[PromptItem | BetweenItem], int, int]:
    """One page of the session's prompts (steps inline) and between rows in [lo, hi], oldest first:
    (items, total, page). `prompt`: the page holding that prompt number, in place of `page`."""
    page_rows = store.query_dicts(
        f"""WITH c AS (SELECT ?::BIGINT AS now),
            items AS (
              SELECT {_PROMPT_COLUMNS}, row_number() OVER (ORDER BY r.seq) AS pos, count(*) OVER () AS total,
                     min(CASE WHEN r.type = 'prompt' AND r.prompt_index = ? THEN r.seq END) OVER () AS at_seq
              FROM prompt_rows r, c
              WHERE r.session_id = ? AND r.t_ns // 1000 * 1000 BETWEEN ? AND ?),
            paged AS (
              SELECT *, coalesce((max(CASE WHEN seq = at_seq THEN pos END) OVER () - 1) // ? + 1, ?) AS page_no
              FROM items)
            SELECT * FROM paged WHERE pos > (page_no - 1) * ? AND pos <= page_no * ? ORDER BY seq""",  # noqa: S608
        [now, prompt, session_id, lo, hi, size, page, size, size],
    )
    if not page_rows:  # past the end: no rows carry the count
        total, at = store.query(
            """SELECT count(*), min(pos) FILTER (WHERE type = 'prompt' AND prompt_index = ?) FROM (
                 SELECT type, prompt_index, row_number() OVER (ORDER BY seq) AS pos FROM prompt_rows
                 WHERE session_id = ? AND t_ns // 1000 * 1000 BETWEEN ? AND ?)""",
            [prompt, session_id, lo, hi],
        )[0]
        return [], total, (at - 1) // size + 1 if at is not None else page
    seqs = [r["seq"] for r in page_rows if r["type"] == "prompt"]
    steps: dict[int, list[StepData]] = defaultdict(list)
    for r in store.query_dicts(
        f"""WITH c AS (SELECT ?::BIGINT AS now)
            SELECT s.*,
                   CASE WHEN s.end_ns IS NULL
                        THEN CASE WHEN {_LIVE} AND c.now - s.t_ns <= {STREAMING_WITHIN_NS}
                                  THEN 'streaming' ELSE 'error' END
                        WHEN s.failed THEN 'error' ELSE 'done' END AS status,
                   {_LIVE} AND s.events_pending_if_live AS events_pending,
                   CASE WHEN {_LIVE} AND s.tools_live_json IS NOT NULL THEN s.tools_live_json
                        ELSE s.tools_json END AS tools,
                   CASE WHEN {_LIVE} AND s.content_live THEN s.commands_live ELSE s.commands END AS commands,
                   CASE WHEN {_LIVE} AND s.content_live THEN s.exit_codes_live ELSE s.exit_codes END AS exit_codes
            FROM prompt_steps s JOIN prompt_rows r USING (session_id, seq), c
            WHERE s.session_id = ? AND s.seq IN (SELECT unnest(?::BIGINT[]))
            ORDER BY s.seq, s.step""",  # noqa: S608 (the clauses are fixed text; values are parameters)
        [now, session_id, seqs],
    ):
        steps[r["seq"]].append(_step_from_table(r))
    first = page_rows[0]
    return [_item_from_table(r, steps[r["seq"]]) for r in page_rows], first["total"], first["page_no"]


def timeline_prompts(
    store: Store, session_ids: Sequence[str], now: int, lo: int | None, hi: int | None
) -> list[TimelinePrompt]:
    """One mark per typed prompt of the sessions (its tengu_input_prompt was captured, or it's a
    Codex turn) in [lo, hi] (None: all), by time then session."""
    return [
        TimelinePrompt(
            t=rows.dt(r["t_ns"]),
            index=r["prompt_index"],
            sessionId=r["session_id"],
            promptId=r["prompt_id"],
            text=_text(r),
            steps=r["steps"],
            durationMs=r["duration_ms"],
            tokensIn=r["tokens_in"],
            tokensOut=r["tokens_out"],
        )
        for r in store.query_dicts(
            f"""WITH c AS (SELECT ?::BIGINT AS now)
                SELECT {_PROMPT_COLUMNS} FROM prompt_rows r, c
                WHERE r.typed AND r.session_id IN (SELECT unnest(?::VARCHAR[]))
                  AND (? OR r.t_ns // 1000 * 1000 BETWEEN ? AND ?)
                ORDER BY r.t_ns // 1000, r.session_id, r.seq""",  # noqa: S608
            [now, list(session_ids), lo is None, lo, hi],
        )
    ]


# --- requests ---------------------------------------------------------------------------------

# The Requests tab is SQL over the processor's session_request_rows (its requestrows.go
# sessionRequestRows): the request log's rows built over the session alone, and a Codex
# thread's client calls no thread names, by time.

# --- events -----------------------------------------------------------------------------------


def is_internal(name: str) -> bool:
    n = name.removeprefix("tengu_")
    return n in INTERNAL_NAMES or n.startswith(INTERNAL_PREFIXES)


def _summary(md: dict[str, Any]) -> str:
    pairs = []
    for k, v in md.items():
        if k in SUMMARY_SKIP or v is None or isinstance(v, dict | list):
            continue
        text = str(v).lower() if isinstance(v, bool) else str(v)
        pairs.append(f"{k} {text[:SUMMARY_VALUE_CHARS]}{'…' if len(text) > SUMMARY_VALUE_CHARS else ''}")
        if len(pairs) == SUMMARY_PAIRS:
            break
    return " · ".join(pairs)


def _link(e: Event, sid: str, by_rid: dict[str, Step], index: dict[str, int]) -> EventLink:
    rid = _str(e.md.get("requestId"))
    tool = _str(e.md.get("toolUseID")) or _str(e.md.get("tool_use_id"))
    if e.name == "tengu_event_loop_stall" and e.device:
        return EventLink(kind="device", id=e.device, label="device")
    if tool:
        return EventLink(kind="tool", id=tool, label=_str(e.md.get("toolName")) or "tool")
    if rid:
        step = by_rid.get(rid)
        if step:
            return EventLink(kind="request", id=step.correlation_id, label=step.detail())
        return EventLink(kind="request", id=f"reported:{rid}", label="not captured")
    if e.prompt_id:
        idx = index.get(e.prompt_id)
        return EventLink(kind="prompt", id=e.prompt_id, label=f"#{idx}" if idx is not None else "prompt")
    return EventLink(kind="session", id=sid, label="session")


def _marker(e: Event) -> str | None:
    if e.name == "tengu_bash_tool_command_executed" and _destructive(e):
        return "destructive"
    if e.name == "tengu_input_prompt" and _true(e.md.get("is_negative")):
        return "frustrated"
    return None


# The Events tab is SQL over the processor's event_rows (its eventrows.go): the rows are stored
# as shown, so a request only filters them (range, internals, `q` against the name and summary,
# lowered as Python lowers them), folds consecutive tool-progress ticks and pages.
_EVENTS = """
WITH f AS (SELECT * FROM event_rows WHERE session_id = ? AND ts_ns BETWEEN ? AND ?),
s AS (SELECT * FROM f WHERE (? OR NOT internal) AND (? = '' OR contains(name_lower, ?) OR contains(summary_lower, ?))),
h AS (SELECT *, CASE WHEN ? AND name = 'tengu_tool_use_progress' AND lag(name) OVER (ORDER BY seq) = name
                     THEN 0 ELSE 1 END AS head FROM s),
g AS (SELECT *, sum(head) OVER (ORDER BY seq ROWS UNBOUNDED PRECEDING) AS grp FROM h)"""


def event_rows(
    store: Store,
    session_id: str,
    lo: int,
    hi: int,
    internal: bool,
    collapse: bool,
    q: str | None,
    page: int,
    page_size: int,
) -> tuple[int, int, list[EventRow]]:
    """(events in range, rows to show, the page's rows)."""
    q = q.lower() if q else ""
    params = [session_id, lo, hi, internal, q, q, q, collapse]
    total, shown = store.query(
        f"{_EVENTS} SELECT (SELECT count(*) FROM f), (SELECT coalesce(max(grp), 0) FROM g)",  # noqa: S608
        params,
    )[0]
    out = [
        EventRow(
            eventId=r["event_id"],
            t=rows.dt(r["ts_ns"]),
            name=r["name"],
            promptIndex=r["prompt_index"],
            linkedTo=EventLink(kind=r["link_kind"], id=r["link_id"], label=r["link_label"]),
            summary=r["summary"],
            marker=r["marker"],
            internal=r["internal"],
            collapsedCount=r["n"] if r["n"] > 1 else None,
        )
        for r in store.query_dicts(
            f"""{_EVENTS} SELECT g.*, n FROM g JOIN (SELECT grp, count(*) AS n FROM g GROUP BY grp) USING (grp)
                WHERE head = 1 AND grp > ? AND grp <= ? ORDER BY seq""",  # noqa: S608
            [*params, (page - 1) * page_size, page * page_size],
        )
    ]
    return total, shown, out


# --- state ------------------------------------------------------------------------------------

# What the state readout counts of the session's events, as of `hi` (capabilities) or within
# [lo, hi]: SQL over the processor's event_rows and session_tools.
_STATE = """
SELECT count(*) > 0,
       count(*) FILTER (WHERE ts_ns BETWEEN $2 AND $3),
       count(DISTINCT hook) FILTER (WHERE ts_ns <= $3),
       count(*) FILTER (WHERE git_op = 'commit' AND ts_ns BETWEEN $2 AND $3),
       count(*) FILTER (WHERE git_op = 'push' AND ts_ns BETWEEN $2 AND $3),
       coalesce(list(DISTINCT pr ORDER BY pr) FILTER (WHERE pr IS NOT NULL AND ts_ns BETWEEN $2 AND $3), []),
       coalesce(bool_or(recap_key), false),
       count(*) FILTER (WHERE recap),
       coalesce(list(ts_ns ORDER BY seq) FILTER (WHERE recap AND ts_ns BETWEEN $2 AND $3), []),
       count(*) FILTER (WHERE name = 'tengu_deferred_tools_pool_change' AND ts_ns BETWEEN $2 AND $3),
       arg_max(count, (ts_ns, count)) FILTER (WHERE name = 'tengu_tool_search_mode_decision' AND ts_ns <= $3),
       arg_max(count, (ts_ns, count)) FILTER (WHERE name = 'tengu_skills_sync_success' AND ts_ns <= $3),
       arg_min(head_sha, (ts_ns, head_sha)) FILTER (WHERE head_sha IS NOT NULL AND ts_ns BETWEEN $2 AND $3),
       arg_max(head_sha, (ts_ns, head_sha)) FILTER (WHERE head_sha IS NOT NULL AND ts_ns BETWEEN $2 AND $3),
       count(DISTINCT head_sha) FILTER (WHERE ts_ns BETWEEN $2 AND $3)
FROM event_rows WHERE session_id = $1"""


def state(store: Store, session_id: str, lo: int, hi: int, requests: int) -> SessionState:
    """`requests` is the Requests tab's total for the same range (its default range differs: see the router)."""
    (
        has_events,
        n_events,
        hooks,
        commits,
        pushes,
        prs,
        reports_recaps,
        recaps_total,
        recaps,
        pool,
        mcp_total,
        skills,
        head_from,
        head_to,
        heads,
    ) = store.query(_STATE, [session_id, lo, hi])[0]
    tools = store.query(
        """SELECT kind, server, count(DISTINCT tool) FROM session_tools WHERE session_id = ? AND first_ns <= ?
           GROUP BY ALL ORDER BY kind, server""",
        [session_id, hi],
    )
    builtin = next((n for kind, _, n in tools if kind == "builtin"), 0)
    mcp = [(server, n) for kind, server, n in tools if kind == "mcp"]
    prompts = store.query(
        """SELECT count(*) FROM prompt_rows
           WHERE session_id = ? AND type = 'prompt' AND t_ns // 1000 * 1000 BETWEEN ? AND ?""",
        [session_id, lo, hi],
    )[0][0]
    return SessionState(
        capabilitiesAt=CapabilitiesAt(
            builtin=ToolTally(total=None, used=builtin if has_events else None),  # total: M7 tool catalog
            mcpServers=[McpServer(name=k, tools=None, toolsUsed=n, riskyVerbs=[]) for k, n in mcp],
            mcpTools=ToolTally(total=mcp_total, used=sum(n for _, n in mcp) if has_events or mcp else None),
            skills=skills,
            hooks=hooks if has_events else None,
            poolChangesInRange=pool if has_events else None,
        ),
        repoInRange=RepoInRange(
            branch=None,  # M7: from injected git status
            headFrom=head_from,
            headTo=head_to,
            headChanges=heads - 1 if heads else None,
            commits=commits,
            pushed=pushes > 0,
            cwdTrail=None,  # M7: from environment-update messages
            prs=list(prs),
        ),
        recapsInRange=[Recap(t=rows.dt(t), text=None) for t in recaps] if reports_recaps else None,
        recapsTotal=recaps_total if reports_recaps else None,
        experimentsAt=None,  # M7: experiment assignments come from flag bodies
        flagFetchesInRange=None,  # M7: flag fetches carry the session only in their body
        flagsChangedInRange=None,
        counts=RangeCounts(
            prompts=prompts,
            modelCalls=_count(store, session_id, "call", lo, hi),
            requests=requests,
            events=n_events,
            compactions=_count(store, session_id, "compaction", lo, hi),
        ),
    )
