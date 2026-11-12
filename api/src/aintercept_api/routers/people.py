from collections import Counter
from datetime import datetime
from typing import Any

from fastapi import APIRouter, Query, Request
from pydantic import AwareDatetime

from aintercept_api import agents
from aintercept_api import claude_code as cc
from aintercept_api import directory as dr
from aintercept_api import rollups as ru
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
from aintercept_api.rows import dt as _dt
from aintercept_api.rows import request_filter, session_rows
from aintercept_api.rows import request_row_from_table as _request_row_from_table
from aintercept_api.rows import session_row_from_table as _session_row_from_table
from aintercept_api.store import Store
from aintercept_api.timeutil import ns_to_dt

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
    store = request.app.state.store
    now_dt = request.app.state.clock()
    now = cc.to_ns(now_dt)
    lo = cc.to_ns(from_) if from_ else None
    hi = cc.to_ns(to) if to else None
    # Subagent and review threads nest under their parent (handoff §11J gap 3): pages
    # count the top-level sessions, newest first, and each parent brings its children with it.
    params = [now, cc.LIVE_WITHIN_NS, _person_session_ids(store, personId), hi, hi, lo, now, lo, agent, agent]
    shown = """WITH s AS (SELECT *, ? - last_ns <= ? AS live FROM session_rows
                           WHERE session_id IN (SELECT unnest(?::VARCHAR[]))),
                 r AS (SELECT * FROM s
                       WHERE (?::BIGINT IS NULL OR start_ns <= ?)
                         AND (?::BIGINT IS NULL OR CASE WHEN live THEN ? ELSE last_ns END >= ?)
                         AND (?::VARCHAR[] IS NULL OR list_contains(?::VARCHAR[], agent))),
                 n AS (SELECT *, coalesce(origin_kind IN ('subagent', 'review')
                                          AND origin_session_id IN (SELECT session_id FROM r), false) AS nested
                       FROM r),
                 roots AS (SELECT session_id AS root, row_number() OVER (ORDER BY start_ns DESC, session_id DESC) AS k
                           FROM n WHERE NOT nested)"""
    total, last = store.query(
        f"{shown} SELECT (SELECT count(*) FROM roots), (SELECT max(last_ns) FROM s)",  # noqa: S608 (fixed text)
        params,
    )[0]
    rows = [
        _session_row_from_table(r)
        for r in store.query_dicts(
            f"""{shown} SELECT n.* FROM n JOIN roots ON n.session_id = roots.root
                                              OR (n.nested AND n.origin_session_id = roots.root)
                WHERE roots.k > ? AND roots.k <= ?
                ORDER BY roots.k, n.nested, n.start_ns, n.session_id""",  # noqa: S608 (fixed text)
            [*params, (page - 1) * pageSize, page * pageSize],
        )
    ]
    meta = Meta(
        asOf=now_dt,
        lastDataAt=ns_to_dt(last),
        from_=from_,
        to=to,
        page=page,
        pageSize=pageSize,
        total=total,
    )
    return PersonSessionsResponse(data=rows, meta=meta)


