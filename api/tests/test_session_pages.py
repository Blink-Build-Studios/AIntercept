"""The session pages (docs/PIPELINE.md §6.1): /sessions/{id} and its five tabs, from the facts and rollups
through a few indexed statements (sessionpages.py).

Each dataset sits in a fresh Postgres (conftest.PgDataset, as test_drawer.py's do). Across the datasets
the pages show every shape they have, and each page reads cells or a page of rows, through an index,
whatever the session's size, at the URLs the pages are read with: the defaults, a range, pages past the
first, each filter and `q`, a prompt number, both orders, and bins under an hour and over one."""

import contextlib
import pathlib
import urllib.parse
from collections import Counter
from collections.abc import Iterator

import pytest

from aintercept_api.timeutil import ns_to_dt
from tests.builders import Codex, Dataset, end_batches
from tests.conftest import PgDataset, pg_admin
from tests.test_codex import codex_work
from tests.test_drawer import SIDES, Side

PAGES = ("", "/timeline", "/state", "/prompts", "/turns", "/requests", "/events")


def codex_threads(ds: Dataset) -> None:
    """Codex threads: a thread with its subagent, review and fork threads, and its client's calls no thread
    names (usage and catalog, in its window and after it); a thread a title thread names (its label)."""
    root = codex_work(ds)
    root.turn(
        "2026-11-04T15:56:10", "turn-2", tool_uses=[{"name": "exec", "tool": "exec", "kind": "builtin", "count": 2}]
    )
    root.turn("2026-11-04T15:56:20", "turn-2", output_tokens=None)
    root.other("2026-11-04T15:55:00", "/backend-api/wham/usage")
    root.other("2026-11-04T15:55:01", "/backend-api/ps/plugins/list")
    root.other("2026-11-04T17:00:00", "/backend-api/wham/usage")
    t = Codex(ds, "01a1d00d-0000-7000-8000-0000000title")
    t.post("2026-11-04T18:00:00", "turn-x", {"input": []}, [])
    Codex(ds, "01a1c1ec-3e49-71b3-b32b-55a748502bab").post(
        "2026-11-04T18:00:01",
        "",
        {"input": []},
        [{"type": "response.output_item.done",
          "item": {"type": "message", "content": [{"type": "output_text", "text": "Fix flaky timer test"}]}}],
        source="thread_title",
    )  # fmt: skip


DATASETS = {**SIDES, "codex threads": codex_threads}


def ended(ds: Dataset) -> None:
    """End every analytics post still in flight (builders.end_batches): v9 learns a post's session (its
    batch's) when it ends. Its complete row and its events carry its credential, as the observer's do: a
    session with no model call of its own lists the posts that carried its events (today's credential
    of the session is its events'; v9's the posts', the same one)."""
    end_batches(ds)
    held = {
        r["correlation_id"]: r["key_hash"] for r in ds.rows if r.get("phase") == "request_headers" and r.get("key_hash")
    }
    for r in ds.rows:
        if r.get("phase") in ("complete", "client_event") and "key_hash" not in r and r["correlation_id"] in held:
            r["key_hash"] = held[r["correlation_id"]]


@pytest.fixture(scope="module")
def sides(tmp_path_factory) -> Iterator[dict[str, Side]]:
    admin = pg_admin()
    tmp = tmp_path_factory.mktemp("sessions")
    with contextlib.ExitStack() as stack:
        out: dict[str, Side] = {}
        for name, build in DATASETS.items():
            ds = Dataset(tmp / name.replace(" ", "-") / "data")
            build(ds)
            ended(ds)
            root = pathlib.Path(ds.write())
            pg = PgDataset(admin, root)
            stack.callback(pg.close)
            out[name] = Side(name, root, pg)
            stack.callback(out[name].close)
        yield out


def session_ids(side: Side) -> list[str]:
    """Every session: every one with a row, and every one an event names."""
    held = {s for (s,) in side.query("SELECT session_id FROM sessions")}
    held |= {s for (s,) in side.query("SELECT DISTINCT session_id FROM events WHERE session_id <> ''")}
    return sorted(held)


def _iso(ns: int) -> str:
    d = ns_to_dt(ns)
    assert d is not None
    return d.isoformat()


