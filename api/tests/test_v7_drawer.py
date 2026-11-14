"""The request drawer, precomputed by the worker (docs/PIPELINE.md v7 §3.7), side by side with the
oracle: today's request_detail.py over the oracle's tables, byte for byte.

For each dataset, `aintercept v1layout -pg` runs the v7 worker into a fresh Postgres: every key
renders its drawer rows (internal/drawer, the Go port of request_detail.py). The API's Postgres store
serves a drawer page from its one row; the oracle derives it at request time. Every response must be
the same bytes:
- every request id (the request logs' ids, WebSocket turns and `reported:` ids among them, and every
  id a key rendered), its /requests/{id}, /details and /raw;
- every event id (every session's events, and every one a key rendered), its /events/{id}.

The one documented difference (the owner's call, §5): a call still in flight has no row, so its
drawer 404s until it ends, and an event of a batch still in flight has its fields but not yet its raw
row (the batch's detail object is written when it ends).

`make check` holds a representative subset of each dataset (a request id per key set, one of each
kind, and a few events per batch); the full run (AINTERCEPT_TESTS_FULL=1) holds every one.

An ended analytics batch spanning sessions is here too: its sessions' Requests tabs count the whole
batch (the sum of every key's batch_parts, read at render time and kept fresh across keys)."""

import contextlib
import pathlib
import urllib.parse
from collections import Counter
from collections.abc import Iterator

import duckdb
import pytest

from aintercept_api.buckets import LocalSource
from aintercept_api.pgstore import PgStore
from aintercept_api.raw import LocalRaw
from tests import pgdb
from tests.builders import INSTANCE, ClaudeCode, Dataset, load_fixture, ns
from tests.conftest import lay_out, make_client
from tests.oracle.harness import Layout, Oracle
from tests.oracle.store import OracleStore
from tests.test_codex import claude_code_account, codex_work
from tests.test_directory import OTHER, S1, person, unattributed
from tests.test_requests import failed_then_retried, retained
from tests.test_session import H, prompt
from tests.test_stream_pipeline import one_stream_one_id

SPAN_A, SPAN_B = "aaaaaaaa-0000-4000-8000-0000000000a1", "bbbbbbbb-0000-4000-8000-0000000000b2"
S2 = "22222222-2222-4222-8222-222222222222"


def ended_spanning_batch(ds: Dataset, batch: str = "batch-span-ended") -> None:
    """An analytics batch carrying two sessions' events, A's the earliest, that ends (its response
    and complete rows): the call is A's; B holds four of its five events."""
    a, b = ClaudeCode(ds, SPAN_A), ClaudeCode(ds, SPAN_B)
    prompt(a, f"{H}:00:01", "pa", 1)
    prompt(b, f"{H}:00:02", "pb", 1)
    rid = b.call(f"{H}:00:02", prompt="pb")
    a.call(f"{H}:00:02", prompt="pa")
    a.event(f"{H}:00:03", "tengu_feature_ok", batch=batch, n=0)
    for i in range(3):
        b.event(f"{H}:00:0{i + 4}", "tengu_feature_ok", batch=batch, n=i)
    b.event(f"{H}:00:08", "tengu_api_success", batch=batch, cc_prompt_id="pb", requestId=rid)
    base = {"correlation_id": batch, "observer_instance": INSTANCE, "host": "api.anthropic.com", "method": "POST",
            "path": "/api/event_logging/v2/batch", "synthetic": False}  # fmt: skip
    ds.add({**base, "seq": 998, "timestamp_ns": ns(f"{H}:00:40"), "phase": "response_headers", "status": 200,
            "direction": "outbound", "headers": {"request-id": "req_batch_ended"}})  # fmt: skip
    ds.add({**base, "seq": 999, "timestamp_ns": ns(f"{H}:00:41"), "phase": "complete", "status": 200,
            "end_of_stream": True, "key_hash": a.key_hash})  # fmt: skip


