"""Replay checkpoints (docs/PIPELINE.md v8, Testing; build step 1): a dataset's prefixes, cut at fixed
points and mid-batch, mid-prompt and mid-tick-run (running sessions, prompts in flight, batches half
there), each laid out by the v7 worker into a database of its own and settled (no flag left, render
flags included), against the oracle over the same prefix: with `now` at the checkpoint, at each
threshold ±1 µs (live, streaming, in flight; the API's clock is whole µs), and a second before the
newest row (a client clock ahead). The oracle's reader against PgStore's through one router.

`make check`: one checkpoint of each small dataset, the first clock of each kind, a URL of each page
(each threshold's: its session's and person's pages). The full run: every checkpoint of every dataset
(the mixed one too), every clock, every page's URLs."""

import json
from typing import Any

import pytest

from tests import pgdb
from tests.side_by_side import (
    PAGES,
    Side,
    _in_flight,
    assert_held,
    built,
    checkpoints,
    compare,
    nows,
    page_urls,
    prefix,
    walk,
)
from tests.test_v7_drawer import DATASETS
from tests.test_v8_side_by_side import admin


def _urls_at(side: Side, sids: list[str], *, every: bool) -> list[str]:
    """A clock's URLs: the pages of the sessions it decides and of their people, or (none named) every
    page; each with no range (make check: its first URL)."""
    people = [p for p in side.oracle.person_ids() if set(side.person_sessions(p)) & set(sids)]
    out: list[str] = []
    for page in PAGES:
        if sids and page.of not in ("session", "person"):
            continue
        idents = {"session": sids or side.sessions(), "person": people or side.people()}.get(page.of)
        for ident in (idents if every or sids else idents[:1]) if idents is not None else [None]:
            got = side.urls(page, every=False) if ident is None else page_urls(page, ident, [], every=False)
            out += got if every else got[:1]
    return list(dict.fromkeys(out))


# make check's checkpoint of each small dataset (the mixed one is the full run's).
_CHECKED = {"drawer cases": "mid-prompt", "spanning in flight": "mid-batch"}


@pytest.mark.parametrize(
    ("dataset", "every"),
    [pytest.param(d, False, id=f"{d}-subset") for d in _CHECKED]
    + [pytest.param(d, True, id=f"{d}-every", marks=pytest.mark.full) for d in DATASETS],
)
def test_replay_checkpoints_equal_the_oracle_at_each_threshold(tmp_path, dataset, every):
    dsn_admin = admin()
    _, rows = built(dataset, tmp_path / "data")
    found = checkpoints(rows)
    if not every:
        found = {k: v for k, v in found.items() if k == _CHECKED[dataset]}
    assert found, dataset
    kinds: set[str] = set()
    for label, t in found.items():
        with pgdb.fresh_database(dsn_admin) as dsn:
            side = Side(prefix(rows, t, tmp_path / label.replace(" ", "-")), dsn)
            try:
                for kind, now, sids in walk(nows(side.oracle, t), every=every):
                    old, new = side.at(now)
                    urls = _urls_at(side, sids, every=every)
                    assert_held(compare(old, new, urls), at=f"{dataset} {label}, {kind} ({now})")
                    kinds.add(kind)
            finally:
                side.close()
    # Not vacuous: the clocks straddle the thresholds the prefix has.
    assert {"at the checkpoint", "live", "a client clock ahead"} <= kinds, kinds


def test_checkpoints_cut_where_the_spec_says(tmp_path):
    kinds: set[str] = set()
    for name in DATASETS:
        _, rows = built(name, tmp_path / name.replace(" ", "-"))
        kinds |= set(checkpoints(rows))
    assert {"a quarter in", "halfway", "mid-batch", "mid-prompt", "mid-tick-run"} <= kinds, kinds


class _Resp:
    def __init__(self, status: int, body: dict) -> None:
        self.status_code, self.body = status, body

    def json(self) -> Any:
        return json.loads(json.dumps(self.body))


class _Client:
    """Each call's summary: X is in flight (no duration; v7 has no row), Y has ended."""

    def __init__(self, new: bool) -> None:
        self.new = new

    def get(self, url: str) -> _Resp:
        rid = url.rsplit("/", 1)[-1]
        if rid == "X":
            return _Resp(404, {}) if self.new else _Resp(200, {"data": {"durationMs": None, "status": 200}})
        return _Resp(200, {"data": {"durationMs": 5.0, "status": 200}})


def test_the_in_flight_exception_is_narrow():
    """Only what names a call in flight may differ: a link to it (null in v7's row), the fields beside
    it, a step count that counts it; and its own drawer's 404."""
    old, new = _Client(False), _Client(True)
    url = "/api/v1/requests/A"

    def held(x: dict, y: dict) -> bool:
        return _in_flight(old, new, url, _Resp(200, {"data": x}), _Resp(200, {"data": y}))

    base = {"t": "1", "nextRequestId": "X", "stepCount": 4, "tool": [{"note": "X", "v": "step 9"}]}
    assert held(base, {**base, "nextRequestId": None, "stepCount": 3, "tool": [{"note": None, "v": None}]})
    assert not held(base, {**base, "nextRequestId": None, "t": "2"})  # anything else differs
    assert not held({**base, "nextRequestId": "Y"}, {**base, "nextRequestId": None})  # Y has ended
    assert not held(base, {**base, "stepCount": 5})  # counts more, not fewer
    assert not held(base, {**base, "tool": [{"note": "X", "v": "step 8"}]})  # beside it, but not left out
    assert _in_flight(old, new, "/api/v1/requests/X/details", _Resp(200, {}), _Resp(404, {}))
    assert not _in_flight(old, new, "/api/v1/requests/Y/details", _Resp(200, {}), _Resp(404, {}))
