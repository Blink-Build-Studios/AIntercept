"""Coding-agent person and session rollups, from the cc_calls / cc_events views.

Every agent's sessions load the same way (cc_calls holds Claude Code's and Codex's
model calls); the analytics-driven facts (coverage, presence, friction) come from
Claude Code's events, so a Codex session simply has none (agents.NOT_REPORTED).

Rules (handoff §1.1, §5.2.5, §5.4; docs/ui/handoff/context/DATA_ANSWERS.md):
- A person is an account_uuid. A session belongs to a person when any of its
  model calls or analytics events carries that account (pre-parser calls don't,
  so their sessions are attributed through their events or not at all).
- Coverage: each tengu_api_success should match a captured call by request id,
  else by client request id. Unmatched ones are calls that bypassed the proxy,
  grouped into windows split by gaps of 15 minutes or more.
- Presence: focus is per session (tengu_return_to_session blur windows), sleep
  per device (tengu_event_loop_stall likely_sleep). At each instant: asleep if
  any device sleeps, else focused if any open session is focused, else away if
  a session is open.
"""

from collections import defaultdict
from dataclasses import dataclass, field
from datetime import datetime

from aintercept_api.store import Store

NS = 1_000_000_000
MS = 1_000_000  # nanoseconds per millisecond (blurDurationMs, clock_jump_ms)
LIVE_WITHIN_NS = 5 * 60 * NS  # handoff §10.5: live = activity in the last 5 minutes
GAP_SPLIT_NS = 15 * 60 * NS  # coverage windows split at gaps of 15 minutes or more

_EVENT_NAMES = (
    "tengu_api_success",
    "tengu_api_query",
    "tengu_input_prompt",
    "tengu_cancel",
    "tengu_bash_tool_command_executed",
    "tengu_return_to_session",
    "tengu_event_loop_stall",
)


@dataclass
class Call:
    ts: int
    prompt_id: str | None
    request_id: str | None
    client_request_id: str | None
    account: str | None
    device: str | None
    output_tokens: int | None
    request_class: str | None = None
    input_tokens: int | None = None
    cache_read: int | None = None
    cache_write: int | None = None
    product: str | None = None
    client: str | None = None
    client_version: str | None = None

    @property
    def context(self) -> int | None:
        """The context the call sent: input + cache read + cache write; None without usage."""
        parts = (self.input_tokens, self.cache_read, self.cache_write)
        return None if all(p is None for p in parts) else sum(p or 0 for p in parts)


@dataclass
class Ev:
    ts: int
    name: str
    device: str | None
    request_id: str | None
    client_request_id: str | None
    permission_mode: str | None
    is_negative: bool
    destructive: str | None
    prompt_index: int | None
    blur_ms: int | None
    sleep_ms: int | None
    prompt_id: str | None = None
    destructive_scope: str | None = None


@dataclass
class Origin:
    """A Codex thread's lineage, from the sessions snapshot."""

    kind: str  # fork, resume, subagent, review
    session_id: str | None
    turn_id: str | None
    turn: int | None
    init: str | None
    agent_name: str | None


@dataclass
class Session:
    id: str
    calls: list[Call] = field(default_factory=list)
    events: list[Ev] = field(default_factory=list)  # only the names in _EVENT_NAMES
    event_count: int = 0
    first_event: int | None = None
    last_event: int | None = None
    hidden: dict[str, int] = field(default_factory=dict)  # hidden calls (prewarm, title) by kind
    hidden_ts: list[tuple[int, str]] = field(default_factory=list)
    title: str | None = None  # Codex's generated title (its title call's answer; recorded bodies only)
    origin: Origin | None = None
    children: list[tuple[str, str]] = field(default_factory=list)  # (session id, kind) of threads it started

    @property
    def product(self) -> str | None:
        return next((c.product for c in self.calls if c.product), None)

    @property
    def client(self) -> str | None:
        return next((c.client for c in reversed(self.calls) if c.client), None)

    @property
    def client_version(self) -> str | None:
        return next((c.client_version for c in reversed(self.calls) if c.client_version), None)

    @property
    def start(self) -> int:
        return min([c.ts for c in self.calls] + ([self.first_event] if self.first_event is not None else []))

    @property
    def last(self) -> int:
        return max([c.ts for c in self.calls] + ([self.last_event] if self.last_event is not None else []))

    def live(self, now: int) -> bool:
        return now - self.last <= LIVE_WITHIN_NS

    def device(self) -> str | None:
        for x in [*(e.device for e in self.events), *(c.device for c in self.calls)]:
            if x:
                return x
        return None