def session_urls(side: Side, sid: str) -> list[str]:
    """A session's pages at the URLs the UI reads them with, and a brush in the middle of its span."""
    base = f"/api/v1/sessions/{urllib.parse.quote(sid, safe='')}"
    urls = [base + p for p in PAGES]
    urls += [
        f"{base}/timeline?bin=60",
        f"{base}/timeline?bin=3600",
        f"{base}/timeline?bin=420",
        f"{base}/prompts?pageSize=2&page=2",
        f"{base}/prompts?pageSize=1&prompt=2",
        f"{base}/prompts?pageSize=3&page=99",
        f"{base}/requests?pageSize=5&page=2",
        f"{base}/requests?pageSize=3&page=2&order=desc",
        f"{base}/requests?hidden=true",
        f"{base}/requests?captured=false",
        f"{base}/requests?captured=true&kind=model",
        f"{base}/requests?kind=analytics&kind=usage&pageSize=4",
        f"{base}/requests?q=model",
        f"{base}/requests?q=kind%3Danalytics",
        f"{base}/requests?q=status%3DERR",
        f"{base}/events?internal=true&pageSize=7&page=2",
        f"{base}/events?q=tool&pageSize=10",
        f"{base}/events?pageSize=3&page=3",
        f"{base}/events?pageSize=20&page=99",
        f"{base}/prompts?pageSize=2&prompt=3",
        f"{base}/prompts?pageSize=20&prompt=999",
        f"{base}/turns?pageSize=2&page=2",
        f"{base}/turns?pageSize=1&turn=2",
        f"{base}/requests?kind=flags&kind=count&kind=metrics&kind=catalog&pageSize=20",
        f"{base}/requests?captured=false&hidden=true",
        f"{base}/requests?pageSize=20&page=99",
    ]
    span = side.query("SELECT start_ns, last_ns FROM sessions WHERE session_id = %s", [sid])
    if span:
        lo, hi = span[0]
        mid = (lo + hi) // 2
        brush = urllib.parse.urlencode({"from": _iso(mid), "to": _iso(hi)})
        early = urllib.parse.urlencode({"from": _iso(lo), "to": _iso(mid)})
        urls += [
            f"{base}/state?{brush}",
            f"{base}/state?{early}",
            f"{base}/prompts?{brush}",
            f"{base}/requests?{brush}",
            f"{base}/requests?{early}&order=desc&pageSize=2&page=2",
            f"{base}/events?{brush}&pageSize=5&page=2",
        ]
    return urls


def test_the_cases_are_all_there(sides):
    """Not vacuous: across the datasets the pages show every shape they have."""
    seen: Counter[str] = Counter()
    for side in sides.values():
        for sid in session_ids(side):
            base = f"/api/v1/sessions/{urllib.parse.quote(sid, safe='')}"
            h = side.new.get(base).json()["data"]
            seen["codex"] += h["agent"] == "codex"
            seen["children"] += h["children"] is not None
            seen["origin"] += h["origin"] is not None
            seen["titled"] += h["label"]["source"] == "title"
            seen["limits"] += h["limitsAtEnd"] is not None
            seen["not captured"] += h["coverage"]["notCaptured"] > 0
            t = side.new.get(f"{base}/timeline").json()["data"]
            for lane in ("prompts", "modelCalls", "analytics", "notCaptured", "friction", "context", "compactions"):
                seen[lane] += bool(t[lane])
            for item in side.new.get(f"{base}/prompts?pageSize=200").json()["data"]:
                seen[item["type"]] += 1
                seen["steps"] += len(item.get("steps") or [])
            r = side.new.get(f"{base}/requests?hidden=true&pageSize=200").json()
            seen["hidden"] += r["hiddenCount"]
            for row in r["data"]:
                seen["missed"] += not row["captured"]
                seen["sessionless"] += row["sessionId"] is None
                seen["errors"] += row["error"] is not None
                seen["retried"] += bool(row["error"] and row["error"]["retries"])
    wanted = ("codex", "children", "origin", "titled", "limits", "not captured", "prompts", "modelCalls",
              "analytics", "notCaptured", "friction", "context", "compactions", "prompt", "between", "steps",
              "hidden", "missed", "sessionless", "errors", "retried")  # fmt: skip
    assert all(seen[k] for k in wanted), seen