def _person_session_ids(store: Store, person_id: str) -> list[str]:
    """The person's sessions (all their linked accounts'); `UNATTRIBUTED`: the sessions no account id appears on."""
    return ru.person_sessions(store, person_id)


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
    sids = _person_session_ids(store, personId)
    rs = session_rows(store, now, sids)
    lo = cc.to_ns(from_) if from_ else min((r["start_ns"] for r in rs), default=now - 14 * 86400 * cc.NS)
    hi = cc.to_ns(to) if to else max([now, *(r["last_ns"] for r in rs)])
    bin_s = bin or cc.auto_bin_seconds(hi - lo)

    def end(r: dict[str, Any]) -> int:  # an open interval ends now
        return now if r["live"] else r["last_ns"]

    bands = sorted(
        (r for r in rs if r["start_ns"] <= hi and end(r) >= lo), key=lambda r: (r["start_ns"], r["session_id"])
    )
    rows = cc.pack_rows([(r["session_id"], r["start_ns"], end(r)) for r in bands])
    timeline = Timeline(
        from_=_dt(lo),
        to=_dt(hi),
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
    last = max((r["last_ns"] for r in rs), default=None)
    meta = Meta(asOf=now_dt, lastDataAt=ns_to_dt(last), from_=_dt(lo), to=_dt(hi))
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
    # One rollup over all the person's sessions (rollups.py); the account's own facts beside it.
    r = ru.rollups(
        store, "SELECT ? AS row_id, session_id FROM person_sessions WHERE person_id = ?", [personId, personId], now
    ).get(personId)
    org, plan = next(
        iter(store.query("SELECT org, plan FROM account_facts WHERE account_id = ?", [personId])), (None, None)
    )
    devices = store.query(
        """SELECT device_id, platform, terminal, shell FROM account_devices
           WHERE account_id = ? AND on_events ORDER BY device_id""",
        [personId],
    )
    linked = [personId] if personId == cc.UNATTRIBUTED else ru.linked(store, personId)
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
    last = ns_to_dt(r["last_ns"]) if r else None
    header = PersonHeader(
        personId=personId,
        name=name,
        email=email,
        # Unattributed traffic has no identity (§11G.C3): its header stats are "—".
        accountIdShort="—" if personId == cc.UNATTRIBUTED else _short(personId),
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
        accounts=dr.linked_accounts(store, personId, linked, now) if personId != cc.UNATTRIBUTED else [],
        linkedBy="email" if len(linked) > 1 else None,
        agentLimits=_agent_limits(r, rl, plan),
        capabilities={a.agent: agents.CAPABILITIES[a.agent] for a in info},
        org=OrgStat(id=org, kind=agents.org_kind(org)) if org else None,
        # Model calls Claude Code reported that the proxy didn't capture; None without analytics.
        missed=r["missed_cc"] if r and r["with_events_cc"] else None,
    )
    return PersonResponse(data=header, meta=Meta(asOf=now_dt, lastDataAt=last))


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
                asOf=_dt(r["last_cc"]),
            )
        )
    if r["codex_limits"]:
        out.append(AgentLimits.model_validate_json(r["codex_limits"]))
    return out


