"""The v5 pipeline end to end (docs/PIPELINE.md): a dataset's raw observer files go through the
observer's own code to records, each key's records are applied in one pass and committed under
v1/keys/, one merge run writes v1/merge/, and the API (v1.V1Store) serves every endpoint from
those folders. Every response must be byte-identical to the v6 oracle's (tests/oracle/: today's
derivation, frozen) over the same layout, and so must every entity's API-shaped tables."""

import json
import pathlib
import subprocess
import urllib.parse
from collections import Counter

import pytest

from aintercept_api.buckets import LocalSource
from aintercept_api.pgstore import PgStore
from aintercept_api.raw import LocalRaw
from aintercept_api.v1 import V1Store
from tests import pgdb
from tests.builders import ClaudeCode, Dataset, cid_of, load_fixture
from tests.conftest import FULL, aintercept_binary, few_per_group, lay_out, make_client
from tests.endpoints import endpoint_urls as _urls
from tests.oracle.harness import KINDS, REQUEST_TABLES, Layout, Oracle, Table, assert_same, entity_tables, extract
from tests.oracle.store import OracleStore
from tests.test_codex import claude_code_account, codex_work
from tests.test_directory import OTHER, S1, person, unattributed
from tests.test_session import H, prompt
from tests.test_stream_pipeline import group, one_stream_one_id
from tests.test_v6_session_rows import _without, differs_across_keys

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


# --- the Postgres store (docs/PIPELINE.md v7 build step 5) ------------------------------------------
# "new" is the API's Postgres loader over the same dataset (v1layout -pg: the v7 worker's rows), for
# the endpoints it serves so far: /status, the session pages, the drawer and /events/{id}.


@pytest.fixture(scope="module")
def pg_clients(laid_out):
    root, layout, tmp = laid_out
    admin = pgdb.admin_dsn()
    if admin is None:
        if pgdb.required():
            pytest.fail("AINTERCEPT_TEST_PG isn't set (CI's service container should set it)")
        pytest.skip("no test Postgres: set AINTERCEPT_TEST_PG (make check starts one: scripts/test-pg.sh)")
    with pgdb.fresh_database(admin) as dsn:
        lay_out(root, pg=dsn)
        old = OracleStore(layout, prefix="sim/", raw=LocalRaw(root))
        new = PgStore(dsn, source=LocalSource(layout), prefix="sim/", threads=1)
        try:
            yield make_client(old), make_client(new), dsn, layout
        finally:
            new.close()
            old.close()


@pytest.fixture(scope="module")
def pg_urls(pg_clients):
    """The gate's URLs of the endpoints the Postgres store serves, and the sessions whose pages
    carry cross-key values."""
    old, _, _, layout = pg_clients
    urls = [u for u in _urls(old) if group(u) in ("status", "sessions", "drawer")]
    with Oracle(Layout(layout)) as oracle:
        crossing = {sid for sid in oracle.session_ids() if differs_across_keys(oracle.session(sid), sid)}
    return urls, crossing


def _page(url: str) -> str:
    """A session URL's page: '' (the header), timeline, prompts, events, state, requests."""
    parts = url.split("?")[0].split("/")
    return parts[5] if len(parts) > 5 else ""


def _session_of(url: str) -> str:
    return urllib.parse.unquote(url.split("?")[0].split("/")[4])


# The pages with read-time joins: every URL in make check; three of each other page.
_JOINED_PAGES = ("", "requests", "state")


def _session_urls(urls: list[str]) -> list[str]:
    sessions = [u for u in urls if group(u) == "sessions"]
    if FULL:
        return sessions
    joined = [u for u in sessions if _page(u) in _JOINED_PAGES]
    return joined + few_per_group([u for u in sessions if _page(u) not in _JOINED_PAGES], _page)


def _differing(old, new, urls: list[str]) -> list[str]:
    out = []
    for url in urls:
        a, b = old.get(url), new.get(url)
        if b.status_code != a.status_code or json.dumps(b.json(), sort_keys=True) != json.dumps(
            a.json(), sort_keys=True
        ):
            out.append(url)
    return out


def test_the_session_pages_match_the_oracle(pg_clients, pg_urls):
    old, new, _, _ = pg_clients
    urls, crossing = pg_urls
    chosen = _session_urls(urls)
    assert len(chosen) >= 50 and crossing, (len(chosen), crossing)
    differing = _differing(old, new, chosen)
    print("session URLs held:", len(chosen) - len(differing), "of", len(chosen))  # noqa: T201
    assert not differing, (len(differing), differing[:5])


