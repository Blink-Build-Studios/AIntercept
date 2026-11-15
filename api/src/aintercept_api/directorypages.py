"""The directory, the overview and /status over v9 (docs/PIPELINE.md §6.4).

- /people: each person's row sums their accounts' `account_days` in the window (owner decision 2, §8
  difference 4): the UTC days the window touches, one row per (account, day, agent). A person is an email
  group under its least account with a session, as today. One statement groups, filters (`agent`, `q` on
  name, email, id and device), sorts and pages the rows in SQL; the page's rows then read their members
  (`accounts`), devices (`account_devices`: the devices seen in the window), Codex limits (the newest Codex
  session's) and, when they missed calls, their not-captured windows (`missed_reports`). The 14-day activity
  strip is the last 14 days' rows; live is the newest day row's last activity.
- /overview: totals over the same rows; requests from `cred_hours` (whole hours inside the window, minutes at
  its edges); unattributed from `sessions`.
- /status: the first captured call by index (its start, else its end: a WebSocket turn has only an end; a
  probe's while waiting);
  whether any call kept its content; `lastDataAt` from `ingest_pods`.

A day row's numbers are its sessions' activity that day (refresh/days.go); a row's sessions are the distinct
ids of its days' sessions, so a session active on several days of the window counts once.
"""

from collections import Counter, defaultdict
from collections.abc import Sequence
from dataclasses import dataclass
from datetime import UTC, datetime
from typing import Any

from aintercept_api import agents
from aintercept_api import claude_code as cc
from aintercept_api import directory as dr
from aintercept_api import sessionpages as sp
from aintercept_api.models.common import AgentLimits, Coverage, CoverageWindow
from aintercept_api.models.people import (
    AgentInfo,
    AgentOverview,
    Overview,
    PersonSummaryRow,
    Unattributed,
)
from aintercept_api.reader import Snapshot
from aintercept_api.rows import dt

Row = dict[str, Any]
NS = cc.NS
DAY = 86400 * NS
_FAR = 1 << 62


def _members(pid: str) -> str:
    """A person's members (today's person_map directory rows): the account, and the accounts with a session
    that share its email. Two indexed reads (the primary key, lower(email))."""
    return f"""SELECT b.* FROM accounts b WHERE b.account_id = {pid}
  UNION
  SELECT b.* FROM accounts a JOIN accounts b ON lower(b.email) = lower(a.email)
  WHERE a.account_id = {pid} AND a.email <> '' AND b.email <> ''
    AND EXISTS (SELECT 1 FROM session_accounts sa WHERE sa.account_id = b.account_id)"""  # noqa: S608 (fixed text)


