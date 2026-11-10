from collections import Counter
from datetime import datetime

from fastapi import APIRouter, Query, Request
from pydantic import AwareDatetime

from aintercept_api import agents
from aintercept_api import claude_code as cc
from aintercept_api import directory as dr
from aintercept_api import person as pp
from aintercept_api import timeline as tl
from aintercept_api.models.common import Agent, AgentLimits, Coverage, CoverageWindow, LimitWindow, Meta, RequestKind
from aintercept_api.models.people import (
    Capabilities,
    CodeShipped,
    CommandWord,
    Cost,
    CoverageDetail,
    CoverageResponse,
    Destructive,
    DeviceInfo,
    Failures,
    Flags,
    Footprint,
    Limits,
    McpServer,
    OrgStat,
    OverviewResponse,
    PeopleResponse,
    PersonHeader,
    PersonRequestsResponse,
    PersonResponse,
    PersonSessionsResponse,
    PersonSummary,
    PersonSummaryResponse,
    PersonTimelineResponse,
    Posture,
    Ribbon,
    Timeline,
    TokenCount,
)
from aintercept_api.rows import coverage as session_coverage
from aintercept_api.rows import dt as _dt
from aintercept_api.rows import last_data as _last_data
from aintercept_api.rows import matches as _matches
from aintercept_api.rows import missed_row as _missed_row
from aintercept_api.rows import request_rows as _request_rows
from aintercept_api.rows import session_row as _session_row
from aintercept_api.store import Store

router = APIRouter()
AGENT = Query(None, description="Repeatable; only these agents' (handoff §11M 3.3). Default all.")

FROM = Query(
    None, alias="from", description="Range start (RFC 3339). Default: unbounded / the person's first activity."
)
TO = Query(None, description="Range end (RFC 3339). Default: unbounded / now.")


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
    now_dt = request.app.state.clock()
    now = cc.to_ns(now_dt)
    sessions = cc.load_person(request.app.state.store, personId)
    lo = cc.to_ns(from_) if from_ else None
    hi = cc.to_ns(to) if to else None
    in_range = [
        s
        for s in sessions.values()
        if (hi is None or s.start <= hi)
        and (lo is None or (now if s.live(now) else s.last) >= lo)
        and (not agent or agents.agent_of(s.product) in agent)
    ]
    in_range.sort(key=lambda s: (s.start, s.id), reverse=True)
    # Subagent and review threads nest under their parent (handoff §11J gap 3): pages
    # count the top-level sessions, and each parent brings its children with it.
    ids = {s.id for s in in_range}
    nested = {
        s.id for s in in_range if s.origin and s.origin.kind in ("subagent", "review") and s.origin.session_id in ids
    }
    roots = [s for s in in_range if s.id not in nested]
    shown: list[cc.Session] = []
    for r in roots[(page - 1) * pageSize : page * pageSize]:
        shown.append(r)
        shown.extend(sorted((sessions[c] for c, _ in r.children if c in nested), key=lambda s: (s.start, s.id)))
    ws = agents.workspace(request.app.state.store, [s.id for s in shown if s.product == "codex"])
    rows = [_session_row(s, now, ws.get(s.id)) for s in shown]
    meta = Meta(
        asOf=now_dt,
        lastDataAt=_last_data(sessions),
        from_=from_,
        to=to,
        page=page,
        pageSize=pageSize,
        total=len(roots),
    )
    return PersonSessionsResponse(data=rows, meta=meta)


