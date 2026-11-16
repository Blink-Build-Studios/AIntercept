"""The routers' page reader (docs/PIPELINE.md §6).

A router parses its parameters, snaps its range (ranges.py), and calls its page's reader for what the
page shows; then it maps that onto the response models and the meta. The store gives each request its
page's reader (`read`): PgStore's (pgstore.PgPages), which reads Postgres through the psycopg reader
(reader.py) in one snapshot. Every method gets the router's snapped bounds (ns; None for an open end).
"""

import contextlib
import re
from collections.abc import Sequence
from dataclasses import dataclass
from datetime import datetime
from typing import Any, Protocol

from fastapi import Request

from aintercept_api.models.people import (
    Overview,
    PersonHeader,
    PersonSummary,
    PersonSummaryRow,
    RequestRow,
    SessionRow,
    Timeline,
)
from aintercept_api.models.requests import EventDetail, RequestDetail, RequestDetails, RequestRaw
from aintercept_api.models.sessions import (
    BetweenItem,
    EventRow,
    PromptItem,
    SessionState,
    SessionTimeline,
)
from aintercept_api.models.turns import RepoGroup, TurnRow

Row = dict[str, Any]

# --- what a page reads, whichever reader --------------------------------------------------------------


@dataclass(frozen=True)
class StatusValues:
    first_request_ns: int | None
    earliest_ns: int | None
    newest_ns: int | None
    content_retained: bool | None


@dataclass(frozen=True)
class RequestsPage:
    """A request log's page: its counts and rows."""

    hidden_count: int
    total: int
    kind_counts: dict[str, int]
    rows: list[RequestRow]
    last_ns: int | None = None  # the person log's lastDataAt


@dataclass(frozen=True)
class PeoplePage:
    rows: list[PersonSummaryRow]
    total: int
    last: datetime | None


class Pages(Protocol):
    """Every page's reads (PgStore's PgPages)."""

    def status(self) -> StatusValues: ...
    def newest_ns(self) -> int | None: ...
    def request(self, rid: str) -> RequestDetail | None: ...
    def request_details(self, rid: str) -> RequestDetails | None: ...
    def request_raw(self, rid: str) -> RequestRaw | None: ...
    def event(self, eid: str) -> EventDetail | None: ...
    def session(self, sid: str, now: int) -> Row | None: ...
    def session_timeline(
        self, sid: str, r: Row, now_dt: datetime, now: int, lo: int, hi: int, bin_s: int
    ) -> SessionTimeline: ...
    def session_state(self, sid: str, lo: int, hi: int, req_lo: int | None, req_hi: int | None) -> SessionState: ...
    def session_prompts(
        self, sid: str, now: int, lo: int, hi: int, page: int, size: int, prompt: int | None
    ) -> tuple[list[PromptItem | BetweenItem], int, int]: ...
    def session_turns(
        self, sid: str, now: int, lo: int, hi: int, page: int, size: int, turn: int | None
    ) -> tuple[list[TurnRow], int, int]: ...
    def session_requests(
        self, sid: str, lo: int | None, hi: int | None, kind: Sequence[str] | None, q: str | None,
        captured: bool | None, page: int, size: int, order: str, hidden: bool,
    ) -> RequestsPage: ...  # fmt: skip
    def session_events(
        self, sid: str, lo: int, hi: int, internal: bool, q: str | None, page: int, size: int
    ) -> tuple[int, int, list[EventRow]]: ...
    def person_sessions(
        self, pid: str, now: int, lo: int | None, hi: int | None, page: int, size: int, agent: Sequence[str] | None
    ) -> tuple[int, int | None, list[SessionRow]]: ...
    def person_session_rows(self, pid: str, now: int) -> tuple[list[str], list[Row]]: ...
    def person_timeline(
        self, sids: list[str], rs: list[Row], now_dt: datetime, now: int, lo: int, hi: int, bin_s: int
    ) -> Timeline: ...
    def person(self, pid: str, now_dt: datetime) -> tuple[PersonHeader, datetime | None]: ...
    def person_span(self, pid: str) -> tuple[list[str], int | None, int | None]: ...
    def person_summary(
        self, pid: str, sids: list[str], now: int, lo: int, hi: int
    ) -> tuple[PersonSummary, int | None]: ...
    def person_work(self, pid: str, now: int, lo: int | None, hi: int | None) -> tuple[list[RepoGroup], int | None]: ...
    def person_requests(
        self, pid: str, lo: int | None, hi: int | None, kind: Sequence[str] | None, q: str | None,
        captured: bool | None, page: int, size: int, order: str, agent: Sequence[str] | None, hidden: bool,
    ) -> RequestsPage: ...  # fmt: skip
    def people(
        self, now_dt: datetime, lo: int | None, hi: int | None, sort: str, agent: Sequence[str] | None,
        q: str | None, page: int, size: int,
    ) -> PeoplePage: ...  # fmt: skip
    def overview(self, now_dt: datetime, lo: int | None, hi: int | None) -> tuple[Overview, datetime | None]: ...


class PageStore(Protocol):
    """A store that gives each request its page's reader (PgStore)."""

    def page_reader(self, page: str, path: str) -> contextlib.AbstractContextManager[Pages]: ...


def read(request: Request, page: str) -> contextlib.AbstractContextManager[Pages]:
    """The request's reader of page, on the app's store. Its reads inside the `with` are one snapshot."""
    store: PageStore = request.app.state.store
    path = request.url.path + ("?" + request.url.query if request.url.query else "")
    return store.page_reader(page, path)


# The pages, by their routes (the routers' paths under /api/v1).
_ROUTES = [
    ("status", r"/status"),
    ("session", r"/sessions/[^/]+"),
    ("session/timeline", r"/sessions/[^/]+/timeline"),
    ("session/state", r"/sessions/[^/]+/state"),
    ("session/prompts", r"/sessions/[^/]+/prompts"),
    ("session/turns", r"/sessions/[^/]+/turns"),
    ("session/requests", r"/sessions/[^/]+/requests"),
    ("session/events", r"/sessions/[^/]+/events"),
    ("people", r"/people"),
    ("overview", r"/overview"),
    ("person", r"/people/[^/]+"),
    ("person/sessions", r"/people/[^/]+/sessions"),
    ("person/timeline", r"/people/[^/]+/timeline"),
    ("person/summary", r"/people/[^/]+/summary"),
    ("person/work", r"/people/[^/]+/work"),
    ("person/requests", r"/people/[^/]+/requests"),
    ("request", r"/requests/[^/]+"),
    ("request/raw", r"/requests/[^/]+/raw"),
    ("request/details", r"/requests/[^/]+/details"),
    ("event", r"/events/[^/]+"),
]
_PATTERNS = [(page, re.compile("/api/v1" + rx + "$")) for page, rx in _ROUTES]
PAGE_NAMES = tuple(page for page, _ in _ROUTES)


def page_of(path: str) -> str | None:
    """The page a request path (without its query) is served by; None for no page."""
    return next((page for page, rx in _PATTERNS if rx.match(path)), None)