# Each person's row over the window's day rows: an account's person is the least account with a session
# among those sharing its email (today's person_map leader). Filters, sort and page are SQL; %(page)s NULL
# reads every row (the overview).
_PEOPLE = f"""WITH win AS (
    SELECT * FROM account_days WHERE day_ns >= %(dlo)s AND day_ns <= %(dhi)s),
  acct AS (SELECT DISTINCT account_id FROM win),
  grp AS (
    SELECT x.account_id, coalesce((
      SELECT min(b.account_id COLLATE "C") FROM accounts a JOIN accounts b ON lower(b.email) = lower(a.email)
      WHERE a.account_id = x.account_id AND a.email <> '' AND b.email <> ''
        AND EXISTS (SELECT 1 FROM session_accounts sa WHERE sa.account_id = b.account_id)), x.account_id) AS pid
    FROM acct x),
  recent AS (
    SELECT g.pid, max(d.last_ns) AS last FROM account_days d JOIN grp g USING (account_id)
    WHERE d.day_ns >= %(live_day)s GROUP BY g.pid),
  strip AS (
    SELECT g.pid, sum(d.model_calls) AS n FROM account_days d JOIN grp g USING (account_id)
    WHERE d.day_ns >= %(strip)s GROUP BY g.pid),
  sess AS (
    SELECT g.pid, count(DISTINCT x.sid) AS sessions FROM win w JOIN grp g USING (account_id),
      unnest(w.session_ids) x(sid) GROUP BY g.pid),
  rows AS (
    SELECT g.pid, sum(w.sessions_with_events)::bigint AS with_events,
      sum(w.prompts)::bigint AS prompts, sum(w.model_calls)::bigint AS calls,
      sum(w.outputs_known)::bigint AS outputs_known, sum(w.tokens_output)::bigint AS tokens_out,
      sum(w.queries)::bigint AS queries, sum(w.bypassed)::bigint AS bypassed,
      sum(w.reported)::bigint AS reported, sum(w.missed)::bigint AS missed,
      max(w.last_ns) AS last_ns, array_agg(DISTINCT w.agent) AS agents,
      array_agg(DISTINCT w.account_id) AS accts
    FROM win w JOIN grp g USING (account_id) GROUP BY g.pid),
  named AS (
    SELECT r.*, coalesce(s.n, 0) AS activity, coalesce(%(now)s - rc.last <= {cc.LIVE_WITHIN_NS}, false) AS live,
      m.name, m.email
    FROM (SELECT * FROM rows JOIN sess USING (pid)) r LEFT JOIN strip s USING (pid) LEFT JOIN recent rc USING (pid)
    LEFT JOIN LATERAL (
      SELECT (array_agg(b.name ORDER BY b.account_id COLLATE "C") FILTER (WHERE b.name <> ''))[1] AS name,
             (array_agg(b.email ORDER BY b.account_id COLLATE "C") FILTER (WHERE b.email <> ''))[1] AS email
      FROM ({_members("r.pid")}) b) m ON true),
  kept AS (
    SELECT * FROM named n
    WHERE (%(agents)s::text[] IS NULL OR n.agents && %(agents)s::text[])
      AND (%(q)s::text IS NULL OR strpos(lower(coalesce(n.name, '')), %(q)s) > 0
           OR strpos(lower(coalesce(n.email, '')), %(q)s) > 0 OR strpos(lower(n.pid), %(q)s) > 0
           OR EXISTS (SELECT 1 FROM account_devices d WHERE d.account_id = n.pid
                      AND strpos(lower(d.device_id), %(q)s) > 0)))
SELECT *, count(*) OVER () AS total, max(last_ns) OVER () AS newest FROM kept
ORDER BY
  CASE WHEN %(sort)s = 'activity' THEN -activity END,
  CASE WHEN %(sort)s = 'name' THEN name IS NULL END,
  CASE WHEN %(sort)s = 'name' THEN lower(name) COLLATE "C" END,
  CASE WHEN %(sort)s = 'lastSeen' THEN last_ns END DESC,
  CASE WHEN %(sort)s = 'lastSeen' THEN pid COLLATE "C" END DESC,
  pid COLLATE "C"
LIMIT %(limit)s OFFSET %(offset)s"""  # noqa: S608 (fixed text)

# The page's rows' members (their group's accounts with a session), and the window's days of each.
_MEMBERS = f"""SELECT p.pid, b.account_id, b.org FROM unnest(%(pids)s::text[]) p(pid),
LATERAL ({_members("p.pid")}) b"""  # noqa: S608 (fixed text)
_DAYS = """SELECT account_id, day_ns, agent, model_calls, first_ns, last_ns FROM account_days
WHERE account_id = ANY(%(accts)s) AND day_ns >= %(from)s AND day_ns <= %(to)s"""
_DEVICES = """SELECT account_id, device_id, agent, version, version_ns FROM account_devices
WHERE account_id = ANY(%(accts)s) AND first_ns <= %(hi)s AND last_ns >= %(lo)s"""
# A Codex account's newest session with limits (newest by start, through session_accounts' index).
_LIMITS = """SELECT a.account_id, l.limits, l.limits_ns FROM unnest(%(accts)s::text[]) a(account_id), LATERAL (
  SELECT s.limits, s.limits_ns FROM session_accounts sa JOIN sessions s USING (session_id)
  WHERE sa.account_id = a.account_id AND s.product = 'codex' AND s.limits IS NOT NULL AND s.limits_ns IS NOT NULL
  ORDER BY sa.first_ns DESC LIMIT 1) l"""
