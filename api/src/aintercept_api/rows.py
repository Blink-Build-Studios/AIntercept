"""Row builders shared by the person and session routers (handoff §4 SessionRowData,
RequestRowData; §5.2.6-5.2.7), so both pages show the same row for the same thing."""

from collections.abc import Mapping
from datetime import datetime
from http import HTTPStatus
from typing import Any

from aintercept_api.models.common import UsageMissing
from aintercept_api.models.people import (
    Friction,
    SessionLabel,
    SessionOrigin,
    SessionRow,
    ThreadChildren,
)
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
        # What only analytics fed (the first prompt number, events, frustration, destructive commands,
        # bypass, coverage) isn't kept: analytics stay in the raw files. A turn's interrupt is the wire's.
        capturedFromPrompt=None,
        modelCalls=r["model_calls"],
        events=None,
        friction=Friction(frustrated=0, interrupted=int(r["turns_interrupted"] or 0), destructive=0),
        bypassPct=None,
        coverage=None,
        fieldsMissing=r["fields_missing"],
    )


def _phrase(status: int) -> str | None:
    try:
        return HTTPStatus(status).phrase
    except ValueError:  # not a registered code (e.g. Anthropic's 529)
        return None
