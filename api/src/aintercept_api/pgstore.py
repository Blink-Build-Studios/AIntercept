"""The read path over Postgres (docs/PIPELINE.md §6): each page's reader (pages.py).

Every page reads v9's facts and rollups through the psycopg reader (reader.py), in one snapshot per
request (READ ONLY, REPEATABLE READ): indexed statements, each bounded by the page's entity and range
(snapped to whole minutes: ranges.py). The drawer reads a call's raw member from the object store with
one ranged GET (objects.py). The API's only store.
"""

import contextlib
import logging
import threading
import time
import urllib.error
from collections.abc import Iterator, Sequence
from datetime import datetime
from functools import partial
from typing import Any, cast

import psycopg

from aintercept_api import directorypages, drawer, pages, personpages, sessionpages, turnpages
from aintercept_api import request_detail as rd
from aintercept_api.models.people import Overview, PersonHeader, PersonSummary, SessionRow, Timeline
from aintercept_api.models.requests import RequestDetail, RequestDetails, RequestRaw
from aintercept_api.models.sessions import BetweenItem, PromptItem, SessionState, SessionTimeline
from aintercept_api.models.turns import RepoGroup, TurnRow
from aintercept_api.models.work import ToolsInRange
from aintercept_api.objects import Source
from aintercept_api.reader import BEGIN, POOL_SIZE, Load, Reader, SchemaMismatch, Snapshot

# The schema this build reads (aintercept/internal/pg/schema.sql's sha256, as pg.SchemaHash and
# schema_version record it). The API ships no copy of the file; tests/test_pg_store.py holds the
# two equal.
SCHEMA_HASH = "1dccee7b5be2bb956bdd206ffe1a51086ed532faf20e48c4ffb328749f1fbf60"

__all__ = ["BEGIN", "POOL_SIZE", "PgStore", "SchemaMismatch"]

log = logging.getLogger("aintercept_api")

# How often a pod checks the database's schema hash again: one waiting for the workers to apply its
# schema (an upgrade), or one whose schema the workers just replaced (an older pod).
RECHECK_SECONDS = 5.0

# The drawer's lastDataAt (it's global, quirk 7): the newest record any observer pod sent (§6.4).
_NEWEST = "SELECT max(last_ns) AS n FROM ingest_pods"


class PgStore:
    """The store the endpoints read: Postgres, and the raw files the drawer's calls' members are in."""

    def __init__(
        self,
        dsn: str,
        *,
        source: Source | None = None,
        prefix: str = "",
        pool_size: int = POOL_SIZE,
        trace: bool = False,
    ) -> None:
        self.dsn = dsn
        self.trace: list[Load] = []
        self.reader = Reader(dsn, pool_size=pool_size, trace=self.trace if trace else None)
        self.source, self.prefix = source, prefix  # the raw files (the drawer's calls' members)
        self._checking = threading.Lock()
        self._checked = -RECHECK_SECONDS
        self._ready: bool | None = None
        self.ready()

    def ready(self) -> bool:
        """Whether the database holds this build's schema (pg.Check), checked again every
        RECHECK_SECONDS. Until it does the API answers 503 and its readiness probe fails (app.py), so
        an upgrade's pods wait for the workers to apply their schema, and never restart for it."""
        if time.monotonic() - self._checked < RECHECK_SECONDS or not self._checking.acquire(blocking=False):
            return bool(self._ready)
        try:
            try:
                self.reader.check_schema(SCHEMA_HASH)
                ready, why = True, ""
            except (SchemaMismatch, psycopg.Error) as e:
                ready, why = False, str(e)
            if ready != self._ready:
                if ready:
                    log.info("ready: the database holds this build's schema", extra={"fields": {"schema": SCHEMA_HASH}})
                else:
                    log.warning("not ready: waiting for this build's schema", extra={"fields": {"reason": why}})
            self._ready, self._checked = ready, time.monotonic()
            return ready
        finally:
            self._checking.release()

    @property
    def opened(self) -> int:
        """Connections opened so far."""
        return self.reader.pool.opened

    def close(self) -> None:
        self.reader.close()

    def page_reader(self, page: str, path: str) -> contextlib.AbstractContextManager[pages.Pages]:
        """The page's reader, in one snapshot."""
        return self._snapshot(path)

    @contextlib.contextmanager
    def _snapshot(self, path: str) -> Iterator[pages.Pages]:
        with self.reader.snapshot(path) as snap:
            yield cast(pages.Pages, PgPages(self, snap))

    def read_range(self, key: str, offset: int, length: int) -> bytes | None:
        """A call's gzip member in its raw file (one ranged GET); None when the store can't give it."""
        if self.source is None:
            return None
        try:
            return self.source.get_range(key, offset, length)
        except FileNotFoundError:
            return None
        except urllib.error.HTTPError as e:
            if e.code in (404, 416):
                return None
            raise


# --- the pages -----------------------------------------------------------------------------------------


