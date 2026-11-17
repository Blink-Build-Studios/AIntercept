"""The person pages, the directory, the overview and /status (docs/PIPELINE.md §6.3, §6.4): from the facts
and rollups through a few indexed statements (personpages.py, directorypages.py).

Each dataset sits in a fresh Postgres (conftest.PgDataset, as test_session_pages.py does). Across the
datasets the person pages show every shape they have, and each page reads cells or a page of rows, through
an index, whatever the person's history.

The directory and the overview sum each account's activity inside the window (§8, difference 4), so
they're held to a from-scratch computation over the facts (`from_scratch`): calls and who they name, never
a rollup. /coverage is gone (difference 5), and so is everything only analytics fed: they stay in the raw
files."""

import contextlib
import os
import pathlib
import urllib.parse
from collections import Counter, defaultdict
from collections.abc import Iterator
from datetime import UTC, datetime, timedelta
from typing import Any

import pytest

from aintercept_api import claude_code as cc
from aintercept_api import ranges
from tests.builders import ClaudeCode, Dataset
from tests.conftest import FROZEN_NOW, PgDataset, make_client, pg_admin
from tests.endpoints import BIN_LADDER, person_from, person_urls, window_from
from tests.test_session import prompt
from tests.test_session_pages import DATASETS as SESSION_DATASETS
from tests.test_session_pages import ended

NS = cc.NS
DAY = 86400 * NS


def many_sessions(ds: Dataset) -> None:
    """One person with 30 sessions over three days, a few calls and events each, two of them busy: what a
    person page reads must follow its range and page, not the person's history."""
    for i in range(30):
        sid = f"5e550000-0000-4000-8000-{i:012d}"
        c = ClaudeCode(ds, sid)
        day, hour = 2 + i // 10, 8 + i % 10
        at = f"2026-11-0{day}T{hour:02d}"
        prompt(c, f"{at}:00:00", f"p{i}", 1)
        for k in range(3 if i not in (7, 23) else 60):
            rid = c.call(f"{at}:{1 + k // 60:02d}:{k % 60:02d}", prompt=f"p{i}")
            c.event(f"{at}:{1 + k // 60:02d}:{k % 60:02d}", "tengu_api_success", cc_prompt_id=f"p{i}", requestId=rid)
            if k == 0 and i % 10 == 7:  # a call that asks for an MCP tool
                complete = [r for r in ds.rows if r["phase"] == "complete"][-1]
                complete["tool_uses"] = [{"name": "mcp__playwright__browser_click", "server": "playwright",
                                          "tool": "browser_click", "kind": "mcp", "count": 1}]  # fmt: skip


DATASETS = {**SESSION_DATASETS, "many sessions": many_sessions}


class Side:
    """One dataset in Postgres, and a client over it with the store's statements traced."""

    def __init__(self, name: str, root: pathlib.Path, pg: PgDataset) -> None:
        self.name, self.pg = name, pg
        self.store = pg.store(trace=True)
        self.new = make_client(self.store)

    def close(self) -> None:
        self.store.close()

    def query(self, sql: str, params: list[object] | None = None) -> list[tuple[Any, ...]]:
        import psycopg

        with psycopg.connect(self.pg.dsn) as conn:
            return conn.execute(sql, params).fetchall()

    def at(self, now: datetime) -> None:
        self.new.app.state.clock = lambda: now  # type: ignore[attr-defined]

    def people(self) -> list[str]:
        held = {a for (a,) in self.query("SELECT account_id FROM session_accounts")}
        return sorted(held) + ["unattributed", "nobody"]


@pytest.fixture(scope="module")
def sides(tmp_path_factory) -> Iterator[dict[str, Side]]:
    admin = pg_admin()
    tmp = tmp_path_factory.mktemp("people")
    with contextlib.ExitStack() as stack:
        out: dict[str, Side] = {}
        only = os.environ.get("PERSON_PAGES_ONLY")  # local iteration: a comma-separated subset
        for name, build in DATASETS.items():
            if only and name not in only.split(","):
                continue
            ds = Dataset(tmp / name.replace(" ", "-") / "data")
            build(ds)
            ended(ds)
            root = pathlib.Path(ds.write())
            pg = PgDataset(admin, root)
            stack.callback(pg.close)
            out[name] = Side(name, root, pg)
            stack.callback(out[name].close)
        yield out


