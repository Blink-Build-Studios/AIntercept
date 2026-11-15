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
from dataclasses import dataclass, field
from functools import cached_property
from typing import Any

from aintercept_api import claude_code as cc
from aintercept_api import person as pp
from aintercept_api.models.people import PromptText
from aintercept_api.models.sessions import (
    DestructiveMark,
    EventLink,
    ToolCallData,
)

STREAMING_WITHIN_NS = 10 * 60 * cc.NS  # an unfinished call older than this never completed (aborted)

TICK = "tengu_tool_use_progress"


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

    def detail(self) -> str:
        return f"{self.request_class or '—'} · {pp.model_family(self.model)} · {self.stop_reason or '—'}"


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


def _destructive(e: Event) -> str | None:
    cat = _str(e.md.get("destructive_category"))
    return cat if cat not in (None, "none") else None


# --- header -----------------------------------------------------------------------------------


# --- timeline lanes ---------------------------------------------------------------------------


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


# --- requests ---------------------------------------------------------------------------------

# The Requests tab is SQL over the processor's session_request_rows (its requestrows.go
# sessionRequestRows): the request log's rows built over the session alone, and a Codex
# thread's client calls no thread names, by time.

# --- events -----------------------------------------------------------------------------------


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


# The Events tab is SQL over the processor's event_rows (its eventrows.go): the rows are stored
# as shown, so a request only filters them (range, internals, `q` against the name and summary,
# lowered as Python lowers them) and pages.
_EVENTS = """
WITH f AS (SELECT * FROM event_rows WHERE session_id = ? AND ts_ns BETWEEN ? AND ?),
s AS (SELECT * FROM f WHERE (? OR NOT internal)
      AND (? = '' OR contains(name_lower, ?) OR contains(summary_lower, ?)))"""


# --- state ------------------------------------------------------------------------------------

# What the state readout counts of the session's events, over the whole session (capabilities) or
# within [lo, hi]: SQL over the processor's event_rows and session_tools.
_STATE = """
SELECT count(*) > 0,
       count(*) FILTER (WHERE ts_ns BETWEEN $2 AND $3),
       count(DISTINCT hook),
       count(*) FILTER (WHERE git_op = 'commit' AND ts_ns BETWEEN $2 AND $3),
       count(*) FILTER (WHERE git_op = 'push' AND ts_ns BETWEEN $2 AND $3),
       coalesce(list(DISTINCT pr ORDER BY pr) FILTER (WHERE pr IS NOT NULL AND ts_ns BETWEEN $2 AND $3), []),
       coalesce(bool_or(recap_key), false),
       count(*) FILTER (WHERE recap),
       coalesce(list(ts_ns ORDER BY seq) FILTER (WHERE recap AND ts_ns BETWEEN $2 AND $3), []),
       count(*) FILTER (WHERE name = 'tengu_deferred_tools_pool_change' AND ts_ns BETWEEN $2 AND $3),
       arg_max(count, (ts_ns, count)) FILTER (WHERE name = 'tengu_tool_search_mode_decision'),
       arg_max(count, (ts_ns, count)) FILTER (WHERE name = 'tengu_skills_sync_success'),
       arg_min(head_sha, (ts_ns, head_sha)) FILTER (WHERE head_sha IS NOT NULL AND ts_ns BETWEEN $2 AND $3),
       arg_max(head_sha, (ts_ns, head_sha)) FILTER (WHERE head_sha IS NOT NULL AND ts_ns BETWEEN $2 AND $3),
       count(DISTINCT head_sha) FILTER (WHERE ts_ns BETWEEN $2 AND $3)
FROM event_rows WHERE session_id = $1"""
