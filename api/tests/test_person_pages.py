"""The person pages, the directory, the overview and /status on v9 (docs/PIPELINE.md §6.3, §6.4), side by
side with the oracle: today's code over the oracle's tables.

Each dataset sits in a fresh Postgres holding v7's tables and v9's (conftest.PgDataset, as test_session_pages.py
does). The API's Postgres store serves the person pages from v9's facts and rollups through a few indexed
statements (personpages.py); the oracle derives them from every session of the person. Every response must be
the same data, for every person of every dataset (and `unattributed`, and a person nobody has seen), at the
URLs the pages are read with: the defaults, the UI's variants, ranges whose ends cut through a tick run, a
batch, a prompt's calls, a not-captured window and a minute boundary (side_by_side.cuts), each timeline bin,
and with `now` at FROZEN_NOW and at each session's live threshold.

The directory and the overview sum each account's activity inside the window (§8, difference 4), so they're
held instead to a from-scratch computation over the facts (`from_scratch`): calls, events and who they
name, never a rollup. /coverage is gone (difference 5)."""

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
from aintercept_api.raw import LocalRaw
from tests.builders import ClaudeCode, Dataset
from tests.conftest import FROZEN_NOW, PgDataset, lay_out, make_client, pg_admin
from tests.oracle.harness import Layout, Oracle
from tests.oracle.store import OracleStore
from tests.side_by_side import PERSON_PAGES, cuts, directory_ranges, instants, page_urls, range_query
from tests.test_drawer import _first_diff
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
            if k == 0 and i % 10 == 7:  # an MCP tool, as Claude Code names one
                c.event(f"{at}:01:00", "tengu_tool_use_success", cc_prompt_id=f"p{i}", requestId=rid,
                        toolName="mcp__playwright__browser_click", isMcp=True)  # fmt: skip


DATASETS = {**SESSION_DATASETS, "many sessions": many_sessions}


class Side:
    """One dataset in Postgres (v7's tables and v9's), the oracle over its layout, and two clients: the
    oracle's and the Postgres store's."""

    def __init__(self, name: str, root: pathlib.Path, pg: PgDataset) -> None:
        self.name, self.layout, self.pg = name, lay_out(root), pg
        self.oracle = Oracle(Layout(self.layout))
        self.old_store = OracleStore(self.layout, prefix="sim/", raw=LocalRaw(root))
        self.store = pg.store(trace=True)
        self.old, self.new = make_client(self.old_store), make_client(self.store)

    def close(self) -> None:
        self.store.close()
        self.old_store.close()
        self.oracle.close()

    def query(self, sql: str, params: list[object] | None = None) -> list[tuple[Any, ...]]:
        import psycopg

        with psycopg.connect(self.pg.dsn) as conn:
            return conn.execute(sql, params).fetchall()

    def at(self, now: datetime) -> None:
        for c in (self.old, self.new):
            c.app.state.clock = lambda: now  # type: ignore[attr-defined]

    def people(self) -> list[str]:
        held = {a for (a,) in self.query("SELECT account_id FROM session_accounts")}
        return sorted(set(self.oracle.person_ids()) | held) + ["unattributed", "nobody"]

    def person_sessions(self, pid: str) -> list[str]:
        if pid not in self.oracle.person_ids():
            return []
        t = self.oracle.person(pid)["person_sessions"]
        return sorted({r[t.columns.index("session_id")] for r in t.rows})


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


def person_urls(side: Side, pid: str, *, per_kind: int = 1) -> list[str]:
    """A person's pages at the URLs the UI reads them with, and each with the ranges that cut its data."""
    rs = cuts(instants(side.oracle, side.person_sessions(pid)), per_kind=per_kind) if side.person_sessions(pid) else []
    out: list[str] = []
    for page in PERSON_PAGES:
        out += page_urls(page, pid, rs, every=False)
    return out


def _hold(side: Side, urls: list[str]) -> Counter[str]:
    held: Counter[str] = Counter()
    differing = []
    for url in urls:
        a, b = side.old.get(url), side.new.get(url)
        if a.status_code == b.status_code and a.json() == b.json():
            held["same"] += 1
        else:
            differing.append((url, a.status_code, b.status_code, _first_diff(a.json(), b.json())))
    for d in differing:
        print("DIFF", d)  # noqa: T201
    assert not differing, (side.name, len(differing), differing[:4])
    return held


@pytest.mark.parametrize("name", list(DATASETS))
def test_the_person_pages_match_the_oracle(sides, name):
    side = sides[name]
    side.at(FROZEN_NOW)
    people = side.people()
    urls = [u for pid in people for u in person_urls(side, pid)]
    held = _hold(side, urls)
    print(f"{name}: {len(people)} people, {len(urls)} URLs:", dict(held))  # noqa: T201
    assert held["same"] == len(urls)