# The not-captured windows of an account's sessions in the window.
_MISSED = """SELECT m.ts_ns FROM session_accounts sa JOIN missed_reports m USING (session_id)
WHERE sa.account_id = ANY(%(accts)s) AND m.ts_ns >= %(lo)s AND m.ts_ns <= %(hi)s"""

# The overview's requests: coding agents' calls in the window, whole hours inside it, minutes at its edges.
_REQUESTS = """SELECT coalesce((SELECT sum(n) FROM cred_hours WHERE hour_ns >= %(hlo)s AND hour_ns < %(hhi)s), 0)
  + coalesce((SELECT sum(n) FROM cred_minutes WHERE minute_ns >= %(lo)s AND minute_ns <= %(hi)s
              AND (minute_ns < %(hlo)s OR minute_ns >= %(hhi)s)), 0) AS n"""
_UNATTRIBUTED = """SELECT count(*) AS n, coalesce(sum(model_calls), 0) AS calls, min(start_ns) AS first
FROM sessions WHERE unattributed"""

# /status.
_STATUS = """SELECT
  least((SELECT min(start_ns) FROM calls WHERE NOT synthetic),
        (SELECT min(end_ns) FROM calls WHERE start_ns IS NULL AND NOT synthetic)) AS first_request_ns,
  EXISTS (SELECT 1 FROM calls) AS any_call,
  EXISTS (SELECT 1 FROM calls WHERE detail IS NOT NULL) AS retained,
  (SELECT max(last_ns) FROM ingest_pods) AS newest"""
_EARLIEST = "SELECT min(start_ns) FROM calls WHERE synthetic"


@dataclass(frozen=True)
class StatusValues:
    first_request_ns: int | None
    earliest_ns: int | None
    newest_ns: int | None
    content_retained: bool | None


def _short(x: str) -> str:
    return f"{x[:4]}…{x[-4:]}" if len(x) > 10 else x


def _day_of(ns: int) -> int:
    return ns - ns % DAY


