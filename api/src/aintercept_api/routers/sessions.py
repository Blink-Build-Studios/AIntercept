"""Session page endpoints (handoff §5.1 rows 8-13, §5.2.7-5.2.9)."""

from collections import Counter
from datetime import datetime
from typing import Any

from fastapi import APIRouter, HTTPException, Query, Request
from pydantic import AwareDatetime

from aintercept_api import agents
from aintercept_api import claude_code as cc
from aintercept_api import session as ss
from aintercept_api import timeline as tl
from aintercept_api.models.common import ErrorResponse, Meta, RequestKind
from aintercept_api.models.people import TokenCount
from aintercept_api.models.sessions import (
    Compaction,
    ContextPoint,
    EventsMeta,
    NetworkSegment,
    PermissionChange,
    PermissionSegment,
    SessionEventsResponse,
    SessionHeader,
    SessionPromptsResponse,
    SessionRequestsResponse,
    SessionResponse,
    SessionStateResponse,
    SessionTimeline,
    SessionTimelineResponse,
)
from aintercept_api.rows import dt, matches, session_row, tool_calls
from aintercept_api.store import Store
from aintercept_api.timeutil import ns_to_dt

# An unknown session is a typed 404, like every other error (app.py error handlers).
router = APIRouter(
    responses={
        404: {"model": ErrorResponse, "description": "No session with this id"},
        422: {"model": ErrorResponse, "description": "Invalid parameters"},
    }
)

FROM = Query(None, alias="from", description="Range start (RFC 3339). Default: the session's start.")
TO = Query(None, description="Range end (RFC 3339). Default: the session's end (now while it is live).")
PAGE = Query(1, ge=1)
PROMPT = Query(None, ge=1, description="A prompt number (#N): return the page that holds it, in place of `page`.")
PAGE_SIZE = Query(20, ge=1, le=200)


def _load(request: Request, session_id: str) -> tuple[cc.Session, datetime, int]:
    now_dt: datetime = request.app.state.clock()
    s = cc.load_session(request.app.state.store, session_id)
    if s is None:
        raise HTTPException(status_code=404, detail=f"No session {session_id}")
    return s, now_dt, cc.to_ns(now_dt)


def _range(s: cc.Session, from_: datetime | None, to: datetime | None, now: int) -> tuple[int, int]:
    return (cc.to_ns(from_) if from_ else ss.start_of(s), cc.to_ns(to) if to else ss.end_of(s, now))


def _page(items: list[Any], page: int, size: int) -> list[Any]:
    return items[(page - 1) * size : page * size]


def _usage(store: Store, session_id: str) -> dict[str, Any]:
    """The header's Tokens and Tool calls: summed over the session's visible model calls."""
    inp, read, write, out, n, with_out = store.query(
        """SELECT sum(input_tokens), sum(cache_read_tokens), sum(cache_write_tokens), sum(output_tokens),
                  count(*), count(output_tokens)
           FROM cc_calls WHERE session_id = ?""",
        [session_id],
    )[0]
    uses = [u for (u,) in store.query("SELECT tool_uses_json FROM cc_calls WHERE session_id = ?", [session_id]) if u]
    return {
        "tokens": TokenCount(
            input=inp,
            cacheRead=read,
            cacheWrite=write,
            cacheWrite5m=None,
            cacheWrite1h=None,
            output=out,
            accuracy="exact" if n and with_out == n else ("missing" if not with_out else "estimated"),
        ),
        "toolCalls": sum(c for u in uses for _, _, c in tool_calls(u)) if uses else None,
    }


@router.get("/sessions/{sessionId}", response_model=SessionResponse)
def get_session(request: Request, sessionId: str) -> SessionResponse:  # noqa: N803 (the contract's camelCase)
    s, now_dt, now = _load(request, sessionId)
    store = request.app.state.store
    codex = s.product == "codex"
    row = session_row(s, now, agents.workspace(store, [s.id]).get(s.id) if codex else None)
    facts = ss.header_facts(store, s)
    header = SessionHeader(
        **dict(row),
        personId=facts.person,
        notCaptured=row.coverage.notCaptured if row.coverage and row.coverage.reported is not None else None,
        device=facts.device,
        networks=facts.networks,
        facts=agents.codex_facts(store, s.id) if codex else None,
        limitsAtEnd=agents.latest_codex_limits(store, [s.id]) if codex else None,
        capabilities=agents.CAPABILITIES[row.agent],
        channels=agents.CHANNELS[row.agent],
        effort=store.query(
            "SELECT arg_max(effort, ts) FROM cc_calls WHERE session_id = ? AND effort IS NOT NULL", [s.id]
        )[0][0],
        **_usage(store, s.id),
    )
    return SessionResponse(data=header, meta=Meta(asOf=now_dt, lastDataAt=ns_to_dt(s.last)))


