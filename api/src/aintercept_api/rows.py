"""Row builders shared by the person and session routers (handoff §4 SessionRowData,
RequestRowData; §5.2.6-5.2.7), so both pages show the same row for the same thing."""

import json
from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from datetime import datetime
from http import HTTPStatus
from typing import Any

from aintercept_api import agents
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


def tool_calls(tool_uses_json: str | None) -> list[tuple[str, str, int]]:
    """A call's tool calls, in order: (name, kind, count), from the processor's tool_uses_json."""
    try:
        uses = json.loads(tool_uses_json) if tool_uses_json else []
    except ValueError:
        return []
    return [
        (str(u.get("name") or u.get("tool") or "?"), str(u.get("kind") or ""), int(u.get("count") or 1))
        for u in uses
        if isinstance(u, dict)
    ]


def tool_labels(tool_uses_json: str | None) -> list[str]:
    """ "exec ×2", one per tool, in order."""
    return [f"{name} ×{n}" for name, _, n in tool_calls(tool_uses_json)]


def detail_of(detail_json: str | None) -> dict[str, Any]:
    """A call's detail from its recorded bodies (the processor's detail_json; full
    interception mode only): its tool calls, first words and earlier calls' results."""
    try:
        d = json.loads(detail_json) if detail_json else {}
    except ValueError:
        return {}
    return d if isinstance(d, dict) else {}


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


@dataclass
class Attempt:
    """A failed call Claude Code retried: its tengu_api_retry and the next attempt, when captured."""

    retry: pp.Retry
    next: pp.Tx | None

    @property
    def succeeded_at(self) -> datetime | None:
        n = self.next
        return dt(n.ts) if n is not None and n.status is not None and n.status < 400 else None


def attempts(txs: list[pp.Tx], retries: dict[str, list[pp.Retry]]) -> dict[str, Attempt]:
    """correlation id -> its retry, for every failed model call Claude Code retried."""
    out: dict[str, Attempt] = {}
    for i, t in enumerate(txs):
        if t.kind != "model" or not failed(t) or not t.session:
            continue
        r = pp.nearest_retry(retries.get(t.session, []), lambda x: (x.ts, x.prompt_id), t.ts, t.end_ts, t.prompt_id)
        if r is None:
            continue
        nxt = next(
            (
                x
                for x in txs[i + 1 :]
                if x.kind == "model"
                and x.session == t.session
                and x.prompt_id == t.prompt_id
                and x.request_class == t.request_class
            ),
            None,
        )
        out[t.correlation_id] = Attempt(r, nxt)
    return out


def _error(t: pp.Tx, duration_ms: float | None, attempt: Attempt | None) -> RequestError | None:
    if not failed(t):
        return None
    phrase = _phrase(t.status) if t.status is not None else None
    return RequestError(
        message=t.error or phrase or f"HTTP {t.status}",
        afterMs=duration_ms,
        bytesReceived=t.bytes_down,
        retries=attempt.retry.attempt if attempt else None,
        retrySucceededAt=attempt.succeeded_at if attempt else None,
    )


def request_row(
    t: pp.Tx,
    index: dict[str, int],
    reported: dict[str, pp.Reported],
    attempt: Attempt | None = None,
    steps: dict[str, int] | None = None,
) -> RequestRow:
    model = t.kind == "model"
    rep = (reported.get(t.request_id or "") or reported.get(t.client_request_id or "")) if model else None
    duration = round((t.end_ts - t.ts) / 1e6, 1) if t.end_ts else None
    return RequestRow(
        requestId=t.correlation_id,
        t=dt(t.ts),
        kind=t.kind,
        agent=agents.agent_of(t.product) if t.product else None,
        client=t.client,
        hiddenKind="prewarm" if t.hidden_kind == "prewarm" else "title" if t.hidden_kind == "title" else None,
        sessionId=t.session,
        sessionLabel=None,  # repo/branch arrive with M7 extraction
        promptIndex=index.get(t.prompt_id) if t.prompt_id else None,
        method=t.method,
        path=t.path,
        detail=t.detail(),
        status=t.status if t.status is not None else ("ERR" if t.end_ts is not None else None),
        durationMs=duration,
        retries=attempt.retry.attempt if attempt else None,
        bytesUp=t.bytes_up,
        bytesDown=t.bytes_down,
        background=t.background,
        captured=True,
        statusText=_phrase(t.status) if t.status is not None else t.error,
        stopReason=t.stop_reason if model else None,
        ttftMs=rep.ttft_ms if rep else None,
        tokens=_tokens(t, rep),
        contentEncoding=t.content_encoding,
        error=_error(t, duration, attempt),
        model=t.model if model else None,
        effort=t.effort if model else None,
        stepIndex=steps.get(t.correlation_id) if steps else None,
        toolCalls=tool_labels(t.tool_uses_json) if model else [],
        usageMissing=usage_missing(t.output, t.status, t.stop_reason, t.ws_turn, t.bytes_down, t.hidden_kind)
        if model
        else None,
        text=detail_of(t.detail_json).get("text") if model else None,
    )


