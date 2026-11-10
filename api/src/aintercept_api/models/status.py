from datetime import datetime
from typing import Literal

from aintercept_api.models.common import Envelope, Model


class Status(Model):
    """Proxy and ingest health; drives the waiting checklist and the delayed banner."""

    proxy: Literal["up", "down"] | None
    """None until the API probes the observer's /healthz/path."""
    certIssued: bool | None
    """None until the API probes certsvc."""
    firstRequestAt: datetime | None
    """First real (non-synthetic) request seen; None while waiting for traffic."""
    waitingSince: datetime | None
    """While waiting (no real request yet), when the pipeline first produced any row; otherwise None."""
    lastIngestAt: datetime | None
    """Newest row of any kind, including the proxy's own probes."""
    contentRetained: bool | None
    """The deployment keeps bodies (full-capture mode); None until there's data. "About this data" shows it."""


class StatusResponse(Envelope[Status]):
    """For /status, `meta.lastDataAt` is the newest row of any kind, probes included:
    the global badge reports whether the pipeline is flowing, not whether anyone is
    active. Person and session scopes use their own rows."""
