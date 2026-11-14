from datetime import datetime

from fastapi import APIRouter, Query, Request
from pydantic import AwareDatetime

from aintercept_api import claude_code as cc
from aintercept_api import pages, ranges
from aintercept_api.models.common import Agent, Meta, RequestKind
from aintercept_api.models.people import (
    CoverageResponse,
    OverviewResponse,
    PeopleResponse,
    PersonRequestsResponse,
    PersonResponse,
    PersonSessionsResponse,
    PersonSummaryResponse,
    PersonTimelineResponse,
)
from aintercept_api.rows import dt as _dt
from aintercept_api.timeutil import ns_to_dt

router = APIRouter()
AGENT = Query(None, description="Repeatable; only these agents' (handoff §11M 3.3). Default all.")

FROM = Query(
    None,
    alias="from",
    description="Range start (RFC 3339), snapped to its minute's start. Default: unbounded / the person's first"
    " activity.",
)
TO = Query(None, description="Range end (RFC 3339), snapped to its minute's end. Default: unbounded / now.")


def _now(request: Request) -> tuple[datetime, int]:
    now_dt: datetime = request.app.state.clock()
    return now_dt, cc.to_ns(now_dt)


@router.get("/people/{personId}/sessions", response_model=PersonSessionsResponse)
def get_person_sessions(
    request: Request,
    personId: str,  # noqa: N803 (the contract's camelCase path parameter)
    from_: AwareDatetime | None = FROM,
    to: AwareDatetime | None = TO,
    page: int = Query(1, ge=1),
    pageSize: int = Query(20, ge=1, le=200),  # noqa: N803
    agent: list[Agent] | None = AGENT,
) -> PersonSessionsResponse:
    now_dt, now = _now(request)
    lo, hi = ranges.given(from_, to)
    with pages.read(request, "person/sessions") as reader:
        total, last, rows = reader.person_sessions(personId, now, lo, hi, page, pageSize, agent)
    meta = Meta(
        asOf=now_dt,
        lastDataAt=ns_to_dt(last),
        from_=ns_to_dt(lo),
        to=ns_to_dt(hi),
        page=page,
        pageSize=pageSize,
        total=total,
    )
    return PersonSessionsResponse(data=rows, meta=meta)


@router.get("/people/{personId}/timeline", response_model=PersonTimelineResponse)
def get_person_timeline(
    request: Request,
    personId: str,  # noqa: N803
    from_: AwareDatetime | None = FROM,
    to: AwareDatetime | None = TO,
    bin: int | None = Query(
        None,
        ge=60,
        multiple_of=ranges.BIN_MULTIPLE_S,
        description="Bin width in seconds, a whole number of minutes. Default: auto from the range (§5.2.5).",
    ),
) -> PersonTimelineResponse:
    now_dt, now = _now(request)
    with pages.read(request, "person/timeline") as reader:
        sids, rs = reader.person_session_rows(personId, now)
        # By default the person's first activity (or the last 14 days) to now, or their last if later.
        lo = cc.to_ns(from_) if from_ else min((r["start_ns"] for r in rs), default=now - 14 * 86400 * cc.NS)
        hi = cc.to_ns(to) if to else max([now, *(r["last_ns"] for r in rs)])
        # The auto bin comes from the span asked for: snapping never changes which bin the UI gets.
        bin_s = bin or cc.auto_bin_seconds(hi - lo)
        lo, hi = ranges.start_of_minute(lo), ranges.end_of_minute(hi)
        timeline = reader.person_timeline(sids, rs, now_dt, now, lo, hi, bin_s)
    last = max((r["last_ns"] for r in rs), default=None)
    meta = Meta(asOf=now_dt, lastDataAt=ns_to_dt(last), from_=_dt(lo), to=_dt(hi))
    return PersonTimelineResponse(data=timeline, meta=meta)


# --- people page (M5) ------------------------------------------------------------------------


@router.get("/overview", response_model=OverviewResponse)
def get_overview(
    request: Request, from_: AwareDatetime | None = FROM, to: AwareDatetime | None = TO
) -> OverviewResponse:
    now_dt = request.app.state.clock()
    lo, hi = ranges.given(from_, to)
    with pages.read(request, "overview") as reader:
        data, last = reader.overview(now_dt, lo, hi)
    return OverviewResponse(data=data, meta=Meta(asOf=now_dt, lastDataAt=last, from_=ns_to_dt(lo), to=ns_to_dt(hi)))