@router.get("/people/{personId}/timeline", response_model=PersonTimelineResponse)
def get_person_timeline(
    request: Request,
    personId: str,  # noqa: N803
    from_: AwareDatetime | None = FROM,
    to: AwareDatetime | None = TO,
    bin: int | None = Query(None, ge=60, description="Bin width in seconds. Default: auto from the range (§5.2.5)."),
) -> PersonTimelineResponse:
    store = request.app.state.store
    now_dt = request.app.state.clock()
    now = cc.to_ns(now_dt)
    sessions = cc.load_person(store, personId)
    all_s = list(sessions.values())
    lo = cc.to_ns(from_) if from_ else min((s.start for s in all_s), default=now - 14 * 86400 * cc.NS)
    hi = cc.to_ns(to) if to else max([now, *(s.last for s in all_s)])
    bin_s = bin or cc.auto_bin_seconds(hi - lo)

    def within(t: int) -> bool:
        return lo <= t <= hi

    shown = [s for s in all_s if s.start <= hi and (now if s.live(now) else s.last) >= lo]
    rows = cc.pack_rows([(s.id, s.start, now if s.live(now) else s.last) for s in shown])
    missed = [t for s in all_s for t in (cc.unmatched_reports(s) or []) if within(t)]
    timeline = Timeline(
        from_=_dt(lo),
        to=_dt(hi),
        now=now_dt if lo <= now <= hi else None,
        binSeconds=bin_s,
        sessions=[tl.band(s, now, rows[s.id]) for s in sorted(shown, key=lambda s: s.start)],
        prompts=tl.prompts(store, all_s, now, within),
        modelCalls=tl.model_calls([c for s in all_s for c in s.calls if within(c.ts)], missed, bin_s),
        analytics=tl.analytics(store, list(sessions), bin_s, lo, hi),
        notCaptured=tl.gaps(all_s, within),
        presence=tl.presence(
            [(max(a, lo), min(b, hi), st) for a, b, st in cc.presence(shown, now) if b > lo and a < hi]
        ),
        friction=tl.friction(all_s, bin_s, within),
    )
    meta = Meta(asOf=now_dt, lastDataAt=_last_data(sessions), from_=_dt(lo), to=_dt(hi))
    return PersonTimelineResponse(data=timeline, meta=meta)


# --- people page (M5) ------------------------------------------------------------------------


def _window(from_: datetime | None, to: datetime | None) -> tuple[int | None, int | None]:
    return (cc.to_ns(from_) if from_ else None, cc.to_ns(to) if to else None)


@router.get("/overview", response_model=OverviewResponse)
def get_overview(
    request: Request, from_: AwareDatetime | None = FROM, to: AwareDatetime | None = TO
) -> OverviewResponse:
    store = request.app.state.store
    now_dt = request.app.state.clock()
    window = _window(from_, to)
    rows = dr.people(store, now_dt, *window)
    last = max((r.lastSeenAt for r in rows), default=None)
    return OverviewResponse(
        data=dr.overview(store, rows, *window), meta=Meta(asOf=now_dt, lastDataAt=last, from_=from_, to=to)
    )


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
    store = request.app.state.store
    now_dt = request.app.state.clock()
    rows = dr.people(store, now_dt, *_window(from_, to))
    if agent:
        rows = [r for r in rows if set(r.agents) & set(agent)]
    if q:
        needle = q.strip().lower()
        devices = dr.device_owners(store)
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
    meta = Meta(asOf=now_dt, lastDataAt=last, from_=from_, to=to, page=page, pageSize=pageSize, total=len(rows))
    return PeopleResponse(data=rows[(page - 1) * pageSize : page * pageSize], meta=meta)


# --- header, summary, requests, coverage (M2) -------------------------------------------------


def _short(x: str) -> str:
    return f"{x[:4]}…{x[-4:]}" if len(x) > 10 else x


