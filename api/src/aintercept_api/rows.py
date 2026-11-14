"""Row builders shared by the person and session routers (handoff §4 SessionRowData,
RequestRowData; §5.2.6-5.2.7), so both pages show the same row for the same thing."""

from collections.abc import Mapping, Sequence
from datetime import datetime
from http import HTTPStatus
from typing import Any

from aintercept_api import claude_code as cc
from aintercept_api import person as pp
from aintercept_api.models.common import Coverage, CoverageWindow, UsageMissing
from aintercept_api.models.people import (
    Friction,
    RequestError,
    RequestRow,
    SessionLabel,
    SessionOrigin,
    SessionRow,
    ThreadChildren,
    TokenCount,
)
from aintercept_api.store import Store
from aintercept_api.timeutil import ns_to_dt

# The observer parsed at most this much of a response before P6 (docs/INTERCEPTION_ISSUES.md):
# a longer response lost the final events that carry its usage.
PARSE_BUFFER = 512 * 1024


def usage_missing(
    output: int | None,
    status: int | None,
    stop_reason: str | None,
    ws_turn: int | None,
    bytes_down: int | None,
    hidden_kind: str | None,
) -> UsageMissing | None:
    """Why a model call has no usage (handoff §11L.10); None when it has it."""
    if output is not None or hidden_kind == "prewarm":  # a prewarm's output is 0, not missing
        return None
    if stop_reason and "interrupt" in stop_reason:
        return "interrupted"
    if status is not None and status >= 400:
        return "failed"
    if not ws_turn and bytes_down is not None and bytes_down > PARSE_BUFFER:
        return "parseBuffer"
    return "streamCut"


def dt(ns: int) -> datetime:
    d = ns_to_dt(ns)
    if d is None:  # only reachable if a caller passes None despite the type
        raise ValueError("timestamp required")
    return d


SESSION_ROWS = "SELECT *, ? - last_ns <= ? AS live FROM session_rows"


def session_rows(store: Store, now: int, ids: Sequence[str] | None = None) -> list[dict[str, Any]]:
    """The sessions' rows (every session's without `ids`), by id."""
    if ids is None:
        return store.query_dicts(f"{SESSION_ROWS} ORDER BY session_id", [now, cc.LIVE_WITHIN_NS])
    return store.query_dicts(
        f"{SESSION_ROWS} WHERE session_id IN (SELECT unnest(?::VARCHAR[])) ORDER BY session_id",
        [now, cc.LIVE_WITHIN_NS, list(ids)],
    )


def session_row_from_table(r: Mapping[str, Any], alone: bool = False) -> SessionRow:
    """A session_rows row (with `live`) as the API's SessionRow: field copies only. `alone`: the
    label the session page shows (a Codex title thread can name another thread there)."""
    return SessionRow(
        sessionId=r["session_id"],
        agent=r["agent"],
        client=r["client"],
        clientVersion=r["client_version"],
        origin=SessionOrigin.model_validate_json(r["origin_json"]) if r["origin_json"] else None,
        children=ThreadChildren.model_validate_json(r["children_json"]) if r["children_json"] else None,
        hiddenCalls=r["hidden_calls"],
        start=dt(r["start_ns"]),
        end=None if r["live"] else dt(r["last_ns"]),
        live=r["live"],
        repo=r["repo"],
        branch=None,
        commit=r["commit"],
        label=SessionLabel(
            text=r["label_text_alone" if alone else "label_text"],
            source=r["label_source_alone" if alone else "label_source"],
        ),
        prompts=r["prompts"],
        capturedFromPrompt=r["captured_from_prompt"],
        modelCalls=r["model_calls"],
        events=r["events"],
        friction=Friction(frustrated=r["frustrated"], interrupted=r["interrupted"], destructive=r["destructive"]),
        bypassPct=r["bypass_pct"],
        coverage=Coverage(
            reported=r["cov_reported"],
            captured=r["cov_captured"],
            notCaptured=r["cov_not_captured"],
            windows=[
                CoverageWindow(from_=dt(a), to=dt(b), count=n)
                for a, b, n in zip(r["cov_window_from"], r["cov_window_to"], r["cov_window_count"], strict=True)
            ],
        ),
        fieldsMissing=r["fields_missing"],
    )


def _phrase(status: int) -> str | None:
    try:
        return HTTPStatus(status).phrase
    except ValueError:  # not a registered code (e.g. Anthropic's 529)
        return None


