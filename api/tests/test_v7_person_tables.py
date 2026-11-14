"""Every person's tables as the Postgres loader reads them, side by side with the oracle's
(docs/PIPELINE.md v7 build step 6, Testing: table level), for every range in the URL set and
generated ranges that cut through prompts, batches and sessions.

`aintercept v1layout -pg` writes each dataset's rows into a fresh Postgres; pgstore_person.entity
loads a person for a range the way its pages do (everything any of them reads), and each table must
equal the oracle's person tables (harness.PERSON_TABLES), seq by order:
- the scope's tables (person_map, person_sessions, accounts, account_facts, account_devices) and the
  person's session_rows, with the labels other keys give them (the Codex title over the account
  component, the alone label, the children), whatever the range;
- the ranged tables, cut to the range exactly as the pages cut them: event_rows (the columns the
  pages read, with `batch` against the oracle's event_id prefix), session_marks, typed prompt_rows
  and call_tool_uses of every person session (the timeline counts them all), presence_segments of
  the sessions the range shows; request_rows (the request log's µs window) and person_last;
- the summary's `_LATEST` values: each shown session's newest count of each name up to the range's end.

Each kind of cut is spread over the person's history: 2 per kind per person in make check, 40 with
-m full (each as a range's start, its end, and a window to the next cut); every _LATEST cut always.
"""

import contextlib
import pathlib
import re
from collections.abc import Iterator
from datetime import datetime
from typing import Any

import pytest

from aintercept_api import claude_code as cc
from aintercept_api import pgstore_person
from aintercept_api.pgstore import PgStore
from tests import pgdb
from tests.builders import Dataset
from tests.conftest import FROZEN_NOW, lay_out
from tests.endpoints import PERSON_BRUSH, person_from
from tests.oracle.harness import PERSON_TABLES, Layout, Oracle, Table, Tables, assert_same, extract
from tests.test_directory import person
from tests.test_session import ENV
from tests.test_v1_pipeline import laid_out  # noqa: F401 (the response gate's dataset, a fixture)
from tests.test_v6_session_rows import _datasets

NOW = cc.to_ns(FROZEN_NOW)
_BATCH = re.compile(r"^(.*):[0-9]+$")
# Compared as multisets: the endpoints order these themselves (and harness.extract's IN filter can
# reorder rows). request_rows' order is compared: the requests sort has no final tiebreaker (quirk 4).
_UNORDERED = (
    "person_map", "person_sessions", "accounts", "account_facts", "account_devices", "session_rows", "event_rows",
    "session_marks", "prompt_rows", "call_tool_uses", "presence_segments",
)  # fmt: skip
_SCOPE = ("person_map", "person_sessions", "accounts", "account_facts", "account_devices", "session_rows")


LATEST_SESSION = "44444444-4444-4444-8444-444444444444"
LATEST_ACCOUNT = "00000000-0000-4000-8000-00000000acc4"


def latest_counts(ds: Dataset) -> None:
    """A session whose Claude Code reports its skills and MCP tools again and again (the summary's
    _LATEST values): newer counts, two at one instant, and one report without a count (skipped)."""
    cc = person(ds, LATEST_SESSION, account=LATEST_ACCOUNT, hour=12, calls=3)
    for minute, total in ((5, 5), (15, 7), (25, None), (35, 9)):
        cc.event(f"2026-11-04T12:{minute:02d}:00", "tengu_skills_sync_success", env=ENV,
                 **({} if total is None else {"total": total}))  # fmt: skip
    for minute, tools in ((10, 40), (20, 30), (20, 45), (30, 12)):
        cc.event(f"2026-11-04T12:{minute:02d}:00", "tengu_tool_search_mode_decision", env=ENV, mcpToolCount=tools,
                 batch=f"tools-{minute}-{tools}")  # fmt: skip
    cc.event("2026-11-04T12:50:00", "tengu_input_prompt", prompt_index=2, cc_prompt_id="p2")  # still going after


@pytest.fixture(scope="module")
def layouts(tmp_path_factory, laid_out) -> Iterator[list[tuple[str, pathlib.Path, str]]]:  # noqa: F811
    """test_v6_session_rows' datasets and the response gate's, each in a fresh Postgres."""
    admin = pgdb.admin_dsn()
    if admin is None:
        if pgdb.required():
            pytest.fail("AINTERCEPT_TEST_PG isn't set (CI's service container should set it)")
        pytest.skip("no test Postgres: set AINTERCEPT_TEST_PG (make check starts one: scripts/test-pg.sh)")
    tmp = tmp_path_factory.mktemp("v7p")
    ds = Dataset(tmp / "latest" / "data")
    latest_counts(ds)
    raws = [*_datasets(tmp), ("response gate", laid_out[0]), ("latest counts", pathlib.Path(ds.write()))]
    with contextlib.ExitStack() as stack:
        out = []
        for name, raw in raws:
            dsn = stack.enter_context(pgdb.fresh_database(admin))
            out.append((name, lay_out(raw, pg=dsn), dsn))
        yield out


