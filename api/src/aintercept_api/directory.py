"""The People page (handoff §5.2.1, §5.2.2): everyone seen, as rows, and the
overview above them. Claude Code and Codex are parsed; the browser agents keep
their types and colors (zero counts) until their traffic has a data shape.
Accounts with the same email are one person: one row (handoff §11J.5)."""

from datetime import UTC, datetime, timedelta

from aintercept_api import agents
from aintercept_api import claude_code as cc
from aintercept_api import person as pp
from aintercept_api.models.common import Agent, Coverage, CoverageWindow
from aintercept_api.models.people import (
    ActivityDay,
    AgentInfo,
    AgentOverview,
    LinkedAccount,
    Overview,
    PersonSummaryRow,
    TraceShape,
    Unattributed,
)
from aintercept_api.rows import dt
from aintercept_api.store import Store

ACTIVITY_DAYS = 14
# AgentCard order, and how each agent's work is shaped (handoff §1.1).
SHAPES: dict[Agent, TraceShape] = {
    "claude-code": "sessions→prompts→steps",
    "codex": "sessions→turns→steps",
    "chatgpt": "conversations→messages",
    "claude-ai": "conversations→messages",
}
UNATTRIBUTED_REASON = "No account id on any of its model calls or analytics events."


def short(x: str) -> str:
    return f"{x[:4]}…{x[-4:]}" if len(x) > 10 else x


def accounts(store: Store) -> dict[str, tuple[str | None, str | None]]:
    """Who each account is: (name, email), from Claude Code's account endpoints (the processor's accounts table)."""
    return {a: (n or None, e or None) for a, n, e in store.query("SELECT account_id, name, email FROM accounts")}


def device_owners(store: Store) -> dict[str, set[str]]:
    """Each person's device ids (for the `q` filter)."""
    out: dict[str, set[str]] = {}
    for acct, dev in store.query(
        """SELECT account_uuid, device_id FROM cc_calls
           WHERE account_uuid IS NOT NULL AND device_id IS NOT NULL
           UNION SELECT account_uuid, device_id FROM cc_events
           WHERE account_uuid IS NOT NULL AND device_id IS NOT NULL"""
    ):
        out.setdefault(acct, set()).add(dev)
    return out


def _devices(sessions: dict[str, cc.Session]) -> set[str]:
    return {d for s in sessions.values() for d in [*(c.device for c in s.calls), *(e.device for e in s.events)] if d}


def _versions(store: Store, sids: list[str]) -> dict[str, tuple[str | None, int]]:
    """Each session's Claude Code version on its newest call, and that call's time (one query for everyone)."""
    if not sids:
        return {}
    rows = store.query(
        """SELECT c.session_id, arg_max(regexp_extract(k.user_agent, 'claude-cli/([^ ]+)', 1), k.ts), max(k.ts)
           FROM calls k JOIN cc_calls c USING (correlation_id)
           WHERE k.user_agent LIKE 'claude-cli/%' AND c.session_id IN (SELECT unnest(?::VARCHAR[]))
           GROUP BY 1""",
        [sids],
    )
    return {sid: (v or None, ts) for sid, v, ts in rows}


def _version(store: Store, sids: list[str]) -> str | None:
    """The Claude Code version on the person's newest call, from its user-agent."""
    if not sids:
        return None
    rows = store.query(
        """SELECT arg_max(regexp_extract(user_agent, 'claude-cli/([^ ]+)', 1), ts)
           FROM calls WHERE correlation_id IN (
               SELECT correlation_id FROM cc_calls WHERE session_id IN (SELECT unnest(?::VARCHAR[])))
             AND user_agent LIKE 'claude-cli/%'""",
        [sids],
    )
    return (rows[0][0] or None) if rows else None


def _coverage(sessions: dict[str, cc.Session]) -> Coverage | None:
    with_events = [s for s in sessions.values() if s.event_count]
    if not with_events:
        return None
    missed = [t for s in with_events for t in (cc.unmatched_reports(s) or [])]
    reported = sum(1 for s in with_events for e in s.events if e.name == "tengu_api_success")
    return Coverage(
        reported=reported,
        captured=reported - len(missed),
        notCaptured=len(missed),
        windows=[CoverageWindow(from_=dt(a), to=dt(b), count=n) for a, b, n in cc.windows(missed)],
    )