@router.get("/people/{personId}", response_model=PersonResponse)
def get_person(request: Request, personId: str) -> PersonResponse:  # noqa: N803
    store = request.app.state.store
    now_dt = request.app.state.clock()
    now = cc.to_ns(now_dt)
    sessions = cc.load_person(store, personId)
    sids = list(sessions)
    f = pp.header_facts(store, personId, sids, pp.key_hashes(store, sids))
    all_s = list(sessions.values())
    bypass = _pct([e for s in all_s for e in s.events if e.name == "tengu_api_query" and e.permission_mode])
    linked = [personId] if personId == cc.UNATTRIBUTED else cc.linked_accounts(store, personId)
    who = dr.accounts(store)
    name = next((who[a][0] for a in linked if a in who and who[a][0]), None)
    email = next((who[a][1] for a in linked if a in who and who[a][1]), None)
    info = dr.agent_info(store, sessions)
    header = PersonHeader(
        personId=personId,
        name=name,
        email=email,
        # Unattributed traffic has no identity (§11G.C3): its header stats are "—".
        accountIdShort="—" if personId == cc.UNATTRIBUTED else _short(personId),
        orgIdShort=_short(f.org) if f.org else None,
        orgKind="unknown",
        role=None,
        plan=f.plan,
        rateLimitTier=None,
        customerSince=None,
        firstUseAt=None,
        devices=[DeviceInfo(deviceId=d, platform=p, terminal=t, shell=sh) for d, p, t, sh in f.devices],
        limits=Limits(status=f.limit_status, utilization7d=f.utilization_7d, overage=f.overage),
        flags=Flags(total=None, experiments=None, changedSince=None),
        posture=Posture(bypassPct=bypass, sandbox=f.sandbox, tokenStorage=None, proxyDetected=f.proxy_detected),
        lastSeenAt=_last_data(sessions),
        live=any(s.live(now) for s in all_s),
        agentInfo=info,
        accounts=dr.linked_accounts(store, linked, sessions) if personId != cc.UNATTRIBUTED else [],
        linkedBy="email" if len(linked) > 1 else None,
        agentLimits=_agent_limits(store, sessions, f),
        capabilities={a.agent: agents.CAPABILITIES[a.agent] for a in info},
        org=OrgStat(id=f.org, kind=agents.org_kind(f.org)) if f.org else None,
        missed=_missed([s for s in all_s if s.product != "codex"]),
    )
    return PersonResponse(data=header, meta=Meta(asOf=now_dt, lastDataAt=_last_data(sessions)))


def _missed(sessions: list[cc.Session]) -> int | None:
    """Model calls Claude Code reported that the proxy didn't capture; None without analytics."""
    covs = [c for c in (session_coverage(s) for s in sessions) if c.reported is not None]
    return sum(c.notCaptured for c in covs) if covs else None


def _agent_limits(store: Store, sessions: dict[str, cc.Session], f: pp.HeaderFacts) -> list[AgentLimits]:
    """Each agent's usage limits on its newest response (the header's AgentStatGroup, handoff §11J gap 5)."""
    out = []
    cc_sids = [k for k, s in sessions.items() if s.product != "codex"]
    if cc_sids and f.utilization_7d is not None:
        newest = max(sessions[k].last for k in cc_sids)
        out.append(
            AgentLimits(
                agent="claude-code",
                plan=f.plan,
                windows=[LimitWindow(name="7d", usedPct=round(f.utilization_7d * 100, 1), resetsAt=None)],
                credits=f"overage {f.overage}" if f.overage else None,
                asOf=_dt(newest),
            )
        )
    codex = agents.latest_codex_limits(store, [k for k, s in sessions.items() if s.product == "codex"])
    if codex:
        out.append(codex)
    return out


def _pct(events: list[cc.Ev]) -> float | None:
    if not events:
        return None
    return round(100 * sum(e.permission_mode == "bypassPermissions" for e in events) / len(events), 1)


def _range(sessions: dict[str, cc.Session], from_: datetime | None, to: datetime | None, now: int) -> tuple[int, int]:
    lo = cc.to_ns(from_) if from_ else min((s.start for s in sessions.values()), default=0)
    hi = cc.to_ns(to) if to else max([now, *(s.last for s in sessions.values())])
    return lo, hi


