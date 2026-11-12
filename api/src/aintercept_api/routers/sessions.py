"""Session page endpoints (handoff §5.1 rows 8-13, §5.2.7-5.2.9)."""

from datetime import datetime
from typing import Any

from fastapi import APIRouter, HTTPException, Query, Request
from pydantic import AwareDatetime

from aintercept_api import agents
from aintercept_api import claude_code as cc
from aintercept_api import session as ss
from aintercept_api import timeline as tl
from aintercept_api.models.common import AgentLimits, ErrorResponse, Meta, RequestKind
from aintercept_api.models.people import DeviceInfo, TokenCount
from aintercept_api.models.sessions import (
    Compaction,
    ContextPoint,
    EventsMeta,
    NetworkSegment,
    PermissionChange,
    PermissionSegment,
    SessionEventsResponse,
    SessionFacts,
    SessionHeader,
    SessionPromptsResponse,
    SessionRequestsResponse,
    SessionResponse,
    SessionStateResponse,
    SessionTimeline,
    SessionTimelineResponse,
)
from aintercept_api.rows import dt, request_filter, request_row_from_table, session_row_from_table, session_rows
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


def _load(request: Request, session_id: str) -> tuple[dict[str, Any], datetime, int]:
    """The session's session_rows row (with `live` as of now)."""
    now_dt: datetime = request.app.state.clock()
    now = cc.to_ns(now_dt)
    found = session_rows(request.app.state.store, now, [session_id])
    if not found:
        raise HTTPException(status_code=404, detail=f"No session {session_id}")
    return found[0], now_dt, now


def _range(r: dict[str, Any], from_: datetime | None, to: datetime | None, now: int) -> tuple[int, int]:
    """Default: the session's start (truncated to the microseconds every response time carries)
    to its end, now while it is live."""
    start = r["start_ns"] - r["start_ns"] % 1000
    return (cc.to_ns(from_) if from_ else start, cc.to_ns(to) if to else now if r["live"] else r["last_ns"])


def _page(items: list[Any], page: int, size: int) -> list[Any]:
    return items[(page - 1) * size : page * size]


def header_from_table(r: dict[str, Any]) -> SessionHeader:
    """A session_rows row (with `live`) as the session page header: field copies only."""
    row = session_row_from_table(r, alone=True)
    return SessionHeader(
        **dict(row),
        personId=r["person_id"],
        notCaptured=r["cov_not_captured"] if r["cov_reported"] is not None else None,
        device=DeviceInfo(
            deviceId=r["device_id"],
            platform=r["device_platform"],
            terminal=r["device_terminal"],
            shell=r["device_shell"],
        )
        if r["device_id"]
        else None,
        networks=r["networks"],
        facts=SessionFacts(
            cwd=r["facts_cwd"], shell=None, tz=None, approval=r["facts_approval"], sandbox=r["facts_sandbox"]
        )
        if r["has_facts"]
        else None,
        limitsAtEnd=AgentLimits.model_validate_json(r["limits_json"]) if r["limits_json"] else None,
        capabilities=agents.CAPABILITIES[row.agent],
        channels=agents.CHANNELS[row.agent],
        effort=r["effort"],
        tokens=TokenCount(
            input=r["tokens_input"],
            cacheRead=r["tokens_cache_read"],
            cacheWrite=r["tokens_cache_write"],
            cacheWrite5m=None,
            cacheWrite1h=None,
            output=r["tokens_output"],
            accuracy=r["tokens_accuracy"],
        ),
        toolCalls=r["tool_calls"],
    )


@router.get("/sessions/{sessionId}", response_model=SessionResponse)
def get_session(request: Request, sessionId: str) -> SessionResponse:  # noqa: N803 (the contract's camelCase)
    r, now_dt, _ = _load(request, sessionId)
    return SessionResponse(data=header_from_table(r), meta=Meta(asOf=now_dt, lastDataAt=ns_to_dt(r["last_ns"])))


@router.get("/sessions/{sessionId}/timeline", response_model=SessionTimelineResponse)
def get_session_timeline(
    request: Request,
    sessionId: str,  # noqa: N803
    bin: int | None = Query(None, ge=60, description="Bin width in seconds. Default: auto from the session's span."),
) -> SessionTimelineResponse:
    store = request.app.state.store
    r, now_dt, now = _load(request, sessionId)
    live = r["live"]
    lo, hi = _range(r, None, None, now)
    bin_s = bin or cc.auto_bin_seconds(hi - lo)
    perm = ss.permission(store, sessionId, hi)
    sid = [sessionId]
    # SQL over the processor's session_marks and presence_segments (docs/architecture.md, Decision 4).
    timeline = SessionTimeline(
        from_=dt(lo),
        to=dt(hi),
        now=now_dt if live else None,
        binSeconds=bin_s,
        sessions=[tl.band_from_table(r, 0, alone=True)],
        prompts=tl.prompts(store, sid, now, None, None),
        modelCalls=tl.model_calls(store, sid, bin_s, None, None),
        analytics=tl.analytics(store, sid, bin_s, None, None),
        notCaptured=tl.gaps(store, sid, None, None),
        presence=tl.presence(store, sid, now, None, None),
        friction=tl.friction(store, sid, bin_s, None, None),
        context=[
            ContextPoint(
                t=dt(x["t_ns"]),
                tokens=x["context"],
                requestId=x["request_id"],
                model=x["model"],
                thread="main",
                windowTokens=None,  # M7: from the request's model and context beta
                promptIndex=x["prompt_index"],
            )
            for x in ss.context_points(store, sessionId)
        ],
        compactions=[
            Compaction(
                t=dt(c["t_ns"]),
                from_=c["tokens_from"],
                to=c["tokens_to"],
                idle=c["idle"],
                waitMs=c["wait_ms"],
                messages=None,
            )
            for c in ss.compactions(store, sessionId)
        ],
        permission=[PermissionSegment(from_=dt(p["a"]), to=dt(p["b"]), mode=p["mode"]) for p in perm],
        permissionChanges=[
            PermissionChange(t=dt(p["a"]), from_=p["prev_mode"], to=p["mode"]) for p in perm if p["prev_mode"]
        ],
        network=[
            NetworkSegment(
                from_=dt(n["a"]),
                to=dt(n["b"]),
                label=n["ip"],
                ipv6=n["ipv6"],
                bypassed=n["bypassed"],
                address=None,  # the coverage job (§5.4)
                notCaptured=n["not_captured"],
            )
            for n in ss.networks(store, sessionId, hi)
        ],
    )
    meta = Meta(asOf=now_dt, lastDataAt=ns_to_dt(r["last_ns"]), from_=dt(lo), to=dt(hi))
    return SessionTimelineResponse(data=timeline, meta=meta)


