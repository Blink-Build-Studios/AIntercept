"""Every response through the router's page-reader seam equals the oracle's on the same snapped range
(docs/PIPELINE.md v8, Testing; build step 1): each page of the API (tests/side_by_side.py PAGES) on
every CI dataset (test_v7_drawer.py's: the mixed builders and demo, every drawer shape, the spanning
batch in flight), the oracle's reader against PgStore's through one router.

`make check`: three URLs of each page on each dataset (test_v8_cuts.py walks every cut for one session
and one person; test_v8_checkpoints.py replays prefixes). The full run (`-m full`): every URL of each
page, each variant with each cut and each bin."""

import pytest

from tests import pgdb
from tests.conftest import FULL
from tests.side_by_side import BY_NAME, PAGES, assert_held, built, compare, sides
from tests.test_v7_drawer import DATASETS


def admin() -> str:
    dsn = pgdb.admin_dsn()
    if dsn is None:
        if pgdb.required():
            pytest.fail("AINTERCEPT_TEST_PG isn't set (CI's service container should set it)")
        pytest.skip("no test Postgres: set AINTERCEPT_TEST_PG (make check starts one: scripts/test-pg.sh)")
    return dsn


@pytest.fixture(scope="module")
def laid_out(tmp_path_factory):
    roots = {n: built(n, tmp_path_factory.mktemp(n.replace(" ", "-")) / "data")[0] for n in DATASETS}
    with sides(admin(), roots) as out:
        yield out


@pytest.mark.parametrize("name", [p.name for p in PAGES])
def test_each_page_through_the_seam_equals_the_oracle(laid_out, name):
    page = BY_NAME[name]
    for ds, side in laid_out.items():
        urls = side.urls(page, every=True, per_kind=3) if FULL else side.urls(page, every=False)[:3]
        assert_held(compare(side.old, side.new, urls), at=f"{name} on {ds}")
