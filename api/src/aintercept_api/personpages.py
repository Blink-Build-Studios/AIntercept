"""The person pages over v9 (docs/PIPELINE.md §6.3): what each page shows, read from the facts and the rollups
through a few indexed statements in the request's snapshot, and shaped by today's code.

A person's scope is their accounts (`accounts` by email group: the URL's account and every account with its
email), their sessions (`session_accounts` by account; `unattributed`: the sessions with a visible call and
no account, `sessions.unattributed`) and their credentials (`session_credentials` of those sessions). Every
page reads the scope first: the group's account rows, and the person's `sessions` rows (one per session).

Analytics events aren't in the database (they stay in the raw files): what only they fed (the plan,
device details, bypass %, posture, missed calls and coverage, the analytics and friction lanes, the
summary's event counts, hooks, skills, cost, TTFT) is null, empty or 0.

- /people/{id}: the group's accounts and devices (`account_devices`); the agents (first seen, versions) and
  the newest values (limits, live) from the person's `sessions` rows, as today's header rolls them up.
- /sessions: a page of the person's `sessions` rows in range (the range and the nesting are over the rows
  the scope read), with their children (`session_edges`) and labels (§6.1).
- /timeline: the model-call lane from the sessions' `session_minutes` (bins under an hour) or
  `session_hours` (hours inside the range, minutes at its edges); bands from `sessions`; prompts from
  `prompt_items` and `prompts` in range; the Work lane from the work keys.
- /summary: sums from the shown sessions' cells in range; distinct counts and the tools their calls asked
  for from their key rollups; account calls from `cred_minutes`/`cred_hours`.
- /requests: a page of `calls` by (credential, time) over the scope's credentials; counts from
  `cred_minutes`/`cred_hours`, which find the page's minutes. Step numbers counted from `steps`, prompt
  numbers from `prompt_items`.

Text search (`q`) scans the person's rows in the range, as the session pages' does (§6.1).
"""

import json
from collections import Counter, defaultdict
from collections.abc import Callable, Sequence
from dataclasses import dataclass, field
from datetime import datetime
from typing import Any

from aintercept_api import agents
from aintercept_api import claude_code as cc
from aintercept_api import directory as dr
from aintercept_api import sessionpages as sp
from aintercept_api import timeline as tl
from aintercept_api import tools as tu
from aintercept_api import turnpages as tp
from aintercept_api import work as wk
from aintercept_api.models.common import AgentLimits, Coverage, LimitWindow
from aintercept_api.models.people import (
    AgentInfo,
    Capabilities,
    CodeShipped,
    Cost,
    Destructive,
    DeviceInfo,
    Failures,
    Flags,
    Footprint,
    FrictionMark,
    Limits,
    LinkedAccount,
    McpServer,
    OrgStat,
    PersonHeader,
    PersonSummary,
    Posture,
    RequestRow,
    Ribbon,
    SessionRow,
    Timeline,
    TimelinePrompt,
    TokenCount,
)
from aintercept_api.models.turns import RepoGroup
from aintercept_api.models.work import OutcomeFacts, PersonWorkStats, WorkBin
from aintercept_api.reader import Snapshot
from aintercept_api.rows import dt, session_row_from_table

Row = dict[str, Any]

NS = cc.NS
MINUTE = sp.MINUTE
HOUR = sp.HOUR
UNATTRIBUTED = cc.UNATTRIBUTED
_FAR = 1 << 62

# --- the scope ------------------------------------------------------------------------------------------

# The group: the URL's account, and every account with its email (lower(email), as today links them).
_GROUP = """SELECT * FROM accounts WHERE account_id = %(pid)s
UNION
SELECT b.* FROM accounts a JOIN accounts b ON lower(b.email) = lower(a.email)
WHERE a.account_id = %(pid)s AND a.email <> '' AND b.email <> ''"""
# The person's sessions: those its accounts appear on.
_SESSIONS_OF = """SELECT s.* FROM sessions s
WHERE s.session_id IN (SELECT session_id FROM session_accounts WHERE account_id = ANY(%(accts)s))"""
# Unattributed: the sessions with a visible call and no account.
_UNATTRIBUTED = "SELECT s.* FROM sessions s WHERE s.unattributed"
_CREDENTIALS = (
    """SELECT DISTINCT key_hash FROM session_credentials WHERE session_id = ANY(%(sids)s) AND key_hash <> ''"""
)

# --- the session rows a page draws: their children and labels (§6.1, §5.4) ------------------------------

_CHILDREN = """SELECT p.parent_session_id AS of, p.child_session_id AS sid, min(a.kind) AS kind,
    min(a.parent_session_id) AS parent, min(a.first_ns) AS first_ns
FROM session_edges p JOIN session_edges a ON a.child_session_id = p.child_session_id
WHERE p.parent_session_id = ANY(%(sids)s) GROUP BY p.parent_session_id, p.child_session_id"""
_TITLES = f"""SELECT x.sid, t.text FROM unnest(%(sids)s::text[], %(los)s::bigint[]) x(sid, lo), LATERAL (
  SELECT c.detail ->> 'text' AS text FROM calls c
  WHERE c.hidden_kind = 'title' AND c.account_id IN (SELECT account_id FROM session_accounts WHERE session_id = x.sid)
    AND c.start_ns BETWEEN x.lo AND x.lo + {sp.TITLE_WITHIN_NS} AND {sp._MODEL_CALL}
    AND jsonb_typeof(c.detail -> 'text') = 'string' AND c.detail ->> 'text' <> ''
    AND NOT EXISTS (SELECT 1 FROM sessions s WHERE s.session_id = c.session_id AND s.model_calls > 0)
  ORDER BY {sp._AT}, {sp._ID} COLLATE "C" LIMIT 1) t"""  # noqa: S608 (fixed text)

