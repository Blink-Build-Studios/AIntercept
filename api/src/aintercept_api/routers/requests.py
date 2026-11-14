"""Request drawer endpoints (handoff §5.1 rows 14-16, §5.2.10) and one analytics event (§5.2.9).

Each reads through its page's reader (pages.py): on the Postgres store the worker's finished row
(docs/PIPELINE.md v7 §3.7; /raw and /events/{id} format the call's, or the batch's, detail object as
today); on the other stores today's derivation at request time (request_detail.py)."""

from fastapi import APIRouter, HTTPException, Request

from aintercept_api import pages
from aintercept_api.models.common import ErrorResponse, Meta
from aintercept_api.models.requests import (
    EventResponse,
    RequestDetailsResponse,
    RequestRawResponse,
    RequestResponse,
)
from aintercept_api.timeutil import ns_to_dt

router = APIRouter(responses={404: {"model": ErrorResponse, "description": "No request (or event) with this id"}})


def _meta(request: Request, reader: pages.Pages) -> Meta:
    return Meta(asOf=request.app.state.clock(), lastDataAt=ns_to_dt(reader.newest_ns()))


def _missing(what: str, id_: str) -> HTTPException:
    return HTTPException(status_code=404, detail=f"No {what} {id_}")


@router.get("/requests/{requestId}", response_model=RequestResponse)
def get_request(request: Request, requestId: str) -> RequestResponse:  # noqa: N803 (the contract's camelCase)
    """A captured transaction (its correlation id), or `reported:<requestId>` for a call only Claude Code reported."""
    with pages.read(request, "request") as reader:
        d = reader.request(requestId)
        if d is None:
            raise _missing("request", requestId)
        return RequestResponse(data=d, meta=_meta(request, reader))


@router.get("/requests/{requestId}/raw", response_model=RequestRawResponse)
def get_request_raw(request: Request, requestId: str) -> RequestRawResponse:  # noqa: N803
    with pages.read(request, "request/raw") as reader:
        r = reader.request_raw(requestId)
        if r is None:
            raise _missing("request", requestId)
        return RequestRawResponse(data=r, meta=_meta(request, reader))


@router.get("/requests/{requestId}/details", response_model=RequestDetailsResponse)
def get_request_details(request: Request, requestId: str) -> RequestDetailsResponse:  # noqa: N803
    with pages.read(request, "request/details") as reader:
        d = reader.request_details(requestId)
        if d is None:
            raise _missing("request", requestId)
        return RequestDetailsResponse(data=d, meta=_meta(request, reader))


@router.get("/events/{eventId}", response_model=EventResponse)
def get_event(request: Request, eventId: str) -> EventResponse:  # noqa: N803
    """An EventRow's `eventId` (`<batch>:<n>`): the client event in full."""
    with pages.read(request, "event") as reader:
        e = reader.event(eventId)
        if e is None:
            raise _missing("event", eventId)
        return EventResponse(data=e, meta=_meta(request, reader))
