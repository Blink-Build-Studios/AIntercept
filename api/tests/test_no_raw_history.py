"""No endpoint reads raw history (docs/STREAMING_PIPELINE.md §4.9, the §2.3 inventory): a
request's cost follows what it shows, never how much is stored.

- /status reads no table at all: each bucket's newest commit carries the stats it shows.
- The directory (/people, /overview) and /coverage read the dimension tables and per-session
  summary rows, never the raw call and event tables (or the snapshots built from them).
- The objects a request reads don't grow as history does.

The request drawer still reads the raw tables, behind its explicit flag, until it moves onto
the per-call detail objects and per-key outputs: that test is marked, strictly, as not yet true."""

import json
import pathlib

import pytest

from aintercept_api.buckets import RAW_TABLES, BucketStore, LocalSource
from aintercept_api.raw import LocalRaw
from tests.bucket_fixture import from_store
from tests.builders import Dataset, load_fixture
from tests.conftest import bounded_store, make_client
from tests.test_codex import claude_code_account, codex_work
from tests.test_directory import OTHER, S1, person, unattributed

_DIRECTORY = ["/api/v1/people", "/api/v1/people?q=codex", "/api/v1/overview"]


def _layout(tmp: pathlib.Path, calls: int) -> tuple[pathlib.Path, pathlib.Path, object]:
    ds = Dataset(tmp / "data")
    load_fixture(ds, "demo")
    claude_code_account(ds)
    codex_work(ds)
    person(ds, S1, account=OTHER, hour=11, calls=calls)
    unattributed(ds)
    root = ds.write()
    old = bounded_store(root)
    old.ingest()
    layout = tmp / "bucket"
    from_store(old, layout)
    return pathlib.Path(root), layout, old


@pytest.fixture(scope="module")
def stores(tmp_path_factory):
    tmp = tmp_path_factory.mktemp("noraw")
    root, layout, old = _layout(tmp, 3)
    new = BucketStore(LocalSource(layout), tmp / "cache", raw=LocalRaw(root))
    yield make_client(old), make_client(new), new, (root, layout, tmp)
    new.close()
    old.close()  # type: ignore[attr-defined]


def _get(client, store, url):
    store.reads.clear()
    store.tables_read.clear()
    r = client.get(url)
    assert r.status_code < 500, url
    return r


# --- atomic: each endpoint group's reads ---------------------------------------------------------


def test_status_reads_no_table(stores):
    old, new, store, _ = stores
    r = _get(new, store, "/api/v1/status")
    assert not store.tables_read, store.tables_read
    assert not [k for k in store.reads if "/tables/" in k], store.reads
    assert json.dumps(r.json()["data"], sort_keys=True) == json.dumps(
        old.get("/api/v1/status").json()["data"], sort_keys=True
    )


@pytest.mark.parametrize("url", _DIRECTORY)
def test_directory_reads_no_raw_table(stores, url):
    old, new, store, _ = stores
    r = _get(new, store, url)
    assert not (store.tables_read & RAW_TABLES), (url, sorted(store.tables_read & RAW_TABLES))
    assert json.dumps(r.json(), sort_keys=True) == json.dumps(old.get(url).json(), sort_keys=True)


def test_coverage_reads_one_person(stores):
    old, new, store, _ = stores
    pid = old.get("/api/v1/people").json()["data"][0]["personId"]
    url = f"/api/v1/coverage?personId={pid}"
    r = _get(new, store, url)
    assert not (store.tables_read & RAW_TABLES), sorted(store.tables_read & RAW_TABLES)
    assert json.dumps(r.json(), sort_keys=True) == json.dumps(old.get(url).json(), sort_keys=True)


@pytest.mark.xfail(strict=True, reason="the drawer reads the raw tables until it moves onto per-call objects (§4.9)")
def test_drawer_reads_no_raw_table(stores):
    old, new, store, _ = stores
    pid = old.get("/api/v1/people").json()["data"][0]["personId"]
    rid = old.get(f"/api/v1/people/{pid}/requests?pageSize=1").json()["data"][0]["requestId"]
    _get(new, store, f"/api/v1/requests/{rid}")
    assert not (store.tables_read & RAW_TABLES), sorted(store.tables_read & RAW_TABLES)


# --- flat: objects read don't grow with history ---------------------------------------------------


def _reads(tmp: pathlib.Path, calls: int, url: str) -> int:
    root, layout, old = _layout(tmp, calls)
    store = BucketStore(LocalSource(layout), tmp / "cache", raw=LocalRaw(root))
    try:
        r = make_client(store).get(url)
        assert r.status_code == 200, url
        return len(store.reads)
    finally:
        store.close()
        old.close()  # type: ignore[attr-defined]


@pytest.mark.parametrize("url", ["/api/v1/status", *_DIRECTORY])
def test_objects_read_stay_flat_as_history_grows(tmp_path, url):
    one = _reads(tmp_path / "x1", 3, url)
    eight = _reads(tmp_path / "x8", 24, url)
    assert eight <= one, (url, one, eight)


# --- the gate: every endpoint but the drawer -------------------------------------------------------


def test_no_endpoint_but_the_drawer_reads_a_raw_table(stores):
    from tests.test_bucket_parity import _urls

    old, new, store, _ = stores
    urls = [u for u in _urls(old) if not u.startswith(("/api/v1/requests/", "/api/v1/events/"))]
    assert len(urls) > 80
    for url in urls:
        _get(new, store, url)
        assert not (store.tables_read & RAW_TABLES), (url, sorted(store.tables_read & RAW_TABLES))