@router.get("/sessions/{sessionId}/timeline", response_model=SessionTimelineResponse)
def get_session_timeline(
    request: Request,
    sessionId: str,  # noqa: N803
    bin: int | None = Query(None, ge=60, description="Bin width in seconds. Default: auto from the session's span."),
) -> SessionTimelineResponse:
    store = request.app.state.store
    s, now_dt, now = _load(request, sessionId)
    live = s.live(now)
    lo, hi = ss.start_of(s), ss.end_of(s, now)
    bin_s = bin or cc.auto_bin_seconds(hi - lo)
    st = ss.steps(store, s.id)
    evs = ss.events(store, s.id, ("tengu_api_query", *ss.COMPACTION_EVENTS))
    missed = cc.unmatched_reports(s) or []
    gaps = cc.windows(missed)
    index = cc.prompt_index(s)
    perm = ss.permission(evs, hi)
    nets = ss.networks(st, hi, gaps)

    def everything(_: int) -> bool:
        return True

    def in_segment(t: int, i: int) -> bool:  # segments share boundaries: the last one includes its end
        a, b = nets[i][0], nets[i][1]
        return a <= t < b or (i == len(nets) - 1 and t == b)

    timeline = SessionTimeline(
        from_=dt(lo),
        to=dt(hi),
        now=now_dt if live else None,
        binSeconds=bin_s,
        sessions=[tl.band(s, now, 0)],
        prompts=tl.prompts(store, [s], now, everything),
        modelCalls=tl.model_calls(s.calls, missed, bin_s),
        analytics=tl.analytics(store, [s.id], bin_s, None, None),
        notCaptured=tl.gaps([s], everything),
        presence=tl.presence(cc.presence([s], now)),
        friction=tl.friction([s], bin_s, everything),
        context=[
            ContextPoint(
                t=dt(x.ts),
                tokens=x.context,
                requestId=x.correlation_id,
                model=x.model,
                thread="main",
                windowTokens=None,  # M7: from the request's model and context beta
                promptIndex=index.get(x.prompt_id or ""),
            )
            for x in st
            if x.request_class == "main" and x.context is not None
        ],
        compactions=[
            Compaction(t=dt(c.t), from_=c.tokens_from, to=c.tokens_to, idle=c.idle, waitMs=c.wait_ms, messages=None)
            for c in ss.compactions(evs, st)
        ],
        permission=[PermissionSegment(from_=dt(a), to=dt(b), mode=m) for a, b, m in perm],
        permissionChanges=[
            PermissionChange(t=dt(cur[0]), from_=prev[2], to=cur[2]) for prev, cur in zip(perm, perm[1:], strict=False)
        ],
        network=[
            NetworkSegment(
                from_=dt(a),
                to=dt(b),
                label=ip,
                ipv6=v6,
                bypassed=byp,
                address=None,  # the coverage job (§5.4)
                notCaptured=sum(1 for t in missed if in_segment(t, i)),
            )
            for i, (a, b, ip, v6, byp) in enumerate(nets)
        ],
    )
    meta = Meta(asOf=now_dt, lastDataAt=ns_to_dt(s.last), from_=dt(lo), to=dt(hi))
    return SessionTimelineResponse(data=timeline, meta=meta)


@router.get("/sessions/{sessionId}/state", response_model=SessionStateResponse)
def get_session_state(
    request: Request,
    sessionId: str,  # noqa: N803
    from_: AwareDatetime | None = FROM,
    to: AwareDatetime | None = TO,
) -> SessionStateResponse:
    store = request.app.state.store
    s, now_dt, now = _load(request, sessionId)
    lo, hi = _range(s, from_, to, now)
    # Counted exactly as the Requests tab filters, so the readout matches it (analytics batches
    # arrive after the events they carry, so the tab's range is open-ended by default).
    requests = sum(1 for r in ss.request_rows(store, s) if matches(r, from_, to, None, None, None))
    data = ss.state(store, s, lo, hi, now, requests)
    return SessionStateResponse(data=data, meta=Meta(asOf=now_dt, lastDataAt=ns_to_dt(s.last), from_=dt(lo), to=dt(hi)))