@router.get("/people", response_model=PeopleResponse)
def get_people(
    request: Request,
    from_: AwareDatetime | None = FROM,
    to: AwareDatetime | None = TO,
    sort: str = Query("lastSeen", pattern="^(lastSeen|activity|name)$"),
    agent: list[Agent] | None = Query(None, description="Repeatable; people who use any of these agents."),  # noqa: B008
    q: str | None = Query(None, description="Substring of name, email, account id or device id (case-insensitive)."),
    page: int = Query(1, ge=1),
    pageSize: int = Query(50, ge=1, le=200),  # noqa: N803
) -> PeopleResponse:
    now_dt = request.app.state.clock()
    lo, hi = ranges.given(from_, to)
    with pages.read(request, "people") as reader:
        got = reader.people(now_dt, lo, hi, sort, agent, q, page, pageSize)
    meta = Meta(
        asOf=now_dt,
        lastDataAt=got.last,
        from_=ns_to_dt(lo),
        to=ns_to_dt(hi),
        page=page,
        pageSize=pageSize,
        total=got.total,
    )
    return PeopleResponse(data=got.rows, meta=meta)


# --- header, summary, requests, coverage (M2) -------------------------------------------------


@router.get("/people/{personId}", response_model=PersonResponse)
def get_person(request: Request, personId: str) -> PersonResponse:  # noqa: N803
    now_dt = request.app.state.clock()
    with pages.read(request, "person") as reader:
        header, last = reader.person(personId, now_dt)
    return PersonResponse(data=header, meta=Meta(asOf=now_dt, lastDataAt=last))


@router.get("/people/{personId}/summary", response_model=PersonSummaryResponse)
def get_person_summary(
    request: Request,
    personId: str,  # noqa: N803
    from_: AwareDatetime | None = FROM,
    to: AwareDatetime | None = TO,
) -> PersonSummaryResponse:
    now_dt, now = _now(request)
    with pages.read(request, "person/summary") as reader:
        sids, first, last = reader.person_span(personId)
        # By default the person's first activity to now (or their last, if later).
        lo = cc.to_ns(from_) if from_ else first if first is not None else 0
        hi = cc.to_ns(to) if to else max(now, last) if last is not None else now
        lo, hi = ranges.start_of_minute(lo), ranges.end_of_minute(hi)
        summary, last_shown = reader.person_summary(personId, sids, now, lo, hi)
    meta = Meta(asOf=now_dt, lastDataAt=ns_to_dt(last_shown), from_=_dt(lo), to=_dt(hi))
    return PersonSummaryResponse(data=summary, meta=meta)


@router.get("/people/{personId}/requests", response_model=PersonRequestsResponse)
def get_person_requests(
    request: Request,
    personId: str,  # noqa: N803
    from_: AwareDatetime | None = FROM,
    to: AwareDatetime | None = TO,
    kind: list[RequestKind] | None = Query(None, description="Repeatable; default all kinds."),  # noqa: B008
    q: str | None = Query(None, description="Substring of path or detail, or field=value (kind, session, status)."),
    captured: bool | None = Query(None, description="false = only calls that bypassed the proxy."),
    page: int = Query(1, ge=1),
    pageSize: int = Query(20, ge=1, le=200),  # noqa: N803
    order: str = Query("desc", pattern="^(asc|desc)$"),
    agent: list[Agent] | None = AGENT,
    hidden: bool = Query(False, description="Include the calls the user never sees (Codex prewarm, title)."),
) -> PersonRequestsResponse:
    now_dt = request.app.state.clock()
    lo, hi = ranges.given(from_, to)
    with pages.read(request, "person/requests") as reader:
        got = reader.person_requests(personId, lo, hi, kind, q, captured, page, pageSize, order, agent, hidden)
    meta = Meta(
        asOf=now_dt,
        lastDataAt=ns_to_dt(got.last_ns),
        from_=ns_to_dt(lo),
        to=ns_to_dt(hi),
        page=page,
        pageSize=pageSize,
        total=got.total,
    )
    return PersonRequestsResponse(
        data=got.rows,
        meta=meta,
        kindCounts=got.kind_counts,
        hiddenCount=got.hidden_count,
    )


@router.get("/coverage", response_model=CoverageResponse)
def get_coverage(
    request: Request,
    personId: str = Query(..., description="The person whose coverage to check."),  # noqa: N803
    sessionId: str | None = Query(None, description="Narrow to one of the person's sessions."),  # noqa: N803
    from_: AwareDatetime | None = FROM,
    to: AwareDatetime | None = TO,
) -> CoverageResponse:
    now_dt = request.app.state.clock()
    lo, hi = ranges.given(from_, to)
    with pages.read(request, "coverage") as reader:
        detail, last = reader.coverage(personId, sessionId, lo, hi)
    meta = Meta(asOf=now_dt, lastDataAt=ns_to_dt(last), from_=ns_to_dt(lo), to=ns_to_dt(hi))
    return CoverageResponse(data=detail, meta=meta)
