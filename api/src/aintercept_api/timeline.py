"""Timeline lanes shared by the person and session timelines (handoff §4 TimelineData,
§5.2.5, §5.2.8). Every mark carries what its tooltip shows (§6B.10): tooltips never fetch."""

from collections import Counter, defaultdict
from collections.abc import Callable

from aintercept_api import claude_code as cc
from aintercept_api import session as ss
from aintercept_api.models.people import (
    AnalyticsBin,
    CallClasses,
    EventCount,
    FrictionMark,
    GapWindow,
    ModelCallBin,
    PresenceSegment,
    TimelinePrompt,
    TimelineSession,
)
from aintercept_api.rows import dt, label
from aintercept_api.store import Store

TOP_EVENTS = 3
FRICTION_DETAIL = {"frustrated": "prompt flagged negative", "interrupted": "turn interrupted"}


def _sum(vals: list[int | None]) -> int | None:
    known = [v for v in vals if v is not None]
    return sum(known) if known else None


def band(s: cc.Session, now: int, row: int) -> TimelineSession:
    live = s.live(now)
    return TimelineSession(
        sessionId=s.id,
        start=dt(s.start),
        end=None if live else dt(s.last),
        label=label(s.id, s.title).text,
        row=row,
        live=live,
        capturedFromPrompt=cc.captured_from_prompt(s),
        repo=None,  # M7 extraction
        branch=None,
        prompts=len(cc.prompt_ids(s)) or None,
        modelCalls=len(s.calls),
    )


def prompts(store: Store, sessions: list[cc.Session], now: int, within: Callable[[int], bool]) -> list[TimelinePrompt]:
    """One mark per typed prompt (tengu_input_prompt), with the prompt row's numbers."""
    out = [
        TimelinePrompt(
            t=p.t,
            index=p.index,
            sessionId=s.id,
            promptId=p.promptId,
            text=p.text,
            steps=p.steps,
            durationMs=p.durationMs,
            tokensIn=p.tokensIn,
            tokensOut=p.tokensOut,
        )
        for s in sessions
        for p in ss.typed_prompts(store, s, now)
        if within(cc.to_ns(p.t))
    ]
    return sorted(out, key=lambda p: (p.t, p.sessionId))


def model_calls(calls: list[cc.Call], missed: list[int], bin_s: int) -> list[ModelCallBin]:
    """Bins with a captured call or a reported-but-not-captured one."""
    by_bin: dict[int, list[cc.Call]] = defaultdict(list)
    for c in calls:
        by_bin[cc.bin_start(c.ts, bin_s)].append(c)
    gaps = Counter(cc.bin_start(t, bin_s) for t in missed)
    out = []
    for t in sorted(set(by_bin) | set(gaps)):
        cs = by_bin.get(t, [])
        classes = Counter(c.request_class for c in cs)
        out.append(
            ModelCallBin(
                t=dt(t),
                count=len(cs),
                tokens=sum(c.output_tokens or 0 for c in cs),
                byClass=CallClasses(
                    main=classes["main"],
                    subagent=classes["subagent"],
                    auxiliary=len(cs) - classes["main"] - classes["subagent"],
                ),
                tokensIn=_sum([c.context for c in cs]),
                tokensOut=_sum([c.output_tokens for c in cs]),
                notCaptured=gaps.get(t, 0),
            )
        )
    return out


def _by_count(kv: tuple[str, int]) -> tuple[int, str]:
    return -kv[1], kv[0]


def analytics(store: Store, sids: list[str], bin_s: int, lo: int | None, hi: int | None) -> list[AnalyticsBin]:
    rows = store.query(
        """SELECT ts - ts % ?, coalesce(name, '(unnamed)'), count(*) FROM cc_events
           WHERE session_id IN (SELECT unnest(?::VARCHAR[])) AND (? OR ts BETWEEN ? AND ?) GROUP BY 1, 2""",
        [bin_s * cc.NS, sids, lo is None or hi is None, lo or 0, hi or 0],
    )
    by_bin: dict[int, Counter[str]] = defaultdict(Counter)
    for t, name, n in rows:
        by_bin[t][name] += n
    return [
        AnalyticsBin(
            t=dt(t),
            count=sum(names.values()),
            top=[EventCount(name=k, count=n) for k, n in sorted(names.items(), key=_by_count)[:TOP_EVENTS]],
        )
        for t, names in sorted(by_bin.items())
    ]


def gaps(sessions: list[cc.Session], within: Callable[[int], bool]) -> list[GapWindow]:
    """Not-captured windows, each with the reported calls inside it and how many were captured."""
    missed = [t for s in sessions for t in (cc.unmatched_reports(s) or []) if within(t)]
    reported = [e.ts for s in sessions for e in s.events if e.name == "tengu_api_success"]
    captured = [c.ts for s in sessions for c in s.calls]
    out = []
    for a, b, n in cc.windows(missed):
        r = sum(1 for t in reported if a <= t <= b)
        # No model call captured at all while Claude Code's analytics were: the client was
        # routing its model calls around the proxy (as over IPv6 before the AAAA fix).
        bypassed = not any(a <= t <= b for t in captured)
        cause = BYPASS_CAUSE if bypassed else None
        out.append(GapWindow(from_=dt(a), to=dt(b), count=n, reported=r, captured=r - n, cause=cause))
    return out


BYPASS_CAUSE = (
    "Model calls went around the proxy: none was captured in this window while Claude Code's "
    "analytics were (a direct route, e.g. over IPv6)"
)


def presence(segs: list[tuple[int, int, str]]) -> list[PresenceSegment]:
    return [
        PresenceSegment(from_=dt(a), to=dt(b), state=st, source="sleep" if st == "asleep" else "focus")
        for a, b, st in segs
    ]


def friction(sessions: list[cc.Session], bin_s: int, within: Callable[[int], bool]) -> list[FrictionMark]:
    """One mark per (bin, kind); its prompt is the first incident's, its detail lists what happened."""
    marks: dict[tuple[int, str], list[tuple[int, cc.Ev, dict[str, int]]]] = defaultdict(list)
    for s in sessions:
        index = cc.prompt_index(s)
        for t, kind, e in cc.friction_marks(s):
            if within(t):
                marks[(cc.bin_start(t, bin_s), kind)].append((t, e, index))
    out = []
    for (t, kind), incidents in sorted(marks.items()):
        incidents.sort(key=lambda x: x[0])
        _, first, index = incidents[0]
        if kind == "destructive":
            details = [" · ".join(x for x in (e.destructive, e.destructive_scope) if x) for _, e, _ in incidents]
            detail = ", ".join(dict.fromkeys(details))
        else:
            detail = FRICTION_DETAIL[kind]
        out.append(
            FrictionMark(
                t=dt(t),
                kind=kind,
                count=len(incidents),
                promptIndex=first.prompt_index if kind == "frustrated" else index.get(first.prompt_id or ""),
                detail=detail,
                tool="Bash" if kind == "destructive" else None,  # destructive marks are Bash commands
            )
        )
    return out