def _activity(sessions: dict[str, cc.Session], now: datetime) -> list[ActivityDay]:
    today = now.astimezone(UTC).date()
    days = [(today - timedelta(days=i)).isoformat() for i in range(ACTIVITY_DAYS - 1, -1, -1)]
    counts = dict.fromkeys(days, 0)
    for s in sessions.values():
        for c in s.calls:
            d = datetime.fromtimestamp(c.ts / cc.NS, UTC).date().isoformat()
            if d in counts:
                counts[d] += 1
    return [ActivityDay(day=d, count=n) for d, n in counts.items()]


def agent_info(
    store: Store, sessions: dict[str, cc.Session], versions: dict[str, tuple[str | None, int]] | None = None
) -> list[AgentInfo]:
    """One per agent the sessions belong to, in the AgentCard order. Claude Code's version comes from
    `versions` (from _versions; queried when None); other agents' from their calls' client version."""
    by_agent: dict[str, dict[str, cc.Session]] = {}
    for k, s in sessions.items():
        by_agent.setdefault(agents.agent_of(s.product), {})[k] = s
    out = []
    for agent in SHAPES:
        mine = by_agent.get(agent)
        if not mine:
            continue
        if agent == "claude-code":
            if versions is None:
                version = _version(store, list(mine))
            else:
                newest = max((versions[k] for k in mine if k in versions), key=lambda v: v[1], default=(None, 0))
                version = newest[0]
        else:
            calls = sorted((c for s in mine.values() for c in s.calls if c.client_version), key=lambda c: c.ts)
            version = calls[-1].client_version if calls else None
        out.append(
            AgentInfo(
                agent=agent,
                version=version,
                firstSeenAt=dt(min(s.start for s in mine.values())),
                devices=len(_devices(mine)),
            )
        )
    return out


def linked_accounts(store: Store, accounts_in: list[str], sessions: dict[str, cc.Session]) -> list[LinkedAccount]:
    """One entry per account of the person (PersonIdentity, handoff §11J.5): its agent, plan and devices."""
    plans = dict(
        store.query(
            """SELECT account_uuid, arg_max(json_extract_string(provider_json, '$."x-codex-plan-type"'), ts)
               FROM agent_calls WHERE account_uuid IN (SELECT unnest(?::VARCHAR[])) AND product = 'codex'
               GROUP BY 1""",
            [accounts_in],
        )
    )
    out = []
    for a in accounts_in:
        mine = {k: s for k, s in sessions.items() if any(c.account == a for c in s.calls)}
        agent = agents.agent_of(next((s.product for s in mine.values() if s.product), None))
        out.append(
            LinkedAccount(
                personId=a, agent=agent, accountIdShort=short(a), plan=plans.get(a), devices=len(_devices(mine))
            )
        )
    return out