@router.get("/people/{personId}/summary", response_model=PersonSummaryResponse)
def get_person_summary(
    request: Request,
    personId: str,  # noqa: N803
    from_: AwareDatetime | None = FROM,
    to: AwareDatetime | None = TO,
) -> PersonSummaryResponse:
    store = request.app.state.store
    now_dt = request.app.state.clock()
    now = cc.to_ns(now_dt)
    sessions = cc.load_person(store, personId)
    lo, hi = _range(sessions, from_, to, now)
    shown = pp.sessions_in_range(sessions, lo, hi, now)
    sids = [s.id for s in shown]
    ev = pp.event_rollup(store, sids, lo, hi)
    calls, t_in, t_read, t_write, t_out, subagents = pp.call_tokens(store, sids, lo, hi)
    hashes = pp.key_hashes(store, list(sessions))
    account_calls = sum(1 for t in pp.transactions(store, hashes) if t.kind == "account" and lo <= t.ts <= hi)

    # Coverage within the range, across the sessions in it.
    missed = [e for s in shown for e in (cc.missed_calls(s) or []) if lo <= e.ts <= hi]
    with_events = [s for s in shown if s.event_count]
    reported = (
        sum(1 for s in with_events for e in s.events if e.name == "tengu_api_success" and lo <= e.ts <= hi)
        if with_events
        else None
    )
    coverage = Coverage(
        reported=reported,
        captured=(reported - len(missed)) if reported is not None else calls,
        notCaptured=len(missed),
        windows=[CoverageWindow(from_=_dt(a), to=_dt(b), count=n) for a, b, n in cc.windows([e.ts for e in missed])],
    )
    # MCP from the responses' tool_use blocks (analytics redact most MCP names to "mcp_tool").
    mcp_used, builtin_used = pp.tool_uses(store, sids, lo, hi)
    for name in ev.mcp_tools:  # and the MCP names the analytics do spell out (mcp__server__tool)
        parts = name.split("__")
        if name.startswith("mcp__") and len(parts) >= 3:
            mcp_used.setdefault(parts[1], set()).add("__".join(parts[2:]))
    servers = {k: len(v) for k, v in mcp_used.items()}
    has_events = bool(ev.names)
    prompt_ids = {c.prompt_id for s in shown for c in s.calls if c.prompt_id and lo <= c.ts <= hi}
    summary = PersonSummary(
        ribbon=Ribbon(
            modelCalls=calls,
            sessions=len(shown),
            analyticsEvents=ev.events,
            analyticsBatches=ev.batches,
            eventTypes=ev.types,
            accountCalls=account_calls,
            toolsReachable=None,
            tokens=TokenCount(
                input=t_in,
                cacheRead=t_read,
                cacheWrite=t_write,
                cacheWrite5m=ev.cache_5m,
                cacheWrite1h=ev.cache_1h,
                output=t_out,
                accuracy="exact" if calls else "missing",
            ),
            cost=Cost(
                usd=round(ev.cost, 2) if ev.cost is not None else None,
                basis="api-equivalent",
                estimatedCalls=sum(1 for s in shown for c in cc.unreported_calls(s) if lo <= c.ts <= hi),
            ),
        ),
        coverage=coverage,
        capabilities=Capabilities(
            mcpServers=[McpServer(name=k, tools=None, toolsUsed=n, riskyVerbs=[]) for k, n in sorted(servers.items())],
            mcpToolsTotal=pp.latest_md(store, sids, "tengu_tool_search_mode_decision", "mcpToolCount", hi),
            mcpToolsUsed=sum(servers.values()) if has_events or servers else None,
            builtinTools=None,
            builtinUsed=len(builtin_used | (set(ev.tools) - set(ev.mcp_tools))) if has_events or builtin_used else None,
            skills=pp.latest_md(store, sids, "tengu_skills_sync_success", "total", hi),
            hooks=ev.hooks if has_events else None,
            hookOverheadMs=round(ev.hook_ms, 1) if ev.hook_ms is not None else None,
            memoryWrites=ev.names.get("tengu_memdir_file_write", 0) if has_events else None,
            variesBySession=None,
        ),
        footprint=Footprint(
            prompts=len(prompt_ids) or None,
            toolCalls=sum(ev.tools.values()) if has_events else None,
            toolsByName=dict(ev.tools.most_common()),
            commands=ev.names.get("tengu_bash_tool_command_executed", 0) if has_events else None,
            # Ties by name, so the order (and the samples) don't depend on event order.
            topCommandWords=[
                CommandWord(word=w, count=n) for w, n in sorted(ev.words.items(), key=lambda x: (-x[1], x[0]))[:8]
            ],
            destructive=Destructive(
                total=sum(ev.destructive.values()),
                rmF=ev.destructive.get("rm_force", 0),
                rmRf=ev.destructive.get("rm_recursive_force", 0),
                outsideCwd=ev.outside_cwd,
            ),
            codeShipped=CodeShipped(
                headChanges=ev.head_changes,
                commits=ev.git.get("commit", 0),
                pushes=ev.git.get("push", 0),
                prs=ev.prs,
            ),
            failures=Failures(
                commands=ev.names.get("tengu_bash_tool_command_failed", 0),
                toolErrors=ev.names.get("tengu_tool_use_error", 0),
                apiRetries=ev.names.get("tengu_api_retry", 0),
            ),
            subagentCalls=subagents,
        ),
    )
    meta = Meta(asOf=now_dt, lastDataAt=_last_data(sessions), from_=_dt(lo), to=_dt(hi))
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
    store = request.app.state.store
    now_dt = request.app.state.clock()
    sessions = cc.load_person(store, personId)
    rows = [
        r
        for r in _request_rows(store, sessions)
        if _matches(r, from_, to, None, q, captured) and (not agent or r.agent in agent)
    ]
    hidden_count = sum(r.hiddenKind is not None for r in rows)
    rows = [r for r in rows if hidden or r.hiddenKind is None]
    kind_counts = Counter(r.kind for r in rows)
    rows = [r for r in rows if not kind or r.kind in kind]
    rows.sort(key=lambda r: (r.t, r.requestId), reverse=order == "desc")
    page_rows = rows[(page - 1) * pageSize : page * pageSize]
    meta = Meta(
        asOf=now_dt, lastDataAt=_last_data(sessions), from_=from_, to=to, page=page, pageSize=pageSize, total=len(rows)
    )
    return PersonRequestsResponse(
        data=page_rows, meta=meta, kindCounts=dict(sorted(kind_counts.items())), hiddenCount=hidden_count
    )


