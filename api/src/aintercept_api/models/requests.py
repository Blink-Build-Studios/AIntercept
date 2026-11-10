"""Request drawer (handoff §5.2.10, §6.6) and the expanded event (§5.2.9).

Numbers stay numbers: the UI formats them (handoff §3.3), so a section row's
value comes with its unit, and a content row's size with what it counts.
"""

from datetime import datetime
from typing import Any, Literal

from aintercept_api.models.common import Envelope, Model, UsageMissing
from aintercept_api.models.people import Cost, TokenCount
from aintercept_api.models.sessions import EventRow

DrawerKind = Literal["normal", "notCaptured", "failed"]
Channel = Literal["model", "analytics", "flags", "count", "account", "skills", "metrics", "usage", "catalog"]
Unit = Literal["tokens", "bytes", "ms", "usd", "pct", "chars"]


class KeyStrip(Model):
    model: str | None
    effort: str | None
    thinking: bool | None
    tokens: TokenCount
    cachedPct: float | None
    """Share of the input read from cache, 0-100."""
    cost: Cost
    ttftMs: float | None
    """Claude Code's own time to first token (tengu_api_success)."""
    proxyTtftMs: float | None
    """Request headers to the first response byte, as the proxy saw them."""
    ttftSource: Literal["client", "proxy"] | None
    """Where the TTFT tile's value comes from (handoff §11L.10): the agent's own report, else
    the proxy's timing; None when neither exists."""
    reasoningTokens: int | None
    """Codex: reasoning tokens in the output."""
    usageMissing: UsageMissing | None
    """Why the call has no usage; None when it has it."""
    streamEvents: int | None
    stopReason: str | None
    toolCalls: list[str]
    """Tools the response asked to run, in order ("Edit", "Bash ×2")."""


class CrossCheck(Model):
    matched: bool
    """Claude Code reported this call (tengu_api_success by requestId, else clientRequestId)."""
    costAgrees: bool | None
    """costUSD against usage × price: None while aintercept keeps no price table (BUILD_PLAN §10)."""


class Notice(Model):
    tone: Literal["danger", "neutral"]
    title: str
    body: str


class RetryInfo(Model):
    """A failed attempt Claude Code retried (tengu_api_retry, joined by session, prompt and time)."""

    attempt: int
    of: int | None
    """Attempts in all, when the last one is known."""
    backoffMs: float | None
    nextRequestId: str | None
    nextStatus: int | Literal["ERR"] | None
    nextAt: datetime | None


class ContentRow(Model):
    label: str
    """What it is: "tool_result · Read", "text", "usage"."""
    detail: str
    size: int | None
    unit: Literal["bytes", "tokens", "chars"] | None
    tone: Literal["tool"] | None = None
    body: str | None = None
    """The content itself: only when content is retained (experimentation mode)."""


class RequestDetail(Model):
    requestId: str
    kind: DrawerKind
    t: datetime
    method: str | None
    path: str
    status: int | Literal["ERR"] | None
    durationMs: float | None
    bytesUp: int | None
    bytesDown: int | None
    sessionId: str | None
    promptIndex: int | None
    stepIndex: int | None
    """1-based position among the prompt's model calls."""
    stepCount: int | None
    requestClass: str | None
    prevRequestId: str | None
    nextRequestId: str | None
    keyStrip: KeyStrip
    crossCheck: CrossCheck
    notice: Notice | None
    retry: RetryInfo | None
    sent: list[ContentRow]
    received: list[ContentRow]
    contentRetained: bool
    """Bodies were kept for this request (the observer ran with full-body capture)."""


class RequestResponse(Envelope[RequestDetail]):
    """One request, captured or reported only (`reported:` ids)."""


# --- raw (`/raw`) -------------------------------------------------------------------------------


class HeaderEntry(Model):
    name: str
    value: str | None
    """None when redacted: credentials are never served."""
    redacted: bool


class StreamEvent(Model):
    tOffsetMs: float | None
    """From the request's start."""
    type: str
    detail: str
    count: int | None
    """Consecutive events of the same type folded into this one."""


class RequestRaw(Model):
    unavailable: str | None
    """Why there's no wire data (or no response), when there isn't."""
    requestLine: str | None
    requestHeaders: list[HeaderEntry]
    requestBody: str | None
    """Only when content is retained."""
    responseLine: str | None
    responseHeaders: list[HeaderEntry]
    responseBody: str | None
    """A non-streamed response body, only when content is retained."""
    stream: list[StreamEvent] | None
    """Server-sent events, only when content is retained; None otherwise."""
    keyHash: str | None
    """The credential's salted hash, shown in place of the redacted auth header."""
    contentRetained: bool


class RequestRawResponse(Envelope[RequestRaw]):
    """Headers (credentials redacted server-side), body and stream of one request."""


# --- details (`/details`) -----------------------------------------------------------------------


class DetailRow(Model):
    k: str
    v: str | int | float | bool | None
    """None = unknown ("—")."""
    unit: Unit | None = None
    tone: Literal["muted", "ok"] | None = None
    note: str | None = None
    """Why a value is unknown, or what qualifies it."""


class DetailSection(Model):
    title: str
    accent: Channel | None
    rows: list[DetailRow]


class ToolCallDetail(Model):
    toolUseId: str | None
    name: str
    mcpServer: str | None
    input: list[DetailRow]
    """From the response's tool_use block: only when content is retained."""
    execution: list[DetailRow]
    """From Claude Code's analytics."""
    result: list[DetailRow]
    """Where the result went: the next request."""


class TimingPhase(Model):
    label: str
    startMs: float | None
    """None: the proxy doesn't record this phase (accept + TLS, upstream connect)."""
    durMs: float | None


class Timing(Model):
    proxy: list[TimingPhase]
    """From the proxy's phase timestamps (headers, body chunks, completion)."""
    client: list[DetailRow]
    """Claude Code's own timings (tengu_api_success)."""


class FieldEntry(Model):
    key: str
    value: str | None
    """None: not on rows yet."""


class RequestDetails(Model):
    composition: TokenCount
    sections: list[DetailSection]
    """The 8 sections, in order (handoff §5.2.10)."""
    tool: list[ToolCallDetail]
    timing: Timing
    events: list[EventRow]
    fields: list[FieldEntry]
    """Every field on the request's captured rows, flattened (content excluded)."""


class RequestDetailsResponse(Envelope[RequestDetails]):
    """Everything known about one request, for the All details tab."""


# --- one event (`/events/{eventId}`) ------------------------------------------------------------


class EventDetail(Model):
    eventId: str
    t: datetime
    name: str
    sessionId: str | None
    raw: dict[str, Any]
    """The client event as Claude Code sent it."""


class EventResponse(Envelope[EventDetail]):
    """One analytics event in full (the EventRow expansion)."""
