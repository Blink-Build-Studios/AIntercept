"""The request drawer on v9 (docs/PIPELINE.md §6.2), side by side with the oracle: today's
request_detail.py over the oracle's tables.

For each dataset, a fresh Postgres holds what the cluster holds while both paths run (conftest.PgDataset):
v7's tables, and v9's facts and rollups loaded from v9 raw files as the workers load them. The API's
Postgres store renders a drawer when it opens, from the call's `calls` row, one ranged GET of its raw
member and a few indexed lookups; the oracle derives it from its whole session. Every response must be
the same data:
- every request id (the request logs' ids, WebSocket turns and `reported:` ids among them, and every
  call v9 holds), its /requests/{id}, /details and /raw;
- every event id (every session's events, and every Claude Code event v9 holds), its /events/{id}.

The differences, each by name:
- "Delivered in step N" is the next call's step within its prompt (§8, difference 7); today's counts
  every call of the session. It's checked against the next call's own step.
- A call that hasn't ended has no raw member yet (its rows are written with its end, §2.1): its drawer
  comes from its row, as a WebSocket turn's does, and an event of a batch that hasn't ended has no raw
  row yet. (The Go drawer had no row for either.)

`make check` holds a representative subset of each dataset (a request id of each kind, every tenth, and a
few events per batch); the full run (AINTERCEPT_TESTS_FULL=1) holds every one.

An ended analytics batch spanning sessions is here too: the call is its earliest event's session's."""

import contextlib
import pathlib
import urllib.parse
from collections import Counter
from collections.abc import Iterator
from typing import Any

import psycopg
import pytest

from aintercept_api import drawer
from aintercept_api.raw import LocalRaw
from tests.builders import INSTANCE, ClaudeCode, Dataset, load_fixture, ns
from tests.conftest import PgDataset, lay_out, make_client, pg_admin
from tests.oracle.harness import Layout, Oracle
from tests.oracle.store import OracleStore
from tests.test_codex import claude_code_account, codex_work
from tests.test_directory import OTHER, S1, person, unattributed
from tests.test_requests import failed_then_retried, retained
from tests.test_session import H, prompt
from tests.test_stream_pipeline import one_stream_one_id

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
# This file's: the CI datasets (side_by_side.py and others read DATASETS), and one big prompt.
SIDES = {**DATASETS, "big prompt": big_prompt}


class Side:
    """One dataset in Postgres (v7's tables and v9's), and two clients over it: the oracle's and the
    Postgres store's."""

    def __init__(self, name: str, root: pathlib.Path, pg: PgDataset) -> None:
        self.name, self.layout, self.pg = name, lay_out(root), pg
        self.old_store = OracleStore(self.layout, prefix="sim/", raw=LocalRaw(root))
        self.store = pg.store(trace=True)
        self.old, self.new = make_client(self.old_store), make_client(self.store)

    def close(self) -> None:
        self.store.close()
        self.old_store.close()

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


def _delivered(side: Side, url: str, a, b) -> bool:
    """Difference 7: "delivered in step N" is the next call's step within its prompt. b holds that, a
    today's number; with the rows made alike, the two are the same data."""
    if not url.endswith("/details") or a.status_code != 200 or b.status_code != 200:
        return False
    x, y = a.json(), b.json()
    for tx, ty in zip(x["data"]["tool"], y["data"]["tool"], strict=False):
        for rx, ry in zip(tx["result"], ty["result"], strict=False):
            if rx["k"] == ry["k"] == "delivered in" and rx["note"] == ry["note"]:
                nxt = rx["note"]
                step = side.query("SELECT step FROM steps WHERE call_id = %s", [nxt.replace("~", "#")]) if nxt else []
                assert ry["v"] == (f"step {step[0][0]}" if step else None), (url, ry, step)
                rx["v"] = ry["v"]
    return bool(x == y)


def _unended(side: Side, url: str, a, b) -> bool:
    """A call or batch that hasn't ended has no raw member yet: the call's drawer comes from its row (its
    headers as the row keeps them, no bodies); its batch's events have no raw row."""
    if a.status_code != 200 or b.status_code != 200:
        return False
    x, y = a.json()["data"], b.json()["data"]
    if url.startswith("/api/v1/events/"):
        batch = urllib.parse.unquote(url.removeprefix("/api/v1/events/")).rpartition(":")[0]
        member = side.query("SELECT raw_key FROM calls WHERE call_id = %s", [batch])
        return bool(not (member and member[0][0]) and y.pop("raw") == {} and x.pop("raw") is not None and x == y)
    rid = urllib.parse.unquote(url.removeprefix("/api/v1/requests/").split("/")[0])
    member = side.query("SELECT raw_key, end_ns FROM calls WHERE call_id = %s", [rid.replace("~", "#")])
    if not member or member[0][0] is not None:
        return False
    if url.endswith("/raw"):
        return bool(y["unavailable"] is not None and x["requestLine"] == y["requestLine"])
    return bool(x["requestId"] == y["requestId"] and x["t"] == y["t"] and x["sessionId"] == y["sessionId"])


