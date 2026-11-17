"""Shared constants and helpers of the coding-agent pages: time units, liveness, the unattributed
person, timeline bins and band packing."""

from datetime import datetime

NS = 1_000_000_000
LIVE_WITHIN_NS = 5 * 60 * NS  # handoff §10.5: live = activity in the last 5 minutes


def to_ns(dt: datetime) -> int:
    return int(dt.timestamp()) * NS + dt.microsecond * 1000


# The person id of the traffic no account appears on (handoff §11G.C3: /people/unattributed).
UNATTRIBUTED = "unattributed"


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
