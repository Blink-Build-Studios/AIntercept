from fastapi import APIRouter, Request

from aintercept_api import pages
from aintercept_api.models.common import Meta
from aintercept_api.models.status import Status, StatusResponse
from aintercept_api.timeutil import ns_to_dt

router = APIRouter()


@router.get("/status", response_model=StatusResponse)
def get_status(request: Request) -> StatusResponse:
    with pages.read(request, "status") as reader:
        s = reader.status()
    status = Status(
        proxy=None,
        certIssued=None,
        firstRequestAt=ns_to_dt(s.first_request_ns),
        waitingSince=ns_to_dt(s.earliest_ns) if s.first_request_ns is None else None,
        lastIngestAt=ns_to_dt(s.newest_ns),
        contentRetained=s.content_retained,
    )
    return StatusResponse(data=status, meta=Meta(asOf=request.app.state.clock(), lastDataAt=ns_to_dt(s.newest_ns)))
