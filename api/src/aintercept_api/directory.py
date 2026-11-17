"""The People page (handoff §5.2.1, §5.2.2): everyone seen, as rows, and the
overview above them. Claude Code and Codex are parsed; the browser agents keep
their types and colors (zero counts) until their traffic has a data shape.
Accounts with the same email are one person: one row (handoff §11J.5)."""

from datetime import UTC, datetime, timedelta

from aintercept_api.models.common import Agent
from aintercept_api.models.people import (
    ActivityDay,
    PersonSummaryRow,
    TraceShape,
)

ACTIVITY_DAYS = 14
# AgentCard order, and how each agent's work is shaped (handoff §1.1).
SHAPES: dict[Agent, TraceShape] = {
    "claude-code": "sessions→prompts→steps",
    "codex": "sessions→turns→steps",
    "chatgpt": "conversations→messages",
    "claude-ai": "conversations→messages",
}
UNATTRIBUTED_REASON = "No account id on any of its model calls."


def activity(days: dict[str, int], now: datetime) -> list[ActivityDay]:
    """The last 14 UTC days' model calls, oldest first, from (day -> count)."""
    today = now.astimezone(UTC).date()
    return [
        ActivityDay(day=d, count=days.get(d, 0))
        for d in ((today - timedelta(days=i)).isoformat() for i in range(ACTIVITY_DAYS - 1, -1, -1))
    ]


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


# The unattributed sessions with a model call: how many, their calls and the first call's time.
_UNATTRIBUTED = """SELECT count(DISTINCT session_id), sum(n), min(first_ns) FROM session_call_days
WHERE session_id IN (SELECT session_id FROM person_sessions WHERE person_id = ?)"""


def _headline(agent: str, rows: list[PersonSummaryRow]) -> dict[str, float | int | None]:
    """An agent card's headline (handoff §11L.1): Codex's highest weekly limit; None where the agent has no
    such field or no data yet (Claude Code's missed calls and bypass share were its analytics')."""
    out: dict[str, float | int | None] = {"missedCalls": None, "bypassPct": None, "highestWeekPct": None}
    if agent == "codex":
        weeks = [
            w.usedPct
            for r in rows
            for lim in r.agentLimits
            for w in lim.windows
            if w.name == "week" and w.usedPct is not None
        ]
        out["highestWeekPct"] = max(weeks) if weeks else None
    return out


# Each product's sessions whose model calls overlap the window (their first and last call's times).
_SESSIONS_BY = """SELECT s.product, count(*) FROM (
  SELECT session_id, min(first_ns) AS first, max(last_ns) AS last FROM session_call_days GROUP BY session_id) c
JOIN session_rows s USING (session_id)
WHERE (?::BIGINT IS NULL OR last >= ?) AND (?::BIGINT IS NULL OR first <= ?)
GROUP BY 1"""
