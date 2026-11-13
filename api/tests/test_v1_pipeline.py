"""The v5 pipeline end to end (docs/PIPELINE.md): a dataset's raw observer files go through the
observer's own code to records, each key's records are applied in one pass and committed under
v1/keys/, one merge run writes v1/merge/, and the API (v1.V1Store) serves every endpoint from
those folders. Every response must be byte-identical to the v6 oracle's (tests/oracle/: today's
derivation, frozen) over the same layout, and so must every entity's API-shaped tables."""

import json
import pathlib
import subprocess
from collections import Counter

import pytest

from aintercept_api.buckets import LocalSource
from aintercept_api.raw import LocalRaw
from aintercept_api.v1 import V1Store
from tests.builders import ClaudeCode, Dataset, cid_of, load_fixture
from tests.conftest import aintercept_binary, few_per_group, lay_out, make_client
from tests.endpoints import endpoint_urls as _urls
from tests.oracle.harness import KINDS, Layout, Oracle, assert_same, entity_tables
from tests.oracle.store import OracleStore
from tests.test_codex import claude_code_account, codex_work
from tests.test_directory import OTHER, S1, person, unattributed
from tests.test_session import H, prompt
from tests.test_stream_pipeline import group, one_stream_one_id

SPAN_A, SPAN_B = "aaaaaaaa-0000-4000-8000-0000000000a1", "bbbbbbbb-0000-4000-8000-0000000000b2"


def v1_layout(raw: pathlib.Path, out: pathlib.Path) -> None:
    subprocess.run(  # noqa: S603
        [str(aintercept_binary()), "v1layout", "-src", str(raw), "-out", str(out), "-prefix", "sim"],
        check=True,
        capture_output=True,
    )


def _stores(root: pathlib.Path, layout: pathlib.Path, cache: pathlib.Path):
    """The oracle's store and today's (V1Store) over one layout, both on one DuckDB thread."""
    raw = LocalRaw(root)
    old = OracleStore(layout, prefix="sim/", raw=raw)
    new = V1Store(LocalSource(layout), cache, prefix="sim/", raw=raw, threads=1)
    return old, new


@pytest.fixture(scope="module")
def laid_out(tmp_path_factory):
    tmp = tmp_path_factory.mktemp("v1")
    ds = Dataset(tmp / "data")
    load_fixture(ds, "demo")
    claude_code_account(ds)
    codex_work(ds)
    person(ds, S1, account=OTHER, hour=11, calls=3)
    unattributed(ds)
    one_stream_one_id(ds)
    root = pathlib.Path(ds.write())
    return root, lay_out(root), tmp


@pytest.fixture(scope="module")
def clients(laid_out):
    root, layout, tmp = laid_out
    old, new = _stores(root, layout, tmp / "cache")
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


def _tables_agree(layout: pathlib.Path, today: V1Store, *, every_request: bool = True) -> Counter[str]:
    """The oracle's tables equal today's for every entity of the layout: each session, person and
    directory day, and a request id per key set (every key set, or enough to hold every key)."""
    seen: Counter[str] = Counter()
    rows: Counter[str] = Counter()
    with Oracle(Layout(layout)) as oracle:
        entities = list(oracle.entities(requests=False))
        drawers = [rids[0] for rids in oracle.request_groups().values()] if every_request else oracle.request_cover()
        entities += [("request", rid) for rid in drawers]
        for kind, ident in entities:
            want = oracle.tables(kind, ident)
            assert_same(want, entity_tables(today, kind, ident), entity=f"{kind} {ident}")
            seen[kind] += 1
            rows.update({f"{kind}.{t}": len(v.rows) for t, v in want.items()})
    print("entities:", dict(seen))  # noqa: T201 (the harness's coverage, in -s output)
    seen.update({k: 1 for k, n in rows.items() if n})
    return seen


def test_the_oracle_derives_todays_tables_for_every_entity(laid_out):
    # The oracle is a faithful copy of today's derivation: on every entity its tables are V1Store's.
    root, layout, tmp = laid_out
    _, today = _stores(root, layout, tmp / "cache-tables")
    try:
        seen = _tables_agree(layout, today, every_request=False)
    finally:
        today.close()
    assert seen["session"] >= 5 and seen["person"] >= 4 and seen["directory"] >= 2 and seen["request"] >= 4, seen
    # Not vacuous: every table holds rows for some entity (but pending: a layout's calls have all ended).
    empty = [f"{k}.{t}" for k, spec in KINDS.items() for t in spec if not seen[f"{k}.{t}"]]
    assert empty == ["request.pending"], empty


@pytest.mark.full
def test_the_oracle_derives_todays_drawer_tables_for_every_key_set(laid_out):
    root, layout, tmp = laid_out
    _, today = _stores(root, layout, tmp / "cache-drawers")
    try:
        seen = _tables_agree(layout, today, every_request=True)
    finally:
        today.close()
    assert seen["request"] >= 20, seen


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


@pytest.fixture
def spanning(tmp_path):
    """One analytics batch carrying two sessions' events: session B's tengu_api_success for B's
    model call arrives first but ranks last (its sequence number is higher), then session A's
    events, the earliest at the same time as B's. The drawer reads only B's call's keys."""
    ds = Dataset(tmp_path / "data")
    a, b = ClaudeCode(ds, SPAN_A), ClaudeCode(ds, SPAN_B)
    prompt(a, f"{H}:00:01", "pa", 1)
    prompt(b, f"{H}:00:02", "pb", 1)
    rid = b.call(f"{H}:00:09", prompt="pb")
    for i in range(4):  # B's later sequence numbers: its event ranks after A's in the batch
        b.event(f"{H}:00:0{i + 3}", "tengu_feature_ok", n=i)
    b.event(f"{H}:00:10", "tengu_api_success", batch="batch-span", cc_prompt_id="pb", requestId=rid)
    for i in range(3):
        a.event(f"{H}:00:1{i}", "tengu_feature_ok", batch="batch-span", n=i)
    root = pathlib.Path(ds.write())
    layout = lay_out(root)
    old, new = _stores(root, layout, tmp_path / "cache")
    yield make_client(old), make_client(new), cid_of(ds, rid)
    new.close()
    old.close()


def test_a_drawers_events_number_across_the_whole_batch(spanning):
    # An event's id is its rank in its whole batch, which spans sessions; the drawer reads only
    # its own call's keys, so the rank must come stored with the event, not counted from what's loaded.
    old, new, cid = spanning
    want = None
    for client in (old, new):
        listed = {e["eventId"] for e in client.get(f"/api/v1/sessions/{SPAN_B}/events").json()["data"]}
        shown = {e["eventId"] for e in client.get(f"/api/v1/requests/{cid}/details").json()["data"]["events"]}
        assert len(shown) == 1 and shown <= listed, (shown, listed)
        assert want is None or shown == want, (shown, want)
        want = shown


def test_the_oracle_derives_todays_tables_for_a_spanning_batch(spanning):
    old, new, _ = spanning
    seen = _tables_agree(old.app.state.store.source.root, new.app.state.store)
    assert seen["session"] == 2 and seen["request"] >= 2, seen


def test_an_analytics_calls_session_is_its_batchs_earliest(spanning):
    # As the request rows have it: the session the batch's earliest event names.
    old, new, _ = spanning
    for client in (old, new):
        assert client.get("/api/v1/requests/batch-span").json()["data"]["sessionId"] == SPAN_A
