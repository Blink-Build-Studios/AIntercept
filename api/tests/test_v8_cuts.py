"""Every cut (docs/PIPELINE.md v8, Testing; build step 1): each ranged page for one session and one
person, with ranges whose ends fall inside a tick run, between two events of one batch (in one session,
and across two), between two calls of one prompt, inside a presence segment, on a minute boundary and
±1 µs and ±1 ms around one, before and after all data, and open at either end; each timeline bin with
three of them, and bin 90 (a 422 on both). The oracle's reader against PgStore's through one router: both get
the same snapped range, and every response must be the oracle's bytes.

The session is the mixed dataset's busiest (its tick runs, prompts and presence); the person is the
drawer cases' busiest (its batch spanning two of its sessions). The full run walks every session and
person of every dataset (test_v8_side_by_side.py)."""

import pytest

from tests.side_by_side import BY_NAME, PAGES, assert_held, built, compare, cuts, instants, page_urls, sides
from tests.test_v8_side_by_side import admin


@pytest.fixture(scope="module")
def laid_out(tmp_path_factory):
    roots = {n: built(n, tmp_path_factory.mktemp(n.replace(" ", "-")) / "data")[0] for n in ("mixed", "drawer cases")}
    with sides(admin(), roots) as out:
        yield out


def test_the_cuts_land_where_the_spec_says(laid_out):
    """Not vacuous: the session's and the person's cuts fall in every place the spec names that their
    data has (the CI datasets' missed calls are each alone: a window of one call is its instant)."""
    session, person = laid_out["mixed"], laid_out["drawer cases"]
    kinds = set(instants(session.oracle, [session.busiest_session()]))
    kinds |= set(instants(person.oracle, person.person_sessions(person.busiest_person())))
    want = {
        "inside a tick run",
        "between a batch's events",
        "between a batch's events across sessions",
        "between a prompt's calls",
        "inside a presence segment",
        "on and around a minute boundary",
        "before all data",
        "after all data",
    }
    assert want <= kinds, want - kinds


@pytest.mark.parametrize("name", [p.name for p in PAGES if p.ranged or p.binned])
def test_every_cut_through_the_seam_equals_the_oracle(laid_out, name):
    page = BY_NAME[name]
    for ds, side in laid_out.items():
        ident = {"session": side.busiest_session, "person": side.busiest_person}.get(page.of, lambda: None)()
        if (page.of == "session") != (ds == "mixed") and page.of:
            continue  # the session from the mixed dataset, the person from the drawer cases
        rs = side.ranges_of(page, ident, per_kind=1)
        assert not page.ranged or len(rs) >= (20 if page.of else 5), (name, len(rs))
        urls = [u for u in page_urls(page, ident, rs, every=False) if "bin=" not in u]
        if page.binned:  # each bin with three of the cuts (the full run: with each)
            urls += page_urls(page, ident, rs[:3], every=False)
        assert_held(compare(side.old, side.new, urls), at=f"{name} cuts on {ds}")


def test_cuts_are_whole_ranges_and_open_ends():
    found = {"a": [5, 1], "on and around a minute boundary": [10, 9, 11]}
    rs = cuts(found)
    assert (None, None) in rs and (1, None) in rs and (None, 11) in rs and (1, 11) in rs
    assert all(lo is None or hi is None or lo <= hi for lo, hi in rs)
    assert 5 not in {x for r in rs for x in r}  # one instant per kind (a boundary's all)