def to_ns(dt: datetime) -> int:
    return int(dt.timestamp()) * NS + dt.microsecond * 1000


# The person id of the traffic no account appears on (handoff §11G.C3: /people/unattributed).
UNATTRIBUTED = "unattributed"


def linked_accounts(store: Store, person_id: str) -> list[str]:
    """The person's accounts: this one, and every account with the same email (handoff §11J.5;
    nothing weaker than an email links accounts). Sorted; the first is the person's id."""
    rows = store.query(
        """SELECT account_id FROM accounts
           WHERE email IS NOT NULL AND email <> ''
             AND lower(email) = (SELECT lower(any_value(email)) FROM accounts WHERE account_id = ?)""",
        [person_id],
    )
    return sorted({person_id, *(r[0] for r in rows)})


def email_groups(store: Store) -> dict[str, list[str]]:
    """Each account linked to others by email -> the group's accounts, sorted (only groups of 2+)."""
    groups: dict[str, list[str]] = {}
    for (accts,) in store.query(
        """SELECT list(DISTINCT account_id ORDER BY account_id) FROM accounts
           WHERE email IS NOT NULL AND email <> '' GROUP BY lower(email) HAVING count(DISTINCT account_id) > 1"""
    ):
        for a in accts:
            groups[a] = list(accts)
    return groups


def person_sessions(store: Store, accounts: list[str]) -> list[str]:
    """Every session any of the accounts appears on (a call or an event)."""
    return [
        r[0]
        for r in store.query(
            """SELECT session_id FROM cc_calls WHERE account_uuid IN (SELECT unnest(?::VARCHAR[]))
               UNION SELECT session_id FROM cc_events
               WHERE account_uuid IN (SELECT unnest(?::VARCHAR[])) AND session_id IS NOT NULL""",
            [accounts, accounts],
        )
    ]


def load_person(store: Store, person_id: str) -> dict[str, Session]:
    """Every session attributed to the person (all their linked accounts), with its calls and the
    events rollups need. `UNATTRIBUTED` loads the sessions no account id appears on, as one person-like page."""
    if person_id == UNATTRIBUTED:
        return load_sessions(store, unattributed_sessions(store))
    return load_sessions(store, person_sessions(store, linked_accounts(store, person_id)))


def unattributed_sessions(store: Store) -> list[str]:
    """Sessions with no account id on any call or event."""
    return [
        r[0]
        for r in store.query(
            """WITH known AS (
                 SELECT session_id FROM cc_calls WHERE account_uuid IS NOT NULL
                 UNION SELECT session_id FROM cc_events WHERE account_uuid IS NOT NULL AND session_id IS NOT NULL
               )
               SELECT DISTINCT session_id FROM cc_calls
               WHERE session_id IS NOT NULL AND session_id NOT IN (SELECT session_id FROM known)
               ORDER BY 1"""
        )
    ]


def load_session(store: Store, session_id: str) -> Session | None:
    """One session by id, whoever it belongs to; None when nothing carries the id.
    Once per commit (every session endpoint starts here)."""

    def build() -> Session | None:
        s = load_sessions(store, [session_id])[session_id]
        return s if s.calls or s.first_event is not None else None

    return store.memo(("session", session_id), build)