def _first_diff(a: object, b: object, path: str = "") -> str | None:
    """Where two responses first differ (scripts/parity_compare.py's first_diff), with the two values."""
    if isinstance(a, dict) and isinstance(b, dict):
        for k in sorted(a.keys() | b.keys()):
            d = _first_diff(a.get(k), b.get(k), f"{path}.{k}")
            if d is not None:
                return d
        return None
    if isinstance(a, list) and isinstance(b, list) and len(a) == len(b):
        for i, (x, y) in enumerate(zip(a, b, strict=True)):
            d = _first_diff(x, y, f"{path}[{i}]")
            if d is not None:
                return d
        return None
    return None if a == b else f"{path}: {str(a)[:200]} != {str(b)[:200]}"


def _hold(side: Side, urls: list[str]) -> Counter[str]:
    """Each URL's two responses are the same data (or one of the named differences)."""
    held: Counter[str] = Counter()
    differing = []
    for url in urls:
        a, b = side.old.get(url), side.new.get(url)
        if a.status_code == b.status_code and a.json() == b.json():
            held["same"] += 1
        elif _delivered(side, url, a, b):
            held["delivered in"] += 1
        elif _unended(side, url, a, b):
            held["not ended"] += 1
        else:
            differing.append((url, a.status_code, b.status_code, _first_diff(a.json(), b.json())))
    assert not differing, (side.name, len(differing), differing[:3])
    return held


def request_ids(side: Side) -> list[str]:
    """Every request id: the logs' (the oracle's), every call v9 holds and every missed report."""
    with Oracle(Layout(side.layout)) as oracle:
        listed = set(oracle.request_ids())
    held = side.query(
        """SELECT replace(call_id, '#', '~') FROM calls
           UNION SELECT 'reported:' || coalesce(request_id, client_request_id, ts_ns::text) FROM missed_reports"""
    )
    return sorted(listed | {r for (r,) in held})


def event_ids(side: Side) -> list[str]:
    """Every event id: each session's events (the oracle's) and every Claude Code event v9 holds."""
    with Oracle(Layout(side.layout)) as oracle:
        listed = set()
        for sid in oracle.session_ids():
            t = oracle.session(sid)["event_rows"]
            listed |= {r[t.columns.index("event_id")] for r in t.rows}
            oracle._memo.clear()
    held = side.query("SELECT batch_call_id || ':' || batch_cc_n FROM events WHERE source = 'claude_code'")
    return sorted(listed | {e for (e,) in held})


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


def _hold_drawers(side: Side, *, every: bool) -> Counter[str]:
    rids, eids = request_ids(side), event_ids(side)
    assert rids and eids, side.name
    chosen, events = (rids, eids) if every else (_subset(rids), _event_subset(eids))
    held = _hold(side, _drawer_urls(chosen))
    held += _hold(side, [f"/api/v1/events/{_q(e)}" for e in events])
    print(f"{side.name}: request ids {len(chosen)} of {len(rids)}, events {len(events)} of {len(eids)}:", dict(held))  # noqa: T201
    assert held["same"] >= 3 * len(chosen) // 2, held
    return held


@pytest.mark.parametrize("name", list(SIDES))
def test_the_drawers_match_the_oracle(sides, name):
    """make check: a request id of each kind and every tenth, a few events per batch."""
    _hold_drawers(sides[name], every=False)


@pytest.mark.full
@pytest.mark.parametrize("name", list(SIDES))
def test_every_drawer_matches_the_oracle(sides, name):
    """The full run: every request id's /requests/{id}, /details and /raw, every event's /events/{id}."""
    _hold_drawers(sides[name], every=True)


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
    # And the differences are exercised: a call delivered in a later step, a call that hasn't ended.
    held = _hold_drawers(side, every=True)
    assert held["delivered in"] and held["not ended"], held


def test_a_websocket_turn_is_drawn_from_its_row(sides):
    """A Codex WebSocket turn has no member of its own: its frames are in its connection's (§2.1), which
    no calls row points at. As today, its drawer is built from its row and /raw says why: no range, no
    GET."""
    side = sides["drawer cases"]
    turns = side.query("SELECT call_id, raw_key FROM calls WHERE ws_turn IS NOT NULL ORDER BY 1")
    assert turns and all(key is None for _, key in turns), turns
    rid = turns[0][0].replace("#", "~")
    for sub in ("", "/details", "/raw"):
        a, b = side.old.get(f"/api/v1/requests/{_q(rid)}{sub}"), side.new.get(f"/api/v1/requests/{_q(rid)}{sub}")
        assert a.status_code == b.status_code == 200 and a.json() == b.json(), (sub, a.text[:300], b.text[:300])
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