def drawer_cases(ds: Dataset) -> None:
    """Every drawer shape on one dataset: a call with retained bodies (its tool's input, a token to
    redact, an SSE response) and the call carrying its result; a failed attempt and its retry; a
    client gone before any response; a call whose request headers are out of reach; calls only
    Claude Code reported; an ended batch spanning sessions; Codex turns on a WebSocket."""
    retained(ds)
    failed_then_retried(ds)
    gone = ds.transaction(f"{H}:05:00")
    ds.rows[:] = [r for r in ds.rows if not (r["correlation_id"] == gone and r["phase"] != "request_headers")]
    start = next(r["timestamp_ns"] for r in ds.rows if r["correlation_id"] == gone)
    ds.add({"correlation_id": gone, "seq": 1, "timestamp_ns": start + 400_000_000, "phase": "disconnect",
            "error": "client disconnected", "observer_instance": INSTANCE})  # fmt: skip
    reach = ds.transaction(f"{H}:06:00")
    ds.rows[:] = [r for r in ds.rows if not (r["correlation_id"] == reach and r["phase"] == "request_headers")]
    other = ClaudeCode(ds, S2)
    prompt(other, f"{H}:07:00", "q1", 1)
    other.call(f"{H}:07:05", prompt="q1")
    other.reported(f"{H}:07:10", "req_bypass_1", cc_prompt_id="q1", inputTokens=12, outputTokens=40, costUSD="0.02")
    other.reported(f"{H}:07:20", "req_bypass_2", cc_prompt_id="q1")
    ended_spanning_batch(ds)
    claude_code_account(ds)
    codex_work(ds)
    one_stream_one_id(ds)


def mixed(ds: Dataset) -> None:
    """test_v1_pipeline's dataset: the demo fixture and the builders' people, Codex threads included."""
    load_fixture(ds, "demo")
    claude_code_account(ds)
    codex_work(ds)
    person(ds, S1, account=OTHER, hour=11, calls=3)
    unattributed(ds)
    one_stream_one_id(ds)


def spanning_in_flight(ds: Dataset) -> None:
    """test_v1_pipeline's spanning batch, which never ends: its events rank across sessions."""
    a, b = ClaudeCode(ds, SPAN_A), ClaudeCode(ds, SPAN_B)
    prompt(a, f"{H}:00:01", "pa", 1)
    prompt(b, f"{H}:00:02", "pb", 1)
    rid = b.call(f"{H}:00:09", prompt="pb")
    for i in range(4):
        b.event(f"{H}:00:0{i + 3}", "tengu_feature_ok", n=i)
    b.event(f"{H}:00:10", "tengu_api_success", batch="batch-span", cc_prompt_id="pb", requestId=rid)
    for i in range(3):
        a.event(f"{H}:00:1{i}", "tengu_feature_ok", batch="batch-span", n=i)


DATASETS = {"drawer cases": drawer_cases, "mixed": mixed, "spanning in flight": spanning_in_flight}


class Side:
    """One dataset laid out, and the two clients over it: the oracle's and the Postgres store's."""

    def __init__(self, name: str, layout: pathlib.Path, root: pathlib.Path, dsn: str) -> None:
        self.name, self.layout, self.dsn = name, layout, dsn
        self.old_store = OracleStore(layout, prefix="sim/", raw=LocalRaw(root))
        self.store = PgStore(dsn, source=LocalSource(layout), prefix="sim/", threads=1, trace=True)
        self.old, self.new = make_client(self.old_store), make_client(self.store)

    def close(self) -> None:
        self.store.close()
        self.old_store.close()

    def rendered(self, sql: str) -> list[str]:
        """The ids a query over the worker's rows lists."""
        con, conn = duckdb.connect(), pgdb.connect(self.dsn)
        try:
            return sorted({r[0] for r in pgdb.load(con, conn, "ids", sql).fetchall()})
        finally:
            conn.close()
            con.close()