class DirectoryReads:
    """The directory's, the overview's and /status's reads, in the request's snapshot."""

    def __init__(self, snap: Snapshot) -> None:
        self.snap = snap

    def _rows(
        self, now: int, lo: int | None, hi: int | None, sort: str, agent: Sequence[str] | None, q: str | None,
        page: int | None, size: int | None,
    ) -> list[Row]:  # fmt: skip
        today = _day_of(now)
        params = {
            "dlo": -_FAR if lo is None else _day_of(lo),
            "dhi": _FAR if hi is None else hi,
            "live_day": today - DAY,
            "strip": today - (dr.ACTIVITY_DAYS - 1) * DAY,
            "now": now,
            "agents": list(agent) if agent else None,
            "q": q.strip().lower() if q else None,
            "sort": sort,
            "limit": size,
            "offset": (page - 1) * size if page and size else 0,
        }
        return self.snap.dicts("people", _PEOPLE, params)

    def _shape(
        self, rows: list[Row], now_dt: datetime, now: int, lo: int | None, hi: int | None
    ) -> list[PersonSummaryRow]:
        """The rows as the API's: their members, devices, agents, limits and not-captured windows."""
        if not rows:
            return []
        pids = [r["pid"] for r in rows]
        members: dict[str, list[Row]] = defaultdict(list)
        for m in self.snap.dicts("members", _MEMBERS, {"pids": pids}):
            members[m["pid"]].append(m)
        accts = sorted({a for r in rows for a in r["accts"]})
        by_acct = {a: r["pid"] for r in rows for a in r["accts"]}
        today = _day_of(now)
        first = min(today - (dr.ACTIVITY_DAYS - 1) * DAY, -_FAR if lo is None else _day_of(lo))
        days = self.snap.dicts(
            "days", _DAYS, {"accts": accts, "from": first, "to": max(_FAR if hi is None else hi, now)}
        )
        lo_ = -_FAR if lo is None else lo
        hi_ = _FAR if hi is None else hi
        devices = self.snap.dicts("devices", _DEVICES, {"accts": accts, "lo": lo_, "hi": hi_})
        codex = sorted({a for r in rows if "codex" in r["agents"] for a in r["accts"]})
        limits: dict[str, tuple[Any, int]] = {}
        if codex:
            for a, lim, at in self.snap.rows("limits", _LIMITS, {"accts": codex}):
                p = by_acct[a]
                if p not in limits or at > limits[p][1]:
                    limits[p] = (lim, at)
        out = []
        for r in rows:
            pid = r["pid"]
            mine = sorted(members[pid], key=lambda m: m["account_id"])
            org = next((m["org"] for m in mine if m["org"]), None)
            strip: Counter[str] = Counter()
            in_window = [d for d in days if by_acct.get(d["account_id"]) == pid and _overlaps(d["day_ns"], lo, hi)]
            for d in days:
                if by_acct.get(d["account_id"]) == pid:
                    strip[datetime.fromtimestamp(d["day_ns"] / 1e9, UTC).date().isoformat()] += d["model_calls"]
            devs = [d for d in devices if by_acct.get(d["account_id"]) == pid]
            info = []
            for agent in dr.SHAPES:
                product = "codex" if agent == "codex" else "claude_code"
                ds = [d for d in in_window if d["agent"] == agent]
                if not ds:
                    continue
                mine_devs = [d for d in devs if (d["agent"] or "claude_code") == product]
                versioned = [d for d in mine_devs if d["version_ns"] is not None and d["version"]]
                newest = max(versioned, key=lambda d: (d["version_ns"], d["version"])) if versioned else None
                info.append(
                    AgentInfo(
                        agent=agent,
                        version=newest["version"] if newest else None,
                        firstSeenAt=dt(min(d["first_ns"] for d in ds)),
                        devices=len({d["device_id"] for d in mine_devs}),
                    )
                )
            coverage = None
            if r["with_events"]:
                wins: list[tuple[int, int, int]] = []
                if r["missed"]:
                    ts = sorted(
                        t for (t,) in self.snap.rows("missed", _MISSED, {"accts": r["accts"], "lo": lo_, "hi": hi_})
                    )
                    wins = _windows(ts)
                coverage = Coverage(
                    reported=r["reported"],
                    captured=r["reported"] - r["missed"],
                    notCaptured=r["missed"],
                    windows=[CoverageWindow(from_=dt(a), to=dt(b), count=n) for a, b, n in wins],
                )
            lim = limits.get(pid)
            agent_limits = []
            if lim is not None:
                got = agents.codex_limits(_json(lim[0]), datetime.fromtimestamp(lim[1] / 1e9, UTC))
                if got is not None:
                    agent_limits = [AgentLimits.model_validate_json(got.model_dump_json(by_alias=True))]
            n = len(mine) or 1
            known, calls = r["outputs_known"], r["calls"]
            out.append(
                PersonSummaryRow(
                    personId=pid,
                    name=r["name"],
                    email=r["email"],
                    accountIdShort=_short(pid),
                    orgIdShort=_short(org) if org else None,
                    orgKind="unknown",
                    devices=len({d["device_id"] for d in devs}),
                    agents=[a.agent for a in info],
                    linkedBy="email" if n > 1 else None,
                    accounts=n,
                    lastSeenAt=dt(r["last_ns"]),
                    live=r["live"],
                    sessions=r["sessions"],
                    prompts=r["prompts"] or None,
                    tokensOut=r["tokens_out"] if known else None,
                    tokensAccuracy="missing" if not known else "exact" if known == calls else "estimated",
                    activity=dr.activity(dict(strip), now_dt),
                    bypassPct=round(100 * r["bypassed"] / r["queries"], 1) if r["queries"] else None,
                    coverage=coverage,
                    agentInfo=info,
                    agentLimits=agent_limits,
                )
            )
        return out

    def people(
        self, now_dt: datetime, lo: int | None, hi: int | None, sort: str, agent: Sequence[str] | None,
        q: str | None, page: int, size: int,
    ) -> tuple[list[PersonSummaryRow], int, datetime | None]:  # fmt: skip
        now = cc.to_ns(now_dt)
        rows = self._rows(now, lo, hi, sort, agent, q, page, size)
        total = rows[0]["total"] if rows else 0
        newest = rows[0]["newest"] if rows else None
        if not rows and page > 1:  # past the end: the total and newest of the whole set
            every = self._rows(now, lo, hi, sort, agent, q, 1, 1)
            total = every[0]["total"] if every else 0
            newest = every[0]["newest"] if every else None
        return self._shape(rows, now_dt, now, lo, hi), total, dt(newest) if newest is not None else None

    def overview(self, now_dt: datetime, lo: int | None, hi: int | None) -> tuple[Overview, datetime | None]:
        now = cc.to_ns(now_dt)
        rows = self._shape(self._rows(now, lo, hi, "lastSeen", None, None, None, None), now_dt, now, lo, hi)
        span = sp._span("", lo, hi)
        edges = {
            "lo": span["lo"],
            "hi": span["hi"],
            "hlo": -_FAR if span["hlo"] is None else span["hlo"],
            "hhi": _FAR if span["hhi"] is None else span["hhi"],
        }
        (requests,) = self.snap.rows("requests", _REQUESTS, edges)[0]
        (u,) = self.snap.dicts("unattributed", _UNATTRIBUTED, {})
        sessions_by = self._sessions_by(now, lo, hi)
        cards = []
        for a, shape in dr.SHAPES.items():
            mine = [r for r in rows if a in r.agents]
            cards.append(
                AgentOverview(
                    agent=a,
                    people=len(mine),
                    sessions=sessions_by.get(a, 0) if mine else 0,
                    tokenAccuracy=("exact" if all(r.tokensAccuracy == "exact" for r in mine) else "estimated")
                    if mine
                    else "missing",
                    shape=shape,
                    **dr._headline(a, mine),
                )
            )
        last = max((r.lastSeenAt for r in rows), default=None)
        return (
            Overview(
                people=len(rows),
                activeNow=sum(r.live for r in rows),
                agents=cards,
                capabilities=agents.CAPABILITIES,
                channels=agents.CHANNELS,
                devices=sum(r.devices for r in rows),
                sessions=sum(r.sessions for r in rows),
                requests=int(requests),
                notCaptured=sum(r.coverage.notCaptured for r in rows if r.coverage),
                unattributed=Unattributed(
                    sessions=u["n"],
                    modelCalls=int(u["calls"]) if u["n"] else 0,
                    firstSeen=dt(u["first"]) if u["first"] is not None else None,
                    reason=dr.UNATTRIBUTED_REASON,
                ),
            ),
            last,
        )

    def _sessions_by(self, now: int, lo: int | None, hi: int | None) -> dict[str, int]:
        """Each agent's sessions in the window: the distinct sessions of its day rows."""
        got = self.snap.rows(
            "sessions_by",
            "SELECT d.agent, count(DISTINCT s.sid) FROM account_days d, unnest(d.session_ids) s(sid)"
            " WHERE d.day_ns >= %(dlo)s AND d.day_ns <= %(dhi)s GROUP BY d.agent",
            {"dlo": -_FAR if lo is None else _day_of(lo), "dhi": _FAR if hi is None else hi},
        )
        return {a: int(n) for a, n in got}

    # --- /status --------------------------------------------------------------------------------

    def status(self) -> StatusValues:
        (row,) = self.snap.dicts("status", _STATUS, {})
        first = row["first_request_ns"]
        earliest = first
        if first is None and row["any_call"]:
            earliest = self.snap.rows("earliest", _EARLIEST, {})[0][0]
        return StatusValues(first, earliest, row["newest"], row["retained"] if row["any_call"] else None)


def _overlaps(day: int, lo: int | None, hi: int | None) -> bool:
    return (lo is None or day + DAY > lo) and (hi is None or day <= hi)


def _windows(ts: list[int]) -> list[tuple[int, int, int]]:
    out: list[list[int]] = []
    prev = None
    for t in ts:
        if prev is None or t - prev >= cc.GAP_SPLIT_NS:
            out.append([t, t, 0])
        out[-1][1] = t
        out[-1][2] += 1
        prev = t
    return [(a, b, n) for a, b, n in out]


def _json(v: Any) -> str:
    import json

    return json.dumps(v)