@router.get("/sessions/{sessionId}/prompts", response_model=SessionPromptsResponse)
def get_session_prompts(
    request: Request,
    sessionId: str,  # noqa: N803
    from_: AwareDatetime | None = FROM,
    to: AwareDatetime | None = TO,
    page: int = PAGE,
    pageSize: int = PAGE_SIZE,  # noqa: N803
    prompt: int | None = PROMPT,
) -> SessionPromptsResponse:
    s, now_dt, now = _load(request, sessionId)
    lo, hi = _range(s, from_, to, now)
    items = [
        i
        for i in ss.items(request.app.state.store, s, now)
        if lo <= cc.to_ns(i.prompt.t if i.type == "prompt" else i.between.t) <= hi
    ]
    if prompt is not None:
        at = next((n for n, i in enumerate(items) if i.type == "prompt" and i.prompt.index == prompt), None)
        if at is not None:
            page = at // pageSize + 1
    meta = Meta(
        asOf=now_dt,
        lastDataAt=ns_to_dt(s.last),
        from_=dt(lo),
        to=dt(hi),
        page=page,
        pageSize=pageSize,
        total=len(items),
    )
    return SessionPromptsResponse(data=_page(items, page, pageSize), meta=meta)


@router.get("/sessions/{sessionId}/requests", response_model=SessionRequestsResponse)
def get_session_requests(
    request: Request,
    sessionId: str,  # noqa: N803
    from_: AwareDatetime | None = FROM,
    to: AwareDatetime | None = TO,
    kind: list[RequestKind] | None = Query(None, description="Repeatable; default all kinds."),  # noqa: B008
    q: str | None = Query(None, description="Substring of path or detail, or field=value (kind, status)."),
    captured: bool | None = Query(None, description="false = only calls that bypassed the proxy."),
    page: int = PAGE,
    pageSize: int = PAGE_SIZE,  # noqa: N803
    order: str = Query("asc", pattern="^(asc|desc)$"),
    hidden: bool = Query(False, description="Include the calls the user never sees (Codex prewarm, title)."),
) -> SessionRequestsResponse:
    s, now_dt, _ = _load(request, sessionId)
    rows = [r for r in ss.request_rows(request.app.state.store, s) if matches(r, from_, to, None, q, captured)]
    hidden_count = sum(r.hiddenKind is not None for r in rows)
    rows = [r for r in rows if hidden or r.hiddenKind is None]
    kind_counts = Counter(r.kind for r in rows)
    rows = [r for r in rows if not kind or r.kind in kind]
    rows.sort(key=lambda r: (r.t, r.requestId), reverse=order == "desc")
    meta = Meta(
        asOf=now_dt,
        lastDataAt=ns_to_dt(s.last),
        from_=from_,
        to=to,
        page=page,
        pageSize=pageSize,
        total=len(rows),
    )
    return SessionRequestsResponse(
        data=_page(rows, page, pageSize),
        meta=meta,
        kindCounts=dict(sorted(kind_counts.items())),
        hiddenCount=hidden_count,
    )


@router.get("/sessions/{sessionId}/events", response_model=SessionEventsResponse)
def get_session_events(
    request: Request,
    sessionId: str,  # noqa: N803
    from_: AwareDatetime | None = FROM,
    to: AwareDatetime | None = TO,
    q: str | None = Query(None, description="Substring of the event name or its summary."),
    internal: bool = Query(False, description="Include client internals (tengu_tether_*, spinner_*, …)."),
    collapseTicks: bool = Query(True, description="Fold consecutive tool-progress ticks into one row."),  # noqa: N803
    page: int = PAGE,
    pageSize: int = PAGE_SIZE,  # noqa: N803
) -> SessionEventsResponse:
    s, now_dt, now = _load(request, sessionId)
    lo, hi = _range(s, from_, to, now)
    total, shown, rows = ss.event_rows(request.app.state.store, s, lo, hi, internal, collapseTicks, q, page, pageSize)
    meta = EventsMeta(
        asOf=now_dt,
        lastDataAt=ns_to_dt(s.last),
        from_=dt(lo),
        to=dt(hi),
        page=page,
        pageSize=pageSize,
        total=total,
        shown=shown,
    )
    return SessionEventsResponse(data=rows, meta=meta)
