"""Turns and work over v9 (handoff §11R; docs/PIPELINE.md §6.1, §6.3): what the turn ledger shows, read from
the rollups that hold it and shaped here.

- /sessions/{id}/turns: a page of the session's turns (`prompt_items`' prompts in range, by seq) with each
  turn's `prompts` row (its work and end; interrupted when it stopped on one or the next prompt says so)
  and `steps` (numbered at read by (t_ns, ord); each step's work kind, program and server, by which
  consecutive steps group, and its work), and for a Codex thread its subagent threads (`session_edges`
  and their `sessions` rows).
- The session header's turns, active time and work: its `sessions` row (cost, the head range and the last
  test were analytics': null).
- The timeline's Turns lane: the session's `prompts` (one block each, prompt to last step); the Work lane:
  the work key rows (`w:*`) of `session_minute_keys` (bins under an hour) or `session_keys`.
- /people/{id}/work: the person's `sessions` rows in range, grouped by repo; their work, turns and active
  time are on the rows.
- The person summary's work: the turns in range (`prompts` by session and time) and the work key rows of
  the range's cells.
"""

import itertools
from collections import Counter, defaultdict
from collections.abc import Iterable, Sequence
from typing import Any, cast

from aintercept_api import agents
from aintercept_api import sessionpages as sp
from aintercept_api import work as wk
from aintercept_api.models.turns import (
    RepoGroup,
    SessionWorkRow,
    SubagentRow,
    TurnFriction,
    TurnRow,
    TurnStep,
    WorkGroup,
    WorkKind,
)
from aintercept_api.models.work import OutcomeFacts, TurnBlock, WorkBin
from aintercept_api.reader import Snapshot
from aintercept_api.rows import dt, session_row_from_table

Row = dict[str, Any]

# The session's turns in range (a page of them, by seq), each with its prompts row.
_TURN_COUNT = """SELECT count(*) FROM prompt_items
WHERE session_id = %(sid)s AND type = 'prompt' AND t_ns BETWEEN %(lo)s AND %(hi)s"""
_TURN_AT = """SELECT count(*) FROM prompt_items
WHERE session_id = %(sid)s AND type = 'prompt' AND t_ns BETWEEN %(lo)s AND %(hi)s
  AND seq < (SELECT min(seq) FROM prompt_items WHERE session_id = %(sid)s AND type = 'prompt'
             AND t_ns BETWEEN %(lo)s AND %(hi)s AND prompt_index = %(n)s)"""
_TURNS = f"""SELECT i.seq, i.t_ns, i.prompt_id, i.prompt_index, i.open_tail, p.text, p.text_chars, p.steps,
    p.unfinished_max_ns, p.tokens_in, p.tokens_out, p.interrupted OR {sp.NEXT_FOLLOWS_INTERRUPT} AS interrupted,
    p.ended_ns, p.work, p.product, p.tool_counts
FROM prompt_items i JOIN prompts p ON p.prompt_id = i.prompt_id AND p.session_id = i.session_id
WHERE i.session_id = %(sid)s AND i.type = 'prompt' AND i.t_ns BETWEEN %(lo)s AND %(hi)s
ORDER BY i.seq LIMIT %(n)s OFFSET %(skip)s"""  # noqa: S608 (fixed text)
# A Codex thread's subagent threads, each under the parent's turn that was running when it started (the
# parent's last turn to start by then).
_THREADS = """SELECT s.session_id, s.origin, s.model_calls, s.work, s.tokens_input, s.tokens_cache_read,
    s.tokens_cache_write, s.tokens_output, (
      SELECT i.prompt_id FROM prompt_items i WHERE i.session_id = %(sid)s AND i.type = 'prompt' AND i.t_ns <= s.start_ns
      ORDER BY i.t_ns DESC, i.seq DESC LIMIT 1) AS turn
FROM session_edges e JOIN sessions s ON s.session_id = e.child_session_id
WHERE e.parent_session_id = %(sid)s AND s.origin_kind = 'subagent'
ORDER BY s.start_ns, s.session_id COLLATE "C\""""
# The timeline's Turns lane: every turn of the session.
_BLOCKS = f"""SELECT i.t_ns, i.prompt_id, i.prompt_index, i.open_tail, p.steps, p.ended_ns, p.unfinished_max_ns,
    p.interrupted OR {sp.NEXT_FOLLOWS_INTERRUPT} AS interrupted
FROM prompt_items i JOIN prompts p ON p.prompt_id = i.prompt_id AND p.session_id = i.session_id
WHERE i.session_id = %(sid)s AND i.type = 'prompt' ORDER BY i.seq"""  # noqa: S608 (fixed text)
# The Work lane's key rows: a session's minutes' or hours'.
_MINUTE_WORK = """SELECT minute_ns AS t, kind, key, n FROM session_minute_keys
WHERE session_id = %(sid)s AND kind = ANY(%(kinds)s)"""
_HOUR_WORK = (
    """SELECT hour_ns AS t, kind, key, n FROM session_keys WHERE session_id = %(sid)s AND kind = ANY(%(kinds)s)"""
)
# A person's turns that started in [lo, hi]: their times (the header row's Active).
_PERSON_TURNS = """SELECT p.t_ns, p.ended_ns FROM prompts p
WHERE p.session_id = ANY(%(sids)s) AND p.t_ns BETWEEN %(lo)s AND %(hi)s"""