@router.get("/coverage", response_model=CoverageResponse)
def get_coverage(
    request: Request,
    personId: str = Query(..., description="The person whose coverage to check."),  # noqa: N803
    sessionId: str | None = Query(None, description="Narrow to one of the person's sessions."),  # noqa: N803
    from_: AwareDatetime | None = FROM,
    to: AwareDatetime | None = TO,
) -> CoverageResponse:
    store = request.app.state.store
    now_dt = request.app.state.clock()
    sessions = cc.load_person(store, personId)
    if sessionId is not None:
        sessions = {k: v for k, v in sessions.items() if k == sessionId}
    lo = cc.to_ns(from_) if from_ else None
    hi = cc.to_ns(to) if to else None

    def within(t: int) -> bool:
        return (lo is None or t >= lo) and (hi is None or t <= hi)

    missed = [(s.id, e) for s in sessions.values() for e in (cc.missed_calls(s) or []) if within(e.ts)]
    reported_by = pp.reports(store, list(sessions))
    with_events = [s for s in sessions.values() if s.event_count]
    reported = (
        sum(1 for s in with_events for e in s.events if e.name == "tengu_api_success" and within(e.ts))
        if with_events
        else None
    )
    calls = sum(1 for s in sessions.values() for c in s.calls if within(c.ts))
    detail = CoverageDetail(
        reported=reported,
        captured=(reported - len(missed)) if reported is not None else calls,
        notCaptured=len(missed),
        windows=[CoverageWindow(from_=_dt(a), to=_dt(b), count=n) for a, b, n in cc.windows([e.ts for _, e in missed])],
        calls=[_missed_row(sid, e, reported_by) for sid, e in sorted(missed, key=lambda x: x[1].ts)],
    )
    return CoverageResponse(data=detail, meta=Meta(asOf=now_dt, lastDataAt=_last_data(sessions), from_=from_, to=to))
