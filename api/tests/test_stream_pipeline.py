"""The streaming pipeline end to end (docs/STREAMING_PIPELINE.md §4.11, §5.4): a dataset's raw
observer files go through the live ingest's code (canon) to records, through three ingest pods
and three stream replicas with chaos (kills, lease steals, pauses, failed uploads) to the
committed bucketed tables, and the stateless API serves every endpoint from the stream state's
tables. Every response must be byte-identical to today's store over today's processor.

Not yet held to it, and reported instead (another change moves them off the raw history):
the request drawer, /status, and the directory (/people, /overview)."""

import json
import pathlib
import subprocess
from collections import Counter

import pytest

from aintercept_api.buckets import BucketStore, LocalSource
from aintercept_api.raw import LocalRaw
from tests.builders import Dataset, load_fixture
from tests.conftest import aintercept_binary, bounded_store, make_client
from tests.test_bucket_parity import _urls
from tests.test_codex import claude_code_account, codex_work
from tests.test_directory import OTHER, S1, person, unattributed

# The groups another change moves off the raw history; their responses are counted, not held.
REPORTED = ("drawer", "status", "directory")


def group(url: str) -> str:
    path = url.split("?")[0]
    if path.startswith(("/api/v1/requests/", "/api/v1/events/")):
        return "drawer"
    if path == "/api/v1/status":
        return "status"
    if path in ("/api/v1/people", "/api/v1/overview"):
        return "directory"
    if path.startswith("/api/v1/sessions/"):
        return "sessions"
    if path.startswith("/api/v1/coverage"):
        return "coverage"
    parts = path.split("/")
    if len(parts) == 5:  # /api/v1/people/{id}
        return "person"
    return "person/" + parts[5]


def stream_layout(raw: pathlib.Path, out: pathlib.Path, *, seed: int = 1) -> None:
    """Raw observer files → canonical records → 3 ingest pods and 3 replicas with chaos → out/stream/v1/."""
    subprocess.run(  # noqa: S603
        [
            str(aintercept_binary()),
            "replay",
            "-src", str(raw),
            "-processors", "3",
            "-ingest-pods", "3",
            "-steps", "60",
            "-checkpoints", "3",
            "-seed", str(seed),
            "-out", str(out),
        ],  # fmt: skip
        check=True,
        capture_output=True,
    )


@pytest.fixture(scope="module")
def clients(tmp_path_factory):
    tmp = tmp_path_factory.mktemp("pipeline")
    ds = Dataset(tmp / "data")
    load_fixture(ds, "demo")
    claude_code_account(ds)
    codex_work(ds)
    person(ds, S1, account=OTHER, hour=11, calls=3)
    unattributed(ds)
    root = ds.write()
    old = bounded_store(root)
    old.ingest()
    layout = tmp / "layout"
    stream_layout(pathlib.Path(root), layout)
    new = BucketStore(LocalSource(layout), tmp / "cache", raw=LocalRaw(pathlib.Path(root)))
    yield make_client(old), make_client(new)
    new.close()
    old.close()


def test_the_stream_layout_holds_the_states_tables(clients, tmp_path_factory):
    _, new = clients
    store = new.app.state.store
    assert store.stream_state, "the layout is the stream state's (request_txs, prompt_items, …)"


def test_every_endpoint_matches_todays_store(clients):
    old, new = clients
    urls = _urls(old)
    assert len(urls) > 100
    held: Counter[str] = Counter()
    differing: dict[str, list[str]] = {}
    reported: Counter[str] = Counter()
    for url in urls:
        g = group(url)
        a, b = old.get(url), new.get(url)
        same = b.status_code == a.status_code and json.dumps(b.json(), sort_keys=True) == json.dumps(
            a.json(), sort_keys=True
        )
        if g.split("/")[0] in REPORTED:
            reported[g + (" same" if same else " differing")] += 1
            continue
        held[g] += 1
        if not same:
            differing.setdefault(g, []).append(url)
    print("held:", dict(held), "reported:", dict(reported))  # noqa: T201 (the gate's counts, in -s output)
    assert not differing, {g: (len(u), u[:3]) for g, u in differing.items()}