def _group_key(s: Row) -> tuple[str, str | None, str | None]:
    """What a step did (the rollup's kind, its program or server): a group is a run of steps with the same."""
    return s["work_kind"], s["program"], s["server"]


def _exits(s: TurnStep) -> list[int]:
    """A step's commands' exit codes: its tool calls', else the recorded results."""
    got = [t.exitCode for t in s.tools if t.command is not None and t.exitCode is not None]
    return got or list(s.exitCodes)


def tool_counts(counts: Iterable[dict[str, int] | None]) -> dict[str, int]:
    """Tool calls by name, summed over steps (or turns) and ordered most first (the content-free work line,
    handoff §11S). A call whose tool had no name ("—") is left out."""
    total: Counter[str] = Counter()
    for c in counts:
        for name, n in (c or {}).items():
            if name and name != "—":
                total[name] += int(n)
    return dict(sorted(total.items(), key=lambda x: (-x[1], x[0])))


def _failed(rows: Iterable[Row]) -> int:
    """The steps that failed once ended (an error status, or none): the turn's ! (handoff §11S)."""
    return sum(1 for r in rows if r["failed"] and r["end_ns"] is not None)


def _opt_sum(xs: Sequence[int | None]) -> int | None:
    known = [x for x in xs if x is not None]
    return sum(known) if known else None


def groups(rows: list[Row], live: bool, now: int) -> list[WorkGroup]:
    """A thread's steps (its rows, in order) as WorkGroups: consecutive steps with the same kind, program and
    server. A finished turn's last step with no tool is its reply."""
    out = []
    keyed = [(_group_key(r), r) for r in rows]
    for (kind, program, server), run in itertools.groupby(keyed, key=lambda x: x[0]):
        rs = [r for _, r in run]
        steps = [TurnStep(**dict(sp.step_data(r, live, now)), step=r["step"]) for r in rs]
        ends = [r["end_ns"] for r in rs]
        patched = [p.file for s in steps for p in s.patches]
        texts = [s.text for s in steps if s.text]
        out.append(
            WorkGroup(
                kind=cast(WorkKind, kind),  # the rollup's kinds are WorkKind's
                program=program,
                server=server,
                work=wk.turn_work(wk.from_pieces(p for r in rs for p in r["work_pieces"])),
                exits=[x for s in steps for x in _exits(s)] if kind == "run" else [],
                paths=list(dict.fromkeys(patched)) if patched else None,
                startedAt=dt(rs[0]["t_ns"]),
                durationMs=sp._round1((max(ends) - rs[0]["t_ns"]) / 1e6) if all(e is not None for e in ends) else None,
                tokensIn=_opt_sum([_ctx(r) for r in rs]),
                tokensOut=_opt_sum([r["tokens_output"] for r in rs]),
                text=texts[-1] if kind == "reply" and texts else None,
                tools=tool_counts(r["tool_counts"] for r in rs),
                steps=steps,
            )
        )
    return out


def _ctx(r: Row) -> int | None:
    parts = [r["tokens_input"], r["tokens_cache_read"], r["tokens_cache_write"]]
    return sum(x or 0 for x in parts) or None


def _subagents(rows: list[Row], live: bool, now: int) -> list[SubagentRow]:
    """Claude Code's subagents in a turn: its subagent steps by subagent (its id, else its type)."""
    by: dict[str | None, list[Row]] = defaultdict(list)
    for r in rows:
        by[r["agent_id"] or r["agent_type"]].append(r)
    out = []
    for rs in sorted(by.values(), key=lambda rs: (rs[0]["t_ns"], rs[0]["ord"])):
        out.append(
            SubagentRow(
                agentId=rs[0]["agent_id"],
                name=rs[0]["agent_type"],
                sessionId=None,
                steps=len(rs),
                work=wk.turn_work(wk.from_pieces(p for r in rs for p in r["work_pieces"])),
                tools=tool_counts(r["tool_counts"] for r in rs),
                tokensIn=_opt_sum([_ctx(r) for r in rs]),
                tokensOut=_opt_sum([r["tokens_output"] for r in rs]),
                groups=groups(rs, live, now),
            )
        )
    return out


