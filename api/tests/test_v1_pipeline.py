"""The v5 pipeline end to end (docs/PIPELINE.md): a dataset's raw observer files go through the
observer's own code to records, each key's records are applied in one pass and committed under
v1/keys/, one merge run writes v1/merge/, and the API (v1.V1Store) serves every endpoint from
those folders. Every response must be byte-identical to today's store over today's processor."""

import json
import pathlib
import subprocess
from collections import Counter

import pytest

from aintercept_api.buckets import LocalSource
from aintercept_api.raw import LocalRaw
from aintercept_api.v1 import V1Store
from tests.builders import Dataset, load_fixture
from tests.conftest import aintercept_binary, bounded_store, few_per_group, make_client
from tests.test_bucket_parity import _urls
from tests.test_codex import claude_code_account, codex_work
from tests.test_directory import OTHER, S1, person, unattributed
from tests.test_stream_pipeline import group, one_stream_one_id


def v1_layout(raw: pathlib.Path, out: pathlib.Path) -> None:
    subprocess.run(  # noqa: S603
        [str(aintercept_binary()), "v1layout", "-src", str(raw), "-out", str(out), "-prefix", "sim"],
        check=True,
        capture_output=True,
    )


@pytest.fixture(scope="module")
def clients(tmp_path_factory):
    tmp = tmp_path_factory.mktemp("v1")
    ds = Dataset(tmp / "data")
    load_fixture(ds, "demo")
    claude_code_account(ds)
    codex_work(ds)
    person(ds, S1, account=OTHER, hour=11, calls=3)
    unattributed(ds)
    one_stream_one_id(ds)
    root = pathlib.Path(ds.write())
    v1_layout(root, tmp / "layout")
    old = bounded_store(root)
    old.ingest()
    new = V1Store(LocalSource(tmp / "layout"), tmp / "cache", prefix="sim/", raw=LocalRaw(root))
    yield make_client(old), make_client(new)
    new.close()
    old.close()


def test_the_layout_has_a_merge_and_key_commits(clients):
    _, new = clients
    store = new.app.state.store
    merge = store.merge()
    assert merge is not None and merge.n == 1
    assert merge.index.tables and merge.days


@pytest.mark.full
def test_every_endpoint_matches_todays_store(clients):
    old, new = clients
    urls = _urls(old)
    assert len(urls) > 100
    _hold(old, new, urls)


def test_each_endpoint_group_matches_todays_store(clients):
    old, new = clients
    urls = _urls(old)
    # The sample, and every in-flight event drawer (the one documented difference).
    _hold(old, new, few_per_group(urls, group) + [u for u in urls if u.startswith("/api/v1/events/batch-")][:2])


def _hold(old, new, urls: list[str]) -> None:
    held: Counter[str] = Counter()
    differing: dict[str, list[str]] = {}
    for url in urls:
        g = group(url)
        a, b = old.get(url), new.get(url)
        same = b.status_code == a.status_code and json.dumps(b.json(), sort_keys=True) == json.dumps(
            a.json(), sort_keys=True
        )
        held[g] += 1
        if not same:
            differing.setdefault(g, []).append(url)
    print("held:", dict(held))  # noqa: T201 (the gate's counts, in -s output)
    # The one difference docs/PIPELINE.md §4 makes: the raw view reads a call's detail object,
    # written when the call ends, so an event of a call still in flight (the builders' analytics
    # batches never end) has its fields but not yet its raw row. Nothing else may differ.
    store = new.app.state.store
    for url in differing.pop("drawer", []):
        corr = url.removeprefix("/api/v1/events/").rpartition(":")[0]
        assert url.startswith("/api/v1/events/") and not store._detail(corr), url
        a, b = old.get(url).json(), new.get(url).json()
        assert a["data"].pop("raw") and b["data"].pop("raw") == {} and a == b, url
        held["drawer (in flight: no raw row yet)"] += 1
    assert not differing, {g: (len(u), u[:3]) for g, u in differing.items()}