@pytest.fixture(scope="module")
def sides(tmp_path_factory) -> Iterator[dict[str, Side]]:
    admin = pgdb.admin_dsn()
    if admin is None:
        if pgdb.required():
            pytest.fail("AINTERCEPT_TEST_PG isn't set (CI's service container should set it)")
        pytest.skip("no test Postgres: set AINTERCEPT_TEST_PG (make check starts one: scripts/test-pg.sh)")
    tmp = tmp_path_factory.mktemp("v7drawer")
    with contextlib.ExitStack() as stack:
        out: dict[str, Side] = {}
        for name, build in DATASETS.items():
            ds = Dataset(tmp / name.replace(" ", "-") / "data")
            build(ds)
            root = pathlib.Path(ds.write())
            dsn = stack.enter_context(pgdb.fresh_database(admin))
            out[name] = Side(name, lay_out(root, pg=dsn), root, dsn)
            stack.callback(out[name].close)
        yield out


def _q(rid: str) -> str:
    return urllib.parse.quote(rid, safe="")


def _in_flight(side: Side, url: str, a, b) -> bool:
    """The documented difference: a call in flight 404s; an in-flight batch's event has no raw row yet."""
    if url.startswith("/api/v1/events/"):
        if a.status_code != 200 or b.status_code != 200:
            return False
        x, y = a.json(), b.json()
        corr = url.removeprefix("/api/v1/events/").rpartition(":")[0]
        gone = not side.store._detail(urllib.parse.unquote(corr))
        return gone and x["data"].pop("raw", None) is not None and y["data"].pop("raw", None) == {} and x == y
    return b.status_code == 404 and a.status_code == 200 and a.json()["data"].get("status") is None


def _hold(side: Side, urls: list[str]) -> Counter[str]:
    """Each URL's two responses are the same bytes (or the documented in-flight difference)."""
    held: Counter[str] = Counter()
    differing = []
    for url in urls:
        a, b = side.old.get(url), side.new.get(url)
        if a.status_code == b.status_code and a.content == b.content:
            held["same"] += 1
        elif _in_flight(side, url, a, b):
            held["in flight"] += 1
        else:
            differing.append((url, a.status_code, b.status_code, a.text[:400], b.text[:400]))
    assert not differing, (side.name, len(differing), differing[:3])
    return held


def request_ids(side: Side) -> list[str]:
    """Every request id: the logs' (the oracle's) and every one the worker rendered."""
    with Oracle(Layout(side.layout)) as oracle:
        listed = set(oracle.request_ids())
    return sorted(listed | set(side.rendered("SELECT request_id FROM drawer_requests")))


def event_ids(side: Side) -> list[str]:
    """Every event id: each session's events (the oracle's) and every one the worker rendered."""
    with Oracle(Layout(side.layout)) as oracle:
        listed = set()
        for sid in oracle.session_ids():
            t = oracle.session(sid)["event_rows"]
            listed |= {r[t.columns.index("event_id")] for r in t.rows}
            oracle._memo.clear()
    return sorted(listed | set(side.rendered("SELECT event_id FROM drawer_events")))


def _subset(rids: list[str]) -> list[str]:
    """A request id of each kind (a turn, a reported call, a batch, a model call; a failed, retained,
    gone or out-of-reach call where the dataset has one) and every tenth."""
    picks = {
        next((r for r in rids if "~" in r), None),
        next((r for r in rids if r.startswith("reported:")), None),
        next((r for r in rids if r.startswith("batch-")), None),
    }
    return sorted({r for r in picks if r} | set(rids[::10]) | {r for r in rids if r.startswith("observer-test-0-")})


def _event_subset(eids: list[str]) -> list[str]:
    by_batch: dict[str, list[str]] = {}
    for e in eids:
        by_batch.setdefault(e.rpartition(":")[0], []).append(e)
    return sorted({e for es in by_batch.values() for e in es[:2]} | set(eids[::10]))


def _drawer_urls(rids: list[str]) -> list[str]:
    return [f"/api/v1/requests/{_q(r)}{sub}" for r in rids for sub in ("", "/details", "/raw")]