def _thread(t: Row) -> SubagentRow:
    """A Codex subagent thread, as a row under the turn that started it."""
    ctx = [t["tokens_input"], t["tokens_cache_read"], t["tokens_cache_write"]]
    return SubagentRow(
        agentId=None,
        name=(t["origin"] or {}).get("agentName"),
        sessionId=t["session_id"],
        steps=t["model_calls"],
        work=wk.turn_work(t["work"]),
        tools={},
        tokensIn=sum(x or 0 for x in ctx) or None,
        tokensOut=t["tokens_output"],
        groups=[],
    )


def _ended(p: Row, steps: list[Row]) -> int | None:
    """A turn's end: its last step's (the rollup's), none while a step is open."""
    if p["ended_ns"] is not None:
        return int(p["ended_ns"])
    if not steps or any(s["end_ns"] is None for s in steps):
        return None
    return max(int(s["end_ns"]) for s in steps)


class TurnReads:
    """The turn ledger's reads, in the request's snapshot."""

    def __init__(self, snap: Snapshot, sessions: sp.SessionReads) -> None:
        self.snap = snap
        self.sessions = sessions

    # --- /sessions/{id}/turns -------------------------------------------------------------------

    def turns(
        self, sid: str, now: int, lo: int, hi: int, page: int, size: int, turn: int | None
    ) -> tuple[list[TurnRow], int, int]:
        span = {"sid": sid, "lo": lo, "hi": hi}
        ((total,),) = self.snap.rows("turn_count", _TURN_COUNT, span)
        if turn is not None and total:
            ((before,),) = self.snap.rows("turn_at", _TURN_AT, {**span, "n": turn})
            if before < total:
                page = before // size + 1
        if not total or (page - 1) * size >= total:
            return [], total, page
        ps = self.snap.dicts("turns", _TURNS, {**span, "n": size, "skip": (page - 1) * size})
        s = self.sessions.stored(sid) or {}
        live = self.sessions._live(sid, now)
        steps: dict[str, list[Row]] = defaultdict(list)
        for r in self.snap.dicts("turn_steps", sp.STEPS, {"sid": sid, "pids": [p["prompt_id"] for p in ps]}):
            steps[r["prompt_id"]].append(r)
        threads: dict[str, list[SubagentRow]] = defaultdict(list)
        if s.get("product") == "codex":
            for t in self.snap.dicts("threads", _THREADS, {"sid": sid}):
                if t["turn"] is not None:
                    threads[t["turn"]].append(_thread(t))
        out = []
        for p in ps:
            rs = steps[p["prompt_id"]]
            main = [r for r in rs if r["kind"] != "subagent"]
            running = sp._in_flight(p, live, now)
            ended = None if running else _ended(p, rs)
            out.append(
                TurnRow(
                    promptId=p["prompt_id"],
                    n=p["prompt_index"],
                    startedAt=dt(p["t_ns"]),
                    endedAt=dt(ended) if ended is not None else None,
                    durationMs=sp._round1((ended - p["t_ns"]) / 1e6) if ended is not None else None,
                    live=running,
                    asked=sp._text(p),
                    steps=p["steps"],
                    tokensIn=p["tokens_in"],
                    tokensOut=p["tokens_out"],
                    friction=TurnFriction(interrupted=bool(p["interrupted"]), failedCalls=_failed(rs)),
                    work=wk.turn_work(p["work"]),
                    tools=tool_counts([p["tool_counts"]]),
                    groups=_with_reply(groups(main, live, now), running),
                    subagents=_subagents([r for r in rs if r["kind"] == "subagent"], live, now)
                    + threads.get(p["prompt_id"], []),
                )
            )
        return out, total, page

    # --- the session page's header, timeline and readout -------------------------------------------

    def blocks(self, sid: str, live: bool, now: int) -> list[TurnBlock]:
        out = []
        for p in self.snap.dicts("turn_blocks", _BLOCKS, {"sid": sid}):
            running = sp._in_flight(p, live, now)
            ended = p["ended_ns"]
            out.append(
                TurnBlock(
                    promptId=p["prompt_id"],
                    n=p["prompt_index"],
                    startedAt=dt(p["t_ns"]),
                    endedAt=dt(ended) if ended is not None and not running else None,
                    steps=p["steps"],
                    live=running,
                    interrupted=bool(p["interrupted"]),
                )
            )
        return out

    def bins(self, sid: str, width: int, steps: dict[int, int]) -> list[WorkBin]:
        """The Work lane: each bin's work from its minutes' (or hours') work keys; steps per bin as given."""
        hourly = width % sp.HOUR == 0
        rows: dict[int, list[tuple[str, str, int]]] = defaultdict(list)
        for t, kind, key, n in self.snap.rows(
            "work_keys", _HOUR_WORK if hourly else _MINUTE_WORK, {"sid": sid, "kinds": wk.KINDS}
        ):
            rows[t - t % width].append((kind, key, n))
        return work_bins(rows, steps)


