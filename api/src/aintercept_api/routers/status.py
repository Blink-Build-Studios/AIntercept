from fastapi import APIRouter, Request

from aintercept_api.models.common import Meta
from aintercept_api.models.status import Status, StatusResponse
from aintercept_api.timeutil import ns_to_dt

router = APIRouter()


@router.get("/status", response_model=StatusResponse)
def get_status(request: Request) -> StatusResponse:
    store = request.app.state.store
    stats = getattr(store, "status_stats", None)
    if stats is not None:  # the v1 store: from the merge commit, no table read
        first, earliest = stats()
    else:
        first, earliest = store.query(
            """SELECT min(ts) FILTER (WHERE NOT synthetic), min(ts)
               FROM (SELECT ts, synthetic FROM calls UNION ALL SELECT row_ns, false FROM analytics
                     UNION ALL SELECT first_ns, true FROM probes)"""
        )[0]
    last = store.newest_ns
    status = Status(
        proxy=None,
        certIssued=None,
        firstRequestAt=ns_to_dt(first),
        waitingSince=ns_to_dt(earliest) if first is None else None,
        lastIngestAt=ns_to_dt(last),
        contentRetained=store.content_retained,
    )
    return StatusResponse(data=status, meta=Meta(asOf=request.app.state.clock(), lastDataAt=ns_to_dt(last)))
