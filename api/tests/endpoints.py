"""The URLs every endpoint-parity gate walks: each endpoint, for every person and session a
client serves, with the parameters the UI sends (ui/src/api/hooks/use*Page.ts, ui/src/lib/bins.ts):
the People page's hour-aligned windows, a person page's unaligned now−span `from` and its brush,
the ladder's bins, the requests' `order` and kinds (the "other" chip asks for its members), the Events
tab's internals toggle, and `q` with a range."""

import urllib.parse
from datetime import datetime, timedelta

from tests.conftest import FROZEN_NOW

# ui/src/lib/bins.ts BIN_LADDER: what the timelines' `bin` can be.
BIN_LADDER = (60, 300, 900, 3600, 10_800, 21_600, 43_200, 86_400)
_SPANS = {"24h": timedelta(hours=24), "7d": timedelta(days=7), "14d": timedelta(days=14)}


def _iso(t: datetime) -> str:
    """As the UI's Date.toISOString(): milliseconds, Z."""
    return t.strftime("%Y-%m-%dT%H:%M:%S.") + f"{t.microsecond // 1000:03d}Z"


def _qs(**params: object) -> str:
    """A query string as the UI's URLSearchParams writes one (lists repeat the key)."""
    pairs = [(k, x) for k, v in params.items() for x in (v if isinstance(v, list | tuple) else [v])]
    return "?" + urllib.parse.urlencode(pairs)


def window_from(win: str) -> str:
    """The People page's `from` for a window preset: now − span, floored to the hour (usePeoplePage.ts)."""
    t = (FROZEN_NOW - _SPANS[win]).replace(minute=0, second=0, microsecond=0)
    return _iso(t)


def person_from(win: str) -> str:
    """A person page's `from`: Date.now() − span when the query runs, so never aligned (usePersonPage.ts)."""
    return _iso(FROZEN_NOW - _SPANS[win] - timedelta(seconds=8, milliseconds=663))


# The "other" chip's kinds (ui/src/screens/SessionScreen/sessionView.ts SHARED_CHANNELS).
OTHER_KINDS = ["flags", "count", "skills", "metrics", "catalog"]

# A brush over the person timeline: arbitrary instants, ms precision.
PERSON_BRUSH = {"from": "2026-11-03T21:13:07.250Z", "to": "2026-11-04T16:41:59.875Z"}


def person_urls(pid: str) -> list[str]:
    """A person's pages at the URLs the UI reads them with."""
    base = f"/api/v1/people/{pid}"
    day, week = person_from("24h"), person_from("7d")
    return [
        base,
        f"{base}/summary",
        f"{base}/summary{_qs(**{'from': day})}",
        f"{base}/summary{_qs(**PERSON_BRUSH)}",
        f"{base}/sessions",
        f"{base}/sessions?pageSize=1&page=2",
        f"{base}/sessions{_qs(**{'from': week, 'agent': 'claude-code', 'page': 1, 'pageSize': 20})}",
        f"{base}/sessions{_qs(**PERSON_BRUSH, pageSize=20)}",
        f"{base}/timeline",
        f"{base}/timeline{_qs(**{'from': day, 'bin': 300})}",
        f"{base}/timeline{_qs(**{'from': week, 'bin': 3600})}",
        f"{base}/timeline{_qs(**{'from': person_from('14d'), 'bin': 3600})}",
        f"{base}/requests",
        f"{base}/requests?pageSize=5&page=3&order=asc",
        f"{base}/requests?q=model",
        f"{base}/requests{_qs(**{'from': day, 'pageSize': 20, 'order': 'desc'})}",
        f"{base}/requests{_qs(**PERSON_BRUSH, kind=OTHER_KINDS, pageSize=20)}",
        f"{base}/requests{_qs(**PERSON_BRUSH, q='model', pageSize=20)}",
        f"{base}/requests{_qs(agent='codex', captured='false', hidden='true', pageSize=20)}",
    ]