def load_sessions(store: Store, sids: list[str]) -> dict[str, Session]:
    """The sessions' calls, event counts and the events rollups need."""
    sessions = {sid: Session(sid) for sid in sids}
    if not sessions:
        return sessions
    for sid, ts, prompt, rid, crid, acct, dev, out, cls, inp, read, write, prod, client, ver in store.query(
        """SELECT session_id, ts, prompt_id, request_id, client_request_id, account_uuid, device_id, output_tokens,
                  request_class, input_tokens, cache_read_tokens, cache_write_tokens, product, client, client_version
           FROM cc_calls WHERE session_id IN (SELECT unnest(?::VARCHAR[])) ORDER BY ts""",
        [sids],
    ):
        sessions[sid].calls.append(
            Call(ts, prompt, rid, crid, acct, dev, out, cls, inp, read, write, prod, client, ver)
        )
    for sid, ts, kind, said in store.query(
        """SELECT session_id, ts, hidden_kind, json_extract_string(detail_json, '$.text') FROM hidden_calls
           WHERE session_id IN (SELECT unnest(?::VARCHAR[])) ORDER BY ts""",
        [sids],
    ):
        s = sessions[sid]
        s.hidden[kind] = s.hidden.get(kind, 0) + 1
        s.hidden_ts.append((ts, kind))
        if kind == "title" and said:
            s.title = said.strip().strip('"')
    for sid, kind, src, turn_id, turn, init, name in store.query(
        """SELECT session_id, coalesce(origin_kind, CASE WHEN init = 'cold_resume' THEN 'resume' END),
                  origin_session_id, origin_turn_id, origin_turn, init, agent_name
           FROM sessions WHERE session_id IN (SELECT unnest(?::VARCHAR[]))
             AND (origin_kind IS NOT NULL OR init = 'cold_resume')""",
        [sids],
    ):
        sessions[sid].origin = Origin(kind, src, turn_id, turn, init, name)
    for parent, sid, kind in store.query(
        """SELECT origin_session_id, session_id, origin_kind FROM sessions
           WHERE origin_kind IN ('subagent', 'review') AND origin_session_id IN (SELECT unnest(?::VARCHAR[]))
           ORDER BY first_ns, session_id""",
        [sids],
    ):
        sessions[parent].children.append((sid, kind))
    for sid, n, lo, hi in store.query(
        """SELECT session_id, count(*), min(ts), max(ts) FROM cc_events
           WHERE session_id IN (SELECT unnest(?::VARCHAR[])) GROUP BY 1""",
        [sids],
    ):
        s = sessions[sid]
        s.event_count, s.first_event, s.last_event = n, lo, hi
    for sid, ts, name, dev, rid, crid, mode, neg, destr, pidx, blur, sleep, jump, pid, scope in store.query(
        """SELECT session_id, ts, name, device_id,
                  json_extract_string(md, '$.requestId'), json_extract_string(md, '$.clientRequestId'),
                  json_extract_string(md, '$.permissionMode'), json_extract_string(md, '$.is_negative'),
                  json_extract_string(md, '$.destructive_category'),
                  TRY_CAST(json_extract_string(md, '$.prompt_index') AS INT),
                  TRY_CAST(json_extract_string(md, '$.blurDurationMs') AS BIGINT),
                  json_extract_string(md, '$.likely_sleep'),
                  TRY_CAST(json_extract_string(md, '$.clock_jump_ms') AS BIGINT),
                  json_extract_string(md, '$.cc_prompt_id'), json_extract_string(md, '$.destructive_target_scope')
           FROM cc_events
           WHERE session_id IN (SELECT unnest(?::VARCHAR[])) AND name IN (SELECT unnest(?::VARCHAR[]))
           ORDER BY ts""",
        [sids, list(_EVENT_NAMES)],
    ):
        sessions[sid].events.append(
            Ev(
                ts,
                name,
                dev,
                rid,
                crid,
                mode,
                neg == "true",
                destr if destr not in (None, "none") else None,
                pidx,
                blur,
                jump if sleep == "true" else None,
                pid,
                scope,
            )
        )
    _attach_titles(store, sessions)
    return sessions


# Codex names a thread with a call in a thread of its own, made just after the named thread's
# first turn; nothing in the call links the two. So a title-only thread is matched to the same
# account's thread that started last before it, within this window.
TITLE_WITHIN_NS = 5 * 60 * NS


