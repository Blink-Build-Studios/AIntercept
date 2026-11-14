"""Ranges snap to whole minutes (docs/PIPELINE.md v8 §5.3, decision 1; build step 1).

The router snaps every range bound a page filters by before any reader sees it, given or default:
lo to its minute's start, hi to its minute's last ns; an open bound stays open. The meta echoes the
snapped bounds (…:00Z, …:59.999999Z). The session header and timeline don't filter by a range and
don't snap. A timeline's bin must be a multiple of 60 s.

Every reader gets these bounds from the one router (pages.py), so the oracle and v8 see the same
range: held here on the oracle's reader (a recording SqlPages), and side by side with PgStore's in
test_v8_side_by_side.py."""

import contextlib
from datetime import UTC, datetime, timedelta
from typing import Any

import pytest

from aintercept_api import claude_code as cc
from aintercept_api import pages, ranges
from tests.builders import ClaudeCode, ns
from tests.conftest import FROZEN_NOW, bounded_store, make_client
from tests.test_session import LIVE, S1, H, prompt

NS = cc.NS
US, MS = 1_000, 1_000_000
MIN = 60 * NS
B = ns("2026-11-04T09:07:00")  # a minute boundary
S2 = "22222222-2222-4222-8222-222222222222"


# --- the rule ---------------------------------------------------------------------------------------


@pytest.mark.parametrize(
    ("t", "lo", "hi"),
    [
        (B, B, B + MIN - 1),  # on a minute boundary
        (B - 1, B - MIN, B - 1),  # 1 ns either side of one
        (B + 1, B, B + MIN - 1),
        (B - US, B - MIN, B - 1),  # 1 µs
        (B + US, B, B + MIN - 1),
        (B - MS, B - MIN, B - 1),  # 1 ms
        (B + MS, B, B + MIN - 1),
        (B + 37 * NS + 123_456_789, B, B + MIN - 1),  # inside a minute
        (B + MIN - 1, B, B + MIN - 1),  # its last ns
    ],
)
def test_a_bound_snaps_to_its_minute(t, lo, hi):
    assert ranges.snap(t, t) == (lo, hi)
    assert ranges.start_of_minute(t) == lo and ranges.end_of_minute(t) == hi


def test_both_ends_in_one_minute_snap_to_that_whole_minute():
    assert ranges.snap(B + 10 * NS, B + 20 * NS) == (B, B + MIN - 1)
    # Even backwards: a range inside one minute is that minute.
    assert ranges.snap(B + 20 * NS, B + 10 * NS) == (B, B + MIN - 1)


def test_an_open_bound_stays_open():
    assert ranges.snap(None, None) == (None, None)
    assert ranges.snap(B + 5, None) == (B, None)
    assert ranges.snap(None, B + 5) == (None, B + MIN - 1)


def test_the_urls_bounds_snap_as_the_router_reads_them():
    # As FastAPI reads them: the last of a repeated one; one it rejects (no zone) as none.
    q = {"from": ["2026-11-04T09:07:00.000001Z"], "to": ["bad", "2026-11-04T09:06:59.999999+00:00"]}
    assert ranges.of_query(q) == (B, B - 1)
    assert ranges.of_query({"from": ["2026-11-04T09:07:00"]}) == (None, None)
    assert ranges.of_query({}) == (None, None)


# --- the router -------------------------------------------------------------------------------------


