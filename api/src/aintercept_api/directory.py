"""The People page (handoff §5.2.1, §5.2.2): everyone seen, as rows, and the
overview above them. Claude Code and Codex are parsed; the browser agents keep
their types and colors (zero counts) until their traffic has a data shape.
Accounts with the same email are one person: one row (handoff §11J.5)."""

from datetime import UTC, datetime, timedelta
from typing import Any

from aintercept_api import agents
from aintercept_api import claude_code as cc
from aintercept_api import rollups as ru
from aintercept_api.models.common import Agent, AgentLimits, Coverage, CoverageWindow
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


def device_owners(store: Store, person_ids: list[str]) -> dict[str, set[str]]:
    """Each person's device ids (for the `q` filter): the ones their calls and events name."""
    out: dict[str, set[str]] = {}
    for acct, dev in store.query(
        "SELECT account_id, device_id FROM account_devices WHERE account_id IN (SELECT unnest(?::VARCHAR[]))",
        [person_ids],
    ):
        out.setdefault(acct, set()).add(dev)
    return out


def activity(days: dict[str, int], now: datetime) -> list[ActivityDay]:
    """The last 14 UTC days' model calls, oldest first, from (day -> count)."""
    today = now.astimezone(UTC).date()
    return [
        ActivityDay(day=d, count=days.get(d, 0))
        for d in ((today - timedelta(days=i)).isoformat() for i in range(ACTIVITY_DAYS - 1, -1, -1))
    ]


def agent_info(row: dict[str, Any]) -> list[AgentInfo]:
    """One per agent of the rollup's sessions (rollups.rollups), in the AgentCard order. Claude
    Code's version is its newest claude-cli call's; other agents', their newest call's client version."""
    return [
        AgentInfo(
            agent=agent,
            version=a["cli_version"] if agent == "claude-code" else a["client_version"],
            firstSeenAt=dt(a["first_ns"]),
            devices=a["devices"],
        )
        for agent in SHAPES
        if (a := row["agents"].get(agent))
    ]


def coverage(row: dict[str, Any]) -> Coverage | None:
    """Reported, captured and missed calls over the rollup's sessions with analytics; None without any."""
    if not row["with_events"]:
        return None
    return Coverage(
        reported=row["reported"],
        captured=row["reported"] - row["missed"],
        notCaptured=row["missed"],
        windows=[CoverageWindow(from_=dt(a), to=dt(b), count=n) for a, b, n in row["windows"]],
    )


def linked_accounts(store: Store, person_id: str, accounts_in: list[str], now: int) -> list[LinkedAccount]:
    """One entry per account of the person (PersonIdentity, handoff §11J.5): its agent (its sessions'
    first product, by session id), its Codex plan and its devices (its sessions' with one of its calls)."""
    rows = store.query(
        """WITH a AS (SELECT unnest(?::VARCHAR[]) AS account_id),
                s AS (SELECT * FROM session_rows
                      WHERE session_id IN (SELECT session_id FROM person_sessions WHERE person_id = ?))
           SELECT a.account_id, any_value(f.codex_plan),
                  (list(s.product ORDER BY s.session_id) FILTER (WHERE s.product <> ''))[1],
                  coalesce(len(list_distinct(flatten(list(s.devices) FILTER (WHERE s.session_id IS NOT NULL)))), 0)
           FROM a LEFT JOIN account_facts f USING (account_id)
                  LEFT JOIN s ON list_contains(s.call_accounts, a.account_id)
           GROUP BY a.account_id""",
        [accounts_in, person_id],
    )
    by = {r[0]: r for r in rows}
    return [
        LinkedAccount(
            personId=a,
            agent=agents.agent_of(by[a][2]),
            accountIdShort=short(a),
            plan=by[a][1],
            devices=by[a][3],
        )
        for a in accounts_in
    ]


# The processor's session times against the rows' own (batches arrive late; clocks jump):
# a session this far outside the range can't overlap it.
RANGE_SLACK_NS = 24 * 3600 * cc.NS

