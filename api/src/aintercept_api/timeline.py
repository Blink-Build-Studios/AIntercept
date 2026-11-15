"""Timeline lanes shared by the person and session timelines (handoff §4 TimelineData,
§5.2.5, §5.2.8). Every mark carries what its tooltip shows (§6B.10): tooltips never fetch."""

from collections.abc import Mapping
from typing import Any

from aintercept_api.models.people import (
    TimelineSession,
)
from aintercept_api.rows import dt

TOP_EVENTS = 3
FRICTION_DETAIL = {"frustrated": "prompt flagged negative", "interrupted": "turn interrupted"}


def band_from_table(r: Mapping[str, Any], row: int, alone: bool = False) -> TimelineSession:
    """A session_rows row (with `live`) as its timeline band: field copies only."""
    return TimelineSession(
        sessionId=r["session_id"],
        start=dt(r["start_ns"]),
        end=None if r["live"] else dt(r["last_ns"]),
        label=r["label_text_alone" if alone else "label_text"],
        row=row,
        live=r["live"],
        capturedFromPrompt=r["captured_from_prompt"],
        repo=None,  # M7 extraction
        branch=None,
        prompts=r["prompts"],
        modelCalls=r["model_calls"],
    )


BYPASS_CAUSE = (
    "Model calls went around the proxy: none was captured in this window while Claude Code's "
    "analytics were (a direct route, e.g. over IPv6)"
)