def _hold_drawers(side: Side, *, every: bool) -> None:
    rids, eids = request_ids(side), event_ids(side)
    assert rids and eids, side.name
    chosen, events = (rids, eids) if every else (_subset(rids), _event_subset(eids))
    held = _hold(side, _drawer_urls(chosen))
    held += _hold(side, [f"/api/v1/events/{_q(e)}" for e in events])
    print(f"{side.name}: request ids {len(chosen)} of {len(rids)}, events {len(events)} of {len(eids)}:", dict(held))  # noqa: T201
    assert held["same"] >= 3 * len(chosen) // 2, held


@pytest.mark.parametrize("name", list(DATASETS))
def test_the_drawers_match_the_oracle(sides, name):
    """make check: a request id of each kind and every tenth, a few events per batch."""
    _hold_drawers(sides[name], every=False)


@pytest.mark.full
@pytest.mark.parametrize("name", list(DATASETS))
def test_every_drawer_matches_the_oracle(sides, name):
    """The full run: every request id's /requests/{id}, /details and /raw, every event's /events/{id}."""
    _hold_drawers(sides[name], every=True)


def test_the_cases_are_all_there(sides):
    # Not vacuous: the drawer dataset holds every kind of drawer, and the worker rendered each.
    side = sides["drawer cases"]
    rendered = set(side.rendered("SELECT request_id FROM drawer_requests"))
    kinds = {
        "turn": any("~" in r for r in rendered),
        "reported": any(r.startswith("reported:") for r in rendered),
        "batch": "batch-span-ended" in rendered,
    }
    assert all(kinds.values()), kinds
    shapes: Counter[str] = Counter()
    for rid in sorted(rendered):
        d = side.new.get(f"/api/v1/requests/{_q(rid)}").json()["data"]
        shapes[d["kind"]] += 1
        shapes["retained"] += d["contentRetained"]
        shapes["retry"] += d["retry"] is not None
        shapes["stepped"] += d["stepIndex"] is not None
        shapes["matched"] += d["crossCheck"]["matched"]
    wanted = ("normal", "failed", "notCaptured", "retained", "retry", "stepped", "matched")
    assert all(shapes[k] for k in wanted), shapes


def test_a_drawer_page_is_one_statement(sides):
    # One primary-key lookup (with /status's values, the global lastDataAt), whatever the call.
    side = sides["drawer cases"]
    rid = next(r for r in request_ids(side) if r.startswith("reported:"))
    for url in (f"/api/v1/requests/{_q(rid)}", f"/api/v1/requests/{_q(rid)}/details", "/api/v1/events/nope:1"):
        side.store.trace.clear()
        side.new.get(url)
        (load,) = side.store.trace
        assert load.connections == 1 and len(load.statements) == 1, (url, [s.name for s in load.statements])


def test_an_ended_batch_spanning_sessions_counts_the_whole_batch(sides):
    """Its call is A's (A holds its earliest event); B holds four of its five events. A's Requests tab
    shows "5 events", as the oracle does: the render sums every key's batch_parts, not just its own
    key's (it showed "1 events")."""
    side = sides["drawer cases"]
    for sid in (SPAN_A, SPAN_B):
        for q in ("", "?kind=analytics", "?order=desc&pageSize=20"):
            url = f"/api/v1/sessions/{sid}/requests{q}"
            a, b = side.old.get(url), side.new.get(url)
            assert a.status_code == b.status_code == 200 and a.content == b.content, (url, a.text[:600], b.text[:600])
    rows = side.new.get(f"/api/v1/sessions/{SPAN_A}/requests?kind=analytics").json()["data"]
    assert [r["detail"] for r in rows if r["requestId"] == "batch-span-ended"] == ["5 events"], rows
    d = side.new.get("/api/v1/requests/batch-span-ended").json()["data"]
    assert d["sessionId"] == SPAN_A and d["status"] == 200, d