class PgPages:
    """Every page, read from Postgres through the psycopg reader in one snapshot (docs/PIPELINE.md §6):
    the drawer (drawer.PgReads, §6.2; /events/{id} finds none: analytics aren't stored), the session pages
    (sessionpages.SessionReads, §6.1), the person pages (personpages.PersonReads, §6.3), and the directory,
    the overview and /status (directorypages.DirectoryReads, §6.4)."""

    def __init__(self, store: PgStore, snap: Snapshot) -> None:
        self.store, self.snap = store, snap
        self._sessions = sessionpages.SessionReads(snap)
        self._people = personpages.PersonReads(snap, self._sessions)
        self._directory = directorypages.DirectoryReads(snap)
        self._turns = turnpages.TurnReads(snap, self._sessions)

    def status(self) -> pages.StatusValues:
        s = self._directory.status()
        return pages.StatusValues(s.first_request_ns, s.earliest_ns, s.newest_ns, s.content_retained)

    def newest_ns(self) -> int | None:
        (row,) = self.snap.dicts("newest", _NEWEST, {})
        return cast(int | None, row["n"])

    def _reads(self) -> drawer.PgReads:
        return drawer.PgReads(self.snap, self.store.read_range)

    def request(self, rid: str) -> RequestDetail | None:
        return rd.detail(self._reads(), rid)

    def request_details(self, rid: str) -> RequestDetails | None:
        return rd.details(self._reads(), rid)

    def request_raw(self, rid: str) -> RequestRaw | None:
        return rd.raw(self._reads(), rid)

    # --- the session pages (v9: sessionpages.SessionReads, docs/PIPELINE.md §6.1) ---------------------

    def session(self, sid: str, now: int) -> dict[str, Any] | None:
        return self._sessions.session(sid, now)

    def session_tools(self, sid: str) -> ToolsInRange:
        return self._sessions.wire(sid, None, None).tools

    def session_timeline(
        self, sid: str, r: dict[str, Any], now_dt: datetime, now: int, lo: int, hi: int, bin_s: int
    ) -> SessionTimeline:
        blocks = self._turns.blocks(sid, r["live"], now)
        return self._sessions.timeline(sid, r, now_dt, now, lo, hi, bin_s, blocks, partial(self._turns.bins, sid))

    def session_state(self, sid: str, lo: int, hi: int, req_lo: int | None, req_hi: int | None) -> SessionState:
        return self._sessions.state(sid, lo, hi, self._sessions.request_count(sid, req_lo, req_hi))

    def session_prompts(
        self, sid: str, now: int, lo: int, hi: int, page: int, size: int, prompt: int | None
    ) -> tuple[list[PromptItem | BetweenItem], int, int]:
        return self._sessions.prompts(sid, now, lo, hi, page, size, prompt)

    def session_turns(
        self, sid: str, now: int, lo: int, hi: int, page: int, size: int, turn: int | None
    ) -> tuple[list[TurnRow], int, int]:
        return self._turns.turns(sid, now, lo, hi, page, size, turn)

    def session_requests(
        self, sid: str, lo: int | None, hi: int | None, kind: Sequence[str] | None, q: str | None,
        captured: bool | None, page: int, size: int, order: str, hidden: bool,
    ) -> pages.RequestsPage:  # fmt: skip
        hidden_count, total, kinds, rows = self._sessions.requests(
            sid, lo, hi, kind, q, captured, page, size, order, hidden
        )
        return pages.RequestsPage(hidden_count, total, kinds, rows)

    # --- the person pages (v9: personpages.PersonReads, docs/PIPELINE.md §6.3) ------------------------

    def person_sessions(
        self, pid: str, now: int, lo: int | None, hi: int | None, page: int, size: int, agent: Sequence[str] | None
    ) -> tuple[int, int | None, list[SessionRow]]:
        return self._people.sessions_page(pid, now, lo, hi, page, size, agent)

    def person_session_rows(self, pid: str, now: int) -> tuple[list[str], list[dict[str, Any]]]:
        return self._people.session_rows(pid, now)

    def person_timeline(
        self, sids: list[str], rs: list[dict[str, Any]], now_dt: datetime, now: int, lo: int, hi: int, bin_s: int
    ) -> Timeline:
        return self._people.timeline(sids, rs, now_dt, now, lo, hi, bin_s)

    def person(self, pid: str, now_dt: datetime) -> tuple[PersonHeader, datetime | None]:
        return self._people.header(pid, now_dt)

    def person_span(self, pid: str) -> tuple[list[str], int | None, int | None]:
        return self._people.span(pid)

    def person_summary(self, pid: str, sids: list[str], now: int, lo: int, hi: int) -> tuple[PersonSummary, int | None]:
        return self._people.summary(pid, now, lo, hi)

    def person_work(self, pid: str, now: int, lo: int | None, hi: int | None) -> tuple[list[RepoGroup], int | None]:
        return self._people.work(pid, now, lo, hi)

    def person_requests(
        self, pid: str, lo: int | None, hi: int | None, kind: Sequence[str] | None, q: str | None,
        captured: bool | None, page: int, size: int, order: str, agent: Sequence[str] | None, hidden: bool,
    ) -> pages.RequestsPage:  # fmt: skip
        hidden_count, total, kinds, rows, last = self._people.requests(
            pid, lo, hi, kind, q, captured, page, size, order, agent, hidden
        )
        return pages.RequestsPage(hidden_count, total, kinds, rows, last)

    # --- the directory and the overview (v9: directorypages.DirectoryReads, docs/PIPELINE.md §6.4) -----

    def people(
        self, now_dt: datetime, lo: int | None, hi: int | None, sort: str, agent: Sequence[str] | None,
        q: str | None, page: int, size: int,
    ) -> pages.PeoplePage:  # fmt: skip
        rows, total, last = self._directory.people(now_dt, lo, hi, sort, agent, q, page, size)
        return pages.PeoplePage(rows, total, last)

    def overview(self, now_dt: datetime, lo: int | None, hi: int | None) -> tuple[Overview, datetime | None]:
        return self._directory.overview(now_dt, lo, hi)
