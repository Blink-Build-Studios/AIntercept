"""What each coding agent reports, and the readers for what only one has (docs/harness-parity.md).

The processor tags every model call and session with its `product` (`claude_code`,
`codex`); the API speaks in agents (`claude-code`, `codex`). The common shape is the
same for both; this module holds what differs:
- `CAPABILITIES`: the provider-specific fields each product reports; the UI renders
  only these, and a field a product doesn't report is simply absent (handoff §11L).
- `CHANNELS`: each product's request channels, in chip order.
- Codex's usage limits (x-codex-* response headers), turn report (codex_turn_event)
  and session facts (its client metadata and turn events).
"""

import json
import os
from datetime import UTC, datetime
from typing import Literal

from aintercept_api.models.common import Agent, AgentLimits, CapabilityField, LimitWindow, RequestKind

AGENT: dict[str, Agent] = {"claude_code": "claude-code", "codex": "codex"}

CAPABILITIES: dict[Agent, list[CapabilityField]] = {
    "claude-code": [
        "bypass",
        "missed",
        "org",
        "role",
        "usingSince",
        "limit7d",
        "flags",
        "plan",
        "repoBranch",
        "recap",
        "permission",
        "cost",
        "presence",
        "focus",
        "device",
        "analytics",
        "frustrated",
    ],
    "codex": [
        "plan",
        "limits",
        "credits",
        "workspace",
        "cwd",
        "shell",
        "tz",
        "approval",
        "sandbox",
        "origin",
        "client",
        "reasoning",
        "steered",
        "hiddenCalls",
    ],
    "chatgpt": [],
    "claude-ai": [],
}

CHANNELS: dict[Agent, list[RequestKind]] = {
    "claude-code": ["model", "analytics", "flags", "count", "account"],
    "codex": ["model", "analytics", "metrics", "usage", "catalog"],
    "chatgpt": [],
    "claude-ai": [],
}


def org_kind(org_id: str) -> Literal["customer", "outside", "unset"]:
    """Whether a Claude Code account's org is the customer's (handoff §11L.4): CUSTOMER_ORG_IDS
    names the customer's org ids, comma separated; until it does, every org is "unset"."""
    ours = {x.strip().lower() for x in os.environ.get("CUSTOMER_ORG_IDS", "").split(",") if x.strip()}
    if not ours:
        return "unset"
    return "customer" if org_id.lower() in ours else "outside"


def agent_of(product: str | None) -> Agent:
    """Rows from before the processor tagged products are Claude Code's (the only one then)."""
    return AGENT.get(product or "", "claude-code")


def _ts(v: str | None) -> datetime | None:
    try:
        return datetime.fromtimestamp(int(v), UTC) if v else None
    except ValueError:
        return None


def _pct(v: str | None) -> float | None:
    try:
        return float(v) if v not in (None, "") else None
    except ValueError:
        return None


def _credits(v: str) -> str:
    """A credit balance as shown: whole credits with separators ("50,652")."""
    try:
        return f"{round(float(v)):,}"
    except ValueError:
        return v


def _window_name(minutes: str | None, fallback: str) -> str:
    m = {"300": "5h", "10080": "week"}.get(minutes or "")
    if m:
        return m
    try:
        n = int(minutes or "")
    except ValueError:
        return fallback
    return f"{n // 60}h" if n % 1440 else f"{n // 1440}d"


def codex_limits(provider_json: str | None, at: datetime) -> AgentLimits | None:
    """Codex's usage limits from one response's x-codex-* headers; None when it sent none."""
    h: dict[str, str] = json.loads(provider_json) if provider_json else {}
    windows = [
        LimitWindow(
            name=_window_name(h.get(f"x-codex-{w}-window-minutes"), w),
            usedPct=_pct(h.get(f"x-codex-{w}-used-percent")),
            resetsAt=_ts(h.get(f"x-codex-{w}-reset-at")),
        )
        for w in ("primary", "secondary")
        # A window the response reports with zero minutes isn't one (handoff §11L.3).
        if f"x-codex-{w}-used-percent" in h and h.get(f"x-codex-{w}-window-minutes") not in ("0", "")
    ]
    plan = h.get("x-codex-plan-type")
    if not windows and not plan:
        return None
    credits = None
    if h.get("x-codex-credits-unlimited") == "True" or h.get("x-codex-credits-unlimited") == "true":
        credits = "unlimited"
    elif h.get("x-codex-credits-balance"):
        credits = _credits(h["x-codex-credits-balance"])
    return AgentLimits(agent="codex", plan=plan, windows=windows, credits=credits, asOf=at)