# --- cells over several sessions ---------------------------------------------------------------------------

_CELL_COLUMNS = """model_calls, main_calls, subagent_calls, input_tokens, cache_read_tokens, cache_write_tokens,
    output_tokens, context_sum"""
# The sessions' cells in [lo, hi]: their whole hours inside it, their minutes at its edges.
_CELLS = f"""SELECT session_id, hour_ns AS t, {HOUR} AS w, {_CELL_COLUMNS} FROM session_hours
WHERE session_id = ANY(%(sids)s) AND (%(hlo)s::bigint IS NULL OR hour_ns >= %(hlo)s)
  AND (%(hhi)s::bigint IS NULL OR hour_ns < %(hhi)s)
UNION ALL
SELECT session_id, minute_ns, {MINUTE}, {_CELL_COLUMNS} FROM session_minutes
WHERE session_id = ANY(%(sids)s) AND minute_ns >= %(lo)s AND minute_ns <= %(hi)s
  AND (minute_ns < %(hlo)s OR minute_ns >= %(hhi)s)"""  # noqa: S608 (fixed text)
_MINUTES = f"""SELECT session_id, minute_ns AS t, {MINUTE} AS w, {_CELL_COLUMNS} FROM session_minutes
WHERE session_id = ANY(%(sids)s) AND minute_ns >= %(lo)s AND minute_ns <= %(hi)s"""  # noqa: S608
_SUMS = ", ".join(f"sum({c})::bigint AS {c}" for c in (x.strip() for x in _CELL_COLUMNS.split(",")))


def _binned(cells: str) -> str:
    """The cells summed per bin of %(width)s ns (bins start on multiples of the width, as today's)."""
    return f"""SELECT t - mod(t, %(width)s) AS t, %(width)s AS w, {_SUMS} FROM ({cells}) c GROUP BY 1 ORDER BY 1"""  # noqa: S608


_HOUR_BINS = _binned(_CELLS)
_MINUTE_BINS = _binned(_MINUTES)
_TOTALS = f"""SELECT {_SUMS} FROM ({_CELLS}) c"""  # noqa: S608
_KEY_ROWS = """SELECT kind, key, n FROM session_keys WHERE session_id = ANY(%(sids)s) AND kind = ANY(%(kinds)s)
    AND (%(hlo)s::bigint IS NULL OR hour_ns >= %(hlo)s) AND (%(hhi)s::bigint IS NULL OR hour_ns < %(hhi)s)
  UNION ALL
  SELECT kind, key, n FROM session_minute_keys WHERE session_id = ANY(%(sids)s) AND kind = ANY(%(kinds)s)
    AND minute_ns >= %(lo)s AND minute_ns <= %(hi)s AND (minute_ns < %(hlo)s OR minute_ns >= %(hhi)s)"""
_KEYS = f"""SELECT kind, key, sum(n)::bigint AS n FROM ({_KEY_ROWS}) k GROUP BY kind, key"""  # noqa: S608
# The distinct count of each kind (what the summary counts but doesn't list: prompts).
_KEY_COUNTS = f"""SELECT kind, count(DISTINCT key) AS n FROM ({_KEY_ROWS}) k GROUP BY kind"""  # noqa: S608


# Each bin's work key rows (handoff §11R's Work lane): the hours' (inside the range) and the minutes' (at its
# edges), or every minute's, summed per bin.
_HOUR_WORK = """SELECT hour_ns AS t, kind, key, n FROM session_keys
WHERE session_id = ANY(%(sids)s) AND kind = ANY(%(kinds)s)
  AND (%(hlo)s::bigint IS NULL OR hour_ns >= %(hlo)s) AND (%(hhi)s::bigint IS NULL OR hour_ns < %(hhi)s)
UNION ALL
SELECT minute_ns, kind, key, n FROM session_minute_keys WHERE session_id = ANY(%(sids)s) AND kind = ANY(%(kinds)s)
  AND minute_ns >= %(lo)s AND minute_ns <= %(hi)s AND (minute_ns < %(hlo)s OR minute_ns >= %(hhi)s)"""
_MINUTE_WORK = """SELECT minute_ns AS t, kind, key, n FROM session_minute_keys WHERE session_id = ANY(%(sids)s)
  AND kind = ANY(%(kinds)s) AND minute_ns >= %(lo)s AND minute_ns <= %(hi)s"""


def _binned_work(rows: str) -> str:
    return f"""SELECT t - mod(t, %(width)s) AS b, kind, key, sum(n)::bigint AS n FROM ({rows}) x GROUP BY 1, 2, 3"""  # noqa: S608


_WORK_BINS = {True: _binned_work(_HOUR_WORK), False: _binned_work(_MINUTE_WORK)}


