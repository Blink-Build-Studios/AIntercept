"""The side-by-side harness (docs/PIPELINE.md v8, Testing; build step 1).

Every page goes through the router's page-reader seam (aintercept_api/pages.py) twice: once on the
oracle's reader (today's SQL over the oracle's tables, tests/oracle) and once on PgStore's (v8's
statements, or v7's loader for a page that hasn't moved yet). One router serves both, so both get the
same snapped range (aintercept_api/ranges.py), and every response must be the oracle's bytes. The one
documented difference: a call still in flight has no drawer row, so its drawer 404s until it ends,
and an event of a batch in flight has no raw row yet (§3.7, the owner's call).

What it walks, generated from a dataset and the oracle's tables:
- **pages** (PAGES): every page of the API, one registration each: its path, what its id names, whether
  it filters by a range (then it's walked with every cut) and takes a bin (then every ladder bin, with
  each cut when ranged, and 90: a 422 on both), and the other parameters the UI sends;
- **cuts** (`cuts`): ranges whose ends fall inside a tick run, between two events of one batch (in one
  session, and across two), between two calls of one prompt, inside a presence segment, inside a
  not-captured window, on a minute boundary and ±1 µs and ±1 ms around one, before and after all data,
  and open at either end;
- **checkpoints** (`checkpoints`, `Side.at`): prefixes of the dataset's raw rows, cut at fixed points
  and mid-batch, mid-prompt and mid-tick-run. Each is laid out by the v7 worker into a database of its
  own (`aintercept v1layout -pg`, whose worker ticks until no flag is left) and settled (`settle`: no
  flag left, render flags included) before anything is compared; then it's compared with `now` at the
  checkpoint, at each of its thresholds (`nows`: live, streaming, in flight; the API's clock is whole
  µs, so ±1 ns is the µs either side), and a second before its newest row (a client clock ahead).

A page moving to v8's reader (pgstore.PgPages.PAGES) needs nothing here: every page is already held.
A page added to the API is one line in PAGES.
"""

import contextlib
import json
import pathlib
import urllib.parse
from collections.abc import Callable, Iterable, Iterator
from dataclasses import dataclass
from datetime import UTC, datetime, timedelta
from typing import Any

import psycopg
from fastapi.testclient import TestClient

from aintercept_api import claude_code as cc
from aintercept_api import pages, ranges
from aintercept_api import session as ss
from aintercept_api.app import create_app
from aintercept_api.buckets import LocalSource
from aintercept_api.pgstore import PgStore
from aintercept_api.raw import LocalRaw
from tests import pgdb
from tests.builders import Dataset
from tests.conftest import FROZEN_NOW, FROZEN_QUERY_ID, lay_out
from tests.endpoints import BIN_LADDER, person_from, window_from
from tests.oracle.harness import Layout, Oracle
from tests.oracle.store import OracleStore

NS = cc.NS
US = 1_000
MS = 1_000_000
MINUTE = ranges.MINUTE_NS
TICK = "tengu_tool_use_progress"

# --- pages ----------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class Page:
    """One page of the API, as the harness walks it."""

    name: str  # its reader's page (pages.PAGE_NAMES)
    path: str  # under /api/v1; {id} is the id of what `of` names
    of: str = ""  # "session", "person", "request" or "event"; "" for a page with no id
    ranged: bool = False  # filters by from/to: walked with every cut
    binned: bool = False  # takes bin: every ladder bin (with each cut when ranged), and 90
    variants: tuple[str, ...] = ("",)  # the other parameters the UI sends, a query string each


