"""Row builders shared by the person and session routers (handoff §4 SessionRowData,
RequestRowData; §5.2.6-5.2.7), so both pages show the same row for the same thing."""

import json
from collections.abc import Sequence
from dataclasses import dataclass
from datetime import datetime
from http import HTTPStatus
from typing import Any

from aintercept_api import agents
from aintercept_api import claude_code as cc
from aintercept_api import person as pp
from aintercept_api.models.common import Coverage, CoverageWindow, UsageMissing
from aintercept_api.models.people import (
    Friction,
    RequestError,
    RequestRow,
    SessionLabel,
    SessionOrigin,
    SessionRow,
    ThreadChildren,
    TokenCount,
)
from aintercept_api.models.sessions import StepKind
from aintercept_api.store import Store
from aintercept_api.timeutil import ns_to_dt

# The observer parsed at most this much of a response before P6 (docs/INTERCEPTION_ISSUES.md):
# a longer response lost the final events that carry its usage.
PARSE_BUFFER = 512 * 1024


def tool_calls(tool_uses_json: str | None) -> list[tuple[str, str, int]]:
    """A call's tool calls, in order: (name, kind, count), from the processor's tool_uses_json."""
    try:
        uses = json.loads(tool_uses_json) if tool_uses_json else []
    except ValueError:
        return []
    return [
        (str(u.get("name") or u.get("tool") or "?"), str(u.get("kind") or ""), int(u.get("count") or 1))
        for u in uses
        if isinstance(u, dict)
    ]


def tool_labels(tool_uses_json: str | None) -> list[str]:
    """ "exec ×2", one per tool, in order."""
    return [f"{name} ×{n}" for name, _, n in tool_calls(tool_uses_json)]


_EXEC = {"exec", "exec_command", "shell", "local_shell", "Bash", "BashOutput"}
_PATCH = {"apply_patch", "Edit", "Write", "MultiEdit", "NotebookEdit"}
_SPAWN = {"spawn_agent", "Task", "Agent"}


def step_kind(tool_uses_json: str | None, stop_reason: str | None) -> StepKind:
    """What a step did (handoff §11L.7): a patch or a spawn breaks a run of commands, so
    they win over exec; a step with no tool call answered in text."""
    if stop_reason and "interrupt" in stop_reason:
        return "interrupt"
    calls = tool_calls(tool_uses_json)
    names = {n for n, _, _ in calls}
    if names & _PATCH:
        return "patch"
    if names & _SPAWN:
        return "spawn"
    if any(k == "mcp" for _, k, _ in calls):
        return "mcp"
    if names & _EXEC:
        return "exec"
    return "tool" if calls else "text"


def detail_of(detail_json: str | None) -> dict[str, Any]:
    """A call's detail from its recorded bodies (the processor's detail_json; full
    interception mode only): its tool calls, first words and earlier calls' results."""
    try:
        d = json.loads(detail_json) if detail_json else {}
    except ValueError:
        return {}
    return d if isinstance(d, dict) else {}


def exit_results(details: list[dict[str, Any]]) -> dict[str, list[int]]:
    """Each tool call's exit codes, by call id, from the requests that carried them back."""
    out: dict[str, list[int]] = {}
    for d in details:
        for cid, r in (d.get("results") or {}).items():
            if isinstance(r, dict) and r.get("exit"):
                out.setdefault(cid, [int(x) for x in r["exit"] if isinstance(x, int)])
            elif isinstance(r, dict) and r.get("error"):
                out.setdefault(cid, [1])
    return out


def usage_missing(
    output: int | None,
    status: int | None,
    stop_reason: str | None,
    ws_turn: int | None,
    bytes_down: int | None,
    hidden_kind: str | None,
) -> UsageMissing | None:
    """Why a model call has no usage (handoff §11L.10); None when it has it."""
    if output is not None or hidden_kind == "prewarm":  # a prewarm's output is 0, not missing
        return None
    if stop_reason and "interrupt" in stop_reason:
        return "interrupted"
    if status is not None and status >= 400:
        return "failed"
    if not ws_turn and bytes_down is not None and bytes_down > PARSE_BUFFER:
        return "parseBuffer"
    return "streamCut"


def dt(ns: int) -> datetime:
    d = ns_to_dt(ns)
    if d is None:  # only reachable if a caller passes None despite the type
        raise ValueError("timestamp required")
    return d


def label(sid: str, title: str | None = None) -> SessionLabel:
    """A Codex thread's generated title, when its title call's answer was recorded; else the id
    (repo/branch and recaps arrive with M7 extraction, handoff §5.2.6)."""
    if title:
        return SessionLabel(text=title, source="title")
    return SessionLabel(text=f"Session {sid[:8]}", source="none")


def coverage(s: cc.Session) -> Coverage:
    reported, captured, missing, wins = cc.coverage(s)
    return Coverage(
        reported=reported,
        captured=captured,
        notCaptured=missing,
        windows=[CoverageWindow(from_=dt(a), to=dt(b), count=n) for a, b, n in wins],
    )