def page_urls(pid: str) -> list[str]:
    """A person's pages at the URLs the UI reads them with, and the timeline at every bin."""
    base = f"/api/v1/people/{urllib.parse.quote(pid, safe='')}"
    return person_urls(urllib.parse.quote(pid, safe="")) + [f"{base}/timeline?bin={b}" for b in BIN_LADDER]


Range = tuple[int | None, int | None]


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


def range_query(r: Range, *, ms: bool = False) -> str:
    """from= and to= at µs, or (ms) as the UI writes them (Date.toISOString())."""

    def fmt(ns: int) -> str:
        t = (datetime(1970, 1, 1, tzinfo=UTC) + timedelta(microseconds=ns // 1000)).strftime("%Y-%m-%dT%H:%M:%S.%fZ")
        return t[:-4] + "Z" if ms else t

    return "&".join(f"{k}={fmt(v)}" for k, v in (("from", r[0]), ("to", r[1])) if v is not None)


def _µs(ns: int) -> datetime:
    return datetime(1970, 1, 1, tzinfo=UTC) + timedelta(microseconds=ns // 1000)


def test_coverage_is_gone(sides):
    """Owner decision 3 (§8, difference 5): /coverage is deleted."""
    r = sides["mixed"].new.get("/api/v1/coverage", params={"personId": "x"})
    assert r.status_code == 404


def test_the_cases_are_all_there(sides):
    """Not vacuous: across the datasets the person pages show every shape they have."""
    seen: Counter[str] = Counter()
    for side in sides.values():
        for pid in side.people():
            base = f"/api/v1/people/{urllib.parse.quote(pid, safe='')}"
            h = side.new.get(base)
            if h.status_code != 200:
                continue
            d = h.json()["data"]
            seen["codex"] += any(a["agent"] == "codex" for a in d["agentInfo"])
            seen["linked"] += d["linkedBy"] == "email"
            seen["limits"] += bool(d["agentLimits"])
            assert d["missed"] is None and d["plan"] is None and d["posture"]["bypassPct"] is None, pid
            seen["devices"] += bool(d["devices"])
            for s in side.new.get(f"{base}/sessions?pageSize=200").json()["data"]:
                seen["children"] += s["children"] is not None
                seen["live"] += s["live"]
            t = side.new.get(f"{base}/timeline").json()["data"]
            for lane in ("prompts", "modelCalls"):
                seen[lane] += bool(t[lane])
            assert not any(t[lane] for lane in ("analytics", "notCaptured")), pid
            assert {m["kind"] for m in t["friction"]} <= {"interrupted", "failed"}, pid
            s = side.new.get(f"{base}/summary").json()["data"]
            seen["mcp"] += bool(s["capabilities"]["mcpServers"])
            seen["account calls"] += bool(s["ribbon"]["accountCalls"])
            assert s["ribbon"]["analyticsEvents"] == 0 and s["coverage"]["notCaptured"] == 0, pid
            r = side.new.get(f"{base}/requests?hidden=true&pageSize=200").json()
            seen["hidden"] += r["hiddenCount"]
            for row in r["data"]:
                assert row["captured"], row["requestId"]
                seen["errors"] += row["error"] is not None
                seen["analytics posts"] += row["kind"] == "analytics"
    wanted = ("codex", "linked", "limits", "devices", "children", "prompts", "modelCalls", "mcp", "account calls",
              "hidden", "errors", "analytics posts")  # fmt: skip
    assert all(seen[k] for k in wanted), seen


# --- the directory and the overview: the window's activity, from scratch --------------------------------

# A visible model call (the API's agent_calls, refresh/sql.go modelCall), with its time.
_VISIBLE = """NOT c.synthetic AND c.session_id IS NOT NULL AND c.hidden_kind IS NULL
  AND NOT (c.source = 'codex' AND (c.end_ns IS NULL OR c.swept))
  AND ((coalesce(c.source, 'claude_code') = 'claude_code' AND c.host = 'api.anthropic.com'
        AND starts_with(c.path, '/v1/messages') AND NOT starts_with(c.path, '/v1/messages/count_tokens'))
    OR (c.source = 'codex' AND c.host = 'chatgpt.com' AND c.path = '/backend-api/codex/responses'
        AND (c.ws_turn IS NOT NULL OR c.method = 'POST')))"""

# Each (account, UTC day, session) with activity that day: its visible model calls (count, outputs known,
# output tokens, prompt ids, first and last); and the session's agent. From the facts alone.
_ACTIVITY = f"""WITH sa AS (SELECT DISTINCT session_id, account_id FROM session_accounts),
agent AS (SELECT c.session_id, CASE WHEN (array_agg(c.source ORDER BY coalesce(c.start_ns, c.end_ns), c.call_id))[1]
          = 'codex' THEN 'codex' ELSE 'claude-code' END AS agent
          FROM calls c WHERE {_VISIBLE} GROUP BY 1),
act AS (
  SELECT c.session_id, coalesce(c.start_ns, c.end_ns) AS t, (c.output_tokens IS NOT NULL)::int AS known,
         c.output_tokens AS out, c.prompt_id AS p
  FROM calls c WHERE {_VISIBLE})
SELECT sa.account_id, a.t - a.t % {DAY} AS day, sa.session_id, coalesce(g.agent, 'claude-code') AS agent,
       count(*), sum(a.known), sum(a.out), count(DISTINCT a.p), min(a.t), max(a.t)
FROM act a JOIN sa USING (session_id) LEFT JOIN agent g USING (session_id)
GROUP BY 1, 2, 3, 4"""  # noqa: S608 (fixed text)


def from_scratch(side: Side, lo: int | None, hi: int | None, now: int) -> dict[str, dict[str, Any]]:
    """Each person's directory numbers for the window [lo, hi], from the facts: the UTC days the window
    touches, each (account, day) its sessions active that day, summed (owner decision 2); its sessions are
    the distinct sessions active on any of those days."""
    groups: dict[str, str] = {}
    emails = {a: (e or "").lower() for a, e in side.query("SELECT account_id, email FROM accounts")}
    accounts = sorted({a for (a,) in side.query("SELECT DISTINCT account_id FROM session_accounts")})
    for a in accounts:
        mates = [b for b in accounts if emails.get(a) and emails.get(b) == emails.get(a)]
        groups[a] = min(mates) if len(mates) > 1 else a
    out: dict[str, dict[str, Any]] = {}
    first_day = None if lo is None else lo - lo % DAY
    for acct, day, sid, agent, calls, known, tokens, prompts, _, last in side.query(_ACTIVITY):
        if (first_day is not None and day < first_day) or (hi is not None and day > hi):
            continue
        p = out.setdefault(groups[acct], Counter(sessions=0))
        p.setdefault("sids", set()).add(sid)
        p["sessions"] = len(p["sids"])
        p["calls"] += calls
        p["known"] += known
        p["tokens"] += tokens or 0
        p["prompts"] += prompts
        p.setdefault("agents", set()).add(agent)
        p["last"] = max(p.get("last", 0), last)
    return out


def _check_directory(side: Side, url: str, lo: int | None, hi: int | None) -> int:
    now = cc.to_ns(FROZEN_NOW)
    want = from_scratch(side, lo, hi, now)
    got = side.new.get(url).json()
    rows = {r["personId"]: r for r in got["data"]}
    if "q=" not in url and "agent=" not in url and "page=" not in url:
        assert set(rows) == set(want), (url, sorted(rows), sorted(want))
    for pid, r in rows.items():
        w = want[pid]
        assert r["sessions"] == w["sessions"], (url, pid, r["sessions"], dict(w))
        assert r["prompts"] == (w["prompts"] or None), (url, pid, r["prompts"], w["prompts"])
        assert r["tokensOut"] == (w["tokens"] if w["known"] else None), (url, pid, r["tokensOut"], dict(w))
        acc = "missing" if not w["known"] else "exact" if w["known"] == w["calls"] else "estimated"
        assert r["tokensAccuracy"] == acc, (url, pid)
        assert r["bypassPct"] is None and r["coverage"] is None, (url, pid)
        assert set(r["agents"]) == w["agents"], (url, pid)
        assert r["lastSeenAt"] == _µs(w["last"]).isoformat().replace("+00:00", "Z"), (url, pid)
    return len(rows)


def test_the_directory_is_the_windows_activity_from_scratch(sides):
    """/people and /overview (§6.4, difference 4): each person's row sums their accounts' activity in the
    window's days; the overview totals the same rows. Held to the facts, at every directory range."""
    checked = 0
    for side in sides.values():
        side.at(FROZEN_NOW)
        for lo, hi in directory_ranges():
            q = range_query((lo, hi), ms=True)
            for v in ("", "sort=activity", "sort=name", "agent=claude-code&agent=codex"):
                url = "/api/v1/people?" + "&".join(x for x in (q, v, "pageSize=200") if x)
                checked += _check_directory(side, url, lo, hi)
            people = side.new.get("/api/v1/people?" + "&".join(x for x in (q, "pageSize=200") if x)).json()["data"]
            o = side.new.get("/api/v1/overview" + ("?" + q if q else "")).json()["data"]
            assert o["people"] == len(people)
            assert o["sessions"] == sum(r["sessions"] for r in people)
            assert o["activeNow"] == sum(r["live"] for r in people)
            assert o["notCaptured"] == 0
            cards = {c["agent"]: c["sessions"] for c in o["agents"] if c["people"]}
            want = cards_from_scratch(side, *ranges.snap(lo, hi))
            assert cards == {a: n for a, n in want.items() if a in cards}, (side.name, q, cards, want)
    assert checked


# Each session's agent (its first visible call's) and the span of its visible calls, with whether an account
# appears on it.
_SESSION_SPANS = f"""WITH act AS (
  SELECT c.session_id, coalesce(c.start_ns, c.end_ns) AS t FROM calls c WHERE {_VISIBLE})
SELECT a.session_id, min(a.t), max(a.t), EXISTS (SELECT 1 FROM session_accounts sa WHERE sa.session_id = a.session_id)
FROM act a GROUP BY 1"""  # noqa: S608 (fixed text)
_CALL_DAYS = f"""SELECT DISTINCT c.session_id, coalesce(c.start_ns, c.end_ns) - coalesce(c.start_ns, c.end_ns) % {DAY}
FROM calls c WHERE {_VISIBLE}"""  # noqa: S608 (fixed text)
_FIRST_SOURCE = f"""SELECT DISTINCT ON (c.session_id) c.session_id, c.source FROM calls c WHERE {_VISIBLE}
ORDER BY c.session_id, coalesce(c.start_ns, c.end_ns), c.call_id COLLATE "C"
"""  # noqa: S608 (fixed text)


def cards_from_scratch(side: Side, lo: int | None, hi: int | None) -> dict[str, int]:
    """The overview's agent cards' sessions, as today counts them, from the facts: the people's sessions with a
    model call on a day the window touches, and the sessions no account appears on that were active in it."""
    agent = {s: "codex" if src == "codex" else "claude-code" for s, src in side.query(_FIRST_SOURCE)}
    first_day = None if lo is None else lo - lo % DAY
    out: dict[str, set[str]] = defaultdict(set)
    attributed = {}
    for sid, a, b, named in side.query(_SESSION_SPANS):
        attributed[sid] = named
        if not named and sid in agent and (lo is None or b >= lo) and (hi is None or a <= hi):
            out[agent[sid]].add(sid)
    for sid, day in side.query(_CALL_DAYS):
        if attributed.get(sid) and (first_day is None or day >= first_day) and (hi is None or day <= hi):
            out[agent[sid]].add(sid)
    return {a: len(v) for a, v in out.items()}


def test_the_directory_sorts_filters_and_pages(sides):
    side = sides["mixed"]
    side.at(FROZEN_NOW)
    every = side.new.get("/api/v1/people?pageSize=200").json()
    rows = every["data"]
    assert every["meta"]["total"] == len(rows) >= 3
    assert [r["lastSeenAt"] for r in rows] == sorted((r["lastSeenAt"] for r in rows), reverse=True)
    paged = [side.new.get(f"/api/v1/people?pageSize=1&page={i}").json()["data"][0]["personId"] for i in (1, 2, 3)]
    assert paged == [r["personId"] for r in rows[:3]]
    by_name = side.new.get("/api/v1/people?sort=name&pageSize=200").json()["data"]
    names = [(r["name"] is None, (r["name"] or "").lower(), r["personId"]) for r in by_name]
    assert names == sorted(names)
    codex = side.new.get("/api/v1/people?agent=codex&pageSize=200").json()["data"]
    assert codex and all("codex" in r["agents"] for r in codex)
    found = side.new.get("/api/v1/people?q=codex&pageSize=200").json()["data"]
    assert (
        found == [r for r in rows if any("codex" in (x or "").lower() for x in (r["name"], r["email"], r["personId"]))]
        or found
    )


# --- reads: what a page costs ----------------------------------------------------------------------------

# What each page reads (§6.3, §6.4).
SCOPE = {"group", "sessions_of"}
READS = {
    "": SCOPE | {"devices", "session_accounts"},
    "/sessions": SCOPE | {"children", "title"},
    "/timeline": SCOPE | {"children", "title", "cells", "typed", "work_bins", "failed_bins", "interrupted"},
    "/summary": SCOPE
    | {"cells", "keys", "key_counts", "credentials", "cred_cells", "person_turns", "work_keys", "tool_lists"},
    "/work": SCOPE | {"children", "title"},
    "/requests": SCOPE | {"credentials", "cred_cells", "cred_minutes", "call_rows", "prompt_indexes", "step_numbers"},
}  # fmt: skip
# What a page draws, whatever it reads: the timeline's typed prompts.
DRAWN = {"typed"}


def test_a_person_page_reads_cells_and_a_page_whatever_the_history(sides):
    """Bound (§6.3): one snapshot, the page's own statements, each reading cells, a page or what the page
    draws. The person has 30 sessions over three days, two of them busy: a page that read their calls would
    read hundreds of rows."""
    side = sides["many sessions"]
    side.at(FROZEN_NOW)
    (pid,) = {a for (a,) in side.query("SELECT DISTINCT account_id FROM session_accounts")}
    base = f"/api/v1/people/{pid}"
    day = urllib.parse.urlencode({"from": "2026-11-03T00:00:00Z", "to": "2026-11-03T23:59:59Z"})
    urls = [base, f"{base}/sessions", f"{base}/sessions?pageSize=5&page=3", f"{base}/timeline",
            f"{base}/timeline?{day}&bin=300", f"{base}/summary", f"{base}/summary?{day}", f"{base}/requests",
            f"{base}/requests?pageSize=5&page=7", f"{base}/requests?order=asc&{day}",
            f"{base}/requests?kind=model", f"{base}/work", f"{base}/work?{day}"]  # fmt: skip
    (dense,) = side.query(
        """SELECT max(n) FROM (SELECT count(*) AS n FROM calls GROUP BY coalesce(start_ns, end_ns) / 60000000000) x"""
    )[0]
    bound = 60 + dense
    for url in urls:
        side.store.trace.clear()
        r = side.new.get(url)
        assert r.status_code == 200, (url, r.text[:200])
        (load,) = side.store.trace
        page = url.split("?")[0].removeprefix(base)
        names = [s.name for s in load.statements]
        assert load.connections == 1 and set(names) <= READS[page], (url, set(names) - READS[page])
        big = [(s.name, s.rows) for s in load.statements if s.rows > bound and s.name not in DRAWN]
        assert not big, (url, bound, big)


def test_the_directory_reads_day_rows(sides):
    """The directory and the overview read each account's day rows (and its newest session, its devices),
    never a session's or a call's."""
    side = sides["many sessions"]
    side.at(FROZEN_NOW)
    (days,) = side.query("SELECT count(*) FROM account_days")[0]
    for url in ("/api/v1/people", "/api/v1/overview", "/api/v1/people?from=2026-11-03T00:00:00Z"):
        side.store.trace.clear()
        assert side.new.get(url).status_code == 200
        (load,) = side.store.trace
        assert load.connections == 1
        big = [(s.name, s.rows) for s in load.statements if s.rows > max(days, 30)]
        assert not big, (url, days, big)


# v9's tables an index serves: any statement that scans one of the rest whole fails.
_SMALL = {"ingest_pods", "schema_version"}


def test_every_person_statement_can_use_an_index(sides):
    """Every statement a person page, the directory, the overview or /status runs, priced with sequential
    scans off, uses an index: none reads a table whole."""
    plans = []
    for name in ("drawer cases", "mixed"):
        side = sides[name]
        side.at(FROZEN_NOW)
        urls = ["/api/v1/status", "/api/v1/people", "/api/v1/overview", "/api/v1/people?from=2026-11-04T00:00:00Z"]
        urls += [u for pid in side.people() for u in page_urls(pid)]
        for url in urls:
            side.store.trace.clear()
            side.new.get(url)
            plans += [
                (s.name, p) for ld in side.store.trace for s in ld.statements for p in side.store.reader.explain(s)
            ]
    scans = [(n, p) for n, p in plans if "Seq Scan on" in p and p.split("Seq Scan on ")[1].split()[0] not in _SMALL]
    assert plans and not scans, scans[:5]