def _span(sids: list[str], lo: int | None, hi: int | None) -> dict[str, Any]:
    """A cell read's parameters for [lo, hi] (each bound None: open), as sessionpages._span's."""
    got = sp._span("", lo, hi)
    del got["sid"]
    return {**got, "sids": sids}


# --- the timeline ------------------------------------------------------------------------------------------

# The interrupted turns in range (the friction lane's ✕, handoff §11S): their prompts rows.
_INTERRUPTED = f"""SELECT p.t_ns, i.prompt_index FROM prompts p
JOIN prompt_items i ON i.prompt_id = p.prompt_id AND i.session_id = p.session_id AND i.type = 'prompt'
WHERE p.session_id = ANY(%(sids)s) AND p.t_ns BETWEEN %(lo)s AND %(hi)s
  AND (p.interrupted OR {sp.NEXT_FOLLOWS_INTERRUPT})"""  # noqa: S608 (fixed text)

# The prompts in range (session.py timeline_prompts: by µs, then session, then place).
_TYPED = """SELECT i.session_id, i.seq, i.t_ns, i.prompt_id, i.prompt_index, i.open_tail, p.steps, p.text,
    p.text_chars, p.tokens_in, p.tokens_out, p.ended_ns, p.unfinished_max_ns
FROM prompt_items i JOIN prompts p ON p.prompt_id = i.prompt_id AND p.session_id = i.session_id
WHERE i.session_id = ANY(%(sids)s) AND i.type = 'prompt' AND i.t_ns >= %(lo)s AND i.t_ns < %(hi)s + 1000
ORDER BY i.t_ns / 1000, i.session_id COLLATE "C", i.seq"""

# --- the summary -------------------------------------------------------------------------------------------

# The credentials' coding-agent calls: whole hours inside the range, minutes at its edges.
_CRED_CELLS = """SELECT hour_ns AS t, 3600000000000 AS w, kind, hidden, agent, sum(n_tx)::bigint AS n FROM cred_hours
WHERE key_hash = ANY(%(creds)s) AND (%(hlo)s::bigint IS NULL OR hour_ns >= %(hlo)s)
  AND (%(hhi)s::bigint IS NULL OR hour_ns < %(hhi)s) AND n_tx > 0
GROUP BY 1, 2, 3, 4, 5
UNION ALL
SELECT minute_ns, 60000000000, kind, hidden, agent, sum(n_tx)::bigint FROM cred_minutes
WHERE key_hash = ANY(%(creds)s) AND minute_ns >= %(lo)s AND minute_ns <= %(hi)s
  AND (minute_ns < %(hlo)s OR minute_ns >= %(hhi)s) AND n_tx > 0
GROUP BY 1, 2, 3, 4, 5
ORDER BY 1"""
_CRED_MINUTES = """SELECT minute_ns AS t, 60000000000 AS w, kind, hidden, agent, sum(n_tx)::bigint AS n
FROM cred_minutes WHERE key_hash = ANY(%(creds)s) AND minute_ns >= %(lo)s AND minute_ns <= %(hi)s AND n_tx > 0
GROUP BY 1, 2, 3, 4, 5 ORDER BY 1"""

# --- the request log ---------------------------------------------------------------------------------------

_CALL_ROWS = f"""SELECT {sp._ROW_COLUMNS} FROM calls c
WHERE c.key_hash = ANY(%(creds)s) AND (c.start_ns >= %(a)s AND c.start_ns < %(b)s
    OR c.start_ns IS NULL AND c.end_ns >= %(a)s AND c.end_ns < %(b)s)
  AND {sp._TRANSACTION} AND (%(hidden)s OR c.hidden_kind IS NULL) AND (%(any_kind)s OR c.kind = ANY(%(kinds)s))"""  # noqa: S608
_PROMPT_INDEXES = """SELECT prompt_id, prompt_index FROM prompt_items
WHERE session_id = ANY(%(sids)s) AND prompt_id = ANY(%(pids)s) AND prompt_index IS NOT NULL
ORDER BY session_id COLLATE "C" DESC"""


@dataclass
class Scope:
    """A person's accounts (their rows), sessions (their `sessions` rows) and, once read, credentials."""

    pid: str
    accounts: list[Row]
    sessions: list[Row]
    creds: list[str] | None = None
    _ids: list[str] = field(default_factory=list)

    @property
    def sids(self) -> list[str]:
        if not self._ids and self.sessions:
            self._ids = sorted(s["session_id"] for s in self.sessions)
        return self._ids

    @property
    def account_ids(self) -> list[str]:
        return sorted({a["account_id"] for a in self.accounts} | ({self.pid} if self.pid != UNATTRIBUTED else set()))


def _short(x: str) -> str:
    return f"{x[:4]}…{x[-4:]}" if len(x) > 10 else x


def _first(rows: list[Row], col: str) -> Any:
    """The first non-empty value by account id (today's `list(x ORDER BY account_id) FILTER (x <> '')[1]`)."""
    return next((r[col] for r in sorted(rows, key=lambda r: r["account_id"]) if r[col]), None)


def _live(r: Row, now: int) -> bool:
    return bool(now - r["last_ns"] <= cc.LIVE_WITHIN_NS)


def _shown(r: Row, lo: int | None, hi: int | None, now: int) -> bool:
    """rollups.SHOWN: started by the range's end, and still going (live: now) at its start."""
    end = now if _live(r, now) else r["last_ns"]
    return (hi is None or r["start_ns"] <= hi) and (lo is None or end >= lo)