def _iso(t: int) -> str:
    return (datetime(1970, 1, 1, tzinfo=UTC) + timedelta(microseconds=t // US)).strftime("%Y-%m-%dT%H:%M:%S.%fZ")


def _shown(t: int) -> str:
    """As the meta shows an instant (to the µs; no fraction when it's zero)."""
    d = datetime(1970, 1, 1, tzinfo=UTC) + timedelta(microseconds=t // US)
    return d.strftime("%Y-%m-%dT%H:%M:%S") + (f".{d.microsecond:06d}" if d.microsecond else "") + "Z"


def _dataset(ds):
    """S1 ended at 09:0x (unaligned times), S2 live at 18:0x (the frozen clock is 18:05:00)."""
    a = ClaudeCode(ds, S1)
    prompt(a, f"{H}:03:17.250", "p1", 1)
    a.call(f"{H}:03:21.5", prompt="p1")
    a.event(f"{H}:04:02.75", "tengu_tool_use_progress")
    a.call(f"{H}:09:41.125", prompt="p1")
    b = ClaudeCode(ds, S2)
    prompt(b, f"{LIVE}:01:13.5", "q1", 1)
    b.call(f"{LIVE}:02:07.25", prompt="q1")


class _Recording(pages.SqlPages):
    """The oracle's reader, recording the bounds each ranged page gets."""

    def __init__(self, store: Any, seen: list[tuple[str, Any, Any]]) -> None:
        super().__init__(store)
        self.seen = seen

    def session_state(self, sid, lo, hi, req_lo, req_hi):
        self.seen += [("session/state", lo, hi), ("session/state (requests)", req_lo, req_hi)]
        return super().session_state(sid, lo, hi, req_lo, req_hi)

    def session_prompts(self, sid, now, lo, hi, *a):
        self.seen.append(("session/prompts", lo, hi))
        return super().session_prompts(sid, now, lo, hi, *a)

    def session_requests(self, sid, lo, hi, *a):
        self.seen.append(("session/requests", lo, hi))
        return super().session_requests(sid, lo, hi, *a)

    def session_events(self, sid, lo, hi, *a):
        self.seen.append(("session/events", lo, hi))
        return super().session_events(sid, lo, hi, *a)

    def person_sessions(self, pid, now, lo, hi, *a):
        self.seen.append(("person/sessions", lo, hi))
        return super().person_sessions(pid, now, lo, hi, *a)

    def person_timeline(self, sids, rs, now_dt, now, lo, hi, bin_s):
        self.seen.append(("person/timeline", lo, hi))
        return super().person_timeline(sids, rs, now_dt, now, lo, hi, bin_s)

    def person_summary(self, pid, sids, now, lo, hi):
        self.seen.append(("person/summary", lo, hi))
        return super().person_summary(pid, sids, now, lo, hi)

    def person_requests(self, pid, lo, hi, *a):
        self.seen.append(("person/requests", lo, hi))
        return super().person_requests(pid, lo, hi, *a)

    def coverage(self, pid, sid, lo, hi):
        self.seen.append(("coverage", lo, hi))
        return super().coverage(pid, sid, lo, hi)

    def people(self, now_dt, lo, hi, *a):
        self.seen.append(("people", lo, hi))
        return super().people(now_dt, lo, hi, *a)

    def overview(self, now_dt, lo, hi):
        self.seen.append(("overview", lo, hi))
        return super().overview(now_dt, lo, hi)


class _Spy:
    """A store whose pages read through _Recording."""

    def __init__(self, inner: Any) -> None:
        self.inner = inner
        self.seen: list[tuple[str, Any, Any]] = []

    def __getattr__(self, name: str) -> Any:
        return getattr(self.inner, name)

    def page_reader(self, page: str, path: str):
        return contextlib.nullcontext(_Recording(self.inner, self.seen))


@pytest.fixture
def spied(dataset):
    _dataset(dataset)
    root = dataset.write()
    store = _Spy(bounded_store(root))
    yield make_client(store), store
    store.inner.close()


RANGED = {
    "session/state": f"/api/v1/sessions/{S1}/state",
    "session/prompts": f"/api/v1/sessions/{S1}/prompts",
    "session/requests": f"/api/v1/sessions/{S1}/requests",
    "session/events": f"/api/v1/sessions/{S1}/events",
    "person/sessions": "/api/v1/people/{p}/sessions",
    "person/timeline": "/api/v1/people/{p}/timeline",
    "person/summary": "/api/v1/people/{p}/summary",
    "person/requests": "/api/v1/people/{p}/requests",
    "coverage": "/api/v1/coverage?personId={p}",
    "people": "/api/v1/people",
    "overview": "/api/v1/overview",
}
# Ends on a minute boundary, ±1 µs and ±1 ms around one, inside a minute, both in one minute (±1 ns
# can't be written in a URL: from= and to= are read to the µs).
CUTS = [
    (B, B + 3 * MIN),
    (B - US, B + 3 * MIN + US),
    (B + US, B + 3 * MIN - US),
    (B - MS, B + 3 * MIN + MS),
    (B + MS, B + 3 * MIN - MS),
    (B + 7 * NS + 250 * MS, B + 2 * MIN + 41 * NS),
    (B + 10 * NS, B + 20 * NS),
    (B + 10 * NS, None),
    (None, B + 20 * NS + 3 * US),
]


def _url(path: str, pid: str, lo: int | None, hi: int | None) -> str:
    q = "&".join(f"{k}={_iso(v)}" for k, v in (("from", lo), ("to", hi)) if v is not None)
    path = path.replace("{p}", pid)
    return path + (("&" if "?" in path else "?") + q if q else "")


def _person(client) -> str:
    return str(client.get("/api/v1/people").json()["data"][0]["personId"])


@pytest.mark.parametrize(("lo", "hi"), CUTS)
def test_every_ranged_page_hands_its_reader_the_snapped_range(spied, lo, hi):
    client, store = spied
    pid = _person(client)
    want_lo, want_hi = ranges.snap(lo, hi)
    for page, path in RANGED.items():
        store.seen.clear()
        r = client.get(_url(path, pid, lo, hi))
        assert r.status_code == 200, (page, r.text[:300])
        got = [(p, a, b) for p, a, b in store.seen if p == page]
        assert len(got) == 1, (page, store.seen)
        _, a, b = got[0]
        if lo is not None:
            assert a == want_lo, (page, a, want_lo)
        if hi is not None:
            assert b == want_hi, (page, b, want_hi)
        meta = r.json()["meta"]
        if want_lo is not None:
            assert meta["from"] == _shown(want_lo) and meta["from"].endswith(":00Z"), (page, meta)
        if want_hi is not None:
            assert meta["to"] == _shown(want_hi) and meta["to"].endswith(":59.999999Z"), (page, meta)


def test_the_requests_tabs_count_on_state_reads_the_same_snapped_range(spied):
    client, store = spied
    lo, hi = B + US, B + 3 * MIN - US
    client.get(_url(RANGED["session/state"], "", lo, hi))
    assert ("session/state (requests)", B, B + 3 * MIN - 1) in store.seen


def test_defaults_snap_too(spied):
    """A session's default range is its start to its last row (now while live); a person's timeline
    and summary default to their first activity (or 14 days back) to now (or their last row, if
    later). Each is snapped to whole minutes before any reader sees it."""
    client, store = spied
    pid = _person(client)
    now = cc.to_ns(FROZEN_NOW)
    s1_start = ns(f"{H}:03:17.250")
    for page in ("session/state", "session/prompts", "session/events"):
        store.seen.clear()
        meta = client.get(RANGED[page]).json()["meta"]
        (_, a, b), *_ = [x for x in store.seen if x[0] == page]
        assert a == ranges.start_of_minute(s1_start), page
        assert b % MIN == MIN - 1 and b >= ns(f"{H}:09:41.125"), (page, b)
        assert meta["from"] == "2026-11-04T09:03:00Z" and meta["to"].endswith(":59.999999Z"), meta
    for page in ("person/timeline", "person/summary"):
        store.seen.clear()
        meta = client.get(RANGED[page].replace("{p}", pid)).json()["meta"]
        (_, a, b), *_ = [x for x in store.seen if x[0] == page]
        assert a % MIN == 0 and b == ranges.end_of_minute(now), (page, a, b)
        assert meta["from"].endswith(":00Z") and meta["to"] == "2026-11-04T18:05:59.999999Z", meta


def test_a_live_sessions_default_end_is_nows_minute(spied):
    client, store = spied
    meta = client.get(f"/api/v1/sessions/{S2}/events").json()["meta"]
    assert meta["from"] == "2026-11-04T18:01:00Z" and meta["to"] == "2026-11-04T18:05:59.999999Z", meta


def test_an_open_default_stays_open(spied):
    # The request logs, a person's sessions, /coverage and the directory filter by a range only when
    # given one: their meta says so.
    client, store = spied
    pid = _person(client)
    for page in ("session/requests", "person/sessions", "person/requests", "coverage", "people", "overview"):
        store.seen.clear()
        meta = client.get(_url(RANGED[page], pid, None, None)).json()["meta"]
        assert [x for x in store.seen if x[0] == page] == [(page, None, None)], (page, store.seen)
        assert meta["from"] is None and meta["to"] is None, (page, meta)


def test_the_session_header_and_timeline_dont_snap(spied):
    # Their lanes cover every row: the timeline's from and to are the session's span, as stored.
    client, _ = spied
    meta = client.get(f"/api/v1/sessions/{S1}/timeline").json()["meta"]
    assert meta["from"] == "2026-11-04T09:03:17.250000Z", meta
    assert not meta["to"].endswith(":59.999999Z"), meta
    header = client.get(f"/api/v1/sessions/{S1}").json()["meta"]
    assert header.get("from") is None and header.get("to") is None, header


def test_the_person_timelines_auto_bin_comes_from_the_snapped_span(spied):
    # With no bin, from the span after snapping (§5.3). A person with no data defaults to the last 14
    # days, which snaps to 14 days and a minute less 1 ns: past the hourly bin's 14 days, so daily.
    client, _ = spied
    got = client.get("/api/v1/people/nobody/timeline").json()
    assert got["data"]["binSeconds"] == 86400, got["data"]["binSeconds"]
    assert got["meta"]["from"].endswith(":00Z") and got["meta"]["to"] == "2026-11-04T18:05:59.999999Z"


@pytest.mark.parametrize("path", [f"/api/v1/sessions/{S1}/timeline", "/api/v1/people/{p}/timeline"])
def test_a_bin_is_a_whole_number_of_minutes(spied, path):
    client, _ = spied
    path = path.replace("{p}", _person(client))
    for b in (90, 30, 61, 3601):
        r = client.get(f"{path}?bin={b}")
        assert r.status_code == 422 and r.json()["error"]["code"] == "invalid_request", (b, r.text[:300])
    for b in (60, 120, 300, 3600, 86400):
        r = client.get(f"{path}?bin={b}")
        assert r.status_code == 200 and r.json()["data"]["binSeconds"] == b, (b, r.text[:300])
