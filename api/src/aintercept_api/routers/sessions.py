"""Session page endpoints (handoff §5.1 rows 8-13, §5.2.7-5.2.9)."""

from datetime import datetime
from typing import Any

from fastapi import APIRouter, HTTPException, Query, Request
from pydantic import AwareDatetime

from aintercept_api import agents, pages, ranges
from aintercept_api import claude_code as cc
from aintercept_api.models.common import AgentLimits, ErrorResponse, Meta, RequestKind
from aintercept_api.models.people import DeviceInfo, TokenCount
from aintercept_api.models.sessions import (
    EventsMeta,
    SessionEventsResponse,
    SessionFacts,
    SessionHeader,
    SessionPromptsResponse,
    SessionRequestsResponse,
    SessionResponse,
    SessionStateResponse,
    SessionTimelineResponse,
)
from aintercept_api.rows import dt, session_row_from_table
from aintercept_api.timeutil import ns_to_dt

# An unknown session is a typed 404, like every other error (app.py error handlers).
router = APIRouter(
    responses={
        404: {"model": ErrorResponse, "description": "No session with this id"},
        422: {"model": ErrorResponse, "description": "Invalid parameters"},
    }
)

FROM = Query(
    None,
    alias="from",
    description="Range start (RFC 3339), snapped to its minute's start. Default: the session's start.",
)
TO = Query(
    None,
    description="Range end (RFC 3339), snapped to its minute's end. Default: the session's end (now while it is live).",
)
PAGE = Query(1, ge=1)
PROMPT = Query(None, ge=1, description="A prompt number (#N): return the page that holds it, in place of `page`.")
PAGE_SIZE = Query(20, ge=1, le=200)


def _now(request: Request) -> tuple[datetime, int]:
    now_dt: datetime = request.app.state.clock()
    return now_dt, cc.to_ns(now_dt)


def _load(reader: pages.Pages, session_id: str, now: int) -> dict[str, Any]:
    """The session's session_rows row (with `live` as of now); a 404 for no such session."""
    found = reader.session(session_id, now)
    if found is None:
        raise HTTPException(status_code=404, detail=f"No session {session_id}")
    return found


def _span(r: dict[str, Any], now: int) -> tuple[int, int]:
    """The session's span: its start (truncated to the microseconds every response time carries) to
    its end, now while it is live."""
    return r["start_ns"] - r["start_ns"] % 1000, now if r["live"] else r["last_ns"]


def _range(r: dict[str, Any], from_: datetime | None, to: datetime | None, now: int) -> tuple[int, int]:
    """The range a page filters by, snapped to whole minutes (ranges.py); by default the session's span."""
    start, end = _span(r, now)
    lo, hi = cc.to_ns(from_) if from_ else start, cc.to_ns(to) if to else end
    return ranges.start_of_minute(lo), ranges.end_of_minute(hi)


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
    now_dt, now = _now(request)
    with pages.read(request, "session") as reader:
        r = _load(reader, sessionId, now)
    return SessionResponse(data=header_from_table(r), meta=Meta(asOf=now_dt, lastDataAt=ns_to_dt(r["last_ns"])))


@router.get("/sessions/{sessionId}/timeline", response_model=SessionTimelineResponse)
def get_session_timeline(
    request: Request,
    sessionId: str,  # noqa: N803
    bin: int | None = Query(
        None,
        ge=60,
        multiple_of=ranges.BIN_MULTIPLE_S,
        description="Bin width in seconds, a whole number of minutes. Default: auto from the session's span.",
    ),
) -> SessionTimelineResponse:
    # The timeline doesn't filter by a range: its lanes cover every row, its from and to are the
    # session's span (not snapped, docs/PIPELINE.md v8 §5.3).
    now_dt, now = _now(request)
    with pages.read(request, "session/timeline") as reader:
        r = _load(reader, sessionId, now)
        lo, hi = _span(r, now)
        bin_s = bin or cc.auto_bin_seconds(hi - lo)
        timeline = reader.session_timeline(sessionId, r, now_dt, now, lo, hi, bin_s)
    meta = Meta(asOf=now_dt, lastDataAt=ns_to_dt(r["last_ns"]), from_=dt(lo), to=dt(hi))
    return SessionTimelineResponse(data=timeline, meta=meta)


@router.get("/sessions/{sessionId}/state", response_model=SessionStateResponse)
def get_session_state(
    request: Request,
    sessionId: str,  # noqa: N803
    from_: AwareDatetime | None = FROM,
    to: AwareDatetime | None = TO,
) -> SessionStateResponse:
    now_dt, now = _now(request)
    with pages.read(request, "session/state") as reader:
        r = _load(reader, sessionId, now)
        lo, hi = _range(r, from_, to, now)
        # The Requests tab's count, over its own range: open-ended by default.
        data = reader.session_state(sessionId, lo, hi, *ranges.given(from_, to))
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
    now_dt, now = _now(request)
    with pages.read(request, "session/prompts") as reader:
        r = _load(reader, sessionId, now)
        lo, hi = _range(r, from_, to, now)
        items, total, page = reader.session_prompts(sessionId, now, lo, hi, page, pageSize, prompt)
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
    now_dt, now = _now(request)
    # The tab's range is open-ended by default: analytics batches arrive after the events they carry.
    lo, hi = ranges.given(from_, to)
    with pages.read(request, "session/requests") as reader:
        found = _load(reader, sessionId, now)
        got = reader.session_requests(sessionId, lo, hi, kind, q, captured, page, pageSize, order, hidden)
    meta = Meta(
        asOf=now_dt,
        lastDataAt=ns_to_dt(found["last_ns"]),
        from_=ns_to_dt(lo),
        to=ns_to_dt(hi),
        page=page,
        pageSize=pageSize,
        total=got.total,
    )
    return SessionRequestsResponse(
        data=got.rows,
        meta=meta,
        kindCounts=got.kind_counts,
        hiddenCount=got.hidden_count,
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
    now_dt, now = _now(request)
    with pages.read(request, "session/events") as reader:
        r = _load(reader, sessionId, now)
        lo, hi = _range(r, from_, to, now)
        total, shown, rows = reader.session_events(sessionId, lo, hi, internal, collapseTicks, q, page, pageSize)
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
