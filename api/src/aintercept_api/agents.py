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
from typing import Any, Literal, cast

from aintercept_api.models.common import Agent, AgentLimits, CapabilityField, LimitWindow, RequestKind
from aintercept_api.models.sessions import SessionFacts, TurnReport
from aintercept_api.store import Store

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


def latest_codex_limits(store: Store, session_ids: list[str]) -> AgentLimits | None:
    """The limits on the newest Codex response among these sessions."""
    if not session_ids:
        return None
    rows = store.query(
        """SELECT provider_json, ts FROM agent_calls
           WHERE product = 'codex' AND session_id IN (SELECT unnest(?::VARCHAR[]))
             AND provider_json LIKE '%x-codex-%'
           ORDER BY ts DESC LIMIT 1""",
        [session_ids],
    )
    if not rows:
        return None
    pj, ts = rows[0]
    return codex_limits(pj, datetime.fromtimestamp(ts / 1e9, UTC))


def _num(v: Any) -> float | None:
    return float(v) if isinstance(v, int | float) and not isinstance(v, bool) else None


def _int(v: Any) -> int | None:
    n = _num(v)
    return int(n) if n is not None else None


_TOOL_KINDS = {
    "shell_command_count": "shell",
    "mcp_tool_call_count": "mcp",
    "file_change_count": "fileChange",
    "web_search_count": "webSearch",
    "subagent_tool_call_count": "subagent",
    "dynamic_tool_call_count": "dynamic",
}


def turn_report(md: dict[str, Any]) -> TurnReport:
    """One codex_turn_event's params as a TurnReport."""
    return TurnReport(
        status=md.get("status") if isinstance(md.get("status"), str) else None,
        durationMs=_num(md.get("duration_ms")),
        samplingMs=_num(md.get("sampling_ms")),
        requests=_int(md.get("sampling_request_count")),
        retries=_int(md.get("sampling_retry_count")),
        toolCalls={k: n for f, k in _TOOL_KINDS.items() if (n := _int(md.get(f)))},
        steers=_int(md.get("steer_count")),
        compactionMs=_num(md.get("compaction_ms")) or None,
        trigger=md.get("turn_trigger") if isinstance(md.get("turn_trigger"), str) else None,
    )


def turn_reports(store: Store, session_id: str) -> dict[str, TurnReport]:
    """Each Codex turn's report, by turn id (the prompt id)."""
    out: dict[str, TurnReport] = {}
    for pid, md in store.query(
        """SELECT prompt_id, md::VARCHAR FROM codex_events
           WHERE session_id = ? AND name = 'codex_turn_event' AND prompt_id IS NOT NULL ORDER BY ts""",
        [session_id],
    ):
        out[pid] = turn_report(json.loads(md) if md else {})
    return out


def codex_facts(store: Store, session_id: str) -> SessionFacts | None:
    """Where a Codex thread ran: the workspace (client metadata), approval and sandbox (its turn events).
    Shell and timezone ride in the prompt's environment block, which is content: not read."""
    ext = store.query(
        """SELECT arg_max(ext_json, ts) FROM agent_calls
           WHERE session_id = ? AND product = 'codex' AND ext_json LIKE '%workspaces%'""",
        [session_id],
    )[0][0]
    ev = store.query(
        """SELECT arg_max(md::VARCHAR, ts) FROM codex_events WHERE session_id = ? AND name = 'codex_turn_event'""",
        [session_id],
    )[0][0]
    sandbox_mode = store.query(
        """SELECT arg_max(json_extract_string(ext_json, '$.sandbox_mode'), ts) FROM agent_calls
           WHERE session_id = ? AND product = 'codex'""",
        [session_id],
    )[0][0]
    ws = cast(dict[str, Any], json.loads(ext).get("workspaces") or {}) if ext else {}
    md = cast(dict[str, Any], json.loads(ev)) if ev else {}
    approval = md.get("approval_policy") if isinstance(md.get("approval_policy"), str) else None
    sandbox = sandbox_mode or (md.get("sandbox_policy") if isinstance(md.get("sandbox_policy"), str) else None)
    cwd = next(iter(ws), None)
    if cwd is None and approval is None and sandbox is None:
        return None
    return SessionFacts(cwd=cwd, shell=None, tz=None, approval=approval, sandbox=sandbox)


def workspace(store: Store, session_ids: list[str]) -> dict[str, tuple[str | None, str | None]]:
    """Each Codex session's workspace basename and short commit (the repo cell, §11J.3)."""
    if not session_ids:
        return {}
    out: dict[str, tuple[str | None, str | None]] = {}
    for sid, ext in store.query(
        """SELECT session_id, arg_max(ext_json, ts) FROM agent_calls
           WHERE product = 'codex' AND ext_json LIKE '%workspaces%' AND session_id IN (SELECT unnest(?::VARCHAR[]))
           GROUP BY 1""",
        [session_ids],
    ):
        ws = cast(dict[str, Any], json.loads(ext).get("workspaces") or {})
        path = next(iter(ws), None)
        sha = (ws.get(path) or {}).get("latest_git_commit_hash") if path else None
        out[sid] = (path.rstrip("/").rsplit("/", 1)[-1] if path else None, sha[:7] if isinstance(sha, str) else None)
    return out
