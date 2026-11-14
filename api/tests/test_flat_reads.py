"""What a request reads stays flat as history grows (docs/PIPELINE.md §4).

The drawer reads its call's keys (their marks, calls/<id>.keys/, name them), /status reads the merge
commit, and the directory reads the merge's day partitions in its window plus the
index. None of them reads a day partition outside its window or a key that isn't theirs, so the
same request against 8x the history reads the same objects."""

import pathlib
from collections import Counter
from typing import Any

import pytest

from aintercept_api.buckets import LocalSource
from aintercept_api.raw import LocalRaw
from aintercept_api.v1 import V1Store
from tests.builders import Dataset, load_fixture
from tests.conftest import make_client
from tests.test_directory import person
from tests.test_v1_pipeline import v1_layout


class CountingSource(LocalSource):
    """A LocalSource that counts what's read: GETs, LISTs and the files DuckDB opens."""

    def __init__(self, root: pathlib.Path) -> None:
        super().__init__(root)
        self.reads: Counter[str] = Counter()
        self.files: list[str] = []

    def reset(self) -> None:
        self.reads.clear()
        self.files.clear()

    def get(self, key: str) -> bytes:
        self.reads["get"] += 1
        return super().get(key)

    def list_after(self, prefix: str, start_after: str) -> list[str]:
        self.reads["list"] += 1
        return super().list_after(prefix, start_after)

    def url(self, key: str) -> str:
        self.reads["file"] += 1
        self.files.append(key)
        return super().url(key)


OLD_DAYS = [f"2026-10-{d:02d}" for d in range(1, 29)]


def _layout(tmp: pathlib.Path, history: int) -> tuple[pathlib.Path, pathlib.Path]:
    """The demo dataset plus `history` x 7 sessions of older traffic, each on its own day."""
    ds = Dataset(tmp / "data")
    load_fixture(ds, "demo")
    for i in range(history * 7):
        sid = f"{i:08d}-0000-4000-8000-00000000f1a7"
        person(ds, sid, day=OLD_DAYS[i % len(OLD_DAYS)], hour=8 + i % 8, calls=2)
    root = pathlib.Path(ds.write())
    v1_layout(root, tmp / "layout")
    return root, tmp / "layout"


@pytest.fixture(scope="module")
def stores(tmp_path_factory):
    out = {}
    made: list[V1Store] = []
    for x in (1, 8):
        tmp = tmp_path_factory.mktemp(f"flat{x}")
        root, layout = _layout(tmp, x)

        def fresh(tmp=tmp, root=root, layout=layout):
            # A new store for each measurement: nothing cached from an earlier request.
            src = CountingSource(layout)
            # A frozen clock: freshness relists at most once a second, so on a slow runner a
            # request spanning a second would list once more, which says nothing about history.
            store = V1Store(src, tmp / f"cache{len(made)}", prefix="sim/", raw=LocalRaw(root), clock=lambda: 0.0)
            made.append(store)
            return src, store

        out[x] = fresh
    yield out
    for store in made:
        store.close()


def _reads(stores, x: int, url: str) -> tuple[Counter[str], int, Any]:
    src, store = stores[x]()
    client = make_client(store)
    src.reset()
    r = client.get(url)
    assert r.status_code == 200, (url, r.status_code, r.text[:300])
    return Counter(src.reads), len(src.files), r.json()


def _a_request_id(stores) -> str:
    _, store = stores[1]()
    client = make_client(store)
    people = client.get("/api/v1/people").json()["data"]
    for p in people:
        for q in client.get(f"/api/v1/people/{p['personId']}/requests?pageSize=5").json()["data"]:
            if not q["requestId"].startswith("reported:"):
                return str(q["requestId"])
    raise AssertionError("no captured request in the demo dataset")


def test_status_reads_no_table(stores):
    for x in (1, 8):
        reads, files, _ = _reads(stores, x, "/api/v1/status")
        assert files == 0, (x, reads)


def test_drawer_reads_flat_in_history(stores):
    rid = _a_request_id(stores)
    for path in (f"/api/v1/requests/{rid}", f"/api/v1/requests/{rid}/details"):
        r1, f1, b1 = _reads(stores, 1, path)
        r8, f8, b8 = _reads(stores, 8, path)
        assert (r8, f8) == (r1, f1), (path, r1, f1, r8, f8)
        assert b8 == b1


def test_directory_reads_flat_in_history(stores):
    # The UI's windows: a recent from=, so older days are outside it.
    for path in ("/api/v1/people?from=2026-11-01T00:00:00Z", "/api/v1/overview?from=2026-11-01T00:00:00Z"):
        r1, f1, b1 = _reads(stores, 1, path)
        r8, f8, b8 = _reads(stores, 8, path)
        assert f8 == f1, (path, f1, f8)
        assert b8["data"] == b1["data"], path


def test_directory_reads_no_fact_table(stores):
    """The directory reads the merge's per-session rows and index, never the raw fact tables:
    /overview counts requests from the merge's per-key call times (merge_agent_calls)."""
    for path in ("/api/v1/people", "/api/v1/overview", "/api/v1/overview?from=2026-11-01T00:00:00Z"):
        src, store = stores[8]()
        make_client(store).get(path)
        facts = [f for f in src.files if f.rsplit("/", 1)[-1].startswith("fact_")]
        assert not facts, (path, facts[:3])
