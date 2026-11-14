"""Ranges snap to whole minutes (docs/PIPELINE.md v8 §5.3, decision 1).

The router snaps every range bound a page filters by before any reader sees it, in ns, whether the URL
gave it or it's the page's default:
- lo to its minute's start: lo − lo mod 60 s;
- hi to its minute's last ns: hi − hi mod 60 s + 60 s − 1.

A bound the page leaves open stays open. Each endpoint's own predicate then keeps exactly the rows in
[L, H), L the snapped lo and H the snapped hi + 1 ns: whole minutes, so v8's reads merge whole
cells. The oracle (today's SQL) gets the same snapped bounds from the same router, so each response
is today's code's for that range.

A response's meta echoes the snapped bounds: `from` at its minute's start (…:00Z), `to` at its
minute's last µs (…:59.999999Z), since times are shown to the µs.

Pages that don't filter by a range don't snap: the session header and timeline (the timeline's lanes
cover every row; its from and to are the session's span).

A timeline's `bin` must be a multiple of 60 s (BIN_MULTIPLE_S), so each minute lies in one bin."""

from datetime import datetime

import pydantic

from aintercept_api import claude_code as cc

MINUTE_NS = 60 * cc.NS
BIN_MULTIPLE_S = 60


def start_of_minute(ns: int) -> int:
    """The start of the minute ns lies in."""
    return ns - ns % MINUTE_NS


def end_of_minute(ns: int) -> int:
    """The last ns of the minute ns lies in."""
    return start_of_minute(ns) + MINUTE_NS - 1


def snap(lo: int | None, hi: int | None) -> tuple[int | None, int | None]:
    """A range's bounds snapped outward to whole minutes; an open bound stays open."""
    return (None if lo is None else start_of_minute(lo), None if hi is None else end_of_minute(hi))


def given(from_: datetime | None, to: datetime | None) -> tuple[int | None, int | None]:
    """A URL's from= and to= in ns, snapped; each None when absent (the page's open default)."""
    return snap(cc.to_ns(from_) if from_ else None, cc.to_ns(to) if to else None)


_INSTANT = pydantic.TypeAdapter(pydantic.AwareDatetime)


def instant(value: str | None) -> int | None:
    """A from= or to= in ns as the endpoint parses it (FastAPI's AwareDatetime); None for none, or one
    the endpoint rejects."""
    if not value:
        return None
    try:
        return cc.to_ns(_INSTANT.validate_python(value))
    except pydantic.ValidationError:
        return None


def of_query(query: dict[str, list[str]]) -> tuple[int | None, int | None]:
    """A URL's from= and to= (each its last, as the endpoint reads a repeated one), snapped: the range
    the router will hand the page's reader (a loader pushing it down reads exactly it)."""
    lo, hi = (instant(query[k][-1]) if query.get(k) else None for k in ("from", "to"))
    return snap(lo, hi)
