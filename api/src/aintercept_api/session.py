"""Shared step helpers: how long an unfinished call can still be streaming, and the scalar readers the
drawer applies to recorded values (Python's str, int(float(v)) and float, None when they don't apply)."""

from typing import Any

from aintercept_api import claude_code as cc

STREAMING_WITHIN_NS = 10 * 60 * cc.NS  # an unfinished call older than this never completed (aborted)


def _str(v: Any) -> str | None:
    return None if v is None or isinstance(v, dict | list) else str(v)


def _int(v: Any) -> int | None:
    try:
        return int(float(v)) if v is not None and not isinstance(v, bool) else None
    except (TypeError, ValueError):
        return None


def _float(v: Any) -> float | None:
    try:
        return float(v) if v is not None and not isinstance(v, bool) else None
    except (TypeError, ValueError):
        return None