def step_indexes(txs: list[pp.Tx]) -> dict[str, int]:
    """Each visible model call's step number within its prompt (1-based, in time order)."""
    out: dict[str, int] = {}
    seen: dict[str, int] = {}
    for t in sorted(txs, key=lambda t: (t.ts, t.correlation_id)):
        if t.kind == "model" and t.prompt_id and not t.hidden_kind:
            seen[t.prompt_id] = seen.get(t.prompt_id, 0) + 1
            out[t.correlation_id] = seen[t.prompt_id]
    return out


def request_rows(store: Store, sessions: dict[str, cc.Session], only: str | None = None) -> list[RequestRow]:
    """Every transaction made with the sessions' credentials, plus their not-captured calls (unordered);
    with `only`, just that session's transactions (filtered in the query)."""
    sids = list(sessions)
    index = pp.prompt_indexes(store, sids)
    reported = pp.reports(store, sids)
    txs = pp.transactions(store, pp.key_hashes(store, sids), only)
    tried = attempts(txs, pp.retries(store, sids))
    step_no = step_indexes(txs)
    rows = [request_row(t, index, reported, tried.get(t.correlation_id), step_no) for t in txs]
    for s in sessions.values():
        for e in cc.missed_calls(s) or []:
            rows.append(missed_row(s.id, e, reported))
    return rows


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
    from_: datetime | None,
    to: datetime | None,
    q: str | None,
    captured: bool | None,
    agent: Sequence[str] | None = None,
) -> tuple[str, list[object]]:
    """The WHERE clause for a request log's stored rows (`scope` = `value`: a person's request_rows,
    a session's session_request_rows): the window (rows' times are compared at the microseconds
    they're shown with), captured, agent and `q` as `matches` reads it. Fixed text with every value
    a parameter."""
    lo = cc.to_ns(from_) // 1000 if from_ else None
    hi = cc.to_ns(to) // 1000 if to else None
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


def missed_row(session_id: str, e: cc.Ev, reported: dict[str, pp.Reported]) -> RequestRow:
    """A call Claude Code reported that the proxy never saw: only what its tengu_api_success carries."""
    rep = reported.get(e.request_id or "") or reported.get(e.client_request_id or "")
    return RequestRow(
        requestId=f"reported:{e.request_id or e.client_request_id or e.ts}",
        t=dt(e.ts),
        kind="model",
        agent="claude-code",
        client=None,
        hiddenKind=None,
        sessionId=session_id,
        sessionLabel=None,
        promptIndex=None,
        method=None,
        path="/v1/messages",
        detail="reported by Claude Code · not captured",
        status=None,
        durationMs=None,
        retries=None,
        bytesUp=None,
        bytesDown=None,
        background=False,
        captured=False,
        statusText=None,  # no response was seen
        stopReason=None,  # analytics don't carry it
        ttftMs=rep.ttft_ms if rep else None,
        tokens=TokenCount(
            input=None,
            cacheRead=None,
            cacheWrite=None,
            cacheWrite5m=rep.cache_5m if rep else None,
            cacheWrite1h=rep.cache_1h if rep else None,
            output=None,
            accuracy="missing",
        ),
        contentEncoding=None,
        error=None,
        model=None,
        effort=None,
        stepIndex=None,
        toolCalls=[],
        usageMissing=None,
        text=None,
    )


def matches(
    r: RequestRow,
    from_: datetime | None,
    to: datetime | None,
    kinds: Sequence[str] | None,
    q: str | None,
    captured: bool | None,
) -> bool:
    if from_ and r.t < from_ or to and r.t > to:
        return False
    if kinds and r.kind not in kinds:
        return False
    if captured is not None and r.captured != captured:
        return False
    if q:
        if "=" in q:
            k, _, v = q.partition("=")
            got = {"kind": r.kind, "session": r.sessionId or "", "status": str(r.status)}.get(k.strip())
            return got is not None and v.strip() in got
        return q.lower() in r.path.lower() or q.lower() in r.detail.lower()
    return True