def origin(s: cc.Session) -> SessionOrigin | None:
    o = s.origin
    if o is None or o.kind not in ("fork", "resume", "subagent", "review"):
        return None
    init = o.init if o.init in ("warm_fork", "cold_fork", "cold_resume") else None
    return SessionOrigin.model_validate(
        {"kind": o.kind, "sessionId": o.session_id, "turn": o.turn, "init": init, "agentName": o.agent_name}
    )


def children(s: cc.Session) -> ThreadChildren | None:
    if not s.children:
        return None
    return ThreadChildren(
        subagent=sum(k == "subagent" for _, k in s.children),
        review=sum(k == "review" for _, k in s.children),
        sessionIds=[sid for sid, _ in s.children],
    )


def session_row(s: cc.Session, now: int, workspace: tuple[str | None, str | None] | None = None) -> SessionRow:
    """`workspace`: a Codex thread's (basename, short commit), from agents.workspace."""
    live = s.live(now)
    frustrated, interrupted, destructive = cc.friction_counts(s)
    prompts = cc.prompt_ids(s)
    return SessionRow(
        sessionId=s.id,
        agent=agents.agent_of(s.product),
        client=s.client,
        clientVersion=s.client_version,
        origin=origin(s),
        children=children(s),
        hiddenCalls=sum(s.hidden.values()),
        start=dt(s.start),
        end=None if live else dt(s.last),
        live=live,
        repo=workspace[0] if workspace else None,
        branch=None,
        commit=workspace[1] if workspace else None,
        label=label(s.id, s.title),
        prompts=len(prompts) or None,
        capturedFromPrompt=cc.captured_from_prompt(s),
        modelCalls=len(s.calls),
        events=s.event_count or None,
        friction=Friction(frustrated=frustrated, interrupted=interrupted, destructive=destructive),
        bypassPct=cc.bypass_pct(s),
        coverage=coverage(s),
        fieldsMissing=bool(s.calls) and all(c.account is None and c.device is None for c in s.calls),
    )


def last_data(sessions: dict[str, cc.Session]) -> datetime | None:
    return ns_to_dt(max((s.last for s in sessions.values()), default=None))


def _phrase(status: int) -> str | None:
    try:
        return HTTPStatus(status).phrase
    except ValueError:  # not a registered code (e.g. Anthropic's 529)
        return None


def _tokens(t: pp.Tx, rep: pp.Reported | None) -> TokenCount | None:
    if t.kind != "model":
        return None
    return TokenCount(
        input=t.input,
        cacheRead=t.cache_read,
        cacheWrite=t.cache_write,
        cacheWrite5m=rep.cache_5m if rep else None,
        cacheWrite1h=rep.cache_1h if rep else None,
        # A prewarm produces nothing (handoff §11M part 4): its output is 0, not unknown.
        output=0 if t.output is None and t.hidden_kind == "prewarm" else t.output,
        accuracy="exact" if t.output is not None or t.hidden_kind == "prewarm" else "missing",
    )


def failed(t: pp.Tx) -> bool:
    return t.status is None and t.end_ts is not None or t.status is not None and t.status >= 400


@dataclass
class Attempt:
    """A failed call Claude Code retried: its tengu_api_retry and the next attempt, when captured."""

    retry: pp.Retry
    next: pp.Tx | None

    @property
    def succeeded_at(self) -> datetime | None:
        n = self.next
        return dt(n.ts) if n is not None and n.status is not None and n.status < 400 else None


def attempts(txs: list[pp.Tx], retries: dict[str, list[pp.Retry]]) -> dict[str, Attempt]:
    """correlation id -> its retry, for every failed model call Claude Code retried."""
    out: dict[str, Attempt] = {}
    for i, t in enumerate(txs):
        if t.kind != "model" or not failed(t) or not t.session:
            continue
        r = pp.nearest_retry(retries.get(t.session, []), lambda x: (x.ts, x.prompt_id), t.ts, t.end_ts, t.prompt_id)
        if r is None:
            continue
        nxt = next(
            (
                x
                for x in txs[i + 1 :]
                if x.kind == "model"
                and x.session == t.session
                and x.prompt_id == t.prompt_id
                and x.request_class == t.request_class
            ),
            None,
        )
        out[t.correlation_id] = Attempt(r, nxt)
    return out


def _error(t: pp.Tx, duration_ms: float | None, attempt: Attempt | None) -> RequestError | None:
    if not failed(t):
        return None
    phrase = _phrase(t.status) if t.status is not None else None
    return RequestError(
        message=t.error or phrase or f"HTTP {t.status}",
        afterMs=duration_ms,
        bytesReceived=t.bytes_down,
        retries=attempt.retry.attempt if attempt else None,
        retrySucceededAt=attempt.succeeded_at if attempt else None,
    )