def _newest(rows: list[Row], value: str, at: str, *, tie: Callable[[Row], Any] | None = None) -> Row | None:
    """The row with the newest `at` (non-NULL), ties as `tie` orders them (greatest first)."""
    got = [r for r in rows if r[at] is not None]
    if not got:
        return None
    return max(got, key=lambda r: (r[at], tie(r) if tie else 0))


class PersonReads:
    """The person pages' reads of one person, in the request's snapshot."""

    def __init__(self, snap: Snapshot, sessions: sp.SessionReads) -> None:
        self.snap = snap
        self.sessions = sessions
        self._scopes: dict[str, Scope] = {}

    # --- the scope ------------------------------------------------------------------------------

    def scope(self, pid: str) -> Scope:
        if pid not in self._scopes:
            if pid == UNATTRIBUTED:
                accounts: list[Row] = []
                rows = self.snap.dicts("sessions_of", _UNATTRIBUTED, {})
            else:
                accounts = self.snap.dicts("group", _GROUP, {"pid": pid})
                accts = sorted({a["account_id"] for a in accounts} | {pid})
                rows = self.snap.dicts("sessions_of", _SESSIONS_OF, {"accts": accts})
            self._scopes[pid] = Scope(pid, accounts, rows)
        return self._scopes[pid]

    def credentials(self, scope: Scope) -> list[str]:
        if scope.creds is None:
            scope.creds = (
                sorted({k for (k,) in self.snap.rows("credentials", _CREDENTIALS, {"sids": scope.sids})})
                if scope.sids
                else []
            )
        return scope.creds

    def rows_of(self, rs: list[Row], now: int) -> list[Row]:
        """The sessions' rows as today's session_rows has them: with their children and labels (§6.1)."""
        if not rs:
            return []
        sids = [r["session_id"] for r in rs]
        kids: dict[str, list[Row]] = defaultdict(list)
        for k in self.snap.dicts("children", _CHILDREN, {"sids": sids}):
            kids[k["of"]].append(k)
        titled = [r for r in rs if sp.needs_title(r)]
        titles: dict[str, str] = {}
        if titled:
            titles = dict(
                self.snap.rows(
                    "title",
                    _TITLES,
                    {"sids": [r["session_id"] for r in titled], "los": [r["start_ns"] for r in titled]},
                )
            )
        return [
            sp.session_row(
                r, now, sp.children_json(r["session_id"], kids[r["session_id"]]), titles.get(r["session_id"])
            )
            for r in rs
        ]

    # --- /people/{id} ---------------------------------------------------------------------------

    def header(self, pid: str, now_dt: datetime) -> tuple[PersonHeader, datetime | None]:
        now = cc.to_ns(now_dt)
        scope = self.scope(pid)
        rs = scope.sessions
        unattributed = pid == UNATTRIBUTED
        me = next((a for a in scope.accounts if a["account_id"] == pid), None)
        org = me["org"] if me else None
        linked = [pid] if unattributed else scope.account_ids
        devices: list[Row] = []
        if not unattributed:
            devices = self.snap.dicts(
                "devices",
                "SELECT * FROM account_devices WHERE account_id = ANY(%(accts)s) ORDER BY account_id, device_id",
                {"accts": linked},
            )
        own = [d for d in devices if d["account_id"] == pid]
        cc_rows = [r for r in rs if r["product"] != "codex"]
        rl_row = _newest(rs, "rl_status", "rl_ns")
        rl = (
            {"status": rl_row["rl_status"], "util": rl_row["rl_util"], "overage": rl_row["rl_overage"]}
            if rl_row
            else {"status": None, "util": None, "overage": None}
        )
        last = max((r["last_ns"] for r in rs), default=None)
        info = self._agent_info(rs, devices)
        limits = self._codex_limits(rs, now)
        header = PersonHeader(
            personId=pid,
            name=_first(scope.accounts, "name"),
            email=_first(scope.accounts, "email"),
            accountIdShort="—" if unattributed else _short(pid),
            orgIdShort=_short(org) if org else None,
            orgKind="unknown",
            role=None,
            plan=None,
            rateLimitTier=None,
            customerSince=None,
            firstUseAt=None,
            devices=[DeviceInfo(deviceId=d["device_id"], platform=None, terminal=None, shell=None) for d in own],
            limits=Limits(status=rl["status"], utilization7d=rl["util"], overage=rl["overage"]),
            flags=Flags(total=None, experiments=None, changedSince=None),
            posture=Posture(bypassPct=None, sandbox=None, tokenStorage=None, proxyDetected=None),
            lastSeenAt=dt(last) if last is not None else None,
            live=any(_live(r, now) for r in rs),
            agentInfo=info,
            accounts=[] if unattributed else self._linked(scope, linked, devices),
            linkedBy="email" if len(linked) > 1 else None,
            agentLimits=self._agent_limits(cc_rows, rl, limits),
            capabilities={a.agent: agents.CAPABILITIES[a.agent] for a in info},
            org=OrgStat(id=org, kind=agents.org_kind(org)) if org else None,
            missed=None,
        )
        return header, dt(last) if last is not None else None

    def _agent_info(self, rs: list[Row], devices: list[Row]) -> list[AgentInfo]:
        by: dict[str, list[Row]] = defaultdict(list)
        for r in rs:
            by[agents.agent_of(r["product"])].append(r)
        out = []
        for agent in dr.SHAPES:
            mine = by.get(agent)
            if not mine:
                continue
            if agent == "claude-code":
                got = [r for r in mine if r["cli_version_ns"] is not None]
                pick = min(got, key=lambda r: (-r["cli_version_ns"], r["session_id"])) if got else None
                version = (pick["cli_version"] or None) if pick else None
            else:
                got = [r for r in mine if r["client_version_ns"] is not None]
                pick = max(got, key=lambda r: (r["client_version_ns"], r["session_id"])) if got else None
                version = pick["client_version"] if pick else None
            product = "codex" if agent == "codex" else "claude_code"
            devs = {d["device_id"] for d in devices if (d["agent"] or "claude_code") == product}
            out.append(
                AgentInfo(
                    agent=agent,
                    version=version,
                    firstSeenAt=dt(min(r["start_ns"] for r in mine)),
                    devices=len(devs),
                )
            )
        return out

    @staticmethod
    def _codex_limits(rs: list[Row], now: int) -> str | None:
        codex = [sp.session_row(r, now, None, None) for r in rs if r["product"] == "codex" and r["limits_ns"]]
        got = [r for r in codex if r["limits_json"] is not None]
        if not got:
            return None
        return str(max(got, key=lambda r: r["limits_ns"])["limits_json"])

    @staticmethod
    def _agent_limits(cc_rows: list[Row], rl: dict[str, Any], codex: str | None) -> list[AgentLimits]:
        out = []
        if cc_rows and rl["util"] is not None:
            out.append(
                AgentLimits(
                    agent="claude-code",
                    plan=None,
                    windows=[LimitWindow(name="7d", usedPct=round(rl["util"] * 100, 1), resetsAt=None)],
                    credits=f"overage {rl['overage']}" if rl["overage"] else None,
                    asOf=dt(max(r["last_ns"] for r in cc_rows)),
                )
            )
        if codex:
            out.append(AgentLimits.model_validate_json(codex))
        return out

    def _linked(self, scope: Scope, linked: list[str], devices: list[Row]) -> list[LinkedAccount]:
        """One entry per account of the person (dr.linked_accounts): its agent (its sessions' first product,
        by session id), its Codex plan and its devices."""
        by_acct = {a["account_id"]: a for a in scope.accounts}
        on: dict[str, list[str]] = defaultdict(list)
        if scope.sids:
            for sid, acct in self.snap.rows(
                "session_accounts",
                "SELECT session_id, account_id FROM session_accounts WHERE session_id = ANY(%(sids)s)"
                " AND account_id = ANY(%(accts)s)",
                {"sids": scope.sids, "accts": linked},
            ):
                on[acct].append(sid)
        product = {r["session_id"]: r["product"] for r in scope.sessions}
        out = []
        for a in linked:
            firsts = [product[s] for s in sorted(on[a]) if product.get(s)]
            out.append(
                LinkedAccount(
                    personId=a,
                    agent=agents.agent_of(firsts[0] if firsts else None),
                    accountIdShort=_short(a),
                    plan=(by_acct.get(a) or {}).get("codex_plan"),
                    # its sessions' devices, of the sessions its calls name: the devices its calls used
                    devices=len({d["device_id"] for d in devices if d["account_id"] == a and d["agent"] is not None}),
                )
            )
        return out

    # --- /people/{id}/sessions ------------------------------------------------------------------

    def sessions_page(
        self, pid: str, now: int, lo: int | None, hi: int | None, page: int, size: int, agent: Sequence[str] | None
    ) -> tuple[int, int | None, list[SessionRow]]:
        scope = self.scope(pid)
        rs = scope.sessions
        last = max((r["last_ns"] for r in rs), default=None)
        kept = [r for r in rs if _shown(r, lo, hi, now) and (not agent or agents.agent_of(r["product"]) in agent)]
        ids = {r["session_id"] for r in kept}

        def nested(r: Row) -> bool:
            return r["origin_kind"] in ("subagent", "review") and r["origin_session_id"] in ids

        roots = sorted((r for r in kept if not nested(r)), key=lambda r: (r["start_ns"], r["session_id"]), reverse=True)
        chosen = roots[(page - 1) * size : page * size]
        rank = {r["session_id"]: k for k, r in enumerate(chosen)}
        shown = [r for r in kept if r["session_id"] in rank or (nested(r) and r["origin_session_id"] in rank)]
        shown.sort(
            key=lambda r: (
                rank[r["origin_session_id"] if nested(r) else r["session_id"]],
                nested(r),
                r["start_ns"],
                r["session_id"],
            )
        )
        rows = [session_row_from_table(r) for r in self.rows_of(shown, now)]
        return len(roots), last, rows

    # --- /people/{id}/timeline ------------------------------------------------------------------

    def session_rows(self, pid: str, now: int) -> tuple[list[str], list[Row]]:
        scope = self.scope(pid)
        return scope.sids, [{**r, "live": _live(r, now)} for r in scope.sessions]

    def timeline(
        self, sids: list[str], rs: list[Row], now_dt: datetime, now: int, lo: int, hi: int, bin_s: int
    ) -> Timeline:
        def end(r: Row) -> int:
            return now if r["live"] else int(r["last_ns"])

        bands = sorted(
            (r for r in rs if r["start_ns"] <= hi and end(r) >= lo), key=lambda r: (r["start_ns"], r["session_id"])
        )
        packed = cc.pack_rows([(r["session_id"], r["start_ns"], end(r)) for r in bands])
        drawn = self.rows_of(bands, now)
        width = bin_s * NS
        hourly = width % HOUR == 0
        live = {r["session_id"]: r["live"] for r in rs}
        span = {**_span(sids, lo, hi), "width": width}
        cells = self._cells("cells", _HOUR_BINS if hourly else _MINUTE_BINS, span) if sids else []
        return Timeline(
            from_=dt(lo),
            to=dt(hi),
            now=now_dt if lo <= now <= hi else None,
            binSeconds=bin_s,
            sessions=[tl.band_from_table(r, packed[r["session_id"]]) for r in drawn],
            prompts=self._typed(sids, live, now, lo, hi) if sids else [],
            modelCalls=sp.model_bins(cells, width),
            analytics=[],
            notCaptured=[],
            friction=self._friction(sids, width, hourly, span, lo, hi) if sids else [],
            work=self._work_bins(cells, width, hourly, span) if sids else [],
        )

    def _work_bins(self, cells: list[sp.Cell], width: int, hourly: bool, span: dict[str, Any]) -> list[WorkBin]:
        steps = {c.start: int(c.row["model_calls"]) for c in cells}
        rows: dict[int, list[tuple[str, str, int]]] = defaultdict(list)
        for b, kind, key, n in self.snap.rows("work_bins", _WORK_BINS[hourly], {**span, "kinds": wk.KINDS}):
            rows[b].append((kind, key, n))
        return tp.work_bins(rows, steps)

    def _friction(
        self, sids: list[str], width: int, hourly: bool, span: dict[str, Any], lo: int, hi: int
    ) -> list[FrictionMark]:
        """The person timeline's friction (handoff §11S): ✕ each interrupted turn that started in range, ! each
        bin's failed calls (its sessions' `failed` key rows)."""
        failed: Counter[int] = Counter()
        for b, _kind, _key, n in self.snap.rows("failed_bins", _WORK_BINS[hourly], {**span, "kinds": ["failed"]}):
            failed[b] += n
        out = [
            FrictionMark(
                t=dt(p["t_ns"]),
                kind="interrupted",
                count=1,
                promptIndex=p["prompt_index"],
                detail="turn interrupted",
                tool=None,
            )
            for p in self.snap.dicts("interrupted", _INTERRUPTED, {"sids": sids, "lo": lo, "hi": hi})
        ]
        out += [
            FrictionMark(t=dt(b), kind="failed", count=n, promptIndex=None, detail="failed calls", tool=None)
            for b, n in sorted(failed.items())
            if n > 0
        ]
        return sorted(out, key=lambda m: m.t)

    def _cells(self, name: str, sql: str, params: dict[str, Any]) -> list[sp.Cell]:
        return [sp.Cell(r["t"], r["t"] + r["w"], r) for r in self.snap.dicts(name, sql, params)]

    def _typed(self, sids: list[str], live: dict[str, bool], now: int, lo: int, hi: int) -> list[TimelinePrompt]:
        out = []
        for p in self.snap.dicts("typed", _TYPED, {"sids": sids, "lo": lo // 1000 * 1000, "hi": hi // 1000 * 1000}):
            sid = p["session_id"]
            if not lo <= p["t_ns"] // 1000 * 1000 <= hi:
                continue
            out.append(
                TimelinePrompt(
                    t=dt(p["t_ns"]),
                    index=p["prompt_index"],
                    sessionId=sid,
                    promptId=p["prompt_id"],
                    text=sp._text(p),
                    steps=p["steps"],
                    durationMs=sp._duration(p, live[sid], now),
                    tokensIn=p["tokens_in"],
                    tokensOut=p["tokens_out"],
                )
            )
        return out

    # --- /people/{id}/summary -------------------------------------------------------------------

    def span(self, pid: str) -> tuple[list[str], int | None, int | None]:
        rs = self.scope(pid).sessions
        first = min((r["start_ns"] for r in rs), default=None)
        last = max((r["last_ns"] for r in rs), default=None)
        return self.scope(pid).sids, first, last

    def summary(self, pid: str, now: int, lo: int, hi: int) -> tuple[PersonSummary, int | None]:
        """The range's numbers. What only analytics fed (event counts, coverage, hooks, skills, commands,
        destructive commands, failures, cost, the cache write split) is null, empty or 0."""
        scope = self.scope(pid)
        rs = scope.sessions
        last = max((r["last_ns"] for r in rs), default=None)
        shown_rows = [r for r in rs if _shown(r, lo, hi, now)]
        shown = sorted(r["session_id"] for r in shown_rows)
        span = _span(shown, lo, hi)
        sums: Row = self.snap.dicts("cells", _TOTALS, span)[0] if shown else {}

        def total(col: str) -> Any:
            return sums.get(col)

        def count(col: str) -> int:
            return int(sums.get(col) or 0)

        calls = count("model_calls")
        keys: dict[str, Counter[str]] = defaultdict(Counter)
        distinct: dict[str, int] = {}
        rows: list[tuple[str, str, int]] = []
        if shown:
            rows = list(self.snap.rows("keys", _KEYS, {**span, "kinds": tu.KINDS + sp.WIRE_KINDS}))
            for kind, key, n in rows:
                keys[kind][key] += n
            distinct = dict(self.snap.rows("key_counts", _KEY_COUNTS, {**span, "kinds": ["call_prompt"]}))
        wire = sp.wire_of(self.snap, rows)
        creds = self.credentials(scope)
        account_calls = 0
        if creds:
            for c in self._cred_cells(creds, lo, hi):
                if c.row["kind"] == "account":
                    account_calls += c.row["n"]
        mcp_used: dict[str, set[str]] = {}
        for key in keys["call_mcp"]:
            server, tool = json.loads(key)
            mcp_used.setdefault(server, set()).add(tool)
        servers = {k: len(v) for k, v in mcp_used.items()}
        work = self._work_stats(shown_rows, span, lo, hi)
        prompts = distinct.get("call_prompt", 0) or None
        summary = PersonSummary(
            ribbon=Ribbon(
                modelCalls=calls,
                sessions=len(shown),
                analyticsEvents=0,
                analyticsBatches=0,
                eventTypes=0,
                accountCalls=account_calls,
                toolsReachable=None,
                tokens=TokenCount(
                    input=total("input_tokens"),
                    cacheRead=total("cache_read_tokens"),
                    cacheWrite=total("cache_write_tokens"),
                    cacheWrite5m=None,
                    cacheWrite1h=None,
                    output=total("output_tokens"),
                    accuracy="exact" if calls else "missing",
                ),
                cost=Cost(usd=None, basis="api-equivalent", estimatedCalls=0),
            ),
            coverage=Coverage(reported=None, captured=calls, notCaptured=0, windows=[]),
            capabilities=Capabilities(
                mcpServers=[
                    McpServer(name=k, tools=None, toolsUsed=n, riskyVerbs=[]) for k, n in sorted(servers.items())
                ],
                mcpToolsTotal=None,
                mcpToolsUsed=sum(servers.values()) if servers else None,
                builtinTools=None,
                builtinUsed=len(keys["call_builtin"]) or None,
                skills=None,
                hooks=None,
                hookOverheadMs=None,
                memoryWrites=None,
                variesBySession=None,
            ),
            footprint=Footprint(
                prompts=prompts,
                turns=prompts,
                toolCalls=wire.tools.calls if shown else None,
                toolsByName=wire.tools.byName,
                commands=None,
                topCommandWords=[],
                destructive=Destructive(total=0, rmF=0, rmRf=0, outsideCwd=0),
                codeShipped=CodeShipped(
                    headChanges=None,
                    commits=work.work.commits,
                    pushes=work.work.pushes,
                    prs=work.work.prs,
                ),
                failures=Failures(commands=0, toolErrors=0, apiRetries=0),
                subagentCalls=count("subagent_calls"),
            ),
            work=work,
            recording="content" if wire.recorded else "metadata",
            tools=wire.tools,
            failedCalls=wire.failed,
            compactions=wire.compactions,
            images=None,
        )
        return summary, last

    def _work_stats(self, shown: list[Row], span: dict[str, Any], lo: int, hi: int) -> PersonWorkStats:
        """The header row's work over [lo, hi] (§11R): the turns that started in it and their times, the work
        key rows of its cells and the repos of its sessions."""
        sids = sorted(r["session_id"] for r in shown)
        turns, active = tp.person_turns(self.snap, sids, lo, hi)
        w = wk.from_keys(self.snap.rows("work_keys", _KEYS, {**span, "kinds": wk.KINDS})) if sids else wk.add([])
        return PersonWorkStats(
            turns=turns,
            activeMs=active,
            work=wk.turn_work(w),
            repos=len({r["repo"] for r in shown if r["repo"]}),
            outcome=OutcomeFacts(
                commits=w["commits"],
                headFrom=None,
                headTo=None,
                editedFiles=w["editedFiles"],
                editTypes=w["editTypes"],
                lastTest=None,
                pushes=w["pushes"],
                prs=w["prs"],
            ),
        )

    # --- /people/{id}/work ----------------------------------------------------------------------

    def work(self, pid: str, now: int, lo: int | None, hi: int | None) -> tuple[list[RepoGroup], int | None]:
        rs = self.scope(pid).sessions
        last = max((r["last_ns"] for r in rs), default=None)
        kept = [r for r in rs if _shown(r, lo, hi, now)]
        return tp.repo_groups(self.rows_of(kept, now), now), last

    # --- /people/{id}/requests ------------------------------------------------------------------

    def _cred_cells(self, creds: list[str], lo: int | None, hi: int | None) -> list[sp.Cell]:
        return self._cells("cred_cells", _CRED_CELLS, {**_span([], lo, hi), "creds": creds})

    def requests(
        self, pid: str, lo: int | None, hi: int | None, kind: Sequence[str] | None, q: str | None,
        captured: bool | None, page: int, size: int, order: str, agent: Sequence[str] | None, hidden: bool,
    ) -> tuple[int, int, dict[str, int], list[RequestRow], int | None]:  # fmt: skip
        """(hidden count, total, counts by kind, the page's rows, the person's newest time). Every row is a
        captured call: `captured=false` (the calls that bypassed the proxy, which Claude Code's analytics
        reported) has none."""
        scope = self.scope(pid)
        sids = scope.sids
        last = max((r["last_ns"] for r in scope.sessions), default=None)
        if not sids:
            return 0, 0, {}, [], last
        creds = self.credentials(scope)
        kinds = list(kind or [])
        agents_ = set(agent or [])
        if captured is False or not creds:
            return 0, 0, {}, [], last

        def keep_agent(a: str) -> bool:
            return not agents_ or agents.agent_of(a) in agents_

        def keep(k: str, hid: bool) -> bool:
            return (hidden or not hid) and (not kinds or k in kinds)

        if q:
            rows = self._rows(sids, creds, lo, hi, True, [])
            rows = [r for r in rows if sp._matches(r, q) and (not agents_ or r.agent in agents_)]
            hidden_count = sum(r.hiddenKind is not None for r in rows)
            by_kind: Counter[str] = Counter(r.kind for r in rows if hidden or r.hiddenKind is None)
            shown = [r for r in rows if keep(r.kind, r.hiddenKind is not None)]
            shown.sort(key=lambda r: (r.t, r.requestId), reverse=order == "desc")
            return hidden_count, len(shown), dict(sorted(by_kind.items())), shown[(page - 1) * size : page * size], last

        span = _span(sids, lo, hi)
        cred = self._cells("cred_cells", _CRED_CELLS, {**span, "creds": creds})
        allc: Counter[tuple[str, bool]] = Counter()
        for c in cred:
            if keep_agent(c.row["agent"]):
                allc[c.row["kind"], c.row["hidden"]] += c.row["n"]
        hidden_count = sum(n for (_, hid), n in allc.items() if hid)
        kind_counts: Counter[str] = Counter()
        for (k, hid), n in allc.items():
            if hidden or not hid:
                kind_counts[k] += n
        counts = dict(sorted((k, n) for k, n in kind_counts.items() if n))
        total = sum(n for (k, hid), n in allc.items() if keep(k, hid))
        skip = (page - 1) * size
        if size <= 0 or skip >= total:
            return hidden_count, total, counts, [], last

        def kept(r: Row) -> bool:
            return keep_agent(r["agent"]) and keep(r["kind"], r["hidden"])

        cells = _merge(cred, kept)

        def refine(cell: sp.Cell) -> list[sp.Cell]:
            p = {**span, "lo": cell.start, "hi": cell.end - 1}
            return _merge(self._cells("cred_minutes", _CRED_MINUTES, {**p, "creds": creds}), kept)

        def count(cell: sp.Cell) -> int:
            return int(cell.row["n"])

        desc = order == "desc"
        found = sp._walk(cells, count, skip, refine, desc=desc)
        if found is None:
            return hidden_count, total, counts, [], last
        at, inner = found
        a, b = sp.SessionReads._window(cells, at, inner + size, count, refine, desc=desc)
        if lo is not None:
            a = max(a, lo)
        if hi is not None:
            b = min(b, hi + 1)
        rows = self._rows(sids, creds, a, b - 1, hidden, kinds)
        rows = [r for r in rows if not agents_ or r.agent in agents_]
        rows.sort(key=lambda r: (r.t, r.requestId), reverse=desc)
        return hidden_count, total, counts, rows[inner : inner + size], last

    def _rows(
        self, sids: list[str], creds: list[str], lo: int | None, hi: int | None, hidden: bool, kinds: list[str]
    ) -> list[RequestRow]:
        """The person's rows in [lo, hi] (None: open) that the filters keep (but agent and `q`)."""
        a = lo if lo is not None else -_FAR
        b = hi + 1 if hi is not None else _FAR
        calls = self.snap.dicts(
            "call_rows",
            _CALL_ROWS,
            {"creds": creds, "a": a, "b": b, "hidden": hidden, "any_kind": not kinds, "kinds": kinds},
        )
        pids = sorted({c["prompt_id"] for c in calls if c["prompt_id"]})
        index: dict[str, int] = {}
        if pids:
            for p, n in self.snap.rows("prompt_indexes", _PROMPT_INDEXES, {"sids": sids, "pids": pids}):
                index[p] = n
        model_ids = [c["id"].replace("~", "#") for c in calls if c["kind"] == "model" and c["prompt_id"]]
        steps: dict[str, int] = {}
        if model_ids:
            steps = {
                cid.replace("#", "~"): n
                for cid, n in self.snap.rows("step_numbers", sp.STEP_NUMBERS, {"ids": model_ids})
            }
        return [sp.call_row(c, index, steps) for c in calls]


# --- shaping helpers ---------------------------------------------------------------------------------------


def _merge(cred: list[sp.Cell], keep: Callable[[Row], bool]) -> list[sp.Cell]:
    """The request log's cells: per time unit (an hour inside the range, a minute at its edges), how many rows
    the filters keep (n), from the credentials' calls."""
    by: dict[tuple[int, int], int] = defaultdict(int)
    for c in cred:
        if keep(c.row):
            by[c.start, c.end] += c.row["n"]
    return [sp.Cell(a, b, {"n": n}) for (a, b), n in sorted(by.items()) if n]