def _tokens(t: pp.Tx, rep: pp.Reported | None) -> TokenCount | None:
    if t.kind != "model":
        return None
    return TokenCount(
        input=t.input,
        cacheRead=t.cache_read,
        cacheWrite=t.cache_write,
        cacheWrite5m=rep.cache_5m if rep else None,
        cacheWrite1h=rep.cache_1h if rep else None,
        # A prewarm produces nothing (handoff §11M part 4): its output is 0, not unknown.
        output=0 if t.output is None and t.hidden_kind == "prewarm" else t.output,
        accuracy="exact" if t.output is not None or t.hidden_kind == "prewarm" else "missing",
    )


def failed(t: pp.Tx) -> bool:
    return t.status is None and t.end_ts is not None or t.status is not None and t.status >= 400


def request_row_from_table(r: Mapping[str, Any]) -> RequestRow:
    """A row of the processor's request_rows table (columns as in aintercept/internal/process/tables.go
    RequestRow) as the API's RequestRow: field copies only. The processor wrote every value already
    as shown; status, tokens and error are spread over columns (see RequestRow there)."""
    tokens = None
    if r["tokens_accuracy"] is not None:
        tokens = TokenCount(
            input=r["tokens_input"],
            cacheRead=r["tokens_cache_read"],
            cacheWrite=r["tokens_cache_write"],
            cacheWrite5m=r["tokens_cache_write_5m"],
            cacheWrite1h=r["tokens_cache_write_1h"],
            output=r["tokens_output"],
            accuracy=r["tokens_accuracy"],
        )
    error = None
    if r["error_message"] is not None:
        error = RequestError(
            message=r["error_message"],
            afterMs=r["error_after_ms"],
            bytesReceived=r["error_bytes_received"],
            retries=r["error_retries"],
            retrySucceededAt=ns_to_dt(r["error_retry_succeeded_ns"]),
        )
    return RequestRow(
        requestId=r["request_id"],
        t=dt(r["ts_ns"]),
        kind=r["kind"],
        agent=r["agent"],
        client=r["client"],
        hiddenKind=r["hidden_kind"],
        sessionId=r["session_id"],
        sessionLabel=r["session_label"],
        promptIndex=r["prompt_index"],
        method=r["method"],
        path=r["path"],
        detail=r["detail"],
        status="ERR" if r["status_err"] else r["status"],
        durationMs=r["duration_ms"],
        retries=r["retries"],
        bytesUp=r["bytes_up"],
        bytesDown=r["bytes_down"],
        background=r["background"],
        captured=r["captured"],
        statusText=r["status_text"],
        stopReason=r["stop_reason"],
        ttftMs=r["ttft_ms"],
        tokens=tokens,
        contentEncoding=r["content_encoding"],
        error=error,
        model=r["model"],
        effort=r["effort"],
        stepIndex=r["step_index"],
        toolCalls=list(r["tool_calls"]),
        usageMissing=r["usage_missing"],
        text=r["text"],
    )


def request_filter(
    scope: str,
    value: str,
    lo_ns: int | None,
    hi_ns: int | None,
    q: str | None,
    captured: bool | None,
    agent: Sequence[str] | None = None,
) -> tuple[str, list[object]]:
    """The WHERE clause for a request log's stored rows (`scope` = `value`: a person's request_rows,
    a session's session_request_rows): the window [lo_ns, hi_ns] (each None: open; rows' times are
    compared at the microseconds they're shown with), captured, agent and `q` as `matches` reads it.
    Fixed text with every value a parameter."""
    lo = lo_ns // 1000 if lo_ns is not None else None
    hi = hi_ns // 1000 if hi_ns is not None else None
    where = [
        f"{scope} = ?",
        "(?::BIGINT IS NULL OR ts_ns // 1000 >= ?)",
        "(?::BIGINT IS NULL OR ts_ns // 1000 <= ?)",
        "(?::BOOLEAN IS NULL OR captured = ?)",
        "(? OR agent IN (SELECT unnest(?::VARCHAR[])))",
    ]
    params: list[object] = [value, lo, lo, hi, hi, captured, captured, not agent, list(agent or [])]
    if q and "=" in q:
        k, _, v = q.partition("=")
        field = {
            "kind": "kind",
            "session": "coalesce(session_id, '')",
            # str() of the shown status: a number, ERR, or None when there's none
            "status": "CASE WHEN status_err THEN 'ERR' WHEN status IS NULL THEN 'None' ELSE status::VARCHAR END",
        }.get(k.strip())
        where.append(f"contains({field}, ?)" if field else "false")
        params += [v.strip()] if field else []
    elif q:
        where.append("(contains(lower(path), ?) OR contains(lower(detail), ?))")
        params += [q.lower(), q.lower()]
    return " AND ".join(where), params
