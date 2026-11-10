from datetime import UTC, datetime, timedelta

_EPOCH = datetime(1970, 1, 1, tzinfo=UTC)


def ns_to_dt(ns: int | None) -> datetime | None:
    """Epoch nanoseconds -> UTC datetime, truncated to microseconds with integer
    math (no float rounding)."""
    return None if ns is None else _EPOCH + timedelta(microseconds=ns // 1000)