@router.get("/sessions/{sessionId}/state", response_model=SessionStateResponse)
def get_session_state(
    request: Request,
    sessionId: str,  # noqa: N803
    from_: AwareDatetime | None = FROM,
    to: AwareDatetime | None = TO,
) -> SessionStateResponse:
    # SQL over the processor's event_rows, session_tools and session_request_rows (docs/architecture.md,
    # Decision 4).
    store = request.app.state.store
    r, now_dt, now = _load(request, sessionId)
    lo, hi = _range(r, from_, to, now)
    # Counted exactly as the Requests tab filters, so the readout matches it (analytics batches
    # arrive after the events they carry, so the tab's range is open-ended by default).
    where, params = request_filter("scope_session_id", sessionId, from_, to, None, None)
    requests = store.query(f"SELECT count(*) FROM session_request_rows WHERE {where}", params)[0][0]  # noqa: S608
    data = ss.state(store, sessionId, lo, hi, requests)
    meta = Meta(asOf=now_dt, lastDataAt=ns_to_dt(r["last_ns"]), from_=dt(lo), to=dt(hi))
    return SessionStateResponse(data=data, meta=meta)


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
    # SQL over the processor's prompt_rows and prompt_steps (docs/architecture.md, Decision 4).
    r, now_dt, now = _load(request, sessionId)
    lo, hi = _range(r, from_, to, now)
    items, total, page = ss.prompt_items(request.app.state.store, sessionId, now, lo, hi, page, pageSize, prompt)
    meta = Meta(
        asOf=now_dt,
        lastDataAt=ns_to_dt(r["last_ns"]),
        from_=dt(lo),
        to=dt(hi),
        page=page,
        pageSize=pageSize,
        total=total,
    )
    return SessionPromptsResponse(data=items, meta=meta)


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
    # SQL over the processor's session_request_rows (docs/architecture.md, Decision 4): the rows are
    # stored as shown, so this only filters, counts and pages them.
    store = request.app.state.store
    found, now_dt, _ = _load(request, sessionId)
    where, params = request_filter("scope_session_id", sessionId, from_, to, q, captured)
    hidden_count, total = store.query(
        f"""SELECT count(*) FILTER (WHERE hidden_kind IS NOT NULL),
                   count(*) FILTER (WHERE (? OR hidden_kind IS NULL) AND (? OR kind IN (SELECT unnest(?::VARCHAR[]))))
            FROM session_request_rows WHERE {where}""",  # noqa: S608 (the clause is fixed text; values are parameters)
        [hidden, not kind, list(kind or []), *params],
    )[0]
    kind_counts = dict(
        store.query(
            f"""SELECT kind, count(*) FROM session_request_rows WHERE {where} AND (? OR hidden_kind IS NULL)
                GROUP BY kind ORDER BY kind""",  # noqa: S608
            [*params, hidden],
        )
    )
    direction = "DESC" if order == "desc" else "ASC"
    page_rows = store.query_dicts(
        f"""SELECT * FROM session_request_rows
            WHERE {where} AND (? OR hidden_kind IS NULL) AND (? OR kind IN (SELECT unnest(?::VARCHAR[])))
            ORDER BY ts_ns // 1000 {direction}, request_id {direction}, seq {direction} LIMIT ? OFFSET ?""",  # noqa: S608
        [*params, hidden, not kind, list(kind or []), pageSize, (page - 1) * pageSize],
    )
    meta = Meta(
        asOf=now_dt,
        lastDataAt=ns_to_dt(found["last_ns"]),
        from_=from_,
        to=to,
        page=page,
        pageSize=pageSize,
        total=total,
    )
    return SessionRequestsResponse(
        data=[request_row_from_table(r) for r in page_rows],
        meta=meta,
        kindCounts=kind_counts,
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
    # SQL over the processor's event_rows (docs/architecture.md, Decision 4).
    r, now_dt, now = _load(request, sessionId)
    lo, hi = _range(r, from_, to, now)
    total, shown, rows = ss.event_rows(
        request.app.state.store, sessionId, lo, hi, internal, collapseTicks, q, page, pageSize
    )
    meta = EventsMeta(
        asOf=now_dt,
        lastDataAt=ns_to_dt(r["last_ns"]),
        from_=dt(lo),
        to=dt(hi),
        page=page,
        pageSize=pageSize,
        total=total,
        shown=shown,
    )
    return SessionEventsResponse(data=rows, meta=meta)
