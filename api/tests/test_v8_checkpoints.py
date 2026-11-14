"""Replay checkpoints (docs/PIPELINE.md v8, Testing; build step 1): a dataset's prefixes, cut at fixed
points and mid-batch, mid-prompt and mid-tick-run (running sessions, prompts in flight, batches half
there), each laid out by the v7 worker into a database of its own and settled (no flag left, render
flags included), against the oracle over the same prefix: with `now` at the checkpoint, at each
threshold ±1 µs (live, streaming, in flight; the API's clock is whole µs), and a second before the
newest row (a client clock ahead). The oracle's reader against PgStore's through one router.

`make check`: one checkpoint of each small dataset, the first clock of each kind, a URL of each page
(each threshold's: its session's and person's pages). The full run: every checkpoint of every dataset
(the mixed one too), every clock, every page's URLs."""

import pytest

from tests import pgdb
from tests.side_by_side import PAGES, Side, assert_held, built, checkpoints, compare, nows, page_urls, prefix, walk
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