def test_the_cross_key_session_pages_match_the_oracle(pg_clients, pg_urls):
    old, new, _, _ = pg_clients
    urls, crossing = pg_urls
    chosen = [u for u in urls if group(u) == "sessions" and _session_of(u) in crossing]
    differing = _differing(old, new, chosen if FULL else few_per_group(chosen, _page, 6))
    assert not differing, (len(differing), differing[:5])


def test_status_matches_the_oracle(pg_clients):
    old, new, _, _ = pg_clients
    assert new.get("/api/v1/status").json() == old.get("/api/v1/status").json()


def test_the_drawer_and_events_match_the_oracle(pg_clients, pg_urls):
    """Every drawer URL; the one documented difference is a call still in flight, whose drawer
    404s until it ends (the owner's call)."""
    old, new, _, _ = pg_clients
    urls, _ = pg_urls
    chosen = [u for u in urls if group(u) == "drawer"]
    assert chosen
    differing = [u for u in _differing(old, new, chosen) if not _in_flight(old, new, u)]
    assert not differing, (len(differing), differing[:5])


def _in_flight(old, new, url: str) -> bool:
    a, b = old.get(url), new.get(url)
    if url.startswith("/api/v1/events/"):  # its raw row is in the detail object, written at its end
        x, y = a.json(), b.json()
        return x["data"].pop("raw", None) is not None and y["data"].pop("raw", None) == {} and x == y
    return b.status_code == 404 and a.status_code == 200 and a.json()["data"].get("status") is None


def test_every_request_and_event_ids_tables_equal_the_oracles(pg_clients):
    """The drawer's tables for every request id the logs list, and each batch's events for /events/{id}.

    The drawer reads the keys holding the call's fact rows; today's read the keys its marks name,
    which also take in a key that held only the call's start while it was in flight (its pending
    fact, which v7 doesn't store: the drawer 404s in flight). So: the call's own rows are equal, and
    every row loaded is one the oracle loads (the response-level gate holds the drawer byte for byte)."""
    _, new, _, layout = pg_clients
    store = new.app.state.store
    fewer = 0
    with Oracle(Layout(layout)) as oracle:
        every = oracle.request_ids()
        assert len(every) >= 20
        # make check: a request id per key set (every key's facts), a WebSocket turn and a reported
        # call; the full run: every one.
        kinds = [
            next((r for r in every if "~" in r), None),
            next((r for r in every if r.startswith("reported:")), None),
        ]
        rids = every if FULL else sorted({*oracle.request_cover(), *(r for r in kinds if r)})
        for rid in rids:
            loaded = store.load_for(f"/api/v1/requests/{urllib.parse.quote(rid, safe='')}")
            assert loaded is not None
            try:
                got = _stored(extract(loaded.con, REQUEST_TABLES, None))
            finally:
                loaded.con.close()
            want = _stored(oracle.request(rid))
            cid = rid.split("~", 1)[0]
            assert_same(_about(want, cid), _about(got, cid), entity=f"request {rid}", unordered=REQUEST_TABLES)
            for name, t in got.items():
                extra = Counter(map(repr, t.rows)) - Counter(map(repr, want[name].rows))
                assert not extra, f"request {rid}: {name}: {sum(extra.values())} rows the oracle doesn't load"
            fewer += any(len(got[n].rows) < len(want[n].rows) for n in got)
            oracle._memo.clear()  # a session's whole facts per id: don't keep them all
        batches = sorted(r for r in every if r.startswith("batch-"))
        assert batches
        for batch in batches if FULL else batches[:2]:
            loaded = store.load_for(f"/api/v1/events/{batch}:1")
            assert loaded is not None
            want = _about(_stored({"analytics": oracle.request(batch)["analytics"]}), batch)
            try:
                got = _stored(extract(loaded.con, {"analytics": "correlation_id = $id"}, batch))
            finally:
                loaded.con.close()
            assert want["analytics"].rows and got
            assert_same(want, got, entity=f"events of {batch}", unordered=("analytics",))
            oracle._memo.clear()
    print("request ids:", len(rids), "with a pending-only key the oracle also read:", fewer)  # noqa: T201


def _about(tables, cid: str):
    """Each table's rows of the call (its correlation id: a WebSocket connection's turns included)."""
    out = {}
    for name, t in tables.items():
        i = t.columns.index("correlation_id")
        out[name] = Table(t.columns, [r for r in t.rows if r[i] == cid])
    return out


# What Postgres doesn't store of the drawer's tables (§3.4): analytics.env_json, which no query reads
# (nor the cc_events view's env over it), and the oracle's own file names.
_UNSTORED = ("env_json", "env", "filename")


def _stored(tables):
    return {name: _without(t, _UNSTORED) for name, t in tables.items()}