# The People page's rows' sessions: each row's (person_map's directory members'), less those
# the `sessions` snapshot puts well outside the range (never live in it) when there is one.
_DIRECTORY = """SELECT p.person_id AS row_id, p.session_id FROM person_sessions p
WHERE p.person_id IN (SELECT person_id FROM person_map WHERE directory)
  AND NOT (? AND p.session_id IN (
    SELECT session_id FROM sessions WHERE first_ns > ? OR (last_ns < ? AND last_ns < ?)))"""

# Each row's members: how many, and the first name, email and org among them.
_MEMBERS = """SELECT m.person_id, count(DISTINCT m.account_id),
       (list(a.name ORDER BY m.account_id) FILTER (WHERE a.name <> ''))[1],
       (list(a.email ORDER BY m.account_id) FILTER (WHERE a.email <> ''))[1],
       (list(f.org ORDER BY m.account_id) FILTER (WHERE f.org <> ''))[1]
FROM person_map m LEFT JOIN accounts a USING (account_id) LEFT JOIN account_facts f USING (account_id)
WHERE m.directory GROUP BY 1"""


def people(store: Store, now_dt: datetime, lo: int | None = None, hi: int | None = None) -> list[PersonSummaryRow]:
    """Everyone with a session overlapping the range (default: all time), by person id. Accounts
    with one email are one person (handoff §11J.5): one row, under the group's first id."""
    now = cc.to_ns(now_dt)
    ranged = lo is not None or hi is not None
    scope = [
        ranged,
        (hi if hi is not None else now) + RANGE_SLACK_NS,
        (lo if lo is not None else 0) - RANGE_SLACK_NS,
        now - cc.LIVE_WITHIN_NS - RANGE_SLACK_NS,
    ]
    where, where_params = ("true", []) if not ranged else (ru.SHOWN, [hi if hi is not None else now, now, lo or 0])
    rows = ru.rollups(store, _DIRECTORY, scope, now, where, where_params)
    members = {r[0]: r[1:] for r in store.query(_MEMBERS)}
    out = []
    for pid in sorted(rows):
        r = rows[pid]
        n, name, email, org = members.get(pid, (1, None, None, None))
        info = agent_info(r)
        out.append(
            PersonSummaryRow(
                personId=pid,
                name=name,
                email=email,
                accountIdShort=short(pid),
                orgIdShort=short(org) if org else None,
                orgKind="unknown",
                devices=r["devices"],
                agents=[a.agent for a in info],
                linkedBy="email" if n > 1 else None,
                accounts=n,
                lastSeenAt=dt(r["last_ns"]),
                live=r["live"],
                sessions=r["sessions"],
                prompts=r["prompts"] or None,
                tokensOut=r["tokens_out"] if r["outputs_known"] else None,
                # Exact when every captured call carries its usage; some without: a lower bound.
                tokensAccuracy="missing"
                if not r["outputs_known"]
                else "exact"
                if r["outputs_known"] == r["calls"]
                else "estimated",
                activity=activity(r["activity"], now_dt),
                bypassPct=ru.pct(r["bypassed"], r["queries"]),
                coverage=coverage(r),
                agentInfo=info,
                agentLimits=[AgentLimits.model_validate_json(r["codex_limits"])] if r["codex_limits"] else [],
            )
        )
    return out


def unattributed(store: Store) -> Unattributed:
    """Sessions with no account id on any call or event (pre-parser rows from an unknown machine)."""
    n, calls, first = store.query(
        """SELECT count(DISTINCT session_id), count(*), min(t_ns) FROM session_marks
           WHERE kind = 'call' AND session_id IN (SELECT session_id FROM person_sessions WHERE person_id = ?)""",
        [cc.UNATTRIBUTED],
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
            """SELECT s.product, count(*) FROM (
                 SELECT session_id, min(t_ns) AS first, max(t_ns) AS last FROM session_marks
                 WHERE kind = 'call' GROUP BY session_id) c JOIN session_rows s USING (session_id)
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
