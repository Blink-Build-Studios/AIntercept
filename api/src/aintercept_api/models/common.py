"""Shared response shapes. These Pydantic models are the API contract: the UI's
TypeScript types are generated from them (docs/ui/BUILD_PLAN.md §4).

Unknown values are `None` (JSON `null`), never 0; the UI renders them as "—".
"""

from datetime import datetime
from typing import Literal

from pydantic import BaseModel, ConfigDict, Field

Agent = Literal["claude-code", "codex", "chatgpt", "claude-ai"]

CapabilityField = Literal[
    # People row and person header (handoff §11L field audit)
    "bypass",
    "missed",
    "org",
    "role",
    "usingSince",
    "limit7d",
    "flags",
    "plan",
    "limits",
    "credits",
    # Session header
    "repoBranch",
    "recap",
    "permission",
    "cost",
    "workspace",
    "cwd",
    "shell",
    "tz",
    "approval",
    "sandbox",
    "origin",
    "client",
    "reasoning",
    # Timeline lanes
    "presence",
    "focus",
    "device",
    "analytics",
    "frustrated",
    "steered",
    # Requests
    "hiddenCalls",
]
"""A field only some providers report (handoff §11L). `capabilities[product]` lists the ones
a product reports: the UI renders a provider-specific field only when it's listed there, and
never as a placeholder. Fields every provider has (sessions, prompts, tokens, model, …) aren't
listed."""

RequestKind = Literal["model", "analytics", "flags", "count", "account", "metrics", "usage", "catalog"]
"""A request's channel. Each product declares its own (`channels[product]`): Claude Code's
model, analytics, flags, count, account; Codex's model, analytics, metrics, usage (wham/*,
accounts), catalog (ps/*, codex/models)."""


UsageMissing = Literal["parseBuffer", "streamCut", "interrupted", "failed"]
"""Why a model call has no usage (handoff §11L.10, §11M): the response ran past the proxy's
512 KB parse buffer before the fix (P6), the stream ended before its final event, the turn
was interrupted, or the call failed. A prewarm isn't unknown: its output is 0."""


class Model(BaseModel):
    model_config = ConfigDict(populate_by_name=True, extra="forbid", serialize_by_alias=True)


class Meta(Model):
    asOf: datetime
    """When the query ran."""
    lastDataAt: datetime | None
    """Newest ingested row for this scope; drives the "delayed" banner."""
    from_: datetime | None = Field(default=None, alias="from")
    """The effective range start (range-scoped endpoints)."""
    to: datetime | None = None
    """The effective range end."""
    page: int | None = None
    pageSize: int | None = None
    total: int | None = None
    """Items in the whole range (list endpoints), before paging."""


class Envelope[T](Model):
    data: T
    meta: Meta


class ErrorBody(Model):
    code: str
    message: str
    queryId: str


class ErrorResponse(Model):
    error: ErrorBody


class CoverageWindow(Model):
    from_: datetime = Field(alias="from")
    to: datetime
    count: int
    cause: str | None = None


class Coverage(Model):
    """Claude Code's own report (tengu_api_success) against what the proxy captured (handoff §5.4)."""

    reported: int | None
    """Model calls Claude Code reported; None when there are no analytics for the scope (pre-parser)."""
    captured: int
    """Reported calls matched to a captured transaction (all captured calls when reported is None)."""
    notCaptured: int
    windows: list[CoverageWindow]


# --- agent-neutral extras (handoff §11J) -------------------------------------------------------


class LimitWindow(Model):
    name: str
    """"5h", "week" (Codex); "7d" (Claude Code)."""
    usedPct: float | None
    resetsAt: datetime | None


class AgentLimits(Model):
    """A product's usage limits, as its newest response reported them (handoff §11J gap 5)."""

    agent: Literal["claude-code", "codex", "chatgpt", "claude-ai"]
    plan: str | None
    windows: list[LimitWindow]
    credits: str | None
    """Codex credits: the balance, "unlimited", or None when the response didn't say."""
    asOf: datetime
