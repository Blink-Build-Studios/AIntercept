"""The request drawer (docs/PIPELINE.md §6.2): rendered when it opens, from the call's `calls` row, one
ranged GET of its raw member and a few indexed lookups.

For each dataset, a fresh Postgres holds its facts and rollups, loaded from raw files as the workers load
them (conftest.PgDataset). Every kind of drawer is drawn (a WebSocket turn from its row, `reported:` ids,
an ended batch spanning sessions), and what a drawer reads stays a few rows and at most one ranged GET,
whatever the session's size, through an index."""

import contextlib
import pathlib
import urllib.parse
from collections import Counter
from collections.abc import Iterator
from typing import Any

import psycopg
import pytest

from aintercept_api import drawer
from tests.builders import INSTANCE, ClaudeCode, Dataset, load_fixture, ns, one_stream_one_id
from tests.conftest import PgDataset, make_client, pg_admin
from tests.test_codex import claude_code_account, codex_work
from tests.test_directory import OTHER, S1, person, unattributed
from tests.test_requests import failed_then_retried, retained
from tests.test_session import H, prompt

SPAN_A, SPAN_B = "aaaaaaaa-0000-4000-8000-0000000000a1", "bbbbbbbb-0000-4000-8000-0000000000b2"
S2 = "22222222-2222-4222-8222-222222222222"
S3 = "33333333-3333-4333-8333-333333333333"


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


def second_prompt_tool(ds: Dataset) -> None:
    """A tool call in a session's second prompt: its result is delivered in the prompt's step 2, the
    session's third call (difference 7)."""
    cc = ClaudeCode(ds, S3)
    prompt(cc, f"{H}:08:00", "r1", 1)
    cc.call(f"{H}:08:05", prompt="r1")
    prompt(cc, f"{H}:08:10", "r2", 2)
    rid = cc.call(f"{H}:08:15", prompt="r2", stop_reason="tool_use")
    cc.event(f"{H}:08:16", "tengu_tool_use_success", cc_prompt_id="r2", requestId=rid, toolName="Read",
             toolUseID="toolu_r2")  # fmt: skip
    cc.call(f"{H}:08:20", prompt="r2")


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
    second_prompt_tool(ds)
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


def big_prompt(ds: Dataset) -> None:
    """One prompt of 120 steps, each with its report and a tool and its Bash event, in a session of a
    few hundred events: what a drawer reads of it must not grow with them."""
    cc = ClaudeCode(ds, S2)
    prompt(cc, f"{H}:00:01", "big", 1)
    for i in range(120):
        at = f"{H}:{10 + i // 60:02d}:{i % 60:02d}"
        rid = cc.call(at, prompt="big")
        cc.event(at, "tengu_api_success", cc_prompt_id="big", requestId=rid, costUSD="0.01")
        cc.event(at, "tengu_tool_use_success", cc_prompt_id="big", requestId=rid, toolName="Bash", toolUseID=f"t{i}")
        cc.event(at, "tengu_bash_tool_command_executed", cc_prompt_id="big", tool_use_id=f"t{i}", bash_argv0="ls",
                 sandbox_enabled="true")  # fmt: skip


DATASETS = {"drawer cases": drawer_cases, "mixed": mixed, "spanning in flight": spanning_in_flight}
# This file's: the CI datasets (the session and person pages' tests read them too), and one big prompt.
SIDES = {**DATASETS, "big prompt": big_prompt}


class Side:
    """One dataset in Postgres, and a client over it with the store's statements traced."""

    def __init__(self, name: str, root: pathlib.Path, pg: PgDataset) -> None:
        self.name, self.pg = name, pg
        self.store = pg.store(trace=True)
        self.new = make_client(self.store)

    def close(self) -> None:
        self.store.close()

    def query(self, sql: str, params: list[object] | None = None) -> list[tuple[Any, ...]]:
        with psycopg.connect(self.pg.dsn) as conn:
            return conn.execute(sql, params).fetchall()


@pytest.fixture(scope="module")
def sides(tmp_path_factory) -> Iterator[dict[str, Side]]:
    admin = pg_admin()
    tmp = tmp_path_factory.mktemp("drawer")
    with contextlib.ExitStack() as stack:
        out: dict[str, Side] = {}
        for name, build in SIDES.items():
            ds = Dataset(tmp / name.replace(" ", "-") / "data")
            build(ds)
            root = pathlib.Path(ds.write())
            pg = PgDataset(admin, root)
            stack.callback(pg.close)
            out[name] = Side(name, root, pg)
            stack.callback(out[name].close)
        yield out


def _q(rid: str) -> str:
    return urllib.parse.quote(rid, safe="")


def request_ids(side: Side) -> list[str]:
    """Every request id: every call held and every missed report."""
    held = side.query(
        """SELECT replace(call_id, '#', '~') FROM calls
           UNION SELECT 'reported:' || coalesce(request_id, client_request_id, ts_ns::text) FROM missed_reports"""
    )
    return sorted(r for (r,) in held)


def event_ids(side: Side) -> list[str]:
    """Every Claude Code event's id."""
    held = side.query("SELECT batch_call_id || ':' || batch_cc_n FROM events WHERE source = 'claude_code'")
    return sorted(e for (e,) in held)


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