def _attach_titles(store: Store, sessions: dict[str, Session]) -> None:
    """Each Codex thread's generated title (recorded bodies only), from its title thread."""
    codex = [s for s in sessions.values() if s.product == "codex" and s.title is None and s.calls]
    accounts = sorted({c.account for s in codex for c in s.calls if c.account})
    if not codex or not accounts:
        return
    for ts, acct, said in store.query(
        """SELECT ts, account_uuid, json_extract_string(detail_json, '$.text') FROM hidden_calls
           WHERE hidden_kind = 'title' AND detail_json LIKE '%"text"%'
             AND account_uuid IN (SELECT unnest(?::VARCHAR[]))
             AND session_id NOT IN (SELECT DISTINCT session_id FROM cc_calls)
           ORDER BY ts""",
        [accounts],
    ):
        named = [
            s
            for s in codex
            if s.title is None and any(c.account == acct for c in s.calls) and 0 <= ts - s.start <= TITLE_WITHIN_NS
        ]
        if named and said:
            max(named, key=lambda s: s.start).title = said.strip().strip('"')


# --- coverage ---------------------------------------------------------------------------------


def missed_calls(s: Session) -> list[Ev] | None:
    """Reported-but-not-captured calls (their tengu_api_success events); None when the session has no analytics."""
    if s.event_count == 0:
        return None
    rids = {c.request_id for c in s.calls if c.request_id}
    crids = {c.client_request_id for c in s.calls if c.client_request_id}
    return [
        e
        for e in s.events
        if e.name == "tengu_api_success" and e.request_id not in rids and e.client_request_id not in crids
    ]


def unmatched_reports(s: Session) -> list[int] | None:
    """Timestamps of reported-but-not-captured calls; None when the session has no analytics."""
    missed = missed_calls(s)
    return None if missed is None else [e.ts for e in missed]


def windows(timestamps: list[int]) -> list[tuple[int, int, int]]:
    """Group timestamps into (from, to, count) windows split by gaps >= 15 minutes."""
    out: list[list[int]] = []
    for t in sorted(timestamps):
        if out and t - out[-1][1] < GAP_SPLIT_NS:
            out[-1][1] = t
            out[-1][2] += 1
        else:
            out.append([t, t, 1])
    return [(a, b, n) for a, b, n in out]


def coverage(s: Session) -> tuple[int | None, int, int, list[tuple[int, int, int]]]:
    """(reported, captured, notCaptured, windows) for a session."""
    missing = unmatched_reports(s)
    if missing is None:
        return None, len(s.calls), 0, []
    reported = sum(1 for e in s.events if e.name == "tengu_api_success")
    return reported, reported - len(missing), len(missing), windows(missing)


# --- per-session facts ------------------------------------------------------------------------


def bypass_pct(s: Session) -> float | None:
    modes = [e.permission_mode for e in s.events if e.name == "tengu_api_query" and e.permission_mode]
    if not modes:
        return None
    return round(100 * sum(m == "bypassPermissions" for m in modes) / len(modes), 1)


def captured_from_prompt(s: Session) -> int | None:
    idx = [e.prompt_index for e in s.events if e.name == "tengu_input_prompt" and e.prompt_index is not None]
    return min(idx) if idx and min(idx) > 1 else None


def friction_counts(s: Session) -> tuple[int, int, int]:
    return (
        sum(1 for e in s.events if e.name == "tengu_input_prompt" and e.is_negative),
        sum(1 for e in s.events if e.name == "tengu_cancel"),
        sum(1 for e in s.events if e.name == "tengu_bash_tool_command_executed" and e.destructive),
    )


def friction_marks(s: Session) -> list[tuple[int, str, Ev]]:
    """(ts, kind, event) per friction incident."""
    marks = [(e.ts, "frustrated", e) for e in s.events if e.name == "tengu_input_prompt" and e.is_negative]
    marks += [(e.ts, "interrupted", e) for e in s.events if e.name == "tengu_cancel"]
    marks += [
        (e.ts, "destructive", e) for e in s.events if e.name == "tengu_bash_tool_command_executed" and e.destructive
    ]
    return marks


def prompt_ids(s: Session) -> set[str]:
    """Every prompt in the session, as the Prompts tab lists them: each typed prompt
    (tengu_input_prompt), plus prompt ids seen only on calls (typed before capture
    began). A prompt whose calls went around the proxy is still a prompt."""
    ids = {c.prompt_id for c in s.calls if c.prompt_id}
    ids |= {e.prompt_id or f"input:{e.ts}" for e in s.events if e.name == "tengu_input_prompt"}
    return ids