def _row(
    person_id: str,
    sessions: dict[str, cc.Session],
    now_dt: datetime,
    lo: int | None,
    hi: int | None,
    org: str | None,
    versions: dict[str, tuple[str | None, int]],
    store: Store,
    who: tuple[str | None, str | None] = (None, None),
    accounts_in: list[str] | None = None,
) -> PersonSummaryRow | None:
    """One person's row from their loaded sessions; with a range, from the sessions overlapping it (None if none).
    `accounts_in`: the accounts linked into the row (default: just this one)."""
    accounts_in = accounts_in or [person_id]
    sessions = {k: s for k, s in sessions.items() if s.calls or s.first_event is not None}
    now = cc.to_ns(now_dt)
    if lo is not None or hi is not None:
        shown = pp.sessions_in_range(sessions, lo if lo is not None else 0, hi if hi is not None else now, now)
        sessions = {s.id: s for s in shown}
    if not sessions:
        return None
    all_s = list(sessions.values())
    calls = [c for s in all_s for c in s.calls]
    prompts = {p for s in all_s for p in cc.prompt_ids(s)}
    outs = [c.output_tokens for c in calls if c.output_tokens is not None]
    modes = [e for s in all_s for e in s.events if e.name == "tengu_api_query" and e.permission_mode]
    info = agent_info(store, sessions, versions)
    codex_sids = [k for k, x in sessions.items() if x.product == "codex"]
    codex_limits = agents.latest_codex_limits(store, codex_sids) if codex_sids else None
    return PersonSummaryRow(
        personId=person_id,
        name=who[0],
        email=who[1],
        accountIdShort=short(person_id),
        orgIdShort=short(org) if org else None,
        orgKind="unknown",
        devices=len(_devices(sessions)),
        agents=[a.agent for a in info],
        linkedBy="email" if len(accounts_in) > 1 else None,
        accounts=len(accounts_in),
        lastSeenAt=dt(max(s.last for s in all_s)),
        live=any(s.live(now) for s in all_s),
        sessions=len(sessions),
        prompts=len(prompts) if prompts else None,
        tokensOut=sum(outs) if outs else None,
        # Exact when every captured call carries its usage; some without: a lower bound.
        tokensAccuracy="missing" if not outs else "exact" if len(outs) == len(calls) else "estimated",
        activity=_activity(sessions, now_dt),
        bypassPct=round(100 * sum(e.permission_mode == "bypassPermissions" for e in modes) / len(modes), 1)
        if modes
        else None,
        coverage=_coverage(sessions),
        agentInfo=info,
        agentLimits=[codex_limits] if codex_limits else [],
    )


# The processor's session times against the rows' own (batches arrive late; clocks jump):
# a session this far outside the range can't overlap it.
RANGE_SLACK_NS = 24 * 3600 * cc.NS


def _attribution(store: Store) -> dict[str, list[str]]:
    """Each person's sessions: every session an account id appears on (a call or an event)."""
    out: dict[str, list[str]] = {}
    for acct, sid in store.query(
        """SELECT account_uuid, session_id FROM cc_calls WHERE account_uuid IS NOT NULL AND session_id IS NOT NULL
           UNION SELECT account_uuid, session_id FROM cc_events
           WHERE account_uuid IS NOT NULL AND session_id IS NOT NULL ORDER BY 1, 2"""
    ):
        out.setdefault(acct, []).append(sid)
    return out


def _outside(store: Store, lo: int | None, hi: int | None, now: int) -> set[str]:
    """Sessions the `sessions` snapshot puts well outside the range (never live in it)."""
    if lo is None and hi is None:
        return set()
    rows = store.query(
        """SELECT session_id FROM sessions
           WHERE first_ns > ? OR (last_ns < ? AND last_ns < ?)""",
        [
            (hi if hi is not None else now) + RANGE_SLACK_NS,
            (lo if lo is not None else 0) - RANGE_SLACK_NS,
            now - cc.LIVE_WITHIN_NS - RANGE_SLACK_NS,
        ],
    )
    return {r[0] for r in rows}


def people(store: Store, now_dt: datetime, lo: int | None = None, hi: int | None = None) -> list[PersonSummaryRow]:
    """Everyone with a session overlapping the range (default: all time). A fixed number of
    queries whatever the number of people: everyone's sessions load together."""
    now = cc.to_ns(now_dt)
    owned = _attribution(store)
    outside = _outside(store, lo, hi, now)
    sids = sorted({sid for ss in owned.values() for sid in ss if sid not in outside})
    # Once per commit for this set of sessions (a moving window rarely changes it).
    loaded = store.memo(("sessions", tuple(sids)), lambda: cc.load_sessions(store, sids))
    orgs = dict(
        store.query(
            """SELECT account_uuid, any_value(organization_uuid) FILTER (WHERE organization_uuid IS NOT NULL)
               FROM cc_events WHERE account_uuid IS NOT NULL GROUP BY 1"""
        )
    )
    versions = _versions(store, sids)
    who = accounts(store)
    groups = cc.email_groups(store)
    rows = []
    done: set[str] = set()
    for pid in sorted(owned):
        if pid in done:
            continue
        # Accounts with one email are one person (handoff §11J.5): one row, the group's first id.
        group = [a for a in groups.get(pid, [pid]) if a in owned] or [pid]
        done.update(group)
        mine = {sid: loaded[sid] for a in group for sid in owned[a] if sid in loaded}
        name = next((who[a][0] for a in group if a in who and who[a][0]), None)
        email = next((who[a][1] for a in group if a in who and who[a][1]), None)
        org = next((orgs[a] for a in group if orgs.get(a)), None)
        r = _row(group[0], mine, now_dt, lo, hi, org, versions, store, (name, email), group)
        if r is not None:
            rows.append(r)
    return rows


