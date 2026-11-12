"""Every endpoint answers the same from the bucketed output as from today's store
(docs/STREAMING_PIPELINE.md §5.4): one dataset with the demo capture, a second Claude Code
account, Codex threads linked by email and an unattributed machine, laid out across bases,
runs and tombstones, then every endpoint compared byte for byte."""

import json
import pathlib

import pytest

from aintercept_api.buckets import BucketStore, LocalSource
from aintercept_api.raw import LocalRaw
from tests.bucket_fixture import from_store
from tests.builders import Dataset, load_fixture
from tests.conftest import bounded_store, make_client
from tests.test_codex import claude_code_account, codex_work
from tests.test_directory import OTHER, S1, person, unattributed


@pytest.fixture(scope="module")
def clients(tmp_path_factory):
    tmp = tmp_path_factory.mktemp("parity")
    ds = Dataset(tmp / "data")
    load_fixture(ds, "demo")
    claude_code_account(ds)
    codex_work(ds)
    person(ds, S1, account=OTHER, hour=11, calls=3)
    unattributed(ds)
    root = ds.write()
    old = bounded_store(root)
    old.ingest()
    layout = tmp / "bucket"
    from_store(old, layout)
    new = BucketStore(LocalSource(layout), tmp / "cache", raw=LocalRaw(pathlib.Path(root)))
    yield make_client(old), make_client(new), old
    new.close()
    old.close()


def _same(old, new, url):
    a, b = old.get(url), new.get(url)
    assert b.status_code == a.status_code, url
    assert json.dumps(b.json(), sort_keys=True) == json.dumps(a.json(), sort_keys=True), url
    return a.json()


def _urls(old) -> list[str]:
    urls = ["/api/v1/status", "/api/v1/overview", "/api/v1/people", "/api/v1/people?q=codex"]
    people = [p["id"] for p in old.get("/api/v1/people").json()["data"]] + ["unattributed", "nobody"]
    sessions: list[str] = []
    for pid in people:
        base = f"/api/v1/people/{pid}"
        urls += [
            base,
            f"{base}/summary",
            f"{base}/sessions",
            f"{base}/sessions?pageSize=1&page=2",
            f"{base}/timeline",
            f"{base}/timeline?binSeconds=60",
            f"{base}/requests",
            f"{base}/requests?pageSize=5&page=3&sort=asc",
            f"{base}/requests?q=model",
            f"/api/v1/coverage?personId={pid}",
        ]
        r = old.get(f"{base}/sessions?pageSize=100").json()
        sessions += [s["id"] for s in r.get("data", [])]
        reqs = old.get(f"{base}/requests?pageSize=3").json().get("data", [])
        for q in reqs:
            urls += [f"/api/v1/requests/{q['id']}", f"/api/v1/requests/{q['id']}/details"]
    for sid in [*sessions, "no-such-session"]:
        base = f"/api/v1/sessions/{sid}"
        urls += [
            base,
            f"{base}/timeline",
            f"{base}/state",
            f"{base}/prompts",
            f"{base}/prompts?pageSize=2&page=2",
            f"{base}/requests",
            f"{base}/events",
            f"{base}/events?q=tool&pageSize=10",
        ]
    return urls


def test_every_endpoint_matches_todays_store(clients):
    old, new, _ = clients
    urls = _urls(old)
    assert len(urls) > 100
    for url in urls:
        _same(old, new, url)
