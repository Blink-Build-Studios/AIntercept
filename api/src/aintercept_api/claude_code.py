"""Coding-agent sessions loaded from the cc_calls / cc_events views: what the request drawer
reads of a session, and the reference the processor's tables are tested against. Every page's
rollups are SQL over the processor's tables (rollups.py; docs/architecture.md, Decision 4),
which keep these rules.

Every agent's sessions load the same way (cc_calls holds Claude Code's and Codex's
model calls); the analytics-driven facts (coverage, friction) come from
Claude Code's events, so a Codex session simply has none (agents.NOT_REPORTED).

Rules (handoff §1.1, §5.2.5, §5.4; docs/ui/handoff/context/DATA_ANSWERS.md):
- A person is an account_uuid. A session belongs to a person when any of its
  model calls or analytics events carries that account (pre-parser calls don't,
  so their sessions are attributed through their events or not at all).
- Coverage: each tengu_api_success should match a captured call by request id,
  else by client request id. Unmatched ones are calls that bypassed the proxy,
  grouped into windows split by gaps of 15 minutes or more.
"""

from dataclasses import dataclass, field
from datetime import datetime

NS = 1_000_000_000
LIVE_WITHIN_NS = 5 * 60 * NS  # handoff §10.5: live = activity in the last 5 minutes
GAP_SPLIT_NS = 15 * 60 * NS  # coverage windows split at gaps of 15 minutes or more

_EVENT_NAMES = (
    "tengu_api_success",
    "tengu_input_prompt",
    "tengu_cancel",
    "tengu_bash_tool_command_executed",
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


@dataclass
class Ev:
    ts: int
    name: str
    device: str | None
    request_id: str | None
    client_request_id: str | None
    is_negative: bool
    destructive: str | None
    prompt_index: int | None
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


# Codex names a thread with a call in a thread of its own, made just after the named thread's
# first turn; nothing in the call links the two. So a title-only thread is matched to the same
# account's thread that started last before it, within this window.
TITLE_WITHIN_NS = 5 * 60 * NS


# --- coverage ---------------------------------------------------------------------------------


# --- per-session facts ------------------------------------------------------------------------


def prompt_index(s: Session) -> dict[str, int]:
    """cc_prompt_id -> the prompt's number, from the session's tengu_input_prompt events."""
    return {
        e.prompt_id: e.prompt_index
        for e in s.events
        if e.name == "tengu_input_prompt" and e.prompt_id and e.prompt_index is not None
    }


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