def unattributed(store: Store) -> Unattributed:
    """Sessions with no account id on any call or event (pre-parser rows from an unknown machine)."""
    n, calls, first = store.query(
        """WITH known AS (
             SELECT session_id FROM cc_calls WHERE account_uuid IS NOT NULL
             UNION SELECT session_id FROM cc_events WHERE account_uuid IS NOT NULL AND session_id IS NOT NULL
           )
           SELECT count(DISTINCT session_id), count(*), min(ts) FROM cc_calls
           WHERE session_id IS NOT NULL AND session_id NOT IN (SELECT session_id FROM known)"""
    )[0]
    return Unattributed(
        sessions=n, modelCalls=calls if n else 0, firstSeen=dt(first) if first else None, reason=UNATTRIBUTED_REASON
    )


def _headline(agent: str, rows: list[PersonSummaryRow]) -> dict[str, float | int | None]:
    """An agent card's headline (handoff §11L.1): Claude Code's missed calls and bypass share,
    Codex's highest weekly limit; None where the agent has no such field or no data yet."""
    out: dict[str, float | int | None] = {"missedCalls": None, "bypassPct": None, "highestWeekPct": None}
    if agent == "claude-code":
        covs = [r.coverage for r in rows if r.coverage and r.coverage.reported is not None]
        out["missedCalls"] = sum(c.notCaptured for c in covs) if covs else None
        pcts = [r.bypassPct for r in rows if r.bypassPct is not None]
        out["bypassPct"] = round(sum(pcts) / len(pcts), 1) if pcts else None
    elif agent == "codex":
        weeks = [
            w.usedPct
            for r in rows
            for lim in r.agentLimits
            for w in lim.windows
            if w.name == "week" and w.usedPct is not None
        ]
        out["highestWeekPct"] = max(weeks) if weeks else None
    return out


def overview(store: Store, rows: list[PersonSummaryRow], lo: int | None = None, hi: int | None = None) -> Overview:
    """Totals over the same window as `rows`: requests in it, and each agent's sessions overlapping it."""
    (requests,) = store.query(
        """SELECT count(*) FROM calls WHERE product IN ('claude_code', 'codex') AND NOT synthetic
           AND (?::BIGINT IS NULL OR ts >= ?) AND (?::BIGINT IS NULL OR ts <= ?)""",
        [lo, lo, hi, hi],
    )[0]
    sessions_by = dict(
        store.query(
            """SELECT product, count(*) FROM (
                 SELECT session_id, any_value(product) AS product, min(ts) AS first, max(ts) AS last
                 FROM cc_calls GROUP BY session_id)
               WHERE (?::BIGINT IS NULL OR last >= ?) AND (?::BIGINT IS NULL OR first <= ?)
               GROUP BY 1""",
            [lo, lo, hi, hi],
        )
    )
    cards = []
    for a, shape in SHAPES.items():
        mine = [r for r in rows if a in r.agents]
        product = next((p for p, x in agents.AGENT.items() if x == a), None)
        cards.append(
            AgentOverview(
                agent=a,
                people=len(mine),
                sessions=sessions_by.get(product, 0) if mine else 0,
                tokenAccuracy=("exact" if all(r.tokensAccuracy == "exact" for r in mine) else "estimated")
                if mine
                else "missing",
                shape=shape,
                **_headline(a, mine),
            )
        )
    return Overview(
        people=len(rows),
        activeNow=sum(r.live for r in rows),
        agents=cards,
        capabilities=agents.CAPABILITIES,
        channels=agents.CHANNELS,
        devices=sum(r.devices for r in rows),
        sessions=sum(r.sessions for r in rows),
        requests=requests,
        notCaptured=sum(r.coverage.notCaptured for r in rows if r.coverage),
        unattributed=unattributed(store),
    )
