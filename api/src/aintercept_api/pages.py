"""The routers' page reader (docs/PIPELINE.md v8 §5.7; build step 1).

A router parses its parameters, snaps its range (ranges.py), and calls its page's reader for what the
page shows; then it maps that onto the response models and the meta. A store picks each page's
reader (`read`):
- `SqlPages` is today's SQL over the API-shaped tables a store has loaded for the request. It's the
  oracle's reader (V1Store's, and tests/oracle's), and, until each page moves to v8's statements,
  PgStore's: v7's loader fills those tables from Postgres in the request's snapshot (pgstore.py).
- PgStore's v8 reader (pgstore.PgPages) reads finished rows through the psycopg reader (reader.py):
  /status and the drawer so far. Each later page plugs in by adding its name to `PgPages.PAGES` and
  its method, with the same signature as SqlPages'.

Every method gets the router's snapped bounds (ns; None for an open end) and returns the same thing
whichever reader serves it, so the side-by-side tests compare readers through one router.
"""

import contextlib
import re
from collections import Counter
from collections.abc import Sequence
from dataclasses import dataclass
from datetime import datetime
from typing import Any, Protocol

from fastapi import Request

from aintercept_api import agents
from aintercept_api import claude_code as cc
from aintercept_api import directory as dr
from aintercept_api import request_detail as rd
from aintercept_api import rollups as ru
from aintercept_api import session as ss
from aintercept_api import timeline as tl
from aintercept_api.models.common import AgentLimits, Coverage, CoverageWindow, LimitWindow
from aintercept_api.models.people import (
    Capabilities,
    CodeShipped,
    CommandWord,
    Cost,
    CoverageDetail,
    Destructive,
    DeviceInfo,
    Failures,
    Flags,
    Footprint,
    Limits,
    McpServer,
    OrgStat,
    Overview,
    PersonHeader,
    PersonSummary,
    PersonSummaryRow,
    Posture,
    RequestRow,
    Ribbon,
    SessionRow,
    Timeline,
    TokenCount,
)
from aintercept_api.models.requests import EventDetail, RequestDetail, RequestDetails, RequestRaw
from aintercept_api.models.sessions import (
    BetweenItem,
    Compaction,
    ContextPoint,
    EventRow,
    NetworkSegment,
    PermissionChange,
    PermissionSegment,
    PromptItem,
    SessionState,
    SessionTimeline,
)
from aintercept_api.rows import dt, request_filter, request_row_from_table, session_row_from_table, session_rows
from aintercept_api.store import Store

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
    """Every page's reads (SqlPages' signatures)."""

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
    def session_requests(
        self, sid: str, lo: int | None, hi: int | None, kind: Sequence[str] | None, q: str | None,
        captured: bool | None, page: int, size: int, order: str, hidden: bool,
    ) -> RequestsPage: ...  # fmt: skip
    def session_events(
        self, sid: str, lo: int, hi: int, internal: bool, collapse: bool, q: str | None, page: int, size: int
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
    def person_requests(
        self, pid: str, lo: int | None, hi: int | None, kind: Sequence[str] | None, q: str | None,
        captured: bool | None, page: int, size: int, order: str, agent: Sequence[str] | None, hidden: bool,
    ) -> RequestsPage: ...  # fmt: skip
    def coverage(
        self, pid: str, sid: str | None, lo: int | None, hi: int | None
    ) -> tuple[CoverageDetail, int | None]: ...
    def people(
        self, now_dt: datetime, lo: int | None, hi: int | None, sort: str, agent: Sequence[str] | None,
        q: str | None, page: int, size: int,
    ) -> PeoplePage: ...  # fmt: skip
    def overview(self, now_dt: datetime, lo: int | None, hi: int | None) -> tuple[Overview, datetime | None]: ...


class PageStore(Protocol):
    """A store that picks its pages' readers (PgStore); any other store's pages are all SqlPages."""

    def page_reader(self, page: str, path: str) -> contextlib.AbstractContextManager[Pages]: ...


def read(request: Request, page: str) -> contextlib.AbstractContextManager[Pages]:
    """The request's reader of page, on the app's store. Its reads inside the `with` are one snapshot."""
    store = request.app.state.store
    picker = getattr(store, "page_reader", None)
    if picker is not None:
        path = request.url.path + ("?" + request.url.query if request.url.query else "")
        return picker(page, path)  # type: ignore[no-any-return]
    return contextlib.nullcontext(SqlPages(store))


# The pages, by their routes (the routers' paths under /api/v1).
_ROUTES = [
    ("status", r"/status"),
    ("session", r"/sessions/[^/]+"),
    ("session/timeline", r"/sessions/[^/]+/timeline"),
    ("session/state", r"/sessions/[^/]+/state"),
    ("session/prompts", r"/sessions/[^/]+/prompts"),
    ("session/requests", r"/sessions/[^/]+/requests"),
    ("session/events", r"/sessions/[^/]+/events"),
    ("people", r"/people"),
    ("overview", r"/overview"),
    ("person", r"/people/[^/]+"),
    ("person/sessions", r"/people/[^/]+/sessions"),
    ("person/timeline", r"/people/[^/]+/timeline"),
    ("person/summary", r"/people/[^/]+/summary"),
    ("person/requests", r"/people/[^/]+/requests"),
    ("coverage", r"/coverage"),
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


# --- today's SQL ------------------------------------------------------------------------------------


# The summary's counts over a range, one pass each (person.py's, over the processor's rows).
_EVENTS = """SELECT * FROM event_rows WHERE session_id IN (SELECT unnest(?::VARCHAR[])) AND ts_ns BETWEEN ? AND ?"""
# An event's analytics batch: its event_id's prefix (<batch>:<n>). A store that keeps it as a column
# names the column (Store.event_batch: the Postgres store's event_rows.batch, docs/PIPELINE.md v7 §5).
_EVENT_BATCH = "regexp_extract(event_id, '^(.*):[0-9]+$', 1)"
_EVENT_TOTALS = f"""WITH e AS ({_EVENTS})
SELECT count(*), count(DISTINCT {{batch}}),
       count(DISTINCT name) FILTER (WHERE NOT unnamed), count(DISTINCT head_sha),
       count(DISTINCT hook_name) FILTER (WHERE name = 'tengu_run_hook'),
       avg(hook_ms) FILTER (WHERE name = 'tengu_tool_use_success'),
       sum(cache_5m) FILTER (WHERE name = 'tengu_api_success'), sum(cache_1h) FILTER (WHERE name = 'tengu_api_success'),
       sum(cost_usd) FILTER (WHERE name = 'tengu_api_success'),
       count(*) FILTER (WHERE name = 'tengu_bash_tool_command_executed' AND outside_cwd)
FROM e"""  # noqa: S608 (fixed text)
# (kind, key, count) for every counted dimension.
_EVENT_DIMENSIONS = f"""WITH e AS ({_EVENTS})
SELECT 'name', name, count(*) FROM e WHERE NOT unnamed GROUP BY 2
UNION ALL
SELECT CASE WHEN is_mcp THEN 'mcp_tool' ELSE 'tool' END, tool_name, count(*) FROM e
WHERE name = 'tengu_tool_use_success' AND tool_name IS NOT NULL GROUP BY 1, 2
UNION ALL
SELECT 'word', argv0, count(*) FROM e WHERE name = 'tengu_bash_tool_command_executed' AND argv0 IS NOT NULL GROUP BY 2
UNION ALL
SELECT 'destructive', category, count(*) FROM e
WHERE name = 'tengu_bash_tool_command_executed' AND category <> 'none' GROUP BY 2
UNION ALL
SELECT 'git', operation, count(*) FROM e WHERE name = 'tengu_git_operation' AND operation IS NOT NULL GROUP BY 2
UNION ALL
SELECT 'pr', pr_number, count(*) FROM e WHERE name = 'tengu_session_linked_to_pr' AND pr_number IS NOT NULL
GROUP BY 2
ORDER BY 2"""  # noqa: S608 (by key: toolsByName lists tools used equally often by name)
_CALLS = """SELECT count(*), sum(input), sum(cache_read), sum(cache_write), sum(output),
       count(*) FILTER (WHERE request_class = 'subagent'), count(*) FILTER (WHERE unreported),
       count(DISTINCT prompt_id)
FROM session_marks WHERE kind = 'call' AND session_id IN (SELECT unnest(?::VARCHAR[])) AND t_ns BETWEEN ? AND ?"""
_MARKS = """SELECT count(*) FILTER (WHERE kind = 'reported'), count(*) FILTER (WHERE kind = 'missed')
FROM session_marks WHERE session_id IN (SELECT unnest(?::VARCHAR[])) AND t_ns BETWEEN ? AND ?"""
_MISSED = """SELECT 0 AS grp, t_ns AS t FROM session_marks
WHERE kind = 'missed' AND session_id IN (SELECT unnest(?::VARCHAR[])) AND t_ns BETWEEN ? AND ?"""
# Claude Code's own latest counts up to the range's end (the MCP tools in its catalog, the skills it synced).
_LATEST = """SELECT arg_max(count, (ts_ns, count)) FROM event_rows
WHERE name = ? AND session_id IN (SELECT unnest(?::VARCHAR[])) AND ts_ns <= ?"""
_WITHIN = "(?::BIGINT IS NULL OR t_ns >= ?) AND (?::BIGINT IS NULL OR t_ns <= ?)"


def _short(x: str) -> str:
    return f"{x[:4]}…{x[-4:]}" if len(x) > 10 else x


def _agent_limits(r: dict[str, Any] | None, rl: dict[str, Any], plan: str | None) -> list[AgentLimits]:
    """Each agent's usage limits on its newest response (the header's AgentStatGroup, handoff §11J gap 5)."""
    if r is None:
        return []
    out = []
    if r["sessions_cc"] and rl["util"] is not None:
        out.append(
            AgentLimits(
                agent="claude-code",
                plan=plan,
                windows=[LimitWindow(name="7d", usedPct=round(rl["util"] * 100, 1), resetsAt=None)],
                credits=f"overage {rl['overage']}" if rl["overage"] else None,
                asOf=dt(r["last_cc"]),
            )
        )
    if r["codex_limits"]:
        out.append(AgentLimits.model_validate_json(r["codex_limits"]))
    return out


class SqlPages:
    """Today's SQL over the tables the store loaded for the request (the oracle's reader)."""

    def __init__(self, store: Store) -> None:
        self.store = store

    # --- /status and the drawer -------------------------------------------------------------------

    def status(self) -> StatusValues:
        store = self.store
        stats = getattr(store, "status_stats", None)
        if stats is not None:  # the v1 store: from the merge commit, no table read
            first, earliest = stats()
        else:
            first, earliest = store.query(
                """SELECT min(ts) FILTER (WHERE NOT synthetic), min(ts)
                   FROM (SELECT ts, synthetic FROM calls UNION ALL SELECT row_ns, false FROM analytics
                         UNION ALL SELECT first_ns, true FROM probes)"""
            )[0]
        return StatusValues(first, earliest, store.newest_ns, store.content_retained)

    def newest_ns(self) -> int | None:
        return self.store.newest_ns

    def request(self, rid: str) -> RequestDetail | None:
        return rd.detail(self.store, rid)

    def request_details(self, rid: str) -> RequestDetails | None:
        return rd.details(self.store, rid)

    def request_raw(self, rid: str) -> RequestRaw | None:
        return rd.raw(self.store, rid)

    def event(self, eid: str) -> EventDetail | None:
        return rd.event(self.store, eid)

    # --- session pages ----------------------------------------------------------------------------

    def session(self, sid: str, now: int) -> Row | None:
        """The session's session_rows row (with `live` as of now)."""
        found = session_rows(self.store, now, [sid])
        return found[0] if found else None

    def session_timeline(
        self, sid: str, r: Row, now_dt: datetime, now: int, lo: int, hi: int, bin_s: int
    ) -> SessionTimeline:
        # SQL over the processor's session_marks and presence_segments (docs/architecture.md, Decision 4).
        store = self.store
        perm = ss.permission(store, sid, hi)
        sids = [sid]
        return SessionTimeline(
            from_=dt(lo),
            to=dt(hi),
            now=now_dt if r["live"] else None,
            binSeconds=bin_s,
            sessions=[tl.band_from_table(r, 0, alone=True)],
            prompts=tl.prompts(store, sids, now, None, None),
            modelCalls=tl.model_calls(store, sids, bin_s, None, None),
            analytics=tl.analytics(store, sids, bin_s, None, None),
            notCaptured=tl.gaps(store, sids, None, None),
            presence=tl.presence(store, sids, now, None, None),
            friction=tl.friction(store, sids, bin_s, None, None),
            context=[
                ContextPoint(
                    t=dt(x["t_ns"]),
                    tokens=x["context"],
                    requestId=x["request_id"],
                    model=x["model"],
                    thread="main",
                    windowTokens=None,  # M7: from the request's model and context beta
                    promptIndex=x["prompt_index"],
                )
                for x in ss.context_points(store, sid)
            ],
            compactions=[
                Compaction(
                    t=dt(c["t_ns"]),
                    from_=c["tokens_from"],
                    to=c["tokens_to"],
                    idle=c["idle"],
                    waitMs=c["wait_ms"],
                    messages=None,
                )
                for c in ss.compactions(store, sid)
            ],
            permission=[PermissionSegment(from_=dt(p["a"]), to=dt(p["b"]), mode=p["mode"]) for p in perm],
            permissionChanges=[
                PermissionChange(t=dt(p["a"]), from_=p["prev_mode"], to=p["mode"]) for p in perm if p["prev_mode"]
            ],
            network=[
                NetworkSegment(
                    from_=dt(n["a"]),
                    to=dt(n["b"]),
                    label=n["ip"],
                    ipv6=n["ipv6"],
                    bypassed=n["bypassed"],
                    address=None,  # the coverage job (§5.4)
                    notCaptured=n["not_captured"],
                )
                for n in ss.networks(store, sid, hi)
            ],
        )

    def session_state(self, sid: str, lo: int, hi: int, req_lo: int | None, req_hi: int | None) -> SessionState:
        # SQL over the processor's event_rows, session_tools and session_request_rows (docs/architecture.md,
        # Decision 4). Counted exactly as the Requests tab filters, so the readout matches it (analytics
        # batches arrive after the events they carry, so the tab's range is open-ended by default).
        where, params = request_filter("scope_session_id", sid, req_lo, req_hi, None, None)
        requests = self.store.query(f"SELECT count(*) FROM session_request_rows WHERE {where}", params)[0][0]  # noqa: S608
        return ss.state(self.store, sid, lo, hi, requests)

    def session_prompts(
        self, sid: str, now: int, lo: int, hi: int, page: int, size: int, prompt: int | None
    ) -> tuple[list[PromptItem | BetweenItem], int, int]:
        # SQL over the processor's prompt_rows and prompt_steps (docs/architecture.md, Decision 4).
        return ss.prompt_items(self.store, sid, now, lo, hi, page, size, prompt)

    def session_requests(
        self, sid: str, lo: int | None, hi: int | None, kind: Sequence[str] | None, q: str | None,
        captured: bool | None, page: int, size: int, order: str, hidden: bool,
    ) -> RequestsPage:  # fmt: skip
        # SQL over the processor's session_request_rows (docs/architecture.md, Decision 4): the rows are
        # stored as shown, so this only filters, counts and pages them.
        return self._requests("session_request_rows", "scope_session_id", sid, lo, hi, kind, q, captured, page,
                              size, order, None, hidden, tiebreak=", seq {d}")  # fmt: skip

    def _requests(
        self, table: str, scope: str, value: str, lo: int | None, hi: int | None, kind: Sequence[str] | None,
        q: str | None, captured: bool | None, page: int, size: int, order: str, agent: Sequence[str] | None,
        hidden: bool, *, tiebreak: str = "",
    ) -> RequestsPage:  # fmt: skip
        store = self.store
        where, params = request_filter(scope, value, lo, hi, q, captured, agent)
        hidden_count, total = store.query(
            f"""SELECT count(*) FILTER (WHERE hidden_kind IS NOT NULL),
                       count(*) FILTER (WHERE (? OR hidden_kind IS NULL)
                                          AND (? OR kind IN (SELECT unnest(?::VARCHAR[]))))
                FROM {table} WHERE {where}""",  # noqa: S608 (the clause is fixed text; values are parameters)
            [hidden, not kind, list(kind or []), *params],
        )[0]
        kind_counts = dict(
            store.query(
                f"""SELECT kind, count(*) FROM {table} WHERE {where} AND (? OR hidden_kind IS NULL)
                    GROUP BY kind ORDER BY kind""",  # noqa: S608
                [*params, hidden],
            )
        )
        d = "DESC" if order == "desc" else "ASC"
        page_rows = store.query_dicts(
            f"""SELECT * FROM {table}
                WHERE {where} AND (? OR hidden_kind IS NULL) AND (? OR kind IN (SELECT unnest(?::VARCHAR[])))
                ORDER BY ts_ns // 1000 {d}, request_id {d}{tiebreak.format(d=d)} LIMIT ? OFFSET ?""",  # noqa: S608
            [*params, hidden, not kind, list(kind or []), size, (page - 1) * size],
        )
        return RequestsPage(hidden_count, total, kind_counts, [request_row_from_table(r) for r in page_rows])

    def session_events(
        self, sid: str, lo: int, hi: int, internal: bool, collapse: bool, q: str | None, page: int, size: int
    ) -> tuple[int, int, list[EventRow]]:
        # SQL over the processor's event_rows (docs/architecture.md, Decision 4).
        return ss.event_rows(self.store, sid, lo, hi, internal, collapse, q, page, size)

    # --- person pages -----------------------------------------------------------------------------

    def person_sessions(
        self, pid: str, now: int, lo: int | None, hi: int | None, page: int, size: int, agent: Sequence[str] | None
    ) -> tuple[int, int | None, list[SessionRow]]:
        store = self.store
        # Subagent and review threads nest under their parent (handoff §11J gap 3): pages
        # count the top-level sessions, newest first, and each parent brings its children with it.
        params = [now, cc.LIVE_WITHIN_NS, ru.person_sessions(store, pid), hi, hi, lo, now, lo, agent, agent]
        shown = """WITH s AS (SELECT *, ? - last_ns <= ? AS live FROM session_rows
                               WHERE session_id IN (SELECT unnest(?::VARCHAR[]))),
                     r AS (SELECT * FROM s
                           WHERE (?::BIGINT IS NULL OR start_ns <= ?)
                             AND (?::BIGINT IS NULL OR CASE WHEN live THEN ? ELSE last_ns END >= ?)
                             AND (?::VARCHAR[] IS NULL OR list_contains(?::VARCHAR[], agent))),
                     n AS (SELECT *, coalesce(origin_kind IN ('subagent', 'review')
                                              AND origin_session_id IN (SELECT session_id FROM r), false) AS nested
                           FROM r),
                     roots AS (SELECT session_id AS root,
                                      row_number() OVER (ORDER BY start_ns DESC, session_id DESC) AS k
                               FROM n WHERE NOT nested)"""
        total, last = store.query(
            f"{shown} SELECT (SELECT count(*) FROM roots), (SELECT max(last_ns) FROM s)",  # noqa: S608 (fixed text)
            params,
        )[0]
        rows = [
            session_row_from_table(r)
            for r in store.query_dicts(
                f"""{shown} SELECT n.* FROM n JOIN roots ON n.session_id = roots.root
                                                  OR (n.nested AND n.origin_session_id = roots.root)
                    WHERE roots.k > ? AND roots.k <= ?
                    ORDER BY roots.k, n.nested, n.start_ns, n.session_id""",  # noqa: S608 (fixed text)
                [*params, (page - 1) * size, page * size],
            )
        ]
        return total, last, rows

    def person_session_rows(self, pid: str, now: int) -> tuple[list[str], list[Row]]:
        """The person's sessions (all their linked accounts'; `unattributed`: the sessions no account id
        appears on), and their rows."""
        sids = ru.person_sessions(self.store, pid)
        return sids, session_rows(self.store, now, sids)

    def person_timeline(
        self, sids: list[str], rs: list[Row], now_dt: datetime, now: int, lo: int, hi: int, bin_s: int
    ) -> Timeline:
        store = self.store

        def end(r: Row) -> int:  # an open interval ends now
            return now if r["live"] else int(r["last_ns"])

        bands = sorted(
            (r for r in rs if r["start_ns"] <= hi and end(r) >= lo), key=lambda r: (r["start_ns"], r["session_id"])
        )
        rows = cc.pack_rows([(r["session_id"], r["start_ns"], end(r)) for r in bands])
        return Timeline(
            from_=dt(lo),
            to=dt(hi),
            now=now_dt if lo <= now <= hi else None,
            binSeconds=bin_s,
            sessions=[tl.band_from_table(r, rows[r["session_id"]]) for r in bands],
            prompts=tl.prompts(store, sids, now, lo, hi),
            modelCalls=tl.model_calls(store, sids, bin_s, lo, hi),
            analytics=tl.analytics(store, sids, bin_s, lo, hi),
            notCaptured=tl.gaps(store, sids, lo, hi),
            presence=tl.presence(store, [r["session_id"] for r in bands], now, lo, hi),
            friction=tl.friction(store, sids, bin_s, lo, hi),
        )

    def person(self, pid: str, now_dt: datetime) -> tuple[PersonHeader, datetime | None]:
        store = self.store
        now = cc.to_ns(now_dt)
        # One rollup over all the person's sessions (rollups.py); the account's own facts beside it.
        r = ru.rollups(
            store, "SELECT ? AS row_id, session_id FROM person_sessions WHERE person_id = ?", [pid, pid], now
        ).get(pid)
        org, plan = next(
            iter(store.query("SELECT org, plan FROM account_facts WHERE account_id = ?", [pid])), (None, None)
        )
        devices = store.query(
            """SELECT device_id, platform, terminal, shell FROM account_devices
               WHERE account_id = ? AND on_events ORDER BY device_id""",
            [pid],
        )
        linked = [pid] if pid == cc.UNATTRIBUTED else ru.linked(store, pid)
        name, email = next(
            iter(
                store.query(
                    """SELECT (list(name ORDER BY account_id) FILTER (WHERE name <> ''))[1],
                              (list(email ORDER BY account_id) FILTER (WHERE email <> ''))[1]
                       FROM accounts WHERE account_id IN (SELECT unnest(?::VARCHAR[]))""",
                    [linked],
                )
            ),
            (None, None),
        )
        info = dr.agent_info(r) if r else []
        rl = (r["rl"] if r else None) or {"status": None, "util": None, "overage": None}
        last = dt(r["last_ns"]) if r else None
        header = PersonHeader(
            personId=pid,
            name=name,
            email=email,
            # Unattributed traffic has no identity (§11G.C3): its header stats are "—".
            accountIdShort="—" if pid == cc.UNATTRIBUTED else _short(pid),
            orgIdShort=_short(org) if org else None,
            orgKind="unknown",
            role=None,
            plan=plan,
            rateLimitTier=None,
            customerSince=None,
            firstUseAt=None,
            devices=[DeviceInfo(deviceId=d, platform=p, terminal=t, shell=sh) for d, p, t, sh in devices],
            limits=Limits(status=rl["status"], utilization7d=rl["util"], overage=rl["overage"]),
            flags=Flags(total=None, experiments=None, changedSince=None),
            posture=Posture(
                bypassPct=ru.pct(r["bypassed"], r["queries"]) if r else None,
                sandbox={"true": "on", "false": "off"}.get((r["sandbox"] if r else None) or ""),
                tokenStorage=None,
                proxyDetected="custom_ca" if r and r["custom_ca"] else None,
            ),
            lastSeenAt=last,
            live=bool(r and r["live"]),
            agentInfo=info,
            accounts=dr.linked_accounts(store, pid, linked, now) if pid != cc.UNATTRIBUTED else [],
            linkedBy="email" if len(linked) > 1 else None,
            agentLimits=_agent_limits(r, rl, plan),
            capabilities={a.agent: agents.CAPABILITIES[a.agent] for a in info},
            org=OrgStat(id=org, kind=agents.org_kind(org)) if org else None,
            # Model calls Claude Code reported that the proxy didn't capture; None without analytics.
            missed=r["missed_cc"] if r and r["with_events_cc"] else None,
        )
        return header, last

    def person_span(self, pid: str) -> tuple[list[str], int | None, int | None]:
        """The person's sessions, and their first activity and last."""
        sids = ru.person_sessions(self.store, pid)
        first, last = self.store.query(
            "SELECT min(start_ns), max(last_ns) FROM session_rows WHERE session_id IN (SELECT unnest(?::VARCHAR[]))",
            [sids],
        )[0]
        return sids, first, last

    def person_summary(self, pid: str, sids: list[str], now: int, lo: int, hi: int) -> tuple[PersonSummary, int | None]:
        store = self.store
        shown, with_events, last = store.query(
            f"""SELECT list(session_id ORDER BY session_id) FILTER (WHERE {ru.SHOWN}),
                       count(*) FILTER (WHERE {ru.SHOWN} AND events IS NOT NULL), max(last_ns)
                FROM ({ru.ROWS}) WHERE session_id IN (SELECT unnest(?::VARCHAR[]))""",  # noqa: S608 (fixed text)
            [hi, now, lo, hi, now, lo, now, cc.LIVE_WITHIN_NS, sids],
        )[0]
        shown = shown or []
        span = [shown, lo, hi]
        totals = _EVENT_TOTALS.format(batch=getattr(store, "event_batch", _EVENT_BATCH))
        events, batches, types, heads, hooks, hook_ms, c5, c1, cost, outside = store.query(totals, span)[0]
        dims: dict[str, Counter[str]] = {
            k: Counter() for k in ("name", "tool", "mcp_tool", "word", "destructive", "git", "pr")
        }
        for kind, key, n in store.query(_EVENT_DIMENSIONS, span):
            dims[kind][key] += n
        tools = dims["tool"] + dims["mcp_tool"]
        calls, t_in, t_read, t_write, t_out, subagents, unreported, prompts = store.query(_CALLS, span)[0]
        reported, missed = store.query(_MARKS, span)[0]
        (account_calls,) = store.query(
            "SELECT count(*) FROM request_rows WHERE person_id = ? AND kind = 'account' AND ts_ns BETWEEN ? AND ?",
            [pid, lo, hi],
        )[0]

        # Coverage within the range, across the sessions in it.
        reported = reported if with_events else None
        coverage = Coverage(
            reported=reported,
            captured=(reported - missed) if reported is not None else calls,
            notCaptured=missed,
            windows=[CoverageWindow(from_=dt(a), to=dt(b), count=n) for _, a, b, n in ru.windows(store, _MISSED, span)],
        )
        # MCP from the responses' tool_use blocks (analytics redact most MCP names to "mcp_tool").
        mcp_used: dict[str, set[str]] = {}
        builtin_used: set[str] = set()
        for kind, server, tool in store.query(
            """SELECT DISTINCT kind, server, tool FROM call_tool_uses
               WHERE session_id IN (SELECT unnest(?::VARCHAR[])) AND ts_ns BETWEEN ? AND ?""",
            span,
        ):
            if kind == "mcp":
                mcp_used.setdefault(server, set()).add(tool)
            else:
                builtin_used.add(tool)
        for name in dims["mcp_tool"]:  # and the MCP names the analytics do spell out (mcp__server__tool)
            parts = name.split("__")
            if name.startswith("mcp__") and len(parts) >= 3:
                mcp_used.setdefault(parts[1], set()).add("__".join(parts[2:]))
        servers = {k: len(v) for k, v in mcp_used.items()}
        names, git = dims["name"], dims["git"]
        has_events = bool(names)
        summary = PersonSummary(
            ribbon=Ribbon(
                modelCalls=calls,
                sessions=len(shown),
                analyticsEvents=events,
                analyticsBatches=batches,
                eventTypes=types,
                accountCalls=account_calls,
                toolsReachable=None,
                tokens=TokenCount(
                    input=t_in,
                    cacheRead=t_read,
                    cacheWrite=t_write,
                    cacheWrite5m=c5,
                    cacheWrite1h=c1,
                    output=t_out,
                    accuracy="exact" if calls else "missing",
                ),
                cost=Cost(
                    usd=round(cost, 2) if cost is not None else None, basis="api-equivalent", estimatedCalls=unreported
                ),
            ),
            coverage=coverage,
            capabilities=Capabilities(
                mcpServers=[
                    McpServer(name=k, tools=None, toolsUsed=n, riskyVerbs=[]) for k, n in sorted(servers.items())
                ],
                mcpToolsTotal=store.query(_LATEST, ["tengu_tool_search_mode_decision", shown, hi])[0][0],
                mcpToolsUsed=sum(servers.values()) if has_events or servers else None,
                builtinTools=None,
                builtinUsed=len(builtin_used | (set(tools) - set(dims["mcp_tool"])))
                if has_events or builtin_used
                else None,
                skills=store.query(_LATEST, ["tengu_skills_sync_success", shown, hi])[0][0],
                hooks=hooks if has_events else None,
                hookOverheadMs=round(hook_ms, 1) if hook_ms is not None else None,
                memoryWrites=names.get("tengu_memdir_file_write", 0) if has_events else None,
                variesBySession=None,
            ),
            footprint=Footprint(
                prompts=prompts or None,
                toolCalls=sum(tools.values()) if has_events else None,
                toolsByName=dict(tools.most_common()),
                commands=names.get("tengu_bash_tool_command_executed", 0) if has_events else None,
                # Ties by name, so the order (and the samples) don't depend on event order.
                topCommandWords=[
                    CommandWord(word=w, count=n)
                    for w, n in sorted(dims["word"].items(), key=lambda x: (-x[1], x[0]))[:8]
                ],
                destructive=Destructive(
                    total=sum(dims["destructive"].values()),
                    rmF=dims["destructive"].get("rm_force", 0),
                    rmRf=dims["destructive"].get("rm_recursive_force", 0),
                    outsideCwd=outside,
                ),
                codeShipped=CodeShipped(
                    headChanges=max(heads - 1, 0) if heads else None,
                    commits=git.get("commit", 0),
                    pushes=git.get("push", 0),
                    prs=sorted(int(k) for k in dims["pr"] if k.isdigit()),
                ),
                failures=Failures(
                    commands=names.get("tengu_bash_tool_command_failed", 0),
                    toolErrors=names.get("tengu_tool_use_error", 0),
                    apiRetries=names.get("tengu_api_retry", 0),
                ),
                subagentCalls=subagents,
            ),
        )
        return summary, last

    def person_requests(
        self, pid: str, lo: int | None, hi: int | None, kind: Sequence[str] | None, q: str | None,
        captured: bool | None, page: int, size: int, order: str, agent: Sequence[str] | None, hidden: bool,
    ) -> RequestsPage:  # fmt: skip
        # SQL over the processor's request_rows (docs/architecture.md, Decision 4): the rows are
        # stored as shown, so this only filters, counts and pages them.
        got = self._requests("request_rows", "person_id", pid, lo, hi, kind, q, captured, page, size, order, agent,
                             hidden)  # fmt: skip
        last = self.store.query("SELECT last_ns FROM person_last WHERE person_id = ?", [pid])
        return RequestsPage(got.hidden_count, got.total, got.kind_counts, got.rows, last[0][0] if last else None)

    def coverage(self, pid: str, sid: str | None, lo: int | None, hi: int | None) -> tuple[CoverageDetail, int | None]:
        store = self.store
        sids = [s for s in ru.person_sessions(store, pid) if sid is None or s == sid]
        span = [sids, lo, lo, hi, hi]
        with_events, last = store.query(
            """SELECT count(*) FILTER (WHERE events IS NOT NULL), max(last_ns) FROM session_rows
               WHERE session_id IN (SELECT unnest(?::VARCHAR[]))""",
            [sids],
        )[0]
        reported, missed, calls = store.query(
            f"""SELECT count(*) FILTER (WHERE kind = 'reported'), count(*) FILTER (WHERE kind = 'missed'),
                       count(*) FILTER (WHERE kind = 'call')
                FROM session_marks WHERE session_id IN (SELECT unnest(?::VARCHAR[])) AND {_WITHIN}""",  # noqa: S608
            span,
        )[0]
        # The missed calls' rows, as the person's request log has them (request_rows).
        rows = store.query_dicts(
            f"""SELECT * FROM request_rows WHERE person_id = ? AND request_id LIKE 'reported:%'
                  AND session_id IN (SELECT unnest(?::VARCHAR[])) AND {_WITHIN.replace("t_ns", "ts_ns")}
                ORDER BY ts_ns, session_id, request_id""",  # noqa: S608
            [pid, *span],
        )
        wins = ru.windows(
            store,
            f"""SELECT 0 AS grp, t_ns AS t FROM session_marks
                WHERE kind = 'missed' AND session_id IN (SELECT unnest(?::VARCHAR[])) AND {_WITHIN}""",  # noqa: S608
            span,
        )
        reported = reported if with_events else None
        detail = CoverageDetail(
            reported=reported,
            captured=(reported - missed) if reported is not None else calls,
            notCaptured=missed,
            windows=[CoverageWindow(from_=dt(a), to=dt(b), count=n) for _, a, b, n in wins],
            calls=[request_row_from_table(r) for r in rows],
        )
        return detail, last

    # --- the directory ----------------------------------------------------------------------------

    def people(
        self, now_dt: datetime, lo: int | None, hi: int | None, sort: str, agent: Sequence[str] | None,
        q: str | None, page: int, size: int,
    ) -> PeoplePage:  # fmt: skip
        store = self.store
        rows = dr.people(store, now_dt, lo, hi)
        if agent:
            rows = [r for r in rows if set(r.agents) & set(agent)]
        if q:
            needle = q.strip().lower()
            devices = dr.device_owners(store, [r.personId for r in rows])
            rows = [
                r
                for r in rows
                if any(needle in (x or "").lower() for x in (r.name, r.email, r.personId))
                or any(needle in d.lower() for d in devices.get(r.personId, ()))
            ]
        if sort == "activity":
            rows.sort(key=lambda r: (-sum(d.count for d in r.activity), r.personId))
        elif sort == "name":
            rows.sort(key=lambda r: (r.name is None, (r.name or "").lower(), r.personId))
        else:
            rows.sort(key=lambda r: (r.lastSeenAt, r.personId), reverse=True)
        last = max((r.lastSeenAt for r in rows), default=None)
        return PeoplePage(rows[(page - 1) * size : page * size], len(rows), last)

    def overview(self, now_dt: datetime, lo: int | None, hi: int | None) -> tuple[Overview, datetime | None]:
        rows = dr.people(self.store, now_dt, lo, hi)
        last = max((r.lastSeenAt for r in rows), default=None)
        return dr.overview(self.store, rows, lo, hi), last
