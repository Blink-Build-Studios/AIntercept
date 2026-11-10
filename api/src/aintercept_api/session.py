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
from collections import Counter, defaultdict
from collections.abc import Sequence
from dataclasses import dataclass, field
from functools import cached_property
from typing import Any, Literal

from aintercept_api import agents, rows
from aintercept_api import claude_code as cc
from aintercept_api import person as pp
from aintercept_api.models.people import DeviceInfo, McpServer, PromptText, RequestRow, TokenCount
from aintercept_api.models.sessions import (
    BetweenData,
    BetweenItem,
    CapabilitiesAt,
    DestructiveMark,
    EventLink,
    EventRow,
    PatchFile,
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
IDLE_LEAD_NS = 10 * cc.NS  # tengu_idle_compact_fired precedes its compaction attempt by well under a second

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

COMPACTION_EVENTS = ("tengu_reactive_compact_attempt", "tengu_reactive_compact_succeeded", "tengu_idle_compact_fired")
PROMPT_EVENTS = (
    "tengu_input_prompt",
    "tengu_turn_end",
    "tengu_cancel",
    "tengu_bash_tool_command_executed",
    "tengu_tool_use_success",
    "tengu_tool_use_error",
    TICK,
    "tengu_api_success",
    "tengu_paste_text",
    "tengu_paste_image",
    "tengu_return_to_session",
    *COMPACTION_EVENTS,
)
STATE_EVENTS = (
    "tengu_tool_use_success",
    "tengu_run_hook",
    "tengu_deferred_tools_pool_change",
    "tengu_git_operation",
    "tengu_session_linked_to_pr",
    "tengu_return_to_session",
    *COMPACTION_EVENTS,
)
REQUEST_CLASSES = ("main", "subagent", "auxiliary", "compaction")


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
                    row_number() OVER (PARTITION BY correlation_id ORDER BY seq, row_ts, ts, name, md::VARCHAR) AS n
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


def start_of(s: cc.Session) -> int:
    """The session's start, truncated to the microseconds every response time carries."""
    return s.start - s.start % 1000


def end_of(s: cc.Session, now: int) -> int:
    """The session's open interval ends now while it is live."""
    return now if s.live(now) else s.last


# --- header -----------------------------------------------------------------------------------


@dataclass
class HeaderFacts:
    person: str | None
    device: DeviceInfo | None
    networks: int | None


def header_facts(store: Store, s: cc.Session) -> HeaderFacts:
    person = store.query(
        """SELECT coalesce(
             (SELECT any_value(account_uuid) FROM cc_events WHERE session_id = ? AND account_uuid IS NOT NULL),
             (SELECT any_value(account_uuid) FROM cc_calls WHERE session_id = ? AND account_uuid IS NOT NULL))""",
        [s.id, s.id],
    )[0][0]
    platform, terminal, shell = store.query(
        """SELECT arg_max(json_extract_string(env, '$.platform'), ts),
                  arg_max(json_extract_string(env, '$.terminal'), ts),
                  arg_max(json_extract_string(env, '$.shell'), ts)
           FROM cc_events WHERE session_id = ?""",
        [s.id],
    )[0]
    ips = store.query("SELECT count(DISTINCT ip) FROM cc_calls WHERE session_id = ?", [s.id])[0][0]
    dev = s.device()
    return HeaderFacts(
        person=person,
        device=DeviceInfo(deviceId=dev, platform=platform, terminal=terminal, shell=shell) if dev else None,
        networks=ips or None,
    )


# --- timeline lanes ---------------------------------------------------------------------------


@dataclass
class CompactionFact:
    t: int
    start: int
    tokens_from: int | None
    tokens_to: int | None
    idle: bool
    wait_ms: float | None
    request_id: str | None


def compactions(evs: list[Event], st: list[Step]) -> list[CompactionFact]:
    main = [(x.ts, x.context) for x in st if x.request_class == "main" and x.context is not None]
    attempts = [e.ts for e in evs if e.name == "tengu_reactive_compact_attempt"]
    fired = [e.ts for e in evs if e.name == "tengu_idle_compact_fired"]
    out: list[CompactionFact] = []
    prev = -1
    for e in (e for e in evs if e.name == "tengu_reactive_compact_succeeded"):
        attempt = max((a for a in attempts if prev < a <= e.ts), default=None)
        start = attempt if attempt is not None else e.ts
        call = next((x for x in st if x.request_class == "compaction" and start <= x.ts <= e.ts), None)
        out.append(
            CompactionFact(
                t=e.ts,
                start=start,
                tokens_from=next((c for t, c in reversed(main) if t < start), None),
                tokens_to=next((c for t, c in main if t > e.ts), None),
                idle=any(start - IDLE_LEAD_NS <= f <= start for f in fired),
                wait_ms=round((e.ts - attempt) / 1e6, 1) if attempt is not None else None,
                request_id=call.correlation_id if call else None,
            )
        )
        prev = e.ts
    return out


def permission(evs: list[Event], end: int) -> list[tuple[int, int, str]]:
    """permissionMode as a state band: a segment per run of the same mode."""
    segs: list[list[Any]] = []
    for e in evs:
        mode = _str(e.md.get("permissionMode")) if e.name == "tengu_api_query" else None
        if not mode or (segs and segs[-1][2] == mode):
            continue
        if segs:
            segs[-1][1] = e.ts
        segs.append([e.ts, max(end, e.ts), mode])
    return [(a, b, m) for a, b, m in segs]


def networks(st: list[Step], end: int, gaps: list[tuple[int, int, int]]) -> list[tuple[int, int, str, bool, bool]]:
    """(from, to, ip, ipv6, bypassed) per run of calls from the same client IP."""
    segs: list[list[Any]] = []
    for x in st:
        if not x.ip or (segs and segs[-1][2] == x.ip):
            continue
        if segs:
            segs[-1][1] = x.ts
        segs.append([x.ts, max(end, x.ts), x.ip])
    return [
        (a, b, ip, ":" in ip and not ip.lower().startswith("::ffff:"), any(ga <= b and gb >= a for ga, gb, _ in gaps))
        for a, b, ip in segs
    ]


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


def _step_status(x: Step, now: int, live: bool) -> Literal["done", "streaming", "error"]:
    if x.end_ts is None:
        return "streaming" if live and now - x.ts <= STREAMING_WITHIN_NS else "error"
    return "error" if x.status is None or x.status >= 400 else "done"


def _step(x: Step, j: _Joins, now: int, live: bool) -> StepData:
    succ = j.success.get(x.request_id or "") or j.success.get(x.client_request_id or "")
    status = _step_status(x, now, live)
    finished = j.tools.get(x.request_id or "", [])
    tools = [_tool(e, j) for e in finished]
    if live:  # progress ticks with no success/error yet are tools still running
        done = {_str(e.md.get("toolUseID")) or _str(e.md.get("toolName")) for e in finished}
        for e in j.ticks.get(x.request_id or "", []):
            key = _str(e.md.get("toolUseID")) or _str(e.md.get("toolName"))
            if key not in done:
                done.add(key)
                tools.append(_tool(e, j).model_copy(update={"status": "running"}))
    md = succ.md if succ else {}
    return StepData(
        requestId=x.correlation_id,
        t=rows.dt(x.ts),
        kind="subagent" if x.request_class == "subagent" else "model",
        model=x.model,
        requestClass=x.request_class if x.request_class in REQUEST_CLASSES else None,
        tokens=TokenCount(
            input=x.input,
            cacheRead=x.cache_read,
            cacheWrite=x.cache_write,
            cacheWrite5m=_int(md.get("cache_creation_5m_input_tokens")),
            cacheWrite1h=_int(md.get("cache_creation_1h_input_tokens")),
            output=x.output,
            accuracy="exact" if x.output is not None else "missing",
        ),
        durationMs=round((x.end_ts - x.ts) / 1e6, 1) if x.end_ts else None,
        ttftMs=_float(md.get("ttftMs")),
        stopReason=x.stop_reason,
        costUsd=_float(md.get("costUSD")),
        status=status,
        # Analytics trail the call by seconds: pending while nothing newer has arrived from a live session.
        eventsPending=succ is None and live and (j.last_event is None or j.last_event <= (x.end_ts or x.ts)),
        agentType=x.agent_type,
        tools=tools,
        does=rows.step_kind(x.tool_uses_json, x.stop_reason),
        **_content(rows.detail_of(x.detail_json), j.results, tools),
        usageMissing=rows.usage_missing(x.output, x.status, x.stop_reason, x.ws_turn, x.response_bytes, None),
    )


def _content(d: dict[str, Any], results: dict[str, list[int]], tools: list[ToolCallData]) -> dict[str, Any]:
    """A step's commands, patches, first words and exit codes, from its recorded bodies.
    Without them, what the agent's own analytics report: Claude Code's Bash events name each
    command's program (bash_argv0) and its exit code, in any mode."""
    calls = [c for c in d.get("calls") or [] if isinstance(c, dict)]
    if not calls:
        bash = [t for t in tools if t.command]
        return {
            "commands": [t.command for t in bash if t.command],
            "patches": [],
            "text": d.get("text") or None,
            "exitCodes": [t.exitCode for t in bash if t.exitCode is not None],
        }
    return {
        "commands": [w for c in calls for w in c.get("commands") or []],
        "patches": [
            PatchFile(file=p["file"], added=int(p.get("added") or 0), removed=int(p.get("removed") or 0))
            for c in calls
            for p in c.get("patches") or []
            if isinstance(p, dict) and p.get("file")
        ],
        "text": d.get("text") or None,
        "exitCodes": [x for c in calls for x in results.get(str(c.get("id") or ""), [])],
    }


def _sum(vals: list[int | None]) -> int | None:
    known = [v for v in vals if v is not None]
    return sum(known) if known else None


def _k(n: int) -> str:
    return f"{round(n / 1000)}k" if n >= 1000 else str(n)


def _hidden_by_prompt(s: cc.Session, starts: dict[str, int], order: list[str]) -> dict[str, dict[str, int]]:
    """Each hidden call to a prompt: a prewarm to the next one to start (it warms the turn after it),
    a title to the last one started before it (it names the turn it follows)."""
    out: dict[str, dict[str, int]] = {}
    for ts, kind in s.hidden_ts:
        after = next((p for p in order if starts[p] >= ts), None)
        before = next((p for p in reversed(order) if starts[p] < ts), None)
        pid = (after or before) if kind == "prewarm" else (before or after)
        if pid is not None:
            out.setdefault(pid, {})
            out[pid][kind] = out[pid].get(kind, 0) + 1
    return out


def _prompt_items(
    s: cc.Session,
    now: int,
    st: list[Step],
    evs: list[Event],
    j: _Joins,
    reports: dict[str, TurnReport] | None = None,
) -> list[tuple[int, PromptItem, bool]]:
    """(start, prompt row, typed) per prompt, oldest first; typed = its tengu_input_prompt was captured
    (a Codex turn always counts as typed: its number is its place in the thread). `reports`: Codex's
    turn reports by turn id."""
    live = s.live(now)
    has_events = s.event_count > 0
    codex = s.product == "codex"
    reports = reports or {}

    # Prompts: each tengu_input_prompt, plus prompt ids seen only on calls (typed before capture began).
    inputs: dict[str, Event] = {}
    starts: dict[str, int] = {}
    for e in evs:
        if e.name == "tengu_input_prompt":
            pid = e.prompt_id or f"input:{e.id}"
            inputs.setdefault(pid, e)
            starts.setdefault(pid, e.ts)
    by_prompt: dict[str, list[Step]] = defaultdict(list)
    for x in st:
        if x.prompt_id:
            by_prompt[x.prompt_id].append(x)
            starts.setdefault(x.prompt_id, x.ts)
    order = sorted(starts, key=lambda p: (starts[p], p))
    hidden = _hidden_by_prompt(s, starts, order)
    pastes = [e for e in evs if e.name in ("tengu_paste_text", "tengu_paste_image")]
    typed = sorted(e.ts for e in inputs.values())

    j.results = rows.exit_results([rows.detail_of(x.detail_json) for x in st])
    out: list[tuple[int, PromptItem, bool]] = []
    for n, pid in enumerate(order, 1):
        t, inp, calls = starts[pid], inputs.get(pid), by_prompt.get(pid, [])
        report = reports.get(pid)
        prompt_events = j.by_prompt.get(pid, [])
        step_data = [_step(x, j, now, live) for x in calls]
        # What the prompt said: its first call's request, when bodies are recorded (full interception).
        said = next((p for x in calls if (p := rows.detail_of(x.detail_json).get("prompt"))), None)
        turn_ends = [e.ts for e in prompt_events if e.name == "tengu_turn_end"]
        in_flight = any(x.status == "streaming" for x in step_data) or (
            live and pid == order[-1] and not turn_ends and not (report and report.status)
        )
        if inp is not None:
            prev = max((x for x in typed if x < t), default=-1)
            pasted = [e for e in pastes if prev < e.ts <= t]
            text_bytes = [_int(e.md.get("pastedTextBytes")) for e in pasted if e.name == "tengu_paste_text"]
            pasted_bytes = _sum(text_bytes)
            images: int | None = sum(1 for e in pasted if e.name == "tengu_paste_image")
        else:
            pasted_bytes, images = None, None
        tool_counts = Counter(
            _str(e.md.get("toolName")) or "—"
            for e in prompt_events
            if e.name in ("tengu_tool_use_success", "tengu_tool_use_error")
        )
        prompt = PromptData(
            promptId=pid,
            index=_int(inp.md.get("prompt_index")) if inp else n if codex else None,
            t=rows.dt(t),
            durationMs=round((max(turn_ends) - t) / 1e6, 1)
            if turn_ends and not in_flight
            else report.durationMs
            if report
            else None,
            # Prompt text is content: never read (retention is off by default, BUILD_PLAN §2).
            text=PromptText(
                retained=said is not None,
                text=said,
                chars=_int(inp.md.get("prompt_length")) if inp else (len(said) if said else None),
                pastedBytes=pasted_bytes,
                images=images if has_events else None,
            ),
            steps=len(calls),
            tokensIn=_sum([x.context for x in calls]),
            tokensOut=_sum([x.output for x in calls]),
            toolCounts=dict(sorted(tool_counts.items(), key=lambda x: (-x[1], x[0]))),
            friction=PromptFriction(
                frustrated=bool(inp and _true(inp.md.get("is_negative"))),
                interrupted=any(e.name == "tengu_cancel" for e in prompt_events),
                destructive=sum(
                    1 for e in prompt_events if e.name == "tengu_bash_tool_command_executed" and _destructive(e)
                ),
            ),
            inFlight=in_flight,
            turnReport=report,
            hidden=hidden.get(pid, {}),
        )
        out.append((t, PromptItem(type="prompt", prompt=prompt, steps=step_data), inp is not None or codex))
    return out


def typed_prompts(store: Store, s: cc.Session, now: int) -> list[PromptData]:
    """The prompts whose tengu_input_prompt was captured (the timeline's prompt marks), oldest first."""
    evs = events(store, s.id, PROMPT_EVENTS)
    reports = agents.turn_reports(store, s.id) if s.product == "codex" else None
    return [i.prompt for _, i, typed in _prompt_items(s, now, steps(store, s.id), evs, _joins(evs), reports) if typed]


def items(store: Store, s: cc.Session, now: int) -> list[PromptItem | BetweenItem]:
    """Every prompt (steps inline) and between row of the session, oldest first."""
    st = steps(store, s.id)
    evs = events(store, s.id, PROMPT_EVENTS)
    reports = agents.turn_reports(store, s.id) if s.product == "codex" else None
    out: list[tuple[int, PromptItem | BetweenItem]] = [
        (t, i) for t, i, _ in _prompt_items(s, now, st, evs, _joins(evs), reports)
    ]

    comps = compactions(evs, st)
    attached = {c.request_id for c in comps if c.request_id}
    for c in comps:
        tokens = f"context {_k(c.tokens_from)} → {_k(c.tokens_to)} tokens" if c.tokens_from and c.tokens_to else None
        between = BetweenData(
            kind="idle-compaction" if c.idle else "compaction",
            t=rows.dt(c.t),
            detail=" · ".join(x for x in ("idle compaction" if c.idle else "compaction", tokens) if x),
            requestId=c.request_id,
            tokensFrom=c.tokens_from,
            tokensTo=c.tokens_to,
            waitMs=c.wait_ms,
        )
        out.append((c.t, BetweenItem(type="between", between=between)))
    for e in evs:
        if e.name == "tengu_return_to_session" and _true(e.md.get("hadRecap")):
            between = BetweenData(
                kind="recap",
                t=rows.dt(e.ts),
                detail="recap shown on return",
                requestId=None,
                tokensFrom=None,
                tokensTo=None,
                waitMs=None,
            )
            out.append((e.ts, BetweenItem(type="between", between=between)))
    for x in st:
        if not x.prompt_id and x.correlation_id not in attached:
            between = BetweenData(
                kind="call",
                t=rows.dt(x.ts),
                detail=x.detail(),
                requestId=x.correlation_id,
                tokensFrom=None,
                tokensTo=None,
                waitMs=None,
            )
            out.append((x.ts, BetweenItem(type="between", between=between)))
    out.sort(key=lambda p: (p[0], p[1].type))
    return [item for _, item in out]


# --- requests ---------------------------------------------------------------------------------


def request_rows(store: Store, s: cc.Session) -> list[RequestRow]:
    """The person request log's rows (rows.request_rows), scoped to this session. A Codex
    thread also gets its client's calls that no thread id names (usage, catalog, metrics,
    analytics: handoff §11L.6), by time: the same client of the same person, during the thread."""
    if s.product != "codex":
        return [r for r in rows.request_rows(store, {s.id: s}, only=s.id) if r.sessionId == s.id]
    lo, hi = rows.dt(s.start), rows.dt(s.last + 60 * cc.NS)  # a turn's usage check lands just after it
    return [
        r
        for r in rows.request_rows(store, {s.id: s})
        if r.sessionId == s.id
        or (
            r.sessionId is None
            and r.agent == "codex"
            and r.kind != "model"
            and r.client == s.client
            and lo <= r.t <= hi
        )
    ]


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


def event_rows(
    store: Store,
    s: cc.Session,
    lo: int,
    hi: int,
    internal: bool,
    collapse: bool,
    q: str | None,
    page: int = 1,
    page_size: int | None = None,
) -> tuple[int, int, list[EventRow]]:
    """(events in range, rows to show, the page's rows): internals hidden unless asked, `q`
    matched against name and summary, then consecutive tool-progress ticks folded into one
    row. Every event is filtered and folded, but rows are built only for the page."""
    evs = [e for e in events(store, s.id) if lo <= e.ts <= hi]
    shown: list[list[Any]] = []  # [event, folded count]
    for e in evs:
        if is_internal(e.name) and not internal:
            continue
        if q and q.lower() not in e.name.lower() and q.lower() not in _summary(e.md).lower():
            continue
        if collapse and e.name == TICK and shown and shown[-1][0].name == TICK:
            shown[-1][1] += 1
            continue
        shown.append([e, 1])
    window = shown if page_size is None else shown[(page - 1) * page_size : page * page_size]
    if not window:
        return len(evs), len(shown), []
    by_rid = {x.request_id: x for x in steps(store, s.id) if x.request_id}
    index = pp.prompt_indexes(store, [s.id])
    out = [
        EventRow(
            eventId=e.id,
            t=rows.dt(e.ts),
            name=e.name,
            promptIndex=index.get(e.prompt_id) if e.prompt_id else None,
            linkedTo=_link(e, s.id, by_rid, index),
            summary=_summary(e.md),
            marker=_marker(e),
            internal=is_internal(e.name),
            collapsedCount=n if n > 1 else None,
        )
        for e, n in window
    ]
    return len(evs), len(shown), out


# --- state ------------------------------------------------------------------------------------


def state(store: Store, s: cc.Session, lo: int, hi: int, now: int, requests: int) -> SessionState:
    """`requests` is the Requests tab's total for the same range (its default range differs: see the router)."""
    evs = events(store, s.id, STATE_EVENTS)
    upto = [e for e in evs if e.ts <= hi]
    within = [e for e in evs if lo <= e.ts <= hi]
    has_events = s.event_count > 0

    # MCP servers and tools from the responses' tool_use blocks: the analytics redact most
    # MCP names to "mcp_tool" (isMcp marks them). Built-ins from both.
    used = [e for e in upto if e.name == "tengu_tool_use_success" and e.md.get("toolName")]
    mcp_used, builtin_used = pp.tool_uses(store, [s.id], 0, hi)
    mcp: dict[str, set[str]] = defaultdict(set, {k: set(v) for k, v in mcp_used.items()})
    for e in used:
        server = _mcp_server(e)
        if server:
            mcp[server].add(str(e.md["toolName"]))
    builtin = builtin_used | {
        str(e.md["toolName"]) for e in used if not _mcp_server(e) and not _true(e.md.get("isMcp"))
    }
    hooks = {str(e.md["hookName"]) for e in upto if e.name == "tengu_run_hook" and e.md.get("hookName")}

    head_from, head_to, heads = store.query(
        """SELECT arg_min(head_sha, ts), arg_max(head_sha, ts), count(DISTINCT head_sha) FROM cc_events
           WHERE session_id = ? AND ts BETWEEN ? AND ? AND head_sha IS NOT NULL""",
        [s.id, lo, hi],
    )[0]
    git = Counter(_str(e.md.get("operation")) for e in within if e.name == "tengu_git_operation")
    prs = {_int(e.md.get("prNumber")) for e in within if e.name == "tengu_session_linked_to_pr"}

    returns = [e for e in evs if e.name == "tengu_return_to_session"]
    reports_recaps = any("hadRecap" in e.md for e in returns)
    recaps = [e for e in returns if _true(e.md.get("hadRecap"))]

    n_events = store.query(
        "SELECT count(*) FROM cc_events WHERE session_id = ? AND ts BETWEEN ? AND ?", [s.id, lo, hi]
    )[0][0]
    prompts = [i for i in items(store, s, now) if isinstance(i, PromptItem) and lo <= cc.to_ns(i.prompt.t) <= hi]
    comps = [c for c in compactions(evs, steps(store, s.id)) if lo <= c.t <= hi]
    return SessionState(
        capabilitiesAt=CapabilitiesAt(
            builtin=ToolTally(total=None, used=len(builtin) if has_events else None),  # total: M7 tool catalog
            mcpServers=[McpServer(name=k, tools=None, toolsUsed=len(v), riskyVerbs=[]) for k, v in sorted(mcp.items())],
            mcpTools=ToolTally(
                total=pp.latest_md(store, [s.id], "tengu_tool_search_mode_decision", "mcpToolCount", hi),
                used=sum(len(v) for v in mcp.values()) if has_events or mcp else None,
            ),
            skills=pp.latest_md(store, [s.id], "tengu_skills_sync_success", "total", hi),
            hooks=len(hooks) if has_events else None,
            poolChangesInRange=sum(1 for e in within if e.name == "tengu_deferred_tools_pool_change")
            if has_events
            else None,
        ),
        repoInRange=RepoInRange(
            branch=None,  # M7: from injected git status
            headFrom=head_from,
            headTo=head_to,
            headChanges=heads - 1 if heads else None,
            commits=git.get("commit", 0),
            pushed=git.get("push", 0) > 0,
            cwdTrail=None,  # M7: from environment-update messages
            prs=sorted(p for p in prs if p is not None),
        ),
        recapsInRange=[Recap(t=rows.dt(e.ts), text=None) for e in recaps if lo <= e.ts <= hi]
        if reports_recaps
        else None,
        recapsTotal=len(recaps) if reports_recaps else None,
        experimentsAt=None,  # M7: experiment assignments come from flag bodies
        flagFetchesInRange=None,  # M7: flag fetches carry the session only in their body
        flagsChangedInRange=None,
        counts=RangeCounts(
            prompts=len(prompts),
            modelCalls=sum(1 for c in s.calls if lo <= c.ts <= hi),
            requests=requests,
            events=n_events,
            compactions=len(comps),
        ),
    )