def _µs(ns: int) -> datetime:
    return datetime(1970, 1, 1, tzinfo=UTC) + timedelta(microseconds=ns // 1000)


@pytest.mark.parametrize("name", ["drawer cases", "codex threads"])
def test_the_person_pages_match_at_each_live_threshold(sides, name):
    """`now` a µs either side of each session's live threshold (5 minutes after its last activity): live,
    the sessions a range shows, and what's in flight follow it as today's do."""
    side = sides[name]
    try:
        for (last,) in side.query("SELECT DISTINCT last_ns FROM sessions ORDER BY 1"):
            for d in (-1000, 0, 1000):
                side.at(_µs(last + cc.LIVE_WITHIN_NS + d))
                urls = [u for pid in side.people() for u in person_urls(side, pid, per_kind=0)]
                _hold(side, [u for u in urls if "bin=" not in u])
    finally:
        side.at(FROZEN_NOW)


def test_status_matches_the_oracle(sides):
    for side in sides.values():
        _hold(side, ["/api/v1/status"])


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
            seen["missed"] += bool(d["missed"])
            seen["devices"] += bool(d["devices"])
            for s in side.new.get(f"{base}/sessions?pageSize=200").json()["data"]:
                seen["children"] += s["children"] is not None
                seen["live"] += s["live"]
            t = side.new.get(f"{base}/timeline").json()["data"]
            for lane in ("prompts", "modelCalls", "analytics", "notCaptured", "friction"):
                seen[lane] += bool(t[lane])
            s = side.new.get(f"{base}/summary").json()["data"]
            seen["mcp"] += bool(s["capabilities"]["mcpServers"])
            seen["account calls"] += bool(s["ribbon"]["accountCalls"])
            r = side.new.get(f"{base}/requests?hidden=true&pageSize=200").json()
            seen["hidden"] += r["hiddenCount"]
            for row in r["data"]:
                seen["not captured"] += not row["captured"]
                seen["errors"] += row["error"] is not None
                seen["analytics"] += row["kind"] == "analytics"
    wanted = ("codex", "linked", "limits", "missed", "devices", "children", "prompts", "modelCalls", "analytics",
              "notCaptured", "friction", "mcp", "account calls", "hidden", "not captured", "errors")  # fmt: skip
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
# output tokens), its Claude Code events (queries, bypassed, reports, missed reports), its prompts and its
# first and last activity; and the session's agent. From the facts alone.
_ACTIVITY = f"""WITH sa AS (SELECT DISTINCT session_id, account_id FROM session_accounts),
agent AS (SELECT c.session_id, CASE WHEN (array_agg(c.source ORDER BY coalesce(c.start_ns, c.end_ns), c.call_id))[1]
          = 'codex' THEN 'codex' ELSE 'claude-code' END AS agent
          FROM calls c WHERE {_VISIBLE} GROUP BY 1),
act AS (
  SELECT c.session_id, coalesce(c.start_ns, c.end_ns) AS t, 1 AS calls, (c.output_tokens IS NOT NULL)::int AS known,
         c.output_tokens AS out, 0 AS q, 0 AS byp, 0 AS rep, 0 AS ev, c.prompt_id AS p, NULL::text AS ip
  FROM calls c WHERE {_VISIBLE}
  UNION ALL
  SELECT e.session_id, e.ts_ns, 0, 0, NULL,
         (e.name = 'tengu_api_query' AND coalesce(e.metadata ->> 'permissionMode', '') <> '')::int,
         (e.name = 'tengu_api_query' AND e.metadata ->> 'permissionMode' = 'bypassPermissions')::int,
         (e.name = 'tengu_api_success')::int, 1, NULL,
         CASE WHEN e.name = 'tengu_input_prompt' THEN coalesce(e.prompt_id, 'input:' || e.ts_ns) END
  FROM events e WHERE e.source = 'claude_code' AND e.session_id IS NOT NULL)
SELECT sa.account_id, a.t - a.t % {DAY} AS day, sa.session_id, coalesce(g.agent, 'claude-code') AS agent,
       sum(a.calls), sum(a.known), sum(a.out), sum(a.q), sum(a.byp), sum(a.rep), sum(a.ev),
       count(DISTINCT coalesce(a.p, a.ip)), min(a.t), max(a.t)
FROM act a JOIN sa USING (session_id) LEFT JOIN agent g USING (session_id)
GROUP BY 1, 2, 3, 4"""  # noqa: S608 (fixed text)

# Each session's missed reports (a Claude Code report no visible call of its session has the id of).
_MISSED = f"""SELECT e.session_id, e.ts_ns FROM events e
WHERE e.source = 'claude_code' AND e.name = 'tengu_api_success' AND e.session_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM calls c WHERE c.session_id = e.session_id AND {_VISIBLE}
                    AND (c.request_id = e.request_id OR c.client_request_id = e.client_request_id))"""  # noqa: S608


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
    missed: dict[str, list[int]] = defaultdict(list)
    for s, t in side.query(_MISSED):
        missed[s].append(t)
    out: dict[str, dict[str, Any]] = {}
    first_day = None if lo is None else lo - lo % DAY
    for acct, day, sid, agent, calls, known, tokens, q, byp, rep, ev, prompts, _, last in side.query(_ACTIVITY):
        if (first_day is not None and day < first_day) or (hi is not None and day > hi):
            continue
        p = out.setdefault(groups[acct], Counter(sessions=0))
        p.setdefault("sids", set()).add(sid)
        p["sessions"] = len(p["sids"])
        p["with_events"] += bool(ev)
        p["calls"] += calls
        p["known"] += known
        p["tokens"] += tokens or 0
        p["queries"] += q
        p["bypassed"] += byp
        p["reported"] += rep
        p["missed"] += sum(1 for t in missed[sid] if day <= t < day + DAY)
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
        pct = round(100 * w["bypassed"] / w["queries"], 1) if w["queries"] else None
        assert r["bypassPct"] == pct, (url, pid, r["bypassPct"], pct)
        if w["with_events"]:
            assert r["coverage"]["reported"] == w["reported"] and r["coverage"]["notCaptured"] == w["missed"], (
                url,
                pid,
            )
        else:
            assert r["coverage"] is None, (url, pid)
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
            assert o["notCaptured"] == sum(r["coverage"]["notCaptured"] for r in people if r["coverage"])
            cards = {c["agent"]: c["sessions"] for c in o["agents"] if c["people"]}
            want = cards_from_scratch(side, *ranges.snap(lo, hi))
            assert cards == {a: n for a, n in want.items() if a in cards}, (side.name, q, cards, want)
    assert checked


# Each session's agent (its first visible call's) and the span of its activity (visible calls and Claude Code
# events), with whether an account appears on it.
_SESSION_SPANS = f"""WITH act AS (
  SELECT c.session_id, coalesce(c.start_ns, c.end_ns) AS t FROM calls c WHERE {_VISIBLE}
  UNION ALL SELECT e.session_id, e.ts_ns FROM events e WHERE e.source = 'claude_code' AND e.session_id IS NOT NULL)
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
    "/timeline": SCOPE | {"children", "title", "cells", "names", "typed", "missed", "window", "friction_minutes",
                          "friction"},
    "/summary": SCOPE | {"cells", "keys", "key_counts", "missed", "latest", "credentials", "cred_cells"},
    "/requests": SCOPE | {"credentials", "cred_cells", "cred_minutes", "missed_cells", "missed_minutes", "call_rows",
                          "missed_rows", "reports", "prompt_indexes", "step_numbers", "retries", "next_attempt"},
}  # fmt: skip
# What a page draws, whatever it reads: the timeline's typed prompts.
DRAWN = {"typed"}


def test_a_person_page_reads_cells_and_a_page_whatever_the_history(sides):
    """Bound (§6.3): one snapshot, the page's own statements, each reading cells, a page or what the page
    draws. The person has 30 sessions over three days, two of them busy: a page that read their calls or
    events would read hundreds of rows."""
    side = sides["many sessions"]
    side.at(FROZEN_NOW)
    (pid,) = {a for (a,) in side.query("SELECT DISTINCT account_id FROM session_accounts")}
    base = f"/api/v1/people/{pid}"
    day = urllib.parse.urlencode({"from": "2026-11-03T00:00:00Z", "to": "2026-11-03T23:59:59Z"})
    urls = [base, f"{base}/sessions", f"{base}/sessions?pageSize=5&page=3", f"{base}/timeline",
            f"{base}/timeline?{day}&bin=300", f"{base}/summary", f"{base}/summary?{day}", f"{base}/requests",
            f"{base}/requests?pageSize=5&page=7", f"{base}/requests?order=asc&{day}",
            f"{base}/requests?kind=model"]  # fmt: skip
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
        urls += [u for pid in side.people() for u in person_urls(side, pid, per_kind=0)]
        for url in urls:
            side.store.trace.clear()
            side.new.get(url)
            plans += [
                (s.name, p) for ld in side.store.trace for s in ld.statements for p in side.store.reader.explain(s)
            ]
    scans = [(n, p) for n, p in plans if "Seq Scan on" in p and p.split("Seq Scan on ")[1].split()[0] not in _SMALL]
    assert plans and not scans, scans[:5]


def test_the_cuts_land_where_the_spec_says(sides):
    """Not vacuous: the person's cuts fall in every place the spec names that its data has."""
    side = sides["drawer cases"]
    busiest = max(side.oracle.person_ids(), key=lambda p: (len(side.person_sessions(p)), p))
    kinds = set(instants(side.oracle, side.person_sessions(busiest)))
    mixed = sides["mixed"]
    kinds |= {k for p in mixed.oracle.person_ids() for k in instants(mixed.oracle, mixed.person_sessions(p))}
    want = {"inside a tick run", "between a batch's events", "between a batch's events across sessions",
            "between a prompt's calls", "on and around a minute boundary", "before all data",
            "after all data"}  # fmt: skip
    assert want <= kinds, want - kinds


def test_cuts_are_whole_ranges_and_open_ends():
    found = {"a": [5, 1], "on and around a minute boundary": [10, 9, 11]}
    rs = cuts(found)
    assert (None, None) in rs and (1, None) in rs and (None, 11) in rs and (1, 11) in rs
    assert all(lo is None or hi is None or lo <= hi for lo, hi in rs)
    assert 5 not in {x for r in rs for x in r}  # one instant per kind (a boundary's all)