# The summary's counts over a range, one pass each (person.py's, over the processor's rows).
_EVENTS = """SELECT * FROM event_rows WHERE session_id IN (SELECT unnest(?::VARCHAR[])) AND ts_ns BETWEEN ? AND ?"""
_EVENT_TOTALS = f"""WITH e AS ({_EVENTS})
SELECT count(*), count(DISTINCT regexp_extract(event_id, '^(.*):[0-9]+$', 1)),
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


def _range(store: Store, sids: list[str], from_: datetime | None, to: datetime | None, now: int) -> tuple[int, int]:
    """The request's range: by default, the person's first activity to now (or their last, if later)."""
    first, last = store.query(
        "SELECT min(start_ns), max(last_ns) FROM session_rows WHERE session_id IN (SELECT unnest(?::VARCHAR[]))",
        [sids],
    )[0]
    lo = cc.to_ns(from_) if from_ else first if first is not None else 0
    hi = cc.to_ns(to) if to else max(now, last) if last is not None else now
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
    sids = ru.person_sessions(store, personId)
    lo, hi = _range(store, sids, from_, to, now)
    shown, with_events, last = store.query(
        f"""SELECT list(session_id ORDER BY session_id) FILTER (WHERE {ru.SHOWN}),
                   count(*) FILTER (WHERE {ru.SHOWN} AND events IS NOT NULL), max(last_ns)
            FROM ({ru.ROWS}) WHERE session_id IN (SELECT unnest(?::VARCHAR[]))""",  # noqa: S608 (fixed text)
        [hi, now, lo, hi, now, lo, now, cc.LIVE_WITHIN_NS, sids],
    )[0]
    shown = shown or []
    span = [shown, lo, hi]
    events, batches, types, heads, hooks, hook_ms, c5, c1, cost, outside = store.query(_EVENT_TOTALS, span)[0]
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
        [personId, lo, hi],
    )[0]

    # Coverage within the range, across the sessions in it.
    reported = reported if with_events else None
    coverage = Coverage(
        reported=reported,
        captured=(reported - missed) if reported is not None else calls,
        notCaptured=missed,
        windows=[CoverageWindow(from_=_dt(a), to=_dt(b), count=n) for _, a, b, n in ru.windows(store, _MISSED, span)],
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
            mcpServers=[McpServer(name=k, tools=None, toolsUsed=n, riskyVerbs=[]) for k, n in sorted(servers.items())],
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
                CommandWord(word=w, count=n) for w, n in sorted(dims["word"].items(), key=lambda x: (-x[1], x[0]))[:8]
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
    meta = Meta(asOf=now_dt, lastDataAt=ns_to_dt(last), from_=_dt(lo), to=_dt(hi))
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
    # SQL over the processor's request_rows (docs/architecture.md, Decision 4): the rows are
    # stored as shown, so this only filters, counts and pages them.
    store = request.app.state.store
    now_dt = request.app.state.clock()
    where, params = request_filter("person_id", personId, from_, to, q, captured, agent)
    hidden_count, total = store.query(
        f"""SELECT count(*) FILTER (WHERE hidden_kind IS NOT NULL),
                   count(*) FILTER (WHERE (? OR hidden_kind IS NULL) AND (? OR kind IN (SELECT unnest(?::VARCHAR[]))))
            FROM request_rows WHERE {where}""",  # noqa: S608 (the clause is fixed text; values are parameters)
        [hidden, not kind, list(kind or []), *params],
    )[0]
    kind_counts = dict(
        store.query(
            f"""SELECT kind, count(*) FROM request_rows WHERE {where} AND (? OR hidden_kind IS NULL)
                GROUP BY kind ORDER BY kind""",  # noqa: S608
            [*params, hidden],
        )
    )
    direction = "DESC" if order == "desc" else "ASC"
    page_rows = store.query_dicts(
        f"""SELECT * FROM request_rows
            WHERE {where} AND (? OR hidden_kind IS NULL) AND (? OR kind IN (SELECT unnest(?::VARCHAR[])))
            ORDER BY ts_ns // 1000 {direction}, request_id {direction} LIMIT ? OFFSET ?""",  # noqa: S608
        [*params, hidden, not kind, list(kind or []), pageSize, (page - 1) * pageSize],
    )
    last = store.query("SELECT last_ns FROM person_last WHERE person_id = ?", [personId])
    meta = Meta(
        asOf=now_dt,
        lastDataAt=ns_to_dt(last[0][0]) if last else None,
        from_=from_,
        to=to,
        page=page,
        pageSize=pageSize,
        total=total,
    )
    return PersonRequestsResponse(
        data=[_request_row_from_table(r) for r in page_rows],
        meta=meta,
        kindCounts=kind_counts,
        hiddenCount=hidden_count,
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
    sids = [s for s in ru.person_sessions(store, personId) if sessionId is None or s == sessionId]
    lo = cc.to_ns(from_) if from_ else None
    hi = cc.to_ns(to) if to else None
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
        [personId, *span],
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
        windows=[CoverageWindow(from_=_dt(a), to=_dt(b), count=n) for _, a, b, n in wins],
        calls=[_request_row_from_table(r) for r in rows],
    )
    return CoverageResponse(data=detail, meta=Meta(asOf=now_dt, lastDataAt=ns_to_dt(last), from_=from_, to=to))


_WITHIN = "(?::BIGINT IS NULL OR t_ns >= ?) AND (?::BIGINT IS NULL OR t_ns <= ?)"