def _with_reply(gs: list[WorkGroup], running: bool) -> list[WorkGroup]:
    """A finished turn's last group, when it called no tool, is its reply."""
    if gs and not running and gs[-1].kind == "think":
        last = gs[-1]
        texts = [s.text for s in last.steps if s.text]
        return [*gs[:-1], last.model_copy(update={"kind": "reply", "text": texts[-1] if texts else None})]
    return gs


def work_bins(rows: dict[int, list[tuple[str, str, int]]], steps: dict[int, int]) -> list[WorkBin]:
    out = []
    for b in sorted(rows):
        w = wk.from_keys(rows[b])
        edits, other = wk.edits_and_other(w)
        if edits or other:
            out.append(WorkBin(t=dt(b), edits=edits, other=other, steps=steps.get(b, 0), work=wk.turn_work(w)))
    return out


def outcome(s: Row) -> OutcomeFacts:
    """A session's OutcomeSlot facts: its `sessions` row's work (the head range and the last test were
    Claude Code's analytics: null)."""
    w = wk.add([s.get("work")])
    return OutcomeFacts(
        commits=w["commits"],
        headFrom=None,
        headTo=None,
        editedFiles=w["editedFiles"],
        editTypes=w["editTypes"],
        lastTest=None,
        pushes=w["pushes"],
        prs=w["prs"],
    )


# --- /people/{id}/work ------------------------------------------------------------------------------


def repo_groups(rows: list[Row], now: int) -> list[RepoGroup]:
    """The person's sessions (`sessions` rows with their labels, rows_of's) grouped by repo: repos by most
    recent activity, sessions newest first."""
    by: dict[str | None, list[Row]] = defaultdict(list)
    for r in rows:
        by[r["repo"]].append(r)

    def last(r: Row) -> int:
        return now if r["live"] else int(r["last_ns"])

    out = []
    for repo, rs in by.items():
        rs.sort(key=lambda r: (r["start_ns"], r["session_id"]), reverse=True)
        work_rows = [_work_row(r) for r in rs]
        out.append(
            RepoGroup(
                repo=repo,
                agents=sorted({agents.agent_of(r["product"]) for r in rs}),
                sessions=len(rs),
                turns=sum(w.turns for w in work_rows),
                activeMs=sum(w.activeMs for w in work_rows),
                work=wk.turn_work(wk.add(r["work"] for r in rs)),
                lastActiveAt=dt(max(last(r) for r in rs)),
                rows=work_rows,
            )
        )
    out.sort(key=lambda g: g.lastActiveAt, reverse=True)
    return out


def _work_row(r: Row) -> SessionWorkRow:
    row = session_row_from_table(r)
    return SessionWorkRow(
        sessionId=row.sessionId,
        agent=row.agent,
        start=row.start,
        end=row.end,
        live=row.live,
        label=row.label,
        turns=int(r["turns"] or 0),
        capturedFromPrompt=None,
        activeMs=(r["active_ns"] or 0) / 1e6,
        work=wk.turn_work(r["work"]),
        friction=row.friction,
        tools=tool_counts([r["tools"]]),
        recording="content" if r["recorded"] else "metadata",
    )


def person_turns(snap: Snapshot, sids: list[str], lo: int, hi: int) -> tuple[int, float]:
    """The person's turns that started in [lo, hi], and their times summed (ms)."""
    if not sids:
        return 0, 0.0
    got = snap.rows("person_turns", _PERSON_TURNS, {"sids": sids, "lo": lo, "hi": hi})
    return len(got), sum((e - t) / 1e6 for t, e in got if e is not None)