# What each session page reads (§6.1): its `sessions` row (and its children and, for a Codex thread
# without a title of its own, its title call), then the page's statements.
SESSION = {"session", "children", "title"}
READS = {
    "": SESSION,
    "/timeline": SESSION
    | {
        "cells",
        "minutes",
        "names",
        "typed",
        "window",
        "friction_minutes",
        "friction",
        "context",
        "compactions",
        "turn_blocks",
        "work_keys",
    },  # fmt: skip
    "/state": SESSION | {"cells", "item_counts", "keys", "heads", "recaps", "latest", "credentials", "extra_rows"},
    "/prompts": SESSION | {"item_span", "prompt_at", "items", "steps"},
    "/turns": SESSION | {"turn_count", "turn_at", "turns", "turn_steps", "threads"},
    "/requests": SESSION
    | {
        "credentials",
        "cells",
        "minutes",
        "extra_rows",
        "call_rows",
        "missed_rows",
        "reports",
        "prompt_indexes",
        "step_numbers",
        "retries",
        "next_attempt",
    },  # fmt: skip
    "/events": SESSION | {"cells", "minutes", "event_counts", "event_page", "linked", "prompt_indexes"},
}
# What a page draws, whatever it reads: the timeline's context points (one per main-thread call) and
# typed prompts, and a page of prompts' steps.
DRAWN = {"context", "typed", "steps", "turn_blocks", "turn_steps"}


def test_a_session_page_reads_cells_and_a_page_whatever_the_session(sides):
    """Bound (§6.1): one snapshot, the page's own statements, each reading cells or a page of rows; what
    grows with the session is only what the page draws. The session holds a 120-step prompt and a few
    hundred events: a page that read its events or calls would read hundreds of rows."""
    side = sides["big prompt"]
    (sid,) = side.query("SELECT session_id FROM sessions ORDER BY events DESC NULLS LAST LIMIT 1")[0]
    (events, calls) = side.query("SELECT events, model_calls FROM sessions WHERE session_id = %s", [sid])[0]
    assert events > 300 and calls >= 120, (events, calls)
    base = f"/api/v1/sessions/{sid}"
    # A page's rows are found by minute: it reads at most a page and its densest minute's rows.
    (dense,) = side.query(
        """SELECT max(n) FROM (SELECT count(*) AS n FROM calls WHERE scope_session_id = %s
           GROUP BY coalesce(start_ns, end_ns) / 60000000000) x""",
        [sid],
    )[0]
    bound = 60 + dense
    for url in session_urls(side, sid):
        side.store.trace.clear()
        r = side.new.get(url)
        assert r.status_code == 200, (url, r.text[:200])
        (load,) = side.store.trace
        page = "/" + url.split("?")[0].removeprefix(base).strip("/") if url.split("?")[0] != base else ""
        page = "" if page == "/" else page
        names = [s.name for s in load.statements]
        assert load.connections == 1 and set(names) <= READS[page], (url, names)
        if "q=" in url:  # text search scans the session's rows in the range (§6.1)
            continue
        big = [(s.name, s.rows) for s in load.statements if s.rows > bound and s.name not in DRAWN]
        assert not big, (url, bound, big)


# v9's tables an index serves: any statement that scans one of the rest whole fails.
_SMALL = {"ingest_pods", "schema_version"}


def test_every_session_statement_can_use_an_index(sides):
    """Every statement a session page runs, priced with sequential scans off, uses an index: none reads a
    table whole."""
    plans = []
    for name in ("drawer cases", "mixed"):
        side = sides[name]
        for sid in session_ids(side):
            for url in session_urls(side, sid):
                side.store.trace.clear()
                side.new.get(url)
                plans += [
                    (s.name, p) for ld in side.store.trace for s in ld.statements for p in side.store.reader.explain(s)
                ]
    scans = [(n, p) for n, p in plans if "Seq Scan on" in p and p.split("Seq Scan on ")[1].split()[0] not in _SMALL]
    assert plans and not scans, scans[:5]