def test_the_cases_are_all_there(sides):
    # Not vacuous: the drawer dataset holds every kind of drawer, and v9 draws each.
    side = sides["drawer cases"]
    rids = request_ids(side)
    kinds = {
        "turn": any("~" in r for r in rids),
        "reported": any(r.startswith("reported:") for r in rids),
        "batch": "batch-span-ended" in rids,
    }
    assert all(kinds.values()), kinds
    shapes: Counter[str] = Counter()
    for rid in rids:
        r = side.new.get(f"/api/v1/requests/{_q(rid)}")
        assert r.status_code == 200, (rid, r.text)
        d = r.json()["data"]
        shapes[d["kind"]] += 1
        shapes["retained"] += d["contentRetained"]
        shapes["retry"] += d["retry"] is not None
        shapes["stepped"] += d["stepIndex"] is not None
        shapes["matched"] += d["crossCheck"]["matched"]
    wanted = ("normal", "failed", "notCaptured", "retained", "retry", "stepped", "matched")
    assert all(shapes[k] for k in wanted), shapes
    for rid in rids:
        for sub in ("/details", "/raw"):
            assert side.new.get(f"/api/v1/requests/{_q(rid)}{sub}").status_code == 200, (rid, sub)
    for eid in event_ids(side):
        assert side.new.get(f"/api/v1/events/{_q(eid)}").status_code == 200, eid


def test_a_websocket_turn_is_drawn_from_its_row(sides):
    """A Codex WebSocket turn has no member of its own: its frames are in its connection's (§2.1), which
    no calls row points at. Its drawer is built from its row and /raw says why: no range, no GET."""
    side = sides["drawer cases"]
    turns = side.query("SELECT call_id, raw_key FROM calls WHERE ws_turn IS NOT NULL ORDER BY 1")
    assert turns and all(key is None for _, key in turns), turns
    rid = turns[0][0].replace("#", "~")
    for sub in ("", "/details", "/raw"):
        assert side.new.get(f"/api/v1/requests/{_q(rid)}{sub}").status_code == 200, sub
    raw = side.new.get(f"/api/v1/requests/{_q(rid)}/raw").json()["data"]
    assert raw["unavailable"].startswith("This is one turn of a WebSocket connection")


# What a drawer page reads (§6.2): the call, the events about it, its place, and for /details the
# sandbox setting; /events/{id} its event. Each ends with the drawer's lastDataAt (its pods' newest).
DRAWER_READS = {
    "": {"call", "call_events", "tool_events", "position", "next_attempt", "newest"},
    "/details": {
        "call",
        "call_events",
        "tool_events",
        "position",
        "next_attempt",
        "sandbox",
        "linked",
        "prompt_indexes",
        "newest",
    },  # fmt: skip
    "/raw": {"call", "newest"},
}


def test_a_drawer_reads_a_few_rows_whatever_the_session(sides):
    """Bound (§6.2): one snapshot, a fixed set of statements, each a few rows, and at most one ranged GET,
    for every call of a 120-step prompt in a session of hundreds of events."""
    side = sides["big prompt"]
    gets: list[int] = []
    read = side.store.read_range

    def counted(key: str, offset: int, length: int) -> bytes | None:
        gets.append(length)
        got: bytes | None = read(key, offset, length)
        return got

    side.store.read_range = counted
    try:
        rids = [r for (r,) in side.query("SELECT replace(call_id, '#', '~') FROM calls ORDER BY 1")]
        assert len(rids) > 120
        for rid in rids:
            for sub, allowed in DRAWER_READS.items():
                side.store.trace.clear()
                gets.clear()
                r = side.new.get(f"/api/v1/requests/{_q(rid)}{sub}")
                assert r.status_code == 200, (rid, sub)
                (load,) = side.store.trace
                names = [s.name for s in load.statements]
                assert load.connections == 1 and set(names) <= allowed and len(names) == len(set(names)), names
                assert max(s.rows for s in load.statements) <= 5, [(s.name, s.rows) for s in load.statements]
                assert len(gets) <= 1, gets
    finally:
        side.store.read_range = read
    side.store.trace.clear()
    eid = side.query("SELECT batch_call_id || ':' || batch_cc_n FROM events LIMIT 1")[0][0]
    assert side.new.get(f"/api/v1/events/{_q(eid)}").status_code == 200
    assert [s.name for s in side.store.trace[0].statements] == ["event", "newest"]


# v9's tables an index serves: any statement that scans one of the rest whole fails.
_SMALL = {"ingest_pods", "schema_version"}


def test_every_drawer_statement_can_use_an_index(sides):
    """Every statement a drawer page runs, priced with sequential scans off, uses an index: none reads a
    table whole."""
    side = sides["drawer cases"]
    urls = [f"/api/v1/requests/{_q(rid)}{sub}" for rid in _subset(request_ids(side)) for sub in DRAWER_READS]
    urls += [f"/api/v1/events/{_q(eid)}" for eid in _event_subset(event_ids(side))[:5]]
    plans = []
    for url in urls:
        side.store.trace.clear()
        side.new.get(url)
        plans += [(s.name, p) for ld in side.store.trace for s in ld.statements for p in side.store.reader.explain(s)]
    scans = [(n, p) for n, p in plans if "Seq Scan on" in p and p.split("Seq Scan on ")[1].split()[0] not in _SMALL]
    assert plans and not scans, scans


def test_an_ended_batch_spanning_sessions_is_its_earliest_events_session(sides):
    """Its call is A's (A holds its earliest event); B holds four of its five events."""
    side = sides["drawer cases"]
    d = side.new.get("/api/v1/requests/batch-span-ended").json()["data"]
    assert d["sessionId"] == SPAN_A and d["status"] == 200, d
    assert drawer.VISIBLE  # the drawer's visible-call predicate is the rollups' (refresh/sql.go)
