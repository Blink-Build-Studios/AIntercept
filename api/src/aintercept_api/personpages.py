"""The person pages over v9 (docs/PIPELINE.md §6.3): what each page shows, read from the facts and the rollups
through a few indexed statements in the request's snapshot, and shaped by today's code.

A person's scope is their accounts (`accounts` by email group: the URL's account and every account with its
email), their sessions (`session_accounts` by account; `unattributed`: the sessions with a visible call and
no account, `sessions.unattributed`) and their credentials (`session_credentials` of those sessions). Every
page reads the scope first: the group's account rows, and the person's `sessions` rows (one per session).

- /people/{id}: the group's accounts and devices (`account_devices`); the lifetime sums (missed, bypass %),
  the agents (first seen, versions) and the newest values (limits, posture, live) from the person's
  `sessions` rows, as today's header rolls them up.
- /sessions: a page of the person's `sessions` rows in range (the range and the nesting are over the rows
  the scope read), with their children (`session_edges`) and labels (§6.1).
- /timeline: lanes from the sessions' `session_minutes` (bins under an hour) or `session_hours` (hours
  inside the range, minutes at its edges), event names from their key rollups; bands from `sessions`; typed
  prompts from `prompt_items` and `prompts` in range; not-captured windows from `missed_reports`; friction
  from the friction minutes' events.
- /summary: sums from the shown sessions' cells in range; distinct counts from their key rollups; the latest
  MCP and skills counts ≤ hi by index; account calls from `cred_minutes`/`cred_hours`.
- /requests: a page of `calls` by (credential, time) over the scope's credentials, with the scope's
  `missed_reports`; counts from `cred_minutes`/`cred_hours` and the sessions' cells, which find the page's
  minutes. Step numbers from `steps`, TTFT and cache from the report (§5.4), prompt numbers from
  `prompt_items`.

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
from aintercept_api.models.common import AgentLimits, Coverage, CoverageWindow, LimitWindow
from aintercept_api.models.people import (
    AgentInfo,
    AnalyticsBin,
    CallClasses,
    Capabilities,
    CodeShipped,
    CommandWord,
    Cost,
    Destructive,
    DeviceInfo,
    EventCount,
    Failures,
    Flags,
    Footprint,
    FrictionMark,
    GapWindow,
    Limits,
    LinkedAccount,
    McpServer,
    ModelCallBin,
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

_CELL_COLUMNS = """model_calls, main_calls, subagent_calls, unreported, input_tokens, cache_read_tokens,
    cache_write_tokens, output_tokens, context_sum, events, frustrated, interrupted, destructive, outside_cwd,
    reported, missed, hook_ms_sum, hook_ms_n, cache_5m, cache_1h, cost_usd"""
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
_SUMS = ", ".join(
    f"sum({c})::bigint AS {c}" if c not in ("hook_ms_sum", "cost_usd") else f"sum({c}) AS {c}"
    for c in (x.strip() for x in _CELL_COLUMNS.split(","))
)


def _binned(cells: str) -> str:
    """The cells summed per bin of %(width)s ns (bins start on multiples of the width, as today's)."""
    return f"""SELECT t - mod(t, %(width)s) AS t, %(width)s AS w, {_SUMS} FROM ({cells}) c GROUP BY 1 ORDER BY 1"""  # noqa: S608


_HOUR_BINS = _binned(_CELLS)
_MINUTE_BINS = _binned(_MINUTES)
_TOTALS = f"""SELECT {_SUMS} FROM ({_CELLS}) c"""  # noqa: S608
# The sessions' friction minutes in the bins that have one.
_FRICTION_MINUTES = """SELECT m.session_id, m.minute_ns FROM unnest(%(bins)s::bigint[]) b(t), LATERAL (
  SELECT session_id, minute_ns FROM session_minutes WHERE session_id = ANY(%(sids)s)
    AND minute_ns >= greatest(b.t, %(lo)s) AND minute_ns < b.t + %(width)s AND minute_ns <= %(hi)s
    AND (frustrated > 0 OR interrupted > 0 OR destructive > 0)) m"""
_KEY_ROWS = """SELECT kind, key, n FROM session_keys WHERE session_id = ANY(%(sids)s) AND kind = ANY(%(kinds)s)
    AND (%(hlo)s::bigint IS NULL OR hour_ns >= %(hlo)s) AND (%(hhi)s::bigint IS NULL OR hour_ns < %(hhi)s)
  UNION ALL
  SELECT kind, key, n FROM session_minute_keys WHERE session_id = ANY(%(sids)s) AND kind = ANY(%(kinds)s)
    AND minute_ns >= %(lo)s AND minute_ns <= %(hi)s AND (minute_ns < %(hlo)s OR minute_ns >= %(hhi)s)"""
_KEYS = f"""SELECT kind, key, sum(n)::bigint AS n FROM ({_KEY_ROWS}) k GROUP BY kind, key"""  # noqa: S608
# The distinct count of each kind (what the summary counts but doesn't list: batches, heads, hooks, prompts).
_KEY_COUNTS = f"""SELECT kind, count(DISTINCT key) AS n FROM ({_KEY_ROWS}) k GROUP BY kind"""  # noqa: S608
# Each cell's event names: the hours' (inside the range) and the minutes' (at its edges), or every minute's.
_HOUR_NAMES = """SELECT hour_ns AS t, key, n FROM session_keys WHERE session_id = ANY(%(sids)s) AND kind = 'name'
  AND (%(hlo)s::bigint IS NULL OR hour_ns >= %(hlo)s) AND (%(hhi)s::bigint IS NULL OR hour_ns < %(hhi)s)
UNION ALL
SELECT minute_ns, key, n FROM session_minute_keys WHERE session_id = ANY(%(sids)s) AND kind = 'name'
  AND minute_ns >= %(lo)s AND minute_ns <= %(hi)s AND (minute_ns < %(hlo)s OR minute_ns >= %(hhi)s)"""
_MINUTE_NAMES = """SELECT minute_ns AS t, key, n FROM session_minute_keys WHERE session_id = ANY(%(sids)s)
  AND kind = 'name' AND minute_ns >= %(lo)s AND minute_ns <= %(hi)s"""


def _top_names(names: str) -> str:
    """Each bin's three commonest event names (most first, then by name) and the count of its named events:
    the top three of a bin, "(unnamed)" among them, are among these."""
    return f"""SELECT b, key, n, named FROM (
  SELECT b, key, n, sum(n) OVER (PARTITION BY b)::bigint AS named,
         row_number() OVER (PARTITION BY b ORDER BY n DESC, key COLLATE "C") AS r
  FROM (SELECT t - mod(t, %(width)s) AS b, key, sum(n)::bigint AS n FROM ({names}) x GROUP BY 1, 2) y) z
WHERE r <= {tl.TOP_EVENTS}"""  # noqa: S608 (fixed text)


_TOP_NAMES = {True: _top_names(_HOUR_NAMES), False: _top_names(_MINUTE_NAMES)}


def _span(sids: list[str], lo: int | None, hi: int | None) -> dict[str, Any]:
    """A cell read's parameters for [lo, hi] (each bound None: open), as sessionpages._span's."""
    got = sp._span("", lo, hi)
    del got["sid"]
    return {**got, "sids": sids}


# --- the timeline ------------------------------------------------------------------------------------------

# The typed prompts in range (session.py timeline_prompts: by µs, then session, then place).
_TYPED = """SELECT i.session_id, i.seq, i.t_ns, i.prompt_id, i.prompt_index, i.open_tail, i.pasted_bytes, i.images,
    p.steps, p.text, p.text_chars, p.tokens_in, p.tokens_out, p.turn_end_ns, p.turn_report, p.unfinished_max_ns,
    p.has_input
FROM prompt_items i JOIN prompts p ON p.prompt_id = i.prompt_id AND p.session_id = i.session_id
WHERE i.session_id = ANY(%(sids)s) AND i.type = 'prompt' AND i.t_ns >= %(lo)s AND i.t_ns < %(hi)s + 1000
ORDER BY i.t_ns / 1000, i.session_id COLLATE "C", i.seq"""
_MISSED = """SELECT session_id, ts_ns, event_key FROM missed_reports
WHERE session_id = ANY(%(sids)s) AND ts_ns >= %(lo)s AND ts_ns <= %(hi)s"""
_WINDOW = f"""SELECT (SELECT count(*) FROM events e WHERE e.session_id = ANY(%(sids)s) AND e.name = 'tengu_api_success'
      AND e.ts_ns BETWEEN %(a)s AND %(b)s AND e.source = 'claude_code') AS reported,
  NOT EXISTS (SELECT 1 FROM calls c WHERE c.session_id = ANY(%(sids)s) AND {sp._AT} BETWEEN %(a)s AND %(b)s
              AND {sp.VISIBLE}) AS bypassed"""  # noqa: S608
_FRICTION = """SELECT e.session_id, e.ts_ns, e.name, e.metadata -> 'is_negative' AS negative,
    e.metadata -> 'prompt_index' AS idx, e.metadata -> 'destructive_category' AS category,
    e.metadata -> 'destructive_target_scope' AS scope,
    (SELECT p.prompt_index FROM prompts p WHERE p.prompt_id = e.metadata ->> 'cc_prompt_id'
       AND p.session_id = e.session_id AND p.has_input) AS prompt_index
FROM unnest(%(sids)s::text[], %(minutes)s::bigint[]) m(sid, t), LATERAL (
  SELECT * FROM events e WHERE e.session_id = m.sid
    AND e.name IN ('tengu_input_prompt', 'tengu_cancel', 'tengu_bash_tool_command_executed')
    AND e.ts_ns >= m.t AND e.ts_ns < m.t + 60000000000 AND e.source = 'claude_code') e
ORDER BY e.ts_ns, e.session_id COLLATE "C", e.batch_call_id COLLATE "C", e.batch_cc_n"""

# --- the summary -------------------------------------------------------------------------------------------


# Claude Code's own latest counts up to the range's end, over the shown sessions (the newest event that
# carries one, as today's arg_max over event_rows.count).
def _json_int_sql(v: str) -> str:
    """refresh/sql.go jsonInt: TRY_CAST(json_extract_string(v) AS BIGINT)."""
    n = "'^[+-]?([0-9]+\\.?[0-9]*|\\.[0-9]+)([eE][+-]?[0-9]+)?$'"
    return f"""(CASE jsonb_typeof({v}) WHEN 'number' THEN round(({v} #>> '{{}}')::numeric)::bigint
        WHEN 'string' THEN CASE WHEN btrim({v} #>> '{{}}') ~ {n} THEN round(btrim({v} #>> '{{}}')::numeric)::bigint END
        END)"""


_LATEST = f"""SELECT x.name, l.ts_ns, l.n FROM unnest(%(sids)s::text[]) s(sid),
  unnest(ARRAY['tengu_tool_search_mode_decision', 'tengu_skills_sync_success']) x(name), LATERAL (
  SELECT e.ts_ns, n FROM events e, LATERAL (SELECT CASE e.name
      WHEN 'tengu_tool_search_mode_decision' THEN {_json_int_sql("e.metadata -> 'mcpToolCount'")}
      ELSE {_json_int_sql("e.metadata -> 'total'")} END AS n) v
  WHERE e.session_id = s.sid AND e.name = x.name AND e.source = 'claude_code' AND e.ts_ns <= %(hi)s
    AND v.n IS NOT NULL
  ORDER BY e.ts_ns DESC, v.n DESC LIMIT 1) l"""  # noqa: S608
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
_MISSED_CELLS = """SELECT hour_ns AS t, 3600000000000 AS w, sum(missed)::bigint AS n FROM session_hours
WHERE session_id = ANY(%(sids)s) AND (%(hlo)s::bigint IS NULL OR hour_ns >= %(hlo)s)
  AND (%(hhi)s::bigint IS NULL OR hour_ns < %(hhi)s) AND missed > 0
GROUP BY 1, 2
UNION ALL
SELECT minute_ns, 60000000000, sum(missed)::bigint FROM session_minutes
WHERE session_id = ANY(%(sids)s) AND minute_ns >= %(lo)s AND minute_ns <= %(hi)s
  AND (minute_ns < %(hlo)s OR minute_ns >= %(hhi)s) AND missed > 0
GROUP BY 1, 2
ORDER BY 1"""
_MISSED_MINUTES = """SELECT minute_ns AS t, 60000000000 AS w, sum(missed)::bigint AS n FROM session_minutes
WHERE session_id = ANY(%(sids)s) AND minute_ns >= %(lo)s AND minute_ns <= %(hi)s AND missed > 0
GROUP BY 1, 2 ORDER BY 1"""

# --- the request log ---------------------------------------------------------------------------------------

_CALL_ROWS = f"""SELECT {sp._ROW_COLUMNS} FROM calls c
WHERE c.key_hash = ANY(%(creds)s) AND (c.start_ns >= %(a)s AND c.start_ns < %(b)s
    OR c.start_ns IS NULL AND c.end_ns >= %(a)s AND c.end_ns < %(b)s)
  AND {sp._TRANSACTION} AND (%(hidden)s OR c.hidden_kind IS NULL) AND (%(any_kind)s OR c.kind = ANY(%(kinds)s))"""  # noqa: S608
_MISSED_ROWS = """SELECT session_id, event_key, ts_ns, request_id, client_request_id FROM missed_reports
WHERE session_id = ANY(%(sids)s) AND ts_ns >= %(a)s AND ts_ns < %(b)s"""
# The first report of each id (as its request or client request id) in the scope's sessions, in today's order.
_REPORTS = """SELECT k, r.metadata -> 'ttftMs' AS ttft, r.metadata -> 'cache_creation_5m_input_tokens' AS c5,
    r.metadata -> 'cache_creation_1h_input_tokens' AS c1
FROM unnest(%(keys)s::text[]) k, LATERAL (
  SELECT r.metadata FROM events r WHERE (r.request_id = k OR r.client_request_id = k) AND r.session_id = ANY(%(sids)s)
    AND r.source = 'claude_code' AND r.name = 'tengu_api_success'
  ORDER BY r.ts_ns, r.event_key COLLATE "C" LIMIT 1) r"""
_PROMPT_INDEXES = """SELECT prompt_id, prompt_index FROM prompt_items
WHERE session_id = ANY(%(sids)s) AND prompt_id = ANY(%(pids)s) AND prompt_index IS NOT NULL
ORDER BY session_id COLLATE "C" DESC"""
_STEP_NUMBERS = "SELECT call_id, step FROM steps WHERE call_id = ANY(%(ids)s)"


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
        self._failed: dict[str, Row] = {}  # the failed model calls a requests read built rows of, by row id

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
        org, plan = (me["org"], me["plan"]) if me else (None, None)
        linked = [pid] if unattributed else scope.account_ids
        devices: list[Row] = []
        if not unattributed:
            devices = self.snap.dicts(
                "devices",
                "SELECT * FROM account_devices WHERE account_id = ANY(%(accts)s) ORDER BY account_id, device_id",
                {"accts": linked},
            )
        own = [d for d in devices if d["account_id"] == pid and d["on_events"]]
        cc_rows = [r for r in rs if r["product"] != "codex"]
        with_events_cc = sum(1 for r in cc_rows if r["events"] is not None)
        rl_row = _newest(rs, "rl_status", "rl_ns")
        rl = (
            {"status": rl_row["rl_status"], "util": rl_row["rl_util"], "overage": rl_row["rl_overage"]}
            if rl_row
            else {"status": None, "util": None, "overage": None}
        )
        sandbox_row = _newest(rs, "sandbox", "sandbox_ns")
        sandbox = sandbox_row["sandbox"] if sandbox_row else None
        queries = sum(r["queries"] for r in rs)
        bypassed = sum(r["bypassed"] for r in rs)
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
            plan=plan,
            rateLimitTier=None,
            customerSince=None,
            firstUseAt=None,
            devices=[
                DeviceInfo(deviceId=d["device_id"], platform=d["platform"], terminal=d["terminal"], shell=d["shell"])
                for d in own
            ],
            limits=Limits(status=rl["status"], utilization7d=rl["util"], overage=rl["overage"]),
            flags=Flags(total=None, experiments=None, changedSince=None),
            posture=Posture(
                bypassPct=round(100 * bypassed / queries, 1) if rs and queries else None,
                sandbox={"true": "on", "false": "off"}.get(sandbox or ""),
                tokenStorage=None,
                proxyDetected="custom_ca" if any(r["custom_ca"] for r in rs) else None,
            ),
            lastSeenAt=dt(last) if last is not None else None,
            live=any(_live(r, now) for r in rs),
            agentInfo=info,
            accounts=[] if unattributed else self._linked(scope, linked, devices),
            linkedBy="email" if len(linked) > 1 else None,
            agentLimits=self._agent_limits(cc_rows, rl, plan, limits),
            capabilities={a.agent: agents.CAPABILITIES[a.agent] for a in info},
            org=OrgStat(id=org, kind=agents.org_kind(org)) if org else None,
            missed=sum(r["cov_not_captured"] for r in cc_rows if r["events"] is not None) if with_events_cc else None,
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
    def _agent_limits(cc_rows: list[Row], rl: dict[str, Any], plan: str | None, codex: str | None) -> list[AgentLimits]:
        out = []
        if cc_rows and rl["util"] is not None:
            out.append(
                AgentLimits(
                    agent="claude-code",
                    plan=plan,
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
        product = {r["session_id"]: r["product"] for r in rs}
        span = {**_span(sids, lo, hi), "width": width}
        cells = self._cells("cells", _HOUR_BINS if hourly else _MINUTE_BINS, span) if sids else []
        return Timeline(
            from_=dt(lo),
            to=dt(hi),
            now=now_dt if lo <= now <= hi else None,
            binSeconds=bin_s,
            sessions=[tl.band_from_table(r, packed[r["session_id"]]) for r in drawn],
            prompts=self._typed(sids, live, product, now, lo, hi) if sids else [],
            modelCalls=_model_bins(cells, width),
            analytics=self._analytics(sids, cells, width, hourly, span) if sids else [],
            notCaptured=self._gaps(sids, lo, hi) if sids else [],
            friction=self._friction(sids, cells, width, span) if sids else [],
        )

    def _cells(self, name: str, sql: str, params: dict[str, Any]) -> list[sp.Cell]:
        return [sp.Cell(r["t"], r["t"] + r["w"], r) for r in self.snap.dicts(name, sql, params)]

    def _typed(
        self, sids: list[str], live: dict[str, bool], product: dict[str, Any], now: int, lo: int, hi: int
    ) -> list[TimelinePrompt]:
        out = []
        for p in self.snap.dicts("typed", _TYPED, {"sids": sids, "lo": lo // 1000 * 1000, "hi": hi // 1000 * 1000}):
            sid = p["session_id"]
            if not (p["has_input"] or product.get(sid) == "codex"):
                continue
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

    def _analytics(
        self, sids: list[str], cells: list[sp.Cell], width: int, hourly: bool, span: dict[str, Any]
    ) -> list[AnalyticsBin]:
        events: Counter[int] = Counter()
        for c in cells:
            events[c.start - c.start % width] += c.row["events"]
        if not any(events.values()):
            return []
        names: dict[int, Counter[str]] = defaultdict(Counter)
        named: dict[int, int] = {}
        for b, key, n, all_named in self.snap.rows("names", _TOP_NAMES[hourly], span):
            names[b][key] += n
            named[b] = all_named
        out = []
        for b in sorted(k for k, n in events.items() if n):
            got = names[b]
            unnamed = events[b] - named.get(b, 0)
            if unnamed > 0:
                got["(unnamed)"] += unnamed
            top = sorted(got.items(), key=lambda x: (-x[1], x[0]))[: tl.TOP_EVENTS]
            out.append(AnalyticsBin(t=dt(b), count=events[b], top=[EventCount(name=k, count=n) for k, n in top]))
        return out

    def _missed(self, sids: list[str], lo: int | None, hi: int | None) -> list[int]:
        got = self.snap.rows(
            "missed", _MISSED, {"sids": sids, "lo": -_FAR if lo is None else lo, "hi": _FAR if hi is None else hi}
        )
        return sorted(t for _, t, _ in got)

    def _gaps(self, sids: list[str], lo: int, hi: int) -> list[GapWindow]:
        out = []
        for a, b, n in _windows(self._missed(sids, lo, hi)):
            ((reported, bypassed),) = self.snap.rows("window", _WINDOW, {"sids": sids, "a": a, "b": b})
            out.append(
                GapWindow(
                    from_=dt(a),
                    to=dt(b),
                    count=n,
                    reported=reported,
                    captured=reported - n,
                    cause=tl.BYPASS_CAUSE if bypassed else None,
                )
            )
        return out

    def _friction(self, sids: list[str], bins: list[sp.Cell], width: int, span: dict[str, Any]) -> list[FrictionMark]:
        hot = [c.start for c in bins if c.row["frustrated"] or c.row["interrupted"] or c.row["destructive"]]
        if not hot:
            return []
        lo, hi = span["lo"], span["hi"]
        pairs = self.snap.rows("friction_minutes", _FRICTION_MINUTES, {**span, "bins": hot})
        marks: dict[tuple[int, str], list[tuple[int | None, str | None]]] = defaultdict(list)
        got = self.snap.dicts("friction", _FRICTION, {"sids": [a for a, _ in pairs], "minutes": [t for _, t in pairs]})
        for e in got:
            if not lo <= e["ts_ns"] <= hi:
                continue
            b = e["ts_ns"] - e["ts_ns"] % width
            if e["name"] == "tengu_input_prompt":
                if sp._json_str(e["negative"]) == "true":
                    marks[b, "frustrated"].append((sp.json_int(e["idx"]), None))
            elif e["name"] == "tengu_cancel":
                marks[b, "interrupted"].append((e["prompt_index"], None))
            else:
                cat = sp._json_str(e["category"])
                if cat and cat != "none":
                    scope = sp._json_str(e["scope"])
                    detail = " · ".join(p for p in (cat, scope) if p)
                    marks[b, "destructive"].append((e["prompt_index"], detail))
        out = []
        for (b, kind), xs in sorted(marks.items()):
            details = list(dict.fromkeys(d for _, d in xs if d is not None))
            out.append(
                FrictionMark(
                    t=dt(b),
                    kind=kind,
                    count=len(xs),
                    promptIndex=xs[0][0],
                    detail=", ".join(details) if kind == "destructive" else tl.FRICTION_DETAIL[kind],
                    tool="Bash" if kind == "destructive" else None,
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
        scope = self.scope(pid)
        rs = scope.sessions
        last = max((r["last_ns"] for r in rs), default=None)
        shown_rows = [r for r in rs if _shown(r, lo, hi, now)]
        shown = sorted(r["session_id"] for r in shown_rows)
        with_events = sum(1 for r in shown_rows if r["events"] is not None)
        span = _span(shown, lo, hi)
        sums: Row = self.snap.dicts("cells", _TOTALS, span)[0] if shown else {}

        def total(col: str) -> Any:
            return sums.get(col)

        def count(col: str) -> int:
            return int(sums.get(col) or 0)

        calls = count("model_calls")
        events = count("events")
        hook_n = count("hook_ms_n")
        hook_ms = total("hook_ms_sum") / hook_n if hook_n else None
        kinds = ["name", "tool", "mcp_tool", "word", "destructive", "git", "pr", "call_mcp", "call_builtin"]
        keys: dict[str, Counter[str]] = defaultdict(Counter)
        distinct: dict[str, int] = {}
        if shown:
            for kind, key, n in sorted(self.snap.rows("keys", _KEYS, {**span, "kinds": kinds}), key=lambda x: x[1]):
                keys[kind][key] += n
            counted = ["batch", "head", "hook", "call_prompt"]
            distinct = dict(self.snap.rows("key_counts", _KEY_COUNTS, {**span, "kinds": counted}))
        dims = {k: keys[k] for k in ("name", "tool", "mcp_tool", "word", "destructive", "git")}
        prs = {str(n) for n in map(sp._pr, keys["pr"]) if n is not None}
        tools = dims["tool"] + dims["mcp_tool"]
        reported = count("reported")
        missed = count("missed")
        missed_at = self._missed(shown, lo, hi) if shown and missed else []
        creds = self.credentials(scope)
        account_calls = 0
        if creds:
            for c in self._cred_cells(creds, lo, hi):
                if c.row["kind"] == "account":
                    account_calls += c.row["n"]
        rep = reported if with_events else None
        coverage = Coverage(
            reported=rep,
            captured=(rep - missed) if rep is not None else calls,
            notCaptured=missed,
            windows=[CoverageWindow(from_=dt(a), to=dt(b), count=n) for a, b, n in _windows(missed_at)],
        )
        mcp_used: dict[str, set[str]] = {}
        builtin_used: set[str] = set(keys["call_builtin"])
        for key in keys["call_mcp"]:
            server, tool = json.loads(key)
            mcp_used.setdefault(server, set()).add(tool)
        for name in dims["mcp_tool"]:
            parts = name.split("__")
            if name.startswith("mcp__") and len(parts) >= 3:
                mcp_used.setdefault(parts[1], set()).add("__".join(parts[2:]))
        servers = {k: len(v) for k, v in mcp_used.items()}
        names, git = dims["name"], dims["git"]
        has_events = bool(names)
        latest = self._latest(shown, hi) if shown else {}
        cost = total("cost_usd")
        heads = distinct.get("head", 0)
        summary = PersonSummary(
            ribbon=Ribbon(
                modelCalls=calls,
                sessions=len(shown),
                analyticsEvents=events,
                analyticsBatches=distinct.get("batch", 0),
                eventTypes=len(names),
                accountCalls=account_calls,
                toolsReachable=None,
                tokens=TokenCount(
                    input=total("input_tokens"),
                    cacheRead=total("cache_read_tokens"),
                    cacheWrite=total("cache_write_tokens"),
                    cacheWrite5m=total("cache_5m"),
                    cacheWrite1h=total("cache_1h"),
                    output=total("output_tokens"),
                    accuracy="exact" if calls else "missing",
                ),
                cost=Cost(
                    usd=round(cost, 2) if cost is not None else None,
                    basis="api-equivalent",
                    estimatedCalls=count("unreported"),
                ),
            ),
            coverage=coverage,
            capabilities=Capabilities(
                mcpServers=[
                    McpServer(name=k, tools=None, toolsUsed=n, riskyVerbs=[]) for k, n in sorted(servers.items())
                ],
                mcpToolsTotal=latest.get("tengu_tool_search_mode_decision"),
                mcpToolsUsed=sum(servers.values()) if has_events or servers else None,
                builtinTools=None,
                builtinUsed=len(builtin_used | (set(tools) - set(dims["mcp_tool"])))
                if has_events or builtin_used
                else None,
                skills=latest.get("tengu_skills_sync_success"),
                hooks=distinct.get("hook", 0) if has_events else None,
                hookOverheadMs=round(hook_ms, 1) if hook_ms is not None else None,
                memoryWrites=names.get("tengu_memdir_file_write", 0) if has_events else None,
                variesBySession=None,
            ),
            footprint=Footprint(
                prompts=distinct.get("call_prompt", 0) or None,
                toolCalls=sum(tools.values()) if has_events else None,
                toolsByName=dict(tools.most_common()),
                commands=names.get("tengu_bash_tool_command_executed", 0) if has_events else None,
                topCommandWords=[
                    CommandWord(word=w, count=n)
                    for w, n in sorted(dims["word"].items(), key=lambda x: (-x[1], x[0]))[:8]
                ],
                destructive=Destructive(
                    total=sum(dims["destructive"].values()),
                    rmF=dims["destructive"].get("rm_force", 0),
                    rmRf=dims["destructive"].get("rm_recursive_force", 0),
                    outsideCwd=count("outside_cwd"),
                ),
                codeShipped=CodeShipped(
                    headChanges=max(heads - 1, 0) if heads else None,
                    commits=git.get("commit", 0),
                    pushes=git.get("push", 0),
                    prs=sorted(int(k) for k in prs if k.isdigit()),
                ),
                failures=Failures(
                    commands=names.get("tengu_bash_tool_command_failed", 0),
                    toolErrors=names.get("tengu_tool_use_error", 0),
                    apiRetries=names.get("tengu_api_retry", 0),
                ),
                subagentCalls=count("subagent_calls"),
            ),
        )
        return summary, last

    def _latest(self, sids: list[str], hi: int) -> dict[str, int]:
        best: dict[str, tuple[int, int]] = {}
        for name, ts, n in self.snap.rows("latest", _LATEST, {"sids": sids, "hi": hi}):
            if name not in best or (ts, n) > best[name]:
                best[name] = (ts, n)
        return {k: v[1] for k, v in best.items()}

    # --- /people/{id}/requests ------------------------------------------------------------------

    def _cred_cells(self, creds: list[str], lo: int | None, hi: int | None) -> list[sp.Cell]:
        return self._cells("cred_cells", _CRED_CELLS, {**_span([], lo, hi), "creds": creds})

    def requests(
        self, pid: str, lo: int | None, hi: int | None, kind: Sequence[str] | None, q: str | None,
        captured: bool | None, page: int, size: int, order: str, agent: Sequence[str] | None, hidden: bool,
    ) -> tuple[int, int, dict[str, int], list[RequestRow], int | None]:  # fmt: skip
        """(hidden count, total, counts by kind, the page's rows, the person's newest time)."""
        scope = self.scope(pid)
        sids = scope.sids
        last = max((r["last_ns"] for r in scope.sessions), default=None)
        if not sids:
            return 0, 0, {}, [], last
        creds = self.credentials(scope)
        kinds = list(kind or [])
        agents_ = set(agent or [])
        with_calls = captured is not False and bool(creds)
        with_missed = captured is not True and (not agents_ or "claude-code" in agents_)

        def keep_agent(a: str) -> bool:
            return not agents_ or agents.agent_of(a) in agents_

        def keep(k: str, hid: bool) -> bool:
            return (hidden or not hid) and (not kinds or k in kinds)

        if q:
            rows = self._rows(scope, creds, lo, hi, with_calls, with_missed, True, [])
            rows = [r for r in rows if sp._matches(r, q) and (not agents_ or r.agent in agents_)]
            hidden_count = sum(r.hiddenKind is not None for r in rows)
            by_kind: Counter[str] = Counter(r.kind for r in rows if hidden or r.hiddenKind is None)
            shown = [r for r in rows if keep(r.kind, r.hiddenKind is not None)]
            shown.sort(key=lambda r: (r.t, r.requestId), reverse=order == "desc")
            return (
                hidden_count,
                len(shown),
                dict(sorted(by_kind.items())),
                self._retried(scope, shown[(page - 1) * size : page * size]),
                last,
            )

        span = _span(sids, lo, hi)
        cred = self._cells("cred_cells", _CRED_CELLS, {**span, "creds": creds}) if with_calls else []
        miss = self._cells("missed_cells", _MISSED_CELLS, span) if with_missed else []
        allc: Counter[tuple[str, bool]] = Counter()
        for c in cred:
            if keep_agent(c.row["agent"]):
                allc[c.row["kind"], c.row["hidden"]] += c.row["n"]
        for c in miss:
            allc["model", False] += c.row["n"]
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

        cells = _merge(cred, miss, lambda r: keep_agent(r["agent"]) and keep(r["kind"], r["hidden"]),
                       ("model", False) if keep("model", False) else None)  # fmt: skip

        def refine(cell: sp.Cell) -> list[sp.Cell]:
            p = {**span, "lo": cell.start, "hi": cell.end - 1}
            c2 = self._cells("cred_minutes", _CRED_MINUTES, {**p, "creds": creds}) if with_calls else []
            m2 = self._cells("missed_minutes", _MISSED_MINUTES, p) if with_missed else []
            return _merge(c2, m2, lambda r: keep_agent(r["agent"]) and keep(r["kind"], r["hidden"]),
                          ("model", False) if keep("model", False) else None)  # fmt: skip

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
        rows = self._rows(scope, creds, a, b - 1, with_calls, with_missed, hidden, kinds)
        rows = [r for r in rows if not agents_ or r.agent in agents_]
        rows.sort(key=lambda r: (r.t, r.requestId), reverse=desc)
        return hidden_count, total, counts, self._retried(scope, rows[inner : inner + size]), last

    def _rows(
        self, scope: Scope, creds: list[str], lo: int | None, hi: int | None, with_calls: bool, with_missed: bool,
        hidden: bool, kinds: list[str],
    ) -> list[RequestRow]:  # fmt: skip
        """The person's rows in [lo, hi] (None: open) that the filters keep (but agent and `q`)."""
        a = lo if lo is not None else -_FAR
        b = hi + 1 if hi is not None else _FAR
        sids = scope.sids
        calls: list[Row] = []
        missed: list[Row] = []
        if with_calls:
            calls = self.snap.dicts(
                "call_rows",
                _CALL_ROWS,
                {"creds": creds, "a": a, "b": b, "hidden": hidden, "any_kind": not kinds, "kinds": kinds},
            )
        if with_missed and (not kinds or "model" in kinds):
            missed = self.snap.dicts("missed_rows", _MISSED_ROWS, {"sids": sids, "a": a, "b": b})
        keys = sorted(
            {k for c in calls if c["kind"] == "model" for k in (c["request_id"], c["client_request_id"]) if k}
            | {k for m in missed for k in (m["request_id"], m["client_request_id"]) if k}
        )
        reports: dict[str, sp._Report] = {}
        if keys:
            for k, ttft, c5, c1 in self.snap.rows("reports", _REPORTS, {"keys": keys, "sids": sids}):
                reports[k] = sp._Report(sp.json_double(ttft), sp.json_int(c5), sp.json_int(c1))
        pids = sorted({c["prompt_id"] for c in calls if c["prompt_id"]})
        index: dict[str, int] = {}
        if pids:
            for p, n in self.snap.rows("prompt_indexes", _PROMPT_INDEXES, {"sids": sids, "pids": pids}):
                index[p] = n
        model_ids = [c["id"].replace("~", "#") for c in calls if c["kind"] == "model" and c["prompt_id"]]
        steps: dict[str, int] = {}
        if model_ids:
            steps = {
                cid.replace("#", "~"): n for cid, n in self.snap.rows("step_numbers", _STEP_NUMBERS, {"ids": model_ids})
            }
        out = []
        for c in calls:
            if c["kind"] == "model" and sp._failed(c) and not c["unnamed"] and c["scope_session_id"]:
                self._failed[c["id"]] = c
            out.append(sp.call_row(c, index, reports, steps, None))
        out += [sp.missed_row(m, m["session_id"], reports) for m in missed]
        return out

    def _retried(self, scope: Scope, rows: list[RequestRow]) -> list[RequestRow]:
        out = []
        for r in rows:
            c = self._failed.get(r.requestId)
            if c is not None and r.error is not None:
                self.sessions._failed[c["id"]] = c
                got = self.sessions._retried(c["scope_session_id"], self.credentials(scope), [r])
                r = got[0]
            out.append(r)
        return out


# --- shaping helpers ---------------------------------------------------------------------------------------


def _windows(ts: list[int]) -> list[tuple[int, int, int]]:
    """(from, to, count) windows of sorted times split at gaps of 15 minutes or more (rollups.windows)."""
    out: list[list[int]] = []
    prev = None
    for t in ts:
        if prev is None or t - prev >= cc.GAP_SPLIT_NS:
            out.append([t, t, 0])
        out[-1][1] = t
        out[-1][2] += 1
        prev = t
    return [(a, b, n) for a, b, n in out]


def _model_bins(cells: list[sp.Cell], width: int) -> list[ModelCallBin]:
    model: dict[int, Counter[str]] = defaultdict(Counter)
    known: dict[int, set[str]] = defaultdict(set)
    for c in cells:
        row = c.row
        if not (row["model_calls"] or row["missed"]):
            continue
        b = c.start - c.start % width
        m = model[b]
        for k in ("model_calls", "main_calls", "subagent_calls", "missed"):
            m[k] += row[k]
        for k in ("output_tokens", "context_sum"):
            if row[k] is not None:
                m[k] += row[k]
                known[b].add(k)
    out = []
    for b in sorted(model):
        m = model[b]
        n = m["model_calls"]
        out.append(
            ModelCallBin(
                t=dt(b),
                count=n,
                tokens=m["output_tokens"],
                byClass=CallClasses(
                    main=m["main_calls"],
                    subagent=m["subagent_calls"],
                    auxiliary=n - m["main_calls"] - m["subagent_calls"],
                ),
                tokensIn=m["context_sum"] if "context_sum" in known[b] else None,
                tokensOut=m["output_tokens"] if "output_tokens" in known[b] else None,
                notCaptured=m["missed"],
            )
        )
    return out


def _merge(
    cred: list[sp.Cell], miss: list[sp.Cell], keep: Callable[[Row], bool], missed_as: tuple[str, bool] | None
) -> list[sp.Cell]:
    """The request log's cells: per time unit (an hour inside the range, a minute at its edges), how many rows
    the filters keep (n), from the credentials' calls and the sessions' missed reports."""
    by: dict[tuple[int, int], int] = defaultdict(int)
    for c in cred:
        if keep(c.row):
            by[c.start, c.end] += c.row["n"]
    if missed_as is not None:
        for c in miss:
            by[c.start, c.end] += c.row["n"]
    return [sp.Cell(a, b, {"n": n}) for (a, b), n in sorted(by.items()) if n]