_OTHER_KINDS = "kind=flags&kind=count&kind=skills&kind=metrics&kind=catalog"  # the "other" chip
PAGES = [
    Page("status", "/status"),
    Page("session", "/sessions/{id}", "session"),
    Page("session/timeline", "/sessions/{id}/timeline", "session", binned=True),
    Page("session/state", "/sessions/{id}/state", "session", ranged=True),
    Page("session/prompts", "/sessions/{id}/prompts", "session", ranged=True,
         variants=("page=1&pageSize=20", "pageSize=2&page=2", "pageSize=20&prompt=2", "pageSize=2&prompt=3",
                   "pageSize=20&prompt=999", "pageSize=20&page=99")),
    Page("session/requests", "/sessions/{id}/requests", "session", ranged=True,
         variants=("pageSize=20", "kind=model&pageSize=20", f"{_OTHER_KINDS}&pageSize=20", "q=model&pageSize=20",
                   "order=desc&pageSize=20", "captured=false&hidden=true&pageSize=20", "pageSize=5&page=2",
                   "pageSize=20&page=99")),
    Page("session/events", "/sessions/{id}/events", "session", ranged=True,
         variants=("collapseTicks=true&pageSize=20", "collapseTicks=true&q=tool&pageSize=10",
                   "internal=true&collapseTicks=false&pageSize=20", "collapseTicks=true&pageSize=5&page=2",
                   "collapseTicks=true&pageSize=20&page=99")),
    Page("person", "/people/{id}", "person"),
    Page("person/sessions", "/people/{id}/sessions", "person", ranged=True,
         variants=("page=1&pageSize=20", "agent=claude-code&page=1&pageSize=20", "pageSize=1&page=2",
                   "pageSize=20&page=99")),
    Page("person/timeline", "/people/{id}/timeline", "person", ranged=True, binned=True),
    Page("person/summary", "/people/{id}/summary", "person", ranged=True),
    Page("person/requests", "/people/{id}/requests", "person", ranged=True,
         variants=("pageSize=20&order=desc", "kind=model&pageSize=20", f"{_OTHER_KINDS}&pageSize=20",
                   "q=model&pageSize=20", "agent=codex&captured=false&hidden=true&pageSize=20",
                   "pageSize=5&page=3&order=asc", "pageSize=20&page=99")),
    Page("coverage", "/coverage?personId={id}", "person", ranged=True),
    Page("people", "/people", ranged=True,
         variants=("", "sort=activity", "sort=name", "q=codex", "agent=claude-code&agent=codex",
                   "q=codex&page=2&pageSize=1")),
    Page("overview", "/overview", ranged=True),
    Page("request", "/requests/{id}", "request"),
    Page("request/details", "/requests/{id}/details", "request"),
    Page("request/raw", "/requests/{id}/raw", "request"),
    Page("event", "/events/{id}", "event"),
]  # fmt: skip
BY_NAME = {p.name: p for p in PAGES}


def page_of(url: str) -> str:
    """The page a URL is served by (pages.page_of)."""
    return pages.page_of(url.split("?")[0]) or "?"


# --- instants and ranges ------------------------------------------------------------------------------