def prompt_index(s: Session) -> dict[str, int]:
    """cc_prompt_id -> the prompt's number, from the session's tengu_input_prompt events."""
    return {
        e.prompt_id: e.prompt_index
        for e in s.events
        if e.name == "tengu_input_prompt" and e.prompt_id and e.prompt_index is not None
    }


def unreported_calls(s: Session) -> list[Call]:
    """Captured calls with no tengu_api_success (by request id, else client request id): their cost is unknown."""
    rids = {e.request_id for e in s.events if e.name == "tengu_api_success" and e.request_id}
    crids = {e.client_request_id for e in s.events if e.name == "tengu_api_success" and e.client_request_id}
    return [c for c in s.calls if c.request_id not in rids and c.client_request_id not in crids]


# --- presence ---------------------------------------------------------------------------------


def _merge(intervals: list[tuple[int, int]]) -> list[tuple[int, int]]:
    out: list[list[int]] = []
    for a, b in sorted(intervals):
        if out and a <= out[-1][1]:
            out[-1][1] = max(out[-1][1], b)
        else:
            out.append([a, b])
    return [(a, b) for a, b in out]


def _inside(t: int, intervals: list[tuple[int, int]]) -> bool:
    return any(a <= t < b for a, b in intervals)


def presence(sessions: list[Session], now: int) -> list[tuple[int, int, str]]:
    """(from, to, state) segments, merged; empty where no session is open."""
    opened = {s.id: (s.start, now if s.live(now) else s.last) for s in sessions}
    blurred = {s.id: _merge([(e.ts - e.blur_ms * MS, e.ts) for e in s.events if e.blur_ms]) for s in sessions}
    sleep = _merge([(e.ts - e.sleep_ms * MS, e.ts) for s in sessions for e in s.events if e.sleep_ms])
    cuts = sorted(
        {t for a, b in opened.values() for t in (a, b)}
        | {t for iv in blurred.values() for a, b in iv for t in (a, b)}
        | {t for a, b in sleep for t in (a, b)}
    )
    segs: list[tuple[int, int, str]] = []
    for a, b in zip(cuts, cuts[1:], strict=False):
        if a == b:
            continue
        open_ids = [sid for sid, (lo, hi) in opened.items() if lo <= a < hi]
        if not open_ids:
            continue
        if _inside(a, sleep):
            state = "asleep"
        elif any(not _inside(a, blurred[sid]) for sid in open_ids):
            state = "focused"
        else:
            state = "away"
        if segs and segs[-1][2] == state and segs[-1][1] == a:
            segs[-1] = (segs[-1][0], b, state)
        else:
            segs.append((a, b, state))
    return segs


def pack_rows(spans: list[tuple[str, int, int]]) -> dict[str, int]:
    """Greedy row packing: each session takes the lowest row free at its start."""
    rows: list[int] = []  # end of the last span in each row
    out: dict[str, int] = {}
    for sid, start, end in sorted(spans, key=lambda x: (x[1], x[0])):
        for i, last_end in enumerate(rows):
            if last_end <= start:
                rows[i] = end
                out[sid] = i
                break
        else:
            rows.append(end)
            out[sid] = len(rows) - 1
    return out


def auto_bin_seconds(span_ns: int) -> int:
    """Handoff §5.2.5: 5 min up to 24 h, 1 h up to 14 days, else 1 day."""
    if span_ns <= 24 * 3600 * NS:
        return 300
    if span_ns <= 14 * 86400 * NS:
        return 3600
    return 86400


def bin_start(ts: int, bin_s: int) -> int:
    return ts - ts % (bin_s * NS)


def group(items: list[tuple[int, int]], bin_s: int) -> list[tuple[int, int, int]]:
    """(ts, value) -> sorted (bin, count, sum of values)."""
    acc: dict[int, list[int]] = defaultdict(lambda: [0, 0])
    for ts, v in items:
        b = acc[bin_start(ts, bin_s)]
        b[0] += 1
        b[1] += v
    return [(t, n, v) for t, (n, v) in sorted(acc.items())]