def request_row(
    t: pp.Tx,
    index: dict[str, int],
    reported: dict[str, pp.Reported],
    attempt: Attempt | None = None,
    steps: dict[str, int] | None = None,
) -> RequestRow:
    model = t.kind == "model"
    rep = (reported.get(t.request_id or "") or reported.get(t.client_request_id or "")) if model else None
    duration = round((t.end_ts - t.ts) / 1e6, 1) if t.end_ts else None
    return RequestRow(
        requestId=t.correlation_id,
        t=dt(t.ts),
        kind=t.kind,
        agent=agents.agent_of(t.product) if t.product else None,
        client=t.client,
        hiddenKind="prewarm" if t.hidden_kind == "prewarm" else "title" if t.hidden_kind == "title" else None,
        sessionId=t.session,
        sessionLabel=None,  # repo/branch arrive with M7 extraction
        promptIndex=index.get(t.prompt_id) if t.prompt_id else None,
        method=t.method,
        path=t.path,
        detail=t.detail(),
        status=t.status if t.status is not None else ("ERR" if t.end_ts is not None else None),
        durationMs=duration,
        retries=attempt.retry.attempt if attempt else None,
        bytesUp=t.bytes_up,
        bytesDown=t.bytes_down,
        background=t.background,
        captured=True,
        statusText=_phrase(t.status) if t.status is not None else t.error,
        stopReason=t.stop_reason if model else None,
        ttftMs=rep.ttft_ms if rep else None,
        tokens=_tokens(t, rep),
        contentEncoding=t.content_encoding,
        error=_error(t, duration, attempt),
        model=t.model if model else None,
        effort=t.effort if model else None,
        stepIndex=steps.get(t.correlation_id) if steps else None,
        toolCalls=tool_labels(t.tool_uses_json) if model else [],
        usageMissing=usage_missing(t.output, t.status, t.stop_reason, t.ws_turn, t.bytes_down, t.hidden_kind)
        if model
        else None,
        text=detail_of(t.detail_json).get("text") if model else None,
    )


def step_indexes(txs: list[pp.Tx]) -> dict[str, int]:
    """Each visible model call's step number within its prompt (1-based, in time order)."""
    out: dict[str, int] = {}
    seen: dict[str, int] = {}
    for t in sorted(txs, key=lambda t: (t.ts, t.correlation_id)):
        if t.kind == "model" and t.prompt_id and not t.hidden_kind:
            seen[t.prompt_id] = seen.get(t.prompt_id, 0) + 1
            out[t.correlation_id] = seen[t.prompt_id]
    return out


def request_rows(store: Store, sessions: dict[str, cc.Session], only: str | None = None) -> list[RequestRow]:
    """Every transaction made with the sessions' credentials, plus their not-captured calls (unordered);
    with `only`, just that session's transactions (filtered in the query)."""
    sids = list(sessions)
    index = pp.prompt_indexes(store, sids)
    reported = pp.reports(store, sids)
    txs = pp.transactions(store, pp.key_hashes(store, sids), only)
    tried = attempts(txs, pp.retries(store, sids))
    step_no = step_indexes(txs)
    rows = [request_row(t, index, reported, tried.get(t.correlation_id), step_no) for t in txs]
    for s in sessions.values():
        for e in cc.missed_calls(s) or []:
            rows.append(missed_row(s.id, e, reported))
    return rows


def missed_row(session_id: str, e: cc.Ev, reported: dict[str, pp.Reported]) -> RequestRow:
    """A call Claude Code reported that the proxy never saw: only what its tengu_api_success carries."""
    rep = reported.get(e.request_id or "") or reported.get(e.client_request_id or "")
    return RequestRow(
        requestId=f"reported:{e.request_id or e.client_request_id or e.ts}",
        t=dt(e.ts),
        kind="model",
        agent="claude-code",
        client=None,
        hiddenKind=None,
        sessionId=session_id,
        sessionLabel=None,
        promptIndex=None,
        method=None,
        path="/v1/messages",
        detail="reported by Claude Code · not captured",
        status=None,
        durationMs=None,
        retries=None,
        bytesUp=None,
        bytesDown=None,
        background=False,
        captured=False,
        statusText=None,  # no response was seen
        stopReason=None,  # analytics don't carry it
        ttftMs=rep.ttft_ms if rep else None,
        tokens=TokenCount(
            input=None,
            cacheRead=None,
            cacheWrite=None,
            cacheWrite5m=rep.cache_5m if rep else None,
            cacheWrite1h=rep.cache_1h if rep else None,
            output=None,
            accuracy="missing",
        ),
        contentEncoding=None,
        error=None,
        model=None,
        effort=None,
        stepIndex=None,
        toolCalls=[],
        usageMissing=None,
        text=None,
    )


def matches(
    r: RequestRow,
    from_: datetime | None,
    to: datetime | None,
    kinds: Sequence[str] | None,
    q: str | None,
    captured: bool | None,
) -> bool:
    if from_ and r.t < from_ or to and r.t > to:
        return False
    if kinds and r.kind not in kinds:
        return False
    if captured is not None and r.captured != captured:
        return False
    if q:
        if "=" in q:
            k, _, v = q.partition("=")
            got = {"kind": r.kind, "session": r.sessionId or "", "status": str(r.status)}.get(k.strip())
            return got is not None and v.strip() in got
        return q.lower() in r.path.lower() or q.lower() in r.detail.lower()
    return True
