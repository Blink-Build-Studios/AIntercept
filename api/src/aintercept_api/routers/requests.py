"""Request drawer endpoints (handoff §5.1 rows 14-16, §5.2.10) and one analytics event (§5.2.9)."""

from fastapi import APIRouter, HTTPException, Request

from aintercept_api import request_detail as rd
from aintercept_api.models.common import ErrorResponse, Meta
from aintercept_api.models.requests import EventResponse, RequestDetailsResponse, RequestRawResponse, RequestResponse
from aintercept_api.timeutil import ns_to_dt

router = APIRouter(responses={404: {"model": ErrorResponse, "description": "No request (or event) with this id"}})


def _meta(request: Request) -> Meta:
    last = request.app.state.store.newest_ns
    return Meta(asOf=request.app.state.clock(), lastDataAt=ns_to_dt(last))


def _missing(what: str, id_: str) -> HTTPException:
    return HTTPException(status_code=404, detail=f"No {what} {id_}")


@router.get("/requests/{requestId}", response_model=RequestResponse)
def get_request(request: Request, requestId: str) -> RequestResponse:  # noqa: N803 (the contract's camelCase)
    """A captured transaction (its correlation id), or `reported:<requestId>` for a call only Claude Code reported."""
    d = rd.detail(request.app.state.store, requestId)
    if d is None:
        raise _missing("request", requestId)
    return RequestResponse(data=d, meta=_meta(request))


@router.get("/requests/{requestId}/raw", response_model=RequestRawResponse)
def get_request_raw(request: Request, requestId: str) -> RequestRawResponse:  # noqa: N803
    r = rd.raw(request.app.state.store, requestId)
    if r is None:
        raise _missing("request", requestId)
    return RequestRawResponse(data=r, meta=_meta(request))


@router.get("/requests/{requestId}/details", response_model=RequestDetailsResponse)
def get_request_details(request: Request, requestId: str) -> RequestDetailsResponse:  # noqa: N803
    d = rd.details(request.app.state.store, requestId)
    if d is None:
        raise _missing("request", requestId)
    return RequestDetailsResponse(data=d, meta=_meta(request))


@router.get("/events/{eventId}", response_model=EventResponse)
def get_event(request: Request, eventId: str) -> EventResponse:  # noqa: N803
    """An EventRow's `eventId` (`<batch>:<n>`): the client event in full."""
    e = rd.event(request.app.state.store, eventId)
    if e is None:
        raise _missing("event", eventId)
    return EventResponse(data=e, meta=_meta(request))