def iso(ns: int) -> str:
    """An instant as a from= or to= at µs precision (the finest the API reads)."""
    return (datetime(1970, 1, 1, tzinfo=UTC) + timedelta(microseconds=ns // US)).strftime("%Y-%m-%dT%H:%M:%S.%fZ")


def iso_ms(ns: int) -> str:
    """As the UI writes one (Date.toISOString(): ms)."""
    return iso(ns)[:-4] + "Z"


Range = tuple[int | None, int | None]


def range_query(r: Range, *, ms: bool = False) -> str:
    fmt = iso_ms if ms else iso
    return "&".join(f"{k}={fmt(v)}" for k, v in (("from", r[0]), ("to", r[1])) if v is not None)


def _rows(table: Any) -> list[dict[str, Any]]:
    return [dict(zip(table.columns, r, strict=True)) for r in table.rows]


def _mid(a: int, b: int) -> int:
    return (a + b) // 2


def instants(oracle: Oracle, sids: Iterable[str]) -> dict[str, list[int]]:
    """Where a range's end can fall in these sessions' data (ns), by what it cuts through."""
    out: dict[str, list[int]] = {}

    def add(kind: str, t: int) -> None:
        out.setdefault(kind, []).append(t)

    times: list[int] = []
    batch_sessions: dict[str, dict[str, int]] = {}
    for sid in sids:
        tables = oracle.session(sid)
        events = sorted(_rows(tables["event_rows"]), key=lambda r: r["seq"])
        times += [e["ts_ns"] for e in events]
        for e1, e2 in zip(events, events[1:], strict=False):
            if e2["ts_ns"] <= e1["ts_ns"]:
                continue
            if e1["name"] == e2["name"] == TICK:
                add("inside a tick run", _mid(e1["ts_ns"], e2["ts_ns"]))
            if e1["event_id"].rpartition(":")[0] == e2["event_id"].rpartition(":")[0]:
                add("between a batch's events", _mid(e1["ts_ns"], e2["ts_ns"]))
        for e in events:
            batch_sessions.setdefault(e["event_id"].rpartition(":")[0], {}).setdefault(sid, e["ts_ns"])
        marks = _rows(tables["session_marks"])
        times += [m["t_ns"] for m in marks]
        calls: dict[str, list[int]] = {}
        for m in marks:
            if m["kind"] == "call" and m["prompt_id"]:
                calls.setdefault(m["prompt_id"], []).append(m["t_ns"])
        for found in calls.values():
            ts = sorted(set(found))
            for a, b in zip(ts, ts[1:], strict=False):
                add("between a prompt's calls", _mid(a, b))
        missed = sorted(m["t_ns"] for m in marks if m["kind"] == "missed")
        for a, b in zip(missed, missed[1:], strict=False):
            if b - a <= 15 * 60 * NS:  # one not-captured window (timeline.gaps)
                add("inside a not-captured window", _mid(a, b))
        for t in missed[:1]:
            add("inside a not-captured window", t)
        for p in _rows(tables["presence_segments"]):
            if p["to_ns"] is not None and p["to_ns"] > p["from_ns"]:
                add("inside a presence segment", _mid(p["from_ns"], p["to_ns"]))
    for by in batch_sessions.values():
        if len(by) >= 2:
            a, b = sorted(by.values())[:2]
            add("between a batch's events across sessions", _mid(a, b) if b > a else a)
    if times:
        lo, hi = min(times), max(times)
        edge = ranges.start_of_minute(lo) + MINUTE
        out["on and around a minute boundary"] = [edge, edge - US, edge + US, edge - MS, edge + MS]
        out["before all data"] = [lo - 3600 * NS]
        out["after all data"] = [hi + 3600 * NS]
    return out


def cuts(found: dict[str, list[int]], *, per_kind: int = 1) -> list[Range]:
    """Ranges whose ends fall at these instants: from each to the next (sorted), open at either end of
    each, and from the first to the last; per_kind instants of each kind (all of a minute boundary's)."""
    chosen = sorted({t for kind, ts in found.items() for t in (ts if "minute" in kind else sorted(set(ts))[:per_kind])})
    out: list[Range] = [(None, None)]
    for t in chosen:
        out += [(t, None), (None, t)]
    out += list(zip(chosen, chosen[1:], strict=False))
    if len(chosen) >= 2:
        out.append((chosen[0], chosen[-1]))
    return list(dict.fromkeys(out))


def directory_ranges() -> list[Range]:
    """The directory's froms: none, the People page's presets (on the hour), an unaligned one, a person
    page's (now − span at ms) and a closed range."""

    def ns_of(s: str) -> int:
        return cc.to_ns(datetime.fromisoformat(s.replace("Z", "+00:00")))

    now = cc.to_ns(FROZEN_NOW)
    return [
        (None, None),
        *((ns_of(window_from(w)), None) for w in ("24h", "7d", "14d")),
        (ns_of(person_from("7d")), None),
        (now - 26 * 3600 * NS - 7_517_000, now - 3 * 3600 * NS + 13_000),
    ]


# --- URLs ----------------------------------------------------------------------------------------------


def url(page: Page, ident: str | None, *parts: str) -> str:
    path = "/api/v1" + page.path.replace("{id}", urllib.parse.quote(ident or "", safe=""))
    query = "&".join(p for p in parts if p)
    if not query:
        return path
    return path + ("&" if "?" in path else "?") + query


def page_urls(page: Page, ident: str | None, rs: list[Range], *, every: bool) -> list[str]:
    """page's URLs for one id: each variant on its own; each range with the first variant (the page as
    the UI opens it), and (every) each variant with three of them; each ladder bin, with each range when
    ranged; and bin 90 (a 422)."""
    out = [url(page, ident, v) for v in page.variants]
    if page.ranged:
        for i, r in enumerate(rs):
            for v in page.variants if every and i < 3 else page.variants[:1]:
                out.append(url(page, ident, v, range_query(r, ms=i % 3 == 2)))
    if page.binned:
        for b in BIN_LADDER:
            for r in rs if page.ranged else [(None, None)]:
                out.append(url(page, ident, f"bin={b}", range_query(r)))
        out.append(url(page, ident, "bin=90"))
    return list(dict.fromkeys(out))


# --- comparing ------------------------------------------------------------------------------------------


def make(store: Any, now: datetime = FROZEN_NOW) -> TestClient:
    app = create_app(store=store, clock=lambda: now, query_id=lambda: FROZEN_QUERY_ID, ingest_interval_s=0)
    return TestClient(app)


def _call_in_flight(old: Any, new: Any, rid: str) -> bool:
    """A call still in flight: the oracle's summary has no duration (no end yet), and v7 has no drawer
    row for it (the owner's call, §3.7)."""
    a = old.get(f"/api/v1/requests/{urllib.parse.quote(rid, safe='')}")
    if a.status_code != 200 or a.json()["data"].get("durationMs") is not None:
        return False
    return bool(new.get(f"/api/v1/requests/{urllib.parse.quote(rid, safe='')}").status_code == 404)


def _leaves(x: Any, y: Any, parent: Any = None) -> Iterator[tuple[str, Any, Any, Any]]:
    """Each leaf where x and y differ: (its key, x's value, y's value, x's dict holding it)."""
    if isinstance(x, dict) and isinstance(y, dict):
        for k in sorted(set(x) | set(y)):
            for key, a, b, p in _leaves(x.get(k), y.get(k), x):
                yield (key or k), a, b, p
    elif isinstance(x, list) and isinstance(y, list) and len(x) == len(y):
        for a, b in zip(x, y, strict=True):
            yield from _leaves(a, b, parent)
    elif x != y:
        yield "", x, y, parent


def _in_flight(old: Any, new: Any, url_: str, a: Any, b: Any) -> bool:
    """The one documented difference (§3.7, the owner's call): a call in flight has no drawer row, so
    - its drawer 404s until it ends;
    - an event of a batch in flight has its fields but no raw row yet;
    - an ended call's drawer leaves it out: no link to it (and nothing beside the link), and a step
      count that doesn't count it."""
    if not url_.startswith(("/api/v1/requests/", "/api/v1/events/")):
        return False
    if url_.startswith("/api/v1/events/") and a.status_code == b.status_code == 200:
        x, y = a.json(), b.json()
        if x["data"].pop("raw", None) is not None and y["data"].pop("raw", None) == {} and x == y:
            return True
    rid = urllib.parse.unquote(url_.split("?")[0].split("/")[4])
    if b.status_code == 404 and a.status_code == 200:
        return url_.startswith("/api/v1/requests/") and _call_in_flight(old, new, rid)
    if not (a.status_code == b.status_code == 200):
        return False
    for key, x, y, holder in _leaves(a.json(), b.json()):
        if key == "stepCount" and isinstance(x, int) and isinstance(y, int) and x > y:
            continue
        if y is not None:
            return False
        if isinstance(x, str) and x != rid and _call_in_flight(old, new, x):
            continue
        beside = (holder or {}).values()
        if not any(isinstance(v, str) and v != rid and _call_in_flight(old, new, v) for v in beside):
            return False
    return True


@dataclass
class Held:
    compared: int
    differing: list[str]
    in_flight: int
    by_page: dict[str, int]


def compare(old: TestClient, new: TestClient, urls: Iterable[str]) -> Held:
    """Each URL's response on both clients: the same status and the same bytes."""
    held = Held(0, [], 0, {})
    for u in urls:
        a, b = old.get(u), new.get(u)
        held.compared += 1
        held.by_page[page_of(u)] = held.by_page.get(page_of(u), 0) + 1
        if a.status_code == b.status_code and a.content == b.content:
            continue
        if _in_flight(old, new, u, a, b):
            held.in_flight += 1
            continue
        held.differing.append(u)
    return held


def assert_held(held: Held, *, at: str = "") -> None:
    shown = f"{at} held {held.compared - len(held.differing)} of {held.compared}"
    if held.in_flight:
        shown += f" ({held.in_flight} by the in-flight drawer difference)"
    print(shown, held.by_page)  # noqa: T201
    assert held.compared and not held.differing, (at, len(held.differing), held.differing[:5])


# --- datasets, laid out and settled -----------------------------------------------------------------------


def settle(dsn: str) -> None:
    """Fails unless the pipeline has settled: no flag left in dirty_keys, render flags included. A
    cross-key value (a batch's count, a label, a child) is right once its render flag has run, so
    nothing is compared before."""
    with psycopg.connect(dsn) as conn:
        n, render = conn.execute("SELECT count(*), count(*) FILTER (WHERE render) FROM dirty_keys").fetchone() or (0, 0)
    assert (n, render) == (0, 0), f"not settled: {n} flags left, {render} of them render flags"


class Side:
    """A dataset (or a prefix of it) laid out, settled in its own database, and both stores over it."""

    def __init__(self, root: pathlib.Path, dsn: str) -> None:
        self.root, self.dsn = root, dsn
        self.layout = lay_out(root, pg=dsn)
        settle(dsn)
        self.old_store = OracleStore(self.layout, prefix="sim/", raw=LocalRaw(root))
        self.store = PgStore(dsn, source=LocalSource(self.layout), prefix="sim/", threads=1)
        self.oracle = Oracle(Layout(self.layout))
        self.old, self.new = make(self.old_store), make(self.store)

    def at(self, now: datetime) -> tuple[TestClient, TestClient]:
        """Both clients, with the clock at now."""
        for c in (self.old, self.new):
            c.app.state.clock = lambda: now  # type: ignore[attr-defined]
        return self.old, self.new

    def close(self) -> None:
        self.oracle.close()
        self.store.close()
        self.old_store.close()

    # --- what it holds ---

    def sessions(self) -> list[str]:
        return [*self.oracle.session_ids(), "no-such-session"]

    def people(self) -> list[str]:
        return [*self.oracle.person_ids(), "unattributed", "nobody"]

    def request_ids(self) -> list[str]:
        return self.oracle.request_ids()

    def event_ids(self) -> list[str]:
        out = []
        for sid in self.oracle.session_ids():
            t = self.oracle.session(sid)["event_rows"]
            out += [r[t.columns.index("event_id")] for r in t.rows]
        return out

    def person_sessions(self, pid: str) -> list[str]:
        t = self.oracle.person(pid)["person_sessions"]
        return sorted({r[t.columns.index("session_id")] for r in t.rows})

    def busiest_session(self) -> str:
        return max(self.oracle.session_ids(), key=lambda s: (len(self.oracle.session(s)["event_rows"].rows), s))

    def busiest_person(self) -> str:
        return max(self.oracle.person_ids(), key=lambda p: (len(self.person_sessions(p)), p))

    def ids(self, page: Page) -> list[str | None]:
        """What page's {id} walks: every session and person; every 10th request id and 50th event id
        (test_v7_drawer.py holds the drawer for every one, byte for byte)."""
        return {
            "session": self.sessions,
            "person": self.people,
            "request": lambda: self.request_ids()[::10],
            "event": lambda: self.event_ids()[::50],
        }.get(page.of, lambda: [None])()  # type: ignore[return-value]

    def ranges_of(self, page: Page, ident: str | None, *, per_kind: int) -> list[Range]:
        if not page.ranged:
            return []
        if page.of == "session":
            sids = [ident] if ident in self.oracle.session_ids() else []
        elif page.of == "person":
            sids = self.person_sessions(ident) if ident in self.oracle.person_ids() else []
        else:
            return directory_ranges()
        return cuts(instants(self.oracle, sids), per_kind=per_kind)

    def urls(
        self, page: Page, *, every: bool, per_kind: int = 1, only: Callable[[str], bool] | None = None
    ) -> list[str]:
        """page's URLs over everything the side holds (only: the ids to walk)."""
        out: list[str] = []
        for ident in self.ids(page):
            if only is not None and ident is not None and not only(ident):
                continue
            out += page_urls(page, ident, self.ranges_of(page, ident, per_kind=per_kind), every=every)
        return out


# --- checkpoints ---------------------------------------------------------------------------------------


def checkpoints(rows: list[dict[str, Any]]) -> dict[str, int]:
    """Where to cut a dataset's raw rows (by timestamp_ns), by what the cut lands in: fixed points (a
    quarter and half of the rows), and mid-batch, mid-prompt and mid-tick-run where the dataset has one."""
    ts = sorted(r["timestamp_ns"] for r in rows)
    out = {"a quarter in": ts[len(ts) // 4], "halfway": ts[len(ts) // 2]}
    batches: dict[str, set[int]] = {}
    ticks: dict[str, list[int]] = {}
    for r in rows:
        if r.get("phase") == "client_event":
            batches.setdefault(r["correlation_id"], set()).add(r["timestamp_ns"])
            ev = r.get("client_event") or {}
            if ev.get("name") == TICK:
                ticks.setdefault(ev.get("session_id") or "", []).append(r["timestamp_ns"])
    for got in batches.values():
        if len(got) >= 2:
            a, b = sorted(got)[:2]
            out["mid-batch"] = _mid(a, b)
            break
    prompts: dict[str, list[int]] = {}
    for r in rows:
        if r.get("phase") == "request_headers" and str(r.get("path", "")).startswith("/v1/messages"):
            p = (r.get("headers") or {}).get("x-claude-code-prompt-id")
            if p:
                prompts.setdefault(p, []).append(r["timestamp_ns"])
    for calls in prompts.values():
        starts = sorted(set(calls))
        if len(starts) >= 2:
            out["mid-prompt"] = _mid(starts[0], starts[1])
            break
    for run in ticks.values():
        at = sorted(set(run))
        if len(at) >= 3:
            out["mid-tick-run"] = _mid(at[1], at[2])
            break
    return out


def prefix(rows: list[dict[str, Any]], t: int, root: pathlib.Path) -> pathlib.Path:
    """The dataset's raw rows up to t, written as the observer would have by then."""
    ds = Dataset(root)
    for r in rows:
        if r["timestamp_ns"] <= t:
            ds.add(json.loads(json.dumps(r)))
    return ds.write()


def _µs(t: int) -> datetime:
    return datetime(1970, 1, 1, tzinfo=UTC) + timedelta(microseconds=t // US)


def nows(oracle: Oracle, t: int) -> dict[str, list[tuple[datetime, list[str]]]]:
    """The clocks a checkpoint at t is compared at, with the sessions whose pages each one decides
    (empty: every page): the checkpoint; each session's live threshold (5 min after its last row); each
    unfinished prompt's and step's streaming one (10 min after); each ±1 µs (the API's clock is whole
    µs); and a second before the newest row (a client clock ahead of the API's)."""
    out: dict[str, list[tuple[datetime, list[str]]]] = {"at the checkpoint": [(_µs(t), [])]}

    def around(kind: str, theta: int, sid: str) -> None:
        for d in (-US, 0, US):
            out.setdefault(kind, []).append((_µs(theta + d), [sid]))

    newest = 0
    for sid in oracle.session_ids():
        tables = oracle.session(sid)
        rows = _rows(tables["session_rows"])
        for r in rows:
            newest = max(newest, r["last_ns"])
            around("live", r["last_ns"] + cc.LIVE_WITHIN_NS, sid)
        for p in _rows(tables["prompt_rows"]):
            if p["unfinished_max_ns"] is not None:
                around("streaming (prompt)", p["unfinished_max_ns"] + ss.STREAMING_WITHIN_NS, sid)
        for s in _rows(tables["prompt_steps"]):
            if s["end_ns"] is None:
                around("streaming (step)", s["t_ns"] + ss.STREAMING_WITHIN_NS, sid)
    if newest:
        out["a client clock ahead"] = [(_µs(newest - NS), [])]
    return out


def walk(
    found: dict[str, list[tuple[datetime, list[str]]]], *, every: bool
) -> Iterator[tuple[str, datetime, list[str]]]:
    """Each clock (every: all of each kind; else the first of each)."""
    for kind, clocks in found.items():
        for now, sids in clocks if every else clocks[:1]:
            yield kind, now, sids


# --- the CI datasets ------------------------------------------------------------------------------------


def built(name: str, root: pathlib.Path) -> tuple[pathlib.Path, list[dict[str, Any]]]:
    """One of test_v7_drawer's datasets, written under root: (its raw root, its rows)."""
    from tests.test_v7_drawer import DATASETS

    ds = Dataset(root)
    DATASETS[name](ds)
    return pathlib.Path(ds.write()), ds.rows


@contextlib.contextmanager
def sides(admin: str, roots: dict[str, pathlib.Path]) -> Iterator[dict[str, Side]]:
    """Each dataset laid out and settled in a database of its own, and both stores over it."""
    with contextlib.ExitStack() as stack:
        out = {}
        for name, root in roots.items():
            dsn = stack.enter_context(pgdb.fresh_database(admin))
            out[name] = Side(root, dsn)
            stack.callback(out[name].close)
        yield out