def _ns(iso: str) -> int:
    return cc.to_ns(datetime.fromisoformat(iso.replace("Z", "+00:00")))


# The URL set's ranges (endpoints.py): none, a person page's unaligned presets, the brush.
URL_RANGES: list[tuple[int | None, int | None]] = [
    (None, None),
    (_ns(person_from("24h")), None),
    (_ns(person_from("7d")), None),
    (_ns(person_from("14d")), None),
    (_ns(PERSON_BRUSH["from"]), _ns(PERSON_BRUSH["to"])),
]


def _col(t: Table, name: str) -> int:
    return t.columns.index(name)


def _mid(a: int, b: int) -> int:
    return (a + b) // 2


def cuts(want: Tables, per_kind: int) -> dict[str, list[tuple[int | None, int | None]]]:
    """Ranges that cut through the person's prompts (between two model calls of one prompt), batches
    (between two events of one batch), sessions (through the middle of one) and the _LATEST
    counts (between two of a session's reports): each cut as a range's
    start, its end, and a window from it to the next cut."""
    rr = want["request_rows"]
    by_prompt: dict[tuple[Any, Any], list[int]] = {}
    for r in rr.rows:
        if r[_col(rr, "kind")] == "model" and r[_col(rr, "prompt_index")] is not None:
            by_prompt.setdefault((r[_col(rr, "session_id")], r[_col(rr, "prompt_index")]), []).append(
                r[_col(rr, "ts_ns")]
            )
    ev = want["event_rows"]
    by_batch: dict[str, list[int]] = {}
    for r in ev.rows:
        m = _BATCH.match(r[_col(ev, "event_id")] or "")
        if m:
            by_batch.setdefault(m.group(1), []).append(r[_col(ev, "ts_ns")])
    sr = want["session_rows"]
    last = {r[_col(sr, "session_id")]: r[_col(sr, "last_ns")] for r in sr.rows}
    by_latest: dict[tuple[str, str], list[int]] = {}
    for r in ev.rows:
        sid = r[_col(ev, "session_id")]
        if r[_col(ev, "name")] in pgstore_person.LATEST and r[_col(ev, "count")] is not None:
            # and the session's end: a range starting after its last report still shows it
            by_latest.setdefault((sid, r[_col(ev, "name")]), [last.get(sid, 0)]).append(r[_col(ev, "ts_ns")])
    out: dict[str, list[tuple[int | None, int | None]]] = {}
    for kind, groups in (
        ("prompt", by_prompt.values()),
        ("batch", by_batch.values()),
        ("latest", by_latest.values()),
        ("session", [[r[_col(sr, "start_ns")], r[_col(sr, "last_ns")]] for r in sr.rows]),
    ):
        points = sorted(
            {_mid(a, b) for ts in groups for a, b in zip(sorted(ts), sorted(ts)[1:], strict=False) if b > a}
        )
        if kind != "latest" and len(points) > per_kind:  # spread over the person's history
            points = [points[i * len(points) // per_kind] for i in range(per_kind)]
        ranges: list[tuple[int | None, int | None]] = []
        for i, t in enumerate(points):
            ranges += [(t, None), (None, t)]
            if i + 1 < len(points):
                ranges.append((t, points[i + 1]))
        out[kind] = ranges
    return out


def _within(t: int | None, lo: int | None, hi: int | None) -> bool:
    return t is not None and (lo is None or t >= lo) and (hi is None or t <= hi)


def _cut(t: Table, time: str, lo: int | None, hi: int | None, *, flag: str | None = None) -> Table:
    """t's rows whose time is in [lo, hi] (and flag, when given, is true)."""
    i, f = _col(t, time), None if flag is None else _col(t, flag)
    return Table(t.columns, [r for r in t.rows if _within(r[i], lo, hi) and (f is None or r[f])])


def _shown(sr: Table, lo: int | None, hi: int | None) -> set[str]:
    """rollups.SHOWN at FROZEN_NOW (an open bound: the endpoints' defaults are wider than any row)."""
    out = set()
    for r in sr.rows:
        start, last = r[_col(sr, "start_ns")], r[_col(sr, "last_ns")]
        end = NOW if NOW - last <= cc.LIVE_WITHIN_NS else last
        if (hi is None or start <= hi) and (lo is None or end >= lo):
            out.add(r[_col(sr, "session_id")])
    return out


def _latest(ev: Table, sessions: set[str], hi: int | None) -> dict[tuple[str, str], Any]:
    """Each session's _LATEST count of each name up to hi: arg_max(count, (ts_ns, count)), NULL skipped."""
    out: dict[tuple[str, str], tuple[int, int]] = {}
    for r in ev.rows:
        sid, name, ts, n = (r[_col(ev, c)] for c in ("session_id", "name", "ts_ns", "count"))
        if sid in sessions and name in pgstore_person.LATEST and n is not None and (hi is None or ts <= hi):
            if (sid, name) not in out or (ts, n) > out[sid, name]:
                out[sid, name] = (ts, n)
    return {k: v[1] for k, v in out.items()}


def comparable(want: Tables, got: Tables, lo: int | None, hi: int | None) -> tuple[Tables, Tables]:
    """Both sides cut to what the person pages read of the range."""
    w: Tables = {t: want[t] for t in _SCOPE}
    g: Tables = {t: got[t] for t in _SCOPE}
    # event_rows: the columns the pages read; batch is the oracle's event_id prefix (as its regex reads it).
    cols = (*pgstore_person.EVENT_COLUMNS, "batch")
    ev = want["event_rows"]
    w["event_rows"] = Table(
        cols,
        [
            (*(r[_col(ev, c)] for c in pgstore_person.EVENT_COLUMNS), _prefix(r[_col(ev, "event_id")]))
            for r in ev.rows
            if _within(r[_col(ev, "ts_ns")], lo, hi)
        ],
    )
    gv = got["event_rows"]
    g["event_rows"] = Table(
        cols, [tuple(r[_col(gv, c)] for c in cols) for r in gv.rows if _within(r[_col(gv, "ts_ns")], lo, hi)]
    )
    shown = _shown(want["session_rows"], lo, hi)
    for side, src in ((w, want), (g, got)):
        side["session_marks"] = _cut(src["session_marks"], "t_ns", lo, hi)
        side["prompt_rows"] = _cut(src["prompt_rows"], "t_ns", lo, hi, flag="typed")
        side["call_tool_uses"] = _cut(src["call_tool_uses"], "ts_ns", lo, hi)
        # presence: the shown sessions' segments overlapping the range (the timeline reads the bands')
        t = src["presence_segments"]
        sid, a, b = _col(t, "session_id"), _col(t, "from_ns"), _col(t, "to_ns")
        side["presence_segments"] = Table(
            t.columns,
            [r for r in t.rows if r[sid] in shown and (hi is None or r[a] <= hi) and (lo is None or r[b] >= lo)],
        )
        # request_rows: the request log's window, in µs
        t = src["request_rows"]
        ts, us = _col(t, "ts_ns"), (None if lo is None else lo // 1000, None if hi is None else hi // 1000)
        side["request_rows"] = Table(t.columns, [r for r in t.rows if _within(r[ts] // 1000, *us)])
        side["person_last"] = src["person_last"]
    latest = (_latest(want["event_rows"], shown, hi), _latest(got["event_rows"], shown, hi))
    w["latest"], g["latest"] = (Table(("key", "count"), sorted(x.items())) for x in latest)
    return w, g


def _prefix(event_id: str | None) -> str | None:
    if event_id is None:
        return None
    m = _BATCH.match(event_id)
    return m.group(1) if m else ""


def _persons(oracle: Oracle) -> list[str]:
    return [*oracle.person_ids(), "unattributed", "nobody"]


def _compare(layouts, *, per_kind: int) -> dict[str, int]:
    seen: dict[str, int] = {}
    rows: dict[str, int] = {}
    for name, root, dsn in layouts:
        with Oracle(Layout(root)) as oracle, contextlib.closing(PgStore(dsn, threads=1)) as store:
            for pid in _persons(oracle):
                want = oracle.person(pid)
                generated = cuts(want, per_kind)
                ranges = list(URL_RANGES)
                for kind, rs in generated.items():
                    ranges += rs
                    seen[f"cut:{kind}"] = seen.get(f"cut:{kind}", 0) + bool(rs)
                for lo, hi in ranges:
                    loaded = pgstore_person.entity(store, pid, lo, hi)
                    try:
                        got = extract(loaded.con, {**PERSON_TABLES, "event_rows": _GOT_EVENTS}, pid)
                    finally:
                        loaded.con.close()
                    w, g = comparable(want, got, lo, hi)
                    assert_same(w, g, entity=f"{name}: person {pid} [{lo}, {hi}]", unordered=_UNORDERED)
                    seen["ranges"] = seen.get("ranges", 0) + 1
                    for t, v in w.items():
                        rows[t] = rows.get(t, 0) + len(v.rows)
                seen["persons"] = seen.get("persons", 0) + 1
                oracle._memo.clear()  # a person's whole tables per id: don't keep them all
    print("person tables compared:", seen, "rows:", rows)  # noqa: T201 (the gate's coverage, in -s output)
    # Not vacuous: every table holds rows somewhere, and every kind of cut was made.
    assert all(rows.get(t) for t in (*_SCOPE, "event_rows", "session_marks", "prompt_rows", "call_tool_uses",
                                      "presence_segments", "request_rows", "latest")), rows  # fmt: skip
    assert all(seen[f"cut:{k}"] for k in ("prompt", "batch", "session", "latest")), seen
    return seen


# The loaded event_rows: the person's sessions' (the loader reads only theirs).
_GOT_EVENTS = "session_id IN (SELECT session_id FROM person_sessions WHERE person_id = $id)"


def test_every_persons_tables_equal_the_oracles_for_the_url_ranges_and_cuts(layouts):
    seen = _compare(layouts, per_kind=2)
    assert seen["persons"] >= 8, seen


@pytest.mark.full
def test_every_persons_tables_equal_the_oracles_for_many_cuts(layouts):
    _compare(layouts, per_kind=40)
