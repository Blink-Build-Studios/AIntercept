"""Timeline lanes shared by the person and session timelines (handoff §4 TimelineData,
§5.2.5, §5.2.8). Every mark carries what its tooltip shows (§6B.10): tooltips never fetch."""

from collections.abc import Mapping
from typing import Any

from aintercept_api import claude_code as cc
from aintercept_api import session as ss
from aintercept_api.models.people import (
    AnalyticsBin,
    CallClasses,
    EventCount,
    FrictionMark,
    GapWindow,
    ModelCallBin,
    TimelinePrompt,
    TimelineSession,
)
from aintercept_api.rows import dt
from aintercept_api.store import Store

TOP_EVENTS = 3
FRICTION_DETAIL = {"frustrated": "prompt flagged negative", "interrupted": "turn interrupted"}


def _sum(vals: list[int | None]) -> int | None:
    known = [v for v in vals if v is not None]
    return sum(known) if known else None


def band_from_table(r: Mapping[str, Any], row: int, alone: bool = False) -> TimelineSession:
    """A session_rows row (with `live`) as its timeline band: field copies only."""
    return TimelineSession(
        sessionId=r["session_id"],
        start=dt(r["start_ns"]),
        end=None if r["live"] else dt(r["last_ns"]),
        label=r["label_text_alone" if alone else "label_text"],
        row=row,
        live=r["live"],
        capturedFromPrompt=r["captured_from_prompt"],
        repo=None,  # M7 extraction
        branch=None,
        prompts=r["prompts"],
        modelCalls=r["model_calls"],
    )


def prompts(store: Store, session_ids: list[str], now: int, lo: int | None, hi: int | None) -> list[TimelinePrompt]:
    """One mark per typed prompt (tengu_input_prompt) in [lo, hi] (None: all), with the prompt
    row's numbers."""
    return ss.timeline_prompts(store, session_ids, now, lo, hi)


def model_calls(store: Store, sids: list[str], bin_s: int, lo: int | None, hi: int | None) -> list[ModelCallBin]:
    """Bins with a captured call or a reported-but-not-captured one."""
    rows = store.query(
        f"""WITH c AS (
              SELECT t_ns - t_ns % ? AS t, count(*) AS n, coalesce(sum(output), 0) AS tokens,
                     count_if(request_class = 'main') AS main, count_if(request_class = 'subagent') AS sub,
                     sum(context) AS tin, sum(output) AS tout
              {ss.MARKS} GROUP BY 1),
            m AS (SELECT t_ns - t_ns % ? AS t, count(*) AS n {ss.MARKS} GROUP BY 1)
            SELECT coalesce(c.t, m.t), coalesce(c.n, 0), coalesce(c.tokens, 0), coalesce(c.main, 0),
                   coalesce(c.sub, 0), c.tin, c.tout, coalesce(m.n, 0)
            FROM c FULL JOIN m ON c.t = m.t ORDER BY 1""",  # noqa: S608 (fixed text; values are parameters)
        [
            bin_s * cc.NS,
            *ss.marks_params("call", sids, lo, hi),
            bin_s * cc.NS,
            *ss.marks_params("missed", sids, lo, hi),
        ],
    )
    return [
        ModelCallBin(
            t=dt(t),
            count=n,
            tokens=tokens,
            byClass=CallClasses(main=main, subagent=sub, auxiliary=n - main - sub),
            tokensIn=tin,
            tokensOut=tout,
            notCaptured=missed,
        )
        for t, n, tokens, main, sub, tin, tout, missed in rows
    ]


def analytics(store: Store, sids: list[str], bin_s: int, lo: int | None, hi: int | None) -> list[AnalyticsBin]:
    """Each bin's events and its three commonest names (most first, then by name): SQL over the
    processor's event_rows."""
    return [
        AnalyticsBin(t=dt(t), count=n, top=[EventCount(name=k, count=c) for k, c in zip(names, counts, strict=True)])
        for t, n, names, counts in store.query(
            f"""WITH c AS (
                  SELECT ts_ns - ts_ns % ? AS t, name, count(*) AS n FROM event_rows
                  WHERE session_id IN (SELECT unnest(?::VARCHAR[])) AND (? OR ts_ns BETWEEN ? AND ?) GROUP BY 1, 2)
                SELECT t, sum(n)::BIGINT, list(name ORDER BY n DESC, name)[:{TOP_EVENTS}],
                       list(n ORDER BY n DESC, name)[:{TOP_EVENTS}]
                FROM c GROUP BY t ORDER BY t""",  # noqa: S608 (a constant; values are parameters)
            [bin_s * cc.NS, sids, lo is None or hi is None, lo or 0, hi or 0],
        )
    ]


def gaps(store: Store, sids: list[str], lo: int | None, hi: int | None) -> list[GapWindow]:
    """Not-captured windows, each with the reported calls inside it and how many were captured."""
    cte, params = ss.gap_windows(sids, lo, hi)
    rows = store.query(
        f"""WITH {cte}
            SELECT a, b, n,
                   (SELECT count(*) FROM session_marks r WHERE r.kind = 'reported'
                      AND r.session_id IN (SELECT unnest(?::VARCHAR[])) AND r.t_ns BETWEEN a AND b),
                   NOT EXISTS (SELECT 1 FROM session_marks c WHERE c.kind = 'call'
                      AND c.session_id IN (SELECT unnest(?::VARCHAR[])) AND c.t_ns BETWEEN a AND b)
            FROM windows ORDER BY a""",  # noqa: S608 (fixed text; values are parameters)
        [*params, sids, sids],
    )
    # No model call captured at all while Claude Code's analytics were: the client was
    # routing its model calls around the proxy (as over IPv6 before the AAAA fix).
    return [
        GapWindow(from_=dt(a), to=dt(b), count=n, reported=r, captured=r - n, cause=BYPASS_CAUSE if bypassed else None)
        for a, b, n, r, bypassed in rows
    ]


BYPASS_CAUSE = (
    "Model calls went around the proxy: none was captured in this window while Claude Code's "
    "analytics were (a direct route, e.g. over IPv6)"
)


def friction(store: Store, sids: list[str], bin_s: int, lo: int | None, hi: int | None) -> list[FrictionMark]:
    """One mark per (bin, kind); its prompt is the first incident's, its detail lists what happened."""
    rows = store.query(
        f"""WITH f AS (SELECT t_ns - t_ns % ? AS b, friction_kind AS kind, (t_ns, session_id, seq) AS o, prompt_index,
                              detail {ss.MARKS}),
            d AS (SELECT b, kind, detail, min(o) AS o FROM f GROUP BY ALL)
            SELECT b, kind, count(*), list(prompt_index ORDER BY o)[1],
                   (SELECT string_agg(d.detail, ', ' ORDER BY d.o) FROM d WHERE d.b = f.b AND d.kind = f.kind)
            FROM f GROUP BY 1, 2 ORDER BY 1, 2""",  # noqa: S608 (fixed text; values are parameters)
        [bin_s * cc.NS, *ss.marks_params("friction", sids, lo, hi)],
    )
    return [
        FrictionMark(
            t=dt(t),
            kind=kind,
            count=n,
            promptIndex=index,
            detail=detail if kind == "destructive" else FRICTION_DETAIL[kind],
            tool="Bash" if kind == "destructive" else None,  # destructive marks are Bash commands
        )
        for t, kind, n, index, detail in rows
    ]
