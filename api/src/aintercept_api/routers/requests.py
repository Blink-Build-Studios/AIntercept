"""Request drawer endpoints (handoff §5.1 rows 14-16, §5.2.10) and one analytics event (§5.2.9).

On the Postgres store (`precomputed`), the drawer is the worker's finished row (docs/PIPELINE.md v7
§3.7): /requests/{id} and /details validate it through their models, /raw and /events/{id} format the
call's (the batch's) detail object as today. The other stores derive it at request time
(request_detail.py)."""

import json
from typing import Any

from fastapi import APIRouter, HTTPException, Request

from aintercept_api import request_detail as rd
from aintercept_api.models.common import ErrorResponse, Meta
from aintercept_api.models.requests import (
    EventDetail,
    EventResponse,
    RequestDetail,
    RequestDetails,
    RequestDetailsResponse,
    RequestRawResponse,
    RequestResponse,
)
from aintercept_api.timeutil import ns_to_dt

router = APIRouter(responses={404: {"model": ErrorResponse, "description": "No request (or event) with this id"}})


def _meta(request: Request) -> Meta:
    last = request.app.state.store.newest_ns
    return Meta(asOf=request.app.state.clock(), lastDataAt=ns_to_dt(last))


def _missing(what: str, id_: str) -> HTTPException:
    return HTTPException(status_code=404, detail=f"No {what} {id_}")


def _precomputed(store: Any) -> bool:
    return getattr(store, "precomputed", False) is True


@router.get("/requests/{requestId}", response_model=RequestResponse)
def get_request(request: Request, requestId: str) -> RequestResponse:  # noqa: N803 (the contract's camelCase)
    """A captured transaction (its correlation id), or `reported:<requestId>` for a call only Claude Code reported."""
    store = request.app.state.store
    if _precomputed(store):
        row = store.drawer_row()
        d = None if row is None else RequestDetail.model_validate(json.loads(row))
    else:
        d = rd.detail(store, requestId)
    if d is None:
        raise _missing("request", requestId)
    return RequestResponse(data=d, meta=_meta(request))


@router.get("/requests/{requestId}/raw", response_model=RequestRawResponse)
def get_request_raw(request: Request, requestId: str) -> RequestRawResponse:  # noqa: N803
    store = request.app.state.store
    if _precomputed(store):
        r = None
        if store.drawer_found():
            r = rd.reported_raw() if requestId.startswith(rd.REPORTED) else rd.wire_raw(store, requestId)
    else:
        r = rd.raw(store, requestId)
    if r is None:
        raise _missing("request", requestId)
    return RequestRawResponse(data=r, meta=_meta(request))


@router.get("/requests/{requestId}/details", response_model=RequestDetailsResponse)
def get_request_details(request: Request, requestId: str) -> RequestDetailsResponse:  # noqa: N803
    store = request.app.state.store
    if _precomputed(store):
        row = store.drawer_row()
        d = None if row is None else RequestDetails.model_validate(json.loads(row))
    else:
        d = rd.details(store, requestId)
    if d is None:
        raise _missing("request", requestId)
    return RequestDetailsResponse(data=d, meta=_meta(request))


@router.get("/events/{eventId}", response_model=EventResponse)
def get_event(request: Request, eventId: str) -> EventResponse:  # noqa: N803
    """An EventRow's `eventId` (`<batch>:<n>`): the client event in full."""
    store = request.app.state.store
    if _precomputed(store):
        e = _event_row(store, eventId)
    else:
        e = rd.event(store, eventId)
    if e is None:
        raise _missing("event", eventId)
    return EventResponse(data=e, meta=_meta(request))


def _event_row(store: Any, event_id: str) -> EventDetail | None:
    """The worker's row of the event, and its raw row from the batch's detail object."""
    cid, _, n = event_id.rpartition(":")
    data, batch, key = store.event_row()
    if not cid or not n.isdigit() or data is None:
        return None
    body = store.raw_event(batch, key, "") or {}
    ev = body.get("client_event") if isinstance(body, dict) else None
    d = json.loads(data)
    d["eventId"], d["raw"] = event_id, rd._scrub(ev if isinstance(ev, dict) else {})
    return EventDetail.model_validate(d)
