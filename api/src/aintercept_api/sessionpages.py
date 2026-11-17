"""The session pages over v9 (docs/PIPELINE.md §6.1): what each page shows, read from the facts and the
rollups through a few indexed statements in the request's snapshot, and shaped by today's code.

Analytics events (Claude Code's tengu_*, Codex's codex_*) aren't in the database: they stay in the raw
files. What only they fed (the analytics lane, coverage and not-captured calls, friction marks, heads,
recaps, hooks, TTFT, cost) is empty or null; everything else comes from the model calls themselves.

- /sessions/{id}: its `sessions` row. What a page computes from the stored values: the agent, live,
  Codex's limits (agents.codex_limits of the stored headers), the children (`session_edges`, §5.4) and
  the label (its own title, else the earliest title call of its accounts 0–5 minutes after its start).
- /timeline: the model-call lane from `session_minutes` (bins under an hour) or `session_hours`; the
  prompts from `prompt_items` and `prompts`; context points from `calls` by (session, time), one per
  main-thread call (what the chart draws).
- /state: counts from `session_minutes` in the brush (hours inside it, minutes at its edges); commits,
  pushes and PRs from the work keys the same way; tools used over the whole session from its
  `session_keys`.
- /events: none (analytics aren't stored).
- /requests: a page of `calls` by (scope session, time), and for a Codex thread its client's sessionless
  calls in its window by (credential, time); the page is found through the minutes' counts per kind and
  credential; step numbers counted from `steps` (§5.2: never stored).
- /prompts: a page of `prompt_items` by (session, seq), with each prompt's `prompts` row and `steps`
  (numbered at read, by (t_ns, ord)); a turn is interrupted when it stopped on an interrupt or the next
  prompt says so (follows_interrupt).

Text search (`q`) scans the session's rows in the range (§6.1). Nothing is cached between requests.
"""

import json
from bisect import bisect_right
from collections import Counter, defaultdict
from collections.abc import Callable, Sequence
from dataclasses import dataclass
from datetime import UTC, datetime
from http import HTTPStatus
from typing import Any

from aintercept_api import agents, drawer
from aintercept_api import claude_code as cc
from aintercept_api import person as pp
from aintercept_api import session as ss
from aintercept_api import timeline as tl
from aintercept_api.models.people import (
    CallClasses,
    McpServer,
    ModelCallBin,
    PromptText,
    RequestError,
    RequestRow,
    TimelinePrompt,
    TokenCount,
)
from aintercept_api.models.sessions import (
    BetweenData,
    BetweenItem,
    CapabilitiesAt,
    ContextPoint,
    EventRow,
    PromptData,
    PromptFriction,
    PromptItem,
    RangeCounts,
    RepoInRange,
    SessionState,
    SessionTimeline,
    StepData,
    ToolTally,
)
from aintercept_api.models.work import TurnBlock, WorkBin
from aintercept_api.reader import Snapshot
from aintercept_api.rows import dt, usage_missing

Row = dict[str, Any]

NS = cc.NS
MINUTE = 60 * NS
HOUR = 60 * MINUTE
TITLE_WITHIN_NS = 5 * MINUTE
# The hidden kinds a request row names (calls.hidden_kind): Codex's prewarm and title calls, Claude Code's
# next-prompt suggestion. Another kind's row is hidden all the same, unnamed.
HIDDEN_KINDS = ("prewarm", "title", "suggestion")

VISIBLE = drawer.VISIBLE
_AT = "coalesce(c.start_ns, c.end_ns)"
_ID = "replace(c.call_id, '#', '~')"
# A model call with a session (the API's agent_calls; refresh/sql.go modelCall).
_MODEL_CALL = """(NOT c.synthetic AND c.session_id IS NOT NULL
  AND NOT (c.source = 'codex' AND (c.end_ns IS NULL OR c.swept))
  AND ((coalesce(c.source, 'claude_code') = 'claude_code' AND c.host = 'api.anthropic.com'
        AND starts_with(c.path, '/v1/messages') AND NOT starts_with(c.path, '/v1/messages/count_tokens'))
    OR (c.source = 'codex' AND c.host = 'chatgpt.com' AND c.path = '/backend-api/codex/responses'
        AND (c.ws_turn IS NOT NULL OR c.method = 'POST'))))"""
# A call that names no session today though it has one here: a Codex call that never really ended
# (refresh/sql.go unnamed).
_UNNAMED = "(c.source = 'codex' AND (c.end_ns IS NULL OR c.swept))"
# A transaction the request log lists (process/requestrows.go transaction): a coding agent's, not a
# probe, and a WebSocket's turns rather than its connection.
_TRANSACTION = """(NOT c.synthetic
  AND (CASE WHEN c.source IS NULL AND c.host = 'api.anthropic.com' THEN 'claude_code' ELSE c.source END)
      IN ('claude_code', 'codex')
  AND NOT (c.ws_turn IS NULL AND c.method = 'GET' AND c.path = '/backend-api/codex/responses'))"""
# What a request row reads of a call (process/requestrows.go callFromRequest): its end only when it's
# real (the sweep's isn't: today's row of a swept call is its start alone).
_ROW_COLUMNS = f"""{_ID} AS id, {_AT} AS ts, CASE WHEN c.swept THEN NULL ELSE c.end_ns END AS end_ns,
  c.end_ns IS NULL AS pending, c.kind,
  c.source, c.host, c.client_mode, c.hidden_kind, c.session_id, c.scope_session_id, {_UNNAMED} AS unnamed,
  c.prompt_id, c.method, c.path, c.status, c.error, c.request_bytes, c.response_bytes, c.request_class,
  c.content_encoding, c.model, c.effort, c.stop_reason, c.input_tokens, c.cache_read_tokens, c.cache_write_tokens,
  c.output_tokens, c.ws_turn, c.tool_uses, c.detail -> 'text' AS text, c.key_hash"""


# --- small helpers ------------------------------------------------------------------------------------


def _round1(x: float) -> float:
    return round(x, 1)


def _phrase(status: int) -> str | None:
    try:
        return HTTPStatus(status).phrase
    except ValueError:  # not a registered code (e.g. Anthropic's 529)
        return None


@dataclass
class Cell:
    """A run of a session's minutes: a whole hour's row, or a minute's."""

    start: int
    end: int  # exclusive
    row: Row

    @property
    def hour(self) -> bool:
        return self.end - self.start == HOUR


_FAR = 1 << 62


def _span(sid: str, lo: int | None, hi: int | None) -> dict[str, Any]:
    """A cell read's parameters for [lo, hi] (each bound None: open): the whole hours inside it, [hlo, hhi)
    (NULL: open), and the range itself, whose minutes outside those hours are its edges."""
    hlo = None if lo is None else -(-lo // HOUR) * HOUR
    hhi = None if hi is None else (hi + 1) // HOUR * HOUR
    return {"sid": sid, "hlo": hlo, "hhi": hhi, "lo": -_FAR if lo is None else lo, "hi": _FAR if hi is None else hi}


def needs_title(s: Row) -> bool:
    """Whether a session's label needs its title call (§6.1): a Codex thread with a call and no title of its own."""
    return s["own_title"] is None and s["product"] == "codex" and s["model_calls"] > 0


def children_json(sid: str, kids: list[Row]) -> str | None:
    """A session's children (§5.4) from its `session_edges` rows (_CHILDREN's), as today's row has them."""
    mine = [k for k in kids if k["kind"] in ("subagent", "review") and k["parent"] == sid]
    if not mine:
        return None
    mine.sort(key=lambda k: (k["first_ns"], k["sid"]))
    return json.dumps(
        {
            "subagent": sum(k["kind"] == "subagent" for k in mine),
            "review": sum(k["kind"] != "subagent" for k in mine),
            "sessionIds": [k["sid"] for k in mine],
        }
    )


def title_text(answer: str) -> str:
    """A title call's answer as a label: the text, or newer Codex's `{"title": ...}` object's title."""
    text = answer.strip()
    if text.startswith("{"):
        try:
            v = json.loads(text)
        except ValueError:
            v = None
        if isinstance(v, dict) and isinstance(v.get("title"), str):
            return str(v["title"]).strip()
    return text.strip('"')


def session_row(s: Row, now: int, children: str | None, title_call: str | None) -> Row:
    """A `sessions` row as today's session_rows has it (with `live` as of now): what a page computes from the
    stored values (the agent, live, Codex's limits), its children and its label (§6.1: its own title, else
    its title call's)."""
    r = dict(s)
    sid = s["session_id"]
    r["agent"] = agents.agent_of(s["product"])
    r["origin_json"] = json.dumps(s["origin"]) if s["origin"] is not None else None
    r["children_json"] = children
    title = s["own_title"]
    if title is None and needs_title(s):
        title = title_text(title_call) if title_call is not None else None
    text, source = (title, "title") if title else (f"Session {sid[:8]}", "none")
    r["label_text"] = r["label_text_alone"] = text
    r["label_source"] = r["label_source_alone"] = source
    limits = None
    if s["limits"] is not None and s["limits_ns"] is not None:
        # As today's row has it: the time as Python's fromtimestamp reads a float (sessionrows.go
        # pyFromTimestamp), not truncated.
        at = datetime.fromtimestamp(s["limits_ns"] / 1e9, UTC)
        got = agents.codex_limits(json.dumps(s["limits"]), at)
        limits = got.model_dump_json(by_alias=True) if got else None
    r["limits_json"] = limits
    r["live"] = now - s["last_ns"] <= cc.LIVE_WITHIN_NS
    return r


# --- the reads ----------------------------------------------------------------------------------------

_SESSION = "SELECT * FROM sessions WHERE session_id = %(sid)s"
# A session's children (§5.4): the sessions whose lineage names it, each as today's sessions snapshot
# has it (its least kind and parent over all its lineage), a subagent's or a review's.
_CHILDREN = """SELECT p.child_session_id AS sid, min(a.kind) AS kind, min(a.parent_session_id) AS parent,
    min(a.first_ns) AS first_ns
FROM session_edges p JOIN session_edges a ON a.child_session_id = p.child_session_id
WHERE p.parent_session_id = %(sid)s GROUP BY p.child_session_id"""
# The label's title call (§6.1): a Codex thread with no title of its own takes the earliest title call
# of its accounts in its first five minutes, of a thread with no visible call (a title thread's).
_TITLE = f"""SELECT c.detail ->> 'text' AS text FROM calls c
WHERE c.hidden_kind = 'title' AND c.account_id IN (SELECT account_id FROM session_accounts WHERE session_id = %(sid)s)
  AND c.start_ns BETWEEN %(lo)s AND %(hi)s AND {_MODEL_CALL}
  AND jsonb_typeof(c.detail -> 'text') = 'string' AND c.detail ->> 'text' <> ''
  AND NOT EXISTS (SELECT 1 FROM sessions s WHERE s.session_id = c.session_id AND s.model_calls > 0)
ORDER BY {_AT}, {_ID} COLLATE "C" LIMIT 1"""  # noqa: S608 (fixed text)

_CELL_COLUMNS = """model_calls, main_calls, subagent_calls, output_tokens, context_sum, requests, requests_hidden"""


def _cells_sql(minutes_only: bool) -> str:
    """A session's cells in [lo, hi] (NULL: open): its hours inside it and its minutes at the edges."""
    hours = f"""SELECT hour_ns AS t, {HOUR} AS w, {_CELL_COLUMNS} FROM session_hours
        WHERE session_id = %(sid)s AND (%(hlo)s::bigint IS NULL OR hour_ns >= %(hlo)s)
          AND (%(hhi)s::bigint IS NULL OR hour_ns < %(hhi)s)"""  # noqa: S608 (fixed text)
    minutes = f"""SELECT minute_ns, {MINUTE}, {_CELL_COLUMNS} FROM session_minutes
        WHERE session_id = %(sid)s AND minute_ns >= %(lo)s AND minute_ns <= %(hi)s
          AND (minute_ns < %(hlo)s OR minute_ns >= %(hhi)s)"""  # noqa: S608 (fixed text)
    if minutes_only:
        return f"""SELECT minute_ns AS t, {MINUTE} AS w, {_CELL_COLUMNS} FROM session_minutes
            WHERE session_id = %(sid)s AND (%(lo)s::bigint IS NULL OR minute_ns >= %(lo)s)
              AND (%(hi)s::bigint IS NULL OR minute_ns <= %(hi)s) ORDER BY 1"""  # noqa: S608
    return f"{hours} UNION ALL {minutes} ORDER BY 1"  # noqa: S608 (fixed text)


_CELLS = _cells_sql(False)
_MINUTES = _cells_sql(True)
_KEYS = """SELECT kind, key, sum(n)::bigint AS n FROM (
  SELECT kind, key, n FROM session_keys WHERE session_id = %(sid)s AND kind = ANY(%(kinds)s)
    AND (%(hlo)s::bigint IS NULL OR hour_ns >= %(hlo)s) AND (%(hhi)s::bigint IS NULL OR hour_ns < %(hhi)s)
  UNION ALL
  SELECT kind, key, n FROM session_minute_keys WHERE session_id = %(sid)s AND kind = ANY(%(kinds)s)
    AND minute_ns >= %(lo)s AND minute_ns <= %(hi)s AND (minute_ns < %(hlo)s OR minute_ns >= %(hhi)s)
) k GROUP BY kind, key"""
PROMPT_INDEX = drawer.PROMPT_INDEX
# Whether the prompt after p in its session (by time, then id byte-wise) says the turn before it was
# interrupted (the harness writes the note into the next turn's request): one indexed LIMIT 1.
NEXT_FOLLOWS_INTERRUPT = """coalesce((SELECT n.follows_interrupt FROM prompts n
  WHERE n.session_id = p.session_id AND n.t_ns >= p.t_ns
    AND (n.t_ns > p.t_ns OR n.prompt_id COLLATE "C" > p.prompt_id COLLATE "C")
  ORDER BY n.t_ns, n.prompt_id COLLATE "C" LIMIT 1), false)"""
# A list of prompts' steps, in order, each with its number.
STEPS = """SELECT s.*, row_number() OVER (PARTITION BY s.prompt_id ORDER BY s.t_ns, s.ord) AS step FROM steps s
WHERE s.session_id = %(sid)s AND s.prompt_id = ANY(%(pids)s) ORDER BY s.prompt_id, s.t_ns, s.ord"""

# The prompts (the timeline's marks): every prompt of the session.
_TYPED = """SELECT i.seq, i.t_ns, i.prompt_id, i.prompt_index, i.open_tail, p.steps, p.text, p.text_chars,
    p.tokens_in, p.tokens_out, p.ended_ns, p.unfinished_max_ns
FROM prompt_items i JOIN prompts p ON p.prompt_id = i.prompt_id AND p.session_id = i.session_id
WHERE i.session_id = %(sid)s AND i.type = 'prompt'
ORDER BY i.t_ns / 1000, i.seq"""
# Context points: one per main-thread visible call that sent context.
_CONTEXT = f"""SELECT {_ID} AS id, {_AT} AS t, c.model,
    nullif(coalesce(c.input_tokens, 0) + coalesce(c.cache_read_tokens, 0) + coalesce(c.cache_write_tokens, 0), 0)
      AS tokens,
    {PROMPT_INDEX.format(s="c.session_id", p="c.prompt_id")} AS prompt_index
FROM calls c WHERE c.session_id = %(sid)s AND c.request_class = 'main' AND {VISIBLE}
  AND (c.input_tokens IS NOT NULL OR c.cache_read_tokens IS NOT NULL OR c.cache_write_tokens IS NOT NULL)
ORDER BY {_AT}, {_ID} COLLATE "C\""""  # noqa: S608

# State: the range's prompts.
_ITEM_COUNTS = """SELECT count(*) AS prompts FROM prompt_items
WHERE session_id = %(sid)s AND type = 'prompt' AND t_ns BETWEEN %(lo)s AND %(hi)s"""

# Prompts: the range's items (seq follows time, so they're a run of seqs), a page of them, and their steps.
_ITEM_SPAN = """SELECT count(*) AS total, min(seq) AS first FROM prompt_items
WHERE session_id = %(sid)s AND t_ns BETWEEN %(lo)s AND %(hi)s"""
_PROMPT_AT = """SELECT min(seq) FROM prompt_items WHERE session_id = %(sid)s AND t_ns BETWEEN %(lo)s AND %(hi)s
  AND type = 'prompt' AND prompt_index = %(n)s"""
_ITEMS = f"""SELECT i.*, p.text, p.text_chars, p.steps, p.unfinished_max_ns, p.ended_ns, p.tokens_in, p.tokens_out,
    p.tool_counts, p.interrupted OR {NEXT_FOLLOWS_INTERRUPT} AS interrupted
FROM prompt_items i LEFT JOIN prompts p ON p.prompt_id = i.prompt_id AND p.session_id = i.session_id
WHERE i.session_id = %(sid)s AND i.seq BETWEEN %(a)s AND %(b)s ORDER BY i.seq"""  # noqa: S608 (fixed text)

# Requests: the session's credentials; its rows (a span of its calls); a Codex thread's client's
# sessionless calls in its window; the joins a row reads.
_CREDENTIALS = "SELECT key_hash FROM session_credentials WHERE session_id = %(sid)s"
_ROW_PREDICATE = f"""c.key_hash = ANY(%(creds)s) AND NOT {_UNNAMED} AND {_TRANSACTION}"""
_CALL_ROWS = f"""SELECT {_ROW_COLUMNS} FROM calls c
WHERE c.scope_session_id = %(sid)s AND (c.start_ns >= %(a)s AND c.start_ns < %(b)s
    OR c.start_ns IS NULL AND c.end_ns >= %(a)s AND c.end_ns < %(b)s)
  AND {_ROW_PREDICATE} AND (%(hidden)s OR c.hidden_kind IS NULL) AND (%(any_kind)s OR c.kind = ANY(%(kinds)s))"""  # noqa: S608
_EXTRA_ROWS = f"""SELECT {_ROW_COLUMNS} FROM calls c
WHERE c.key_hash = ANY(%(creds)s) AND c.start_ns BETWEEN %(lo)s AND %(hi)s
  AND (c.scope_session_id IS NULL OR {_UNNAMED}) AND c.source = 'codex' AND c.kind <> 'model'
  AND (CASE WHEN c.end_ns IS NULL THEN '' ELSE coalesce(c.client_mode, '') END) = %(client)s
  AND {_TRANSACTION}"""  # noqa: S608
STEP_NUMBERS = drawer.STEP_NUMBERS


# --- shaping --------------------------------------------------------------------------------------


def _family_detail(cls: str | None, model: str | None, stop: str | None) -> str:
    return f"{cls or '—'} · {pp.model_family(model)} · {stop or '—'}"


def _tool_labels(tool_uses: Any) -> list[str]:
    """A call's tool calls as labels, "exec ×2", in order (process/requestrows.go toolLabels)."""
    if not isinstance(tool_uses, list):
        return []
    out = []
    for u in tool_uses:
        if not isinstance(u, dict):
            continue
        name = u.get("name") if isinstance(u.get("name"), str) and u.get("name") else None
        name = name or (u.get("tool") if isinstance(u.get("tool"), str) and u.get("tool") else None) or "?"
        n = u.get("count")
        count = int(n) if isinstance(n, int | float) and not isinstance(n, bool) and n != 0 else 1
        out.append(f"{name} ×{count}")
    return out


def _call_detail(c: Row) -> str:
    """A call row's one-line summary (process/requestrows.go detail)."""
    kind = c["kind"]
    if kind == "model":
        return _family_detail(c["request_class"], c["model"], c["stop_reason"])
    if kind == "flags":
        return "feature flags"
    if kind == "count":
        return "count_tokens"
    path = c["path"] or ""
    p = path.split("?", 1)[0]
    if kind in ("usage", "catalog", "metrics"):
        return p.removeprefix("/backend-api/").lstrip("/") or p
    return p[p.rfind("/") + 1 :] or path


def _failed(c: Row) -> bool:
    status = c["status"] or 0
    return status == 0 and c["end_ns"] is not None or status >= 400


def _agent(c: Row) -> str | None:
    if c["source"] == "codex":
        return "codex"
    if c["source"] == "claude_code" or c["source"] is None and c["host"] == "api.anthropic.com":
        return "claude-code"
    return None


# What a call still open shows: today's row of it is read from its request headers alone
# (process/requestrows.go callFromPending), without these. (One the sweep closed is today's expired row:
# its start as recorded, with no end: stream/state/expire.go todaysRow.)
_NOT_YET = {
    "client_mode": None, "hidden_kind": None, "model": None, "effort": None, "stop_reason": None, "error": None,
    "input_tokens": None, "cache_read_tokens": None, "cache_write_tokens": None, "output_tokens": None,
    "ws_turn": None, "tool_uses": None, "text": None,
}  # fmt: skip


def call_row(c: Row, index: dict[str, int], steps: dict[str, int]) -> RequestRow:
    """A captured transaction's row (process/requestrows.go requestRow). TTFT, the cache write split and
    retries were Claude Code's analytics: null."""
    if c["pending"]:
        c = {**c, **_NOT_YET}
    kind = c["kind"]
    model = kind == "model"
    status = c["status"] or 0
    end = c["end_ns"]
    duration = _round1((end - c["ts"]) / 1e6) if end is not None else None
    hidden = c["hidden_kind"] if c["hidden_kind"] in HIDDEN_KINDS else None
    prewarm = c["hidden_kind"] == "prewarm"
    output = c["output_tokens"] or 0
    tokens = None
    if model:
        tokens = TokenCount(
            input=c["input_tokens"] or None,
            cacheRead=c["cache_read_tokens"] or None,
            cacheWrite=c["cache_write_tokens"] or None,
            cacheWrite5m=None,
            cacheWrite1h=None,
            output=0 if output == 0 and prewarm else (output or None),
            accuracy="exact" if output != 0 or prewarm else "missing",
        )
    error = None
    if _failed(c):
        msg = c["error"] or ""
        if not msg and status:
            msg = _phrase(status) or ""
        if not msg:
            msg = "HTTP " + (str(status) if status else "None")
        error = RequestError(
            message=msg,
            afterMs=duration,
            bytesReceived=c["response_bytes"] or None,
            retries=None,
            retrySucceededAt=None,
        )
    text = c["text"] if isinstance(c["text"], str) else None
    session = None if c["unnamed"] else c["scope_session_id"]
    return RequestRow(
        requestId=c["id"],
        t=dt(c["ts"]),
        kind=kind,
        agent=_agent(c),
        client=c["client_mode"] or None,
        hiddenKind=hidden,
        sessionId=session,
        sessionLabel=None,
        promptIndex=index.get(c["prompt_id"]) if c["prompt_id"] else None,
        method=c["method"] or None,
        path=c["path"] or "",
        detail=_call_detail(c),
        status=status if status else ("ERR" if end is not None else None),
        durationMs=duration,
        retries=None,
        bytesUp=c["request_bytes"] or None,
        bytesDown=c["response_bytes"] or None,
        background=kind in ("count", "analytics") or c["request_class"] in ("auxiliary", "compaction"),
        captured=True,
        statusText=_phrase(status) if status else (c["error"] or None),
        stopReason=(c["stop_reason"] or None) if model else None,
        ttftMs=None,
        tokens=tokens,
        contentEncoding=c["content_encoding"] or None,
        error=error,
        model=(c["model"] or None) if model else None,
        effort=(c["effort"] or None) if model else None,
        stepIndex=steps.get(c["id"]),
        toolCalls=_tool_labels(c["tool_uses"]) if model else [],
        usageMissing=usage_missing(
            output or None, status or None, c["stop_reason"], c["ws_turn"], c["response_bytes"], c["hidden_kind"]
        )
        if model
        else None,
        text=text if model else None,
    )


def _matches(r: RequestRow, q: str | None) -> bool:
    """`q` as the request logs read it (rows.request_filter)."""
    if not q:
        return True
    if "=" in q:
        k, _, v = q.partition("=")
        fields: dict[str, str] = {"kind": r.kind, "session": r.sessionId or "", "status": str(r.status)}
        field = fields.get(k.strip())
        return field is not None and v.strip() in field
    needle = q.lower()
    return needle in r.path.lower() or needle in r.detail.lower()


def _walk(
    cells: list[Cell], count: Callable[[Cell], int], skip: int, refine: Callable[[Cell], list[Cell]], *, desc: bool
) -> tuple[int, int] | None:
    """Where the skip-th row (0-based, in order) lies: the start (or, descending, the end) of the minute
    holding it and how many of that minute's rows come before it. An hour is refined into its
    minutes. None when there are fewer rows."""
    seq = list(reversed(cells)) if desc else cells
    for cell in seq:
        n = count(cell)
        if skip < n:
            if cell.hour:
                return _walk(refine(cell), count, skip, refine, desc=desc)
            return (cell.end if desc else cell.start), skip
        skip -= n
    return None


# --- the pages ------------------------------------------------------------------------------------


class SessionReads:
    """The session pages' reads of one session, in the request's snapshot."""

    def __init__(self, snap: Snapshot) -> None:
        self.snap = snap
        self._rows: dict[str, Row | None] = {}

    # --- the session row ------------------------------------------------------------------------

    def stored(self, sid: str) -> Row | None:
        """The session's `sessions` row."""
        if sid not in self._rows:
            found = self.snap.dicts("session", _SESSION, {"sid": sid})
            self._rows[sid] = found[0] if found else None
        return self._rows[sid]

    def session(self, sid: str, now: int) -> Row | None:
        """The session's row as today's session_rows has it (with `live` as of now)."""
        s = self.stored(sid)
        if s is None:
            return None
        title = None
        if needs_title(s):
            found = self.snap.rows(
                "title", _TITLE, {"sid": sid, "lo": s["start_ns"], "hi": s["start_ns"] + TITLE_WITHIN_NS}
            )
            title = found[0][0] if found else None
        kids = self.snap.dicts("children", _CHILDREN, {"sid": sid})
        return session_row(s, now, children_json(sid, kids), title)

    def _live(self, sid: str, now: int) -> bool:
        s = self.stored(sid)
        return s is not None and now - s["last_ns"] <= cc.LIVE_WITHIN_NS

    # --- cells ----------------------------------------------------------------------------------

    def cells(self, sid: str, lo: int | None, hi: int | None) -> list[Cell]:
        """The session's cells in [lo, hi] (each bound None: open): whole hours, minutes at the edges."""
        return [Cell(r["t"], r["t"] + r["w"], r) for r in self.snap.dicts("cells", _CELLS, _span(sid, lo, hi))]

    def minutes(self, sid: str, lo: int | None, hi: int | None) -> list[Cell]:
        return [
            Cell(r["t"], r["t"] + MINUTE, r)
            for r in self.snap.dicts("minutes", _MINUTES, {"sid": sid, "lo": lo, "hi": hi})
        ]

    def keys(self, sid: str, lo: int | None, hi: int | None, kinds: list[str]) -> list[tuple[str, str, int]]:
        return [(k, key, n) for k, key, n in self.snap.rows("keys", _KEYS, {**_span(sid, lo, hi), "kinds": kinds})]

    # --- the timeline ---------------------------------------------------------------------------

    def timeline(
        self,
        sid: str,
        r: Row,
        now_dt: datetime,
        now: int,
        lo: int,
        hi: int,
        bin_s: int,
        turns: list[TurnBlock],
        work: Callable[[int, dict[int, int]], list[WorkBin]],
    ) -> SessionTimeline:
        """The timeline, with the Turns lane given and the Work lane from `work(width, steps per bin)`
        (turnpages.TurnReads, §11R). The analytics, not-captured, friction and compaction lanes were
        analytics': empty."""
        width = bin_s * NS
        hourly = width % HOUR == 0
        cells = self.cells(sid, None, None) if hourly else self.minutes(sid, None, None)
        model = model_bins(cells, width)
        steps: Counter[int] = Counter()
        for c in cells:
            steps[c.start - c.start % width] += c.row["model_calls"]
        return SessionTimeline(
            from_=dt(lo),
            to=dt(hi),
            now=now_dt if r["live"] else None,
            binSeconds=bin_s,
            sessions=[tl.band_from_table(r, 0, alone=True)],
            prompts=self.typed_prompts(sid, r, now),
            modelCalls=model,
            analytics=[],
            notCaptured=[],
            friction=[],
            context=[
                ContextPoint(
                    t=dt(x["t"]),
                    tokens=x["tokens"],
                    requestId=x["id"],
                    model=x["model"],
                    thread="main",
                    windowTokens=None,
                    promptIndex=x["prompt_index"],
                )
                for x in self.snap.dicts("context", _CONTEXT, {"sid": sid})
            ],
            compactions=[],
            turns=turns,
            work=work(width, dict(steps)),
        )

    def typed_prompts(self, sid: str, r: Row, now: int) -> list[TimelinePrompt]:
        live = r["live"]
        out = []
        for p in self.snap.dicts("typed", _TYPED, {"sid": sid}):
            out.append(
                TimelinePrompt(
                    t=dt(p["t_ns"]),
                    index=p["prompt_index"],
                    sessionId=sid,
                    promptId=p["prompt_id"],
                    text=_text(p),
                    steps=p["steps"],
                    durationMs=_duration(p, live, now),
                    tokensIn=p["tokens_in"],
                    tokensOut=p["tokens_out"],
                )
            )
        return out

    # --- state ----------------------------------------------------------------------------------

    def state(self, sid: str, lo: int, hi: int, requests: int) -> SessionState:
        """The range readout: its counts, and the commits, pushes and PRs its steps' tool calls made; the
        tools the whole session's calls asked for. Heads, hooks, skills, recaps and pool changes were
        analytics': null."""
        cells = self.cells(sid, lo, hi)
        model_calls = sum(c.row["model_calls"] for c in cells)
        (counts,) = self.snap.dicts("item_counts", _ITEM_COUNTS, {"sid": sid, "lo": lo, "hi": hi})
        ranged: dict[str, Counter[str]] = defaultdict(Counter)
        for kind, key, n in self.keys(sid, lo, hi, ["w:commit", "w:push", "w:pr"]):
            ranged[kind][key] += n
        prs = sorted(int(k) for k in ranged["w:pr"] if k.isdigit())
        whole: dict[str, set[str]] = defaultdict(set)
        for kind, key, _ in self.keys(sid, None, None, ["call_builtin", "call_mcp"]):
            whole[kind].add(key)
        servers: dict[str, set[str]] = defaultdict(set)
        for key in whole["call_mcp"]:
            server, tool = json.loads(key)
            servers[server].add(tool)
        mcp = sorted((k, len(v)) for k, v in servers.items())
        return SessionState(
            capabilitiesAt=CapabilitiesAt(
                builtin=ToolTally(total=None, used=len(whole["call_builtin"]) or None),
                mcpServers=[McpServer(name=k, tools=None, toolsUsed=n, riskyVerbs=[]) for k, n in mcp],
                mcpTools=ToolTally(total=None, used=sum(n for _, n in mcp) if mcp else None),
                skills=None,
                hooks=None,
                poolChangesInRange=None,
            ),
            repoInRange=RepoInRange(
                branch=None,
                headFrom=None,
                headTo=None,
                headChanges=None,
                commits=sum(ranged["w:commit"].values()),
                pushed=sum(ranged["w:push"].values()) > 0,
                cwdTrail=None,
                prs=prs,
            ),
            recapsInRange=None,
            recapsTotal=None,
            experimentsAt=None,
            flagFetchesInRange=None,
            flagsChangedInRange=None,
            counts=RangeCounts(
                prompts=counts["prompts"],
                modelCalls=model_calls,
                requests=requests,
                events=0,
                compactions=0,
                turns=counts["prompts"],
                steps=model_calls,
            ),
        )

    # --- prompts --------------------------------------------------------------------------------

    def prompts(
        self, sid: str, now: int, lo: int, hi: int, page: int, size: int, prompt: int | None
    ) -> tuple[list[PromptItem | BetweenItem], int, int]:
        (span,) = self.snap.dicts("item_span", _ITEM_SPAN, {"sid": sid, "lo": lo, "hi": hi})
        total, first = span["total"], span["first"]
        if prompt is not None and total:
            at = self.snap.rows("prompt_at", _PROMPT_AT, {"sid": sid, "lo": lo, "hi": hi, "n": prompt})[0][0]
            if at is not None:
                page = (at - first) // size + 1
        if not total or (page - 1) * size >= total:
            return [], total, page
        a = first + (page - 1) * size
        b = min(a + size, first + total) - 1
        items = self.snap.dicts("items", _ITEMS, {"sid": sid, "a": a, "b": b})
        pids = sorted({i["prompt_id"] for i in items if i["type"] == "prompt"})
        live = self._live(sid, now)
        steps: dict[str, list[StepData]] = defaultdict(list)
        if pids:
            for s in self.snap.dicts("steps", STEPS, {"sid": sid, "pids": pids}):
                steps[s["prompt_id"]].append(step_data(s, live, now))
        return [_item(i, steps[i["prompt_id"]] if i["type"] == "prompt" else [], live, now) for i in items], total, page

    # --- events ---------------------------------------------------------------------------------

    def events(
        self, sid: str, lo: int, hi: int, internal: bool, q: str | None, page: int, size: int
    ) -> tuple[int, int, list[EventRow]]:
        """None: analytics events aren't stored (they stay in the raw files)."""
        return 0, 0, []

    # --- requests -------------------------------------------------------------------------------

    def requests(
        self, sid: str, lo: int | None, hi: int | None, kind: Sequence[str] | None, q: str | None,
        captured: bool | None, page: int, size: int, order: str, hidden: bool,
    ) -> tuple[int, int, dict[str, int], list[RequestRow]]:  # fmt: skip
        """(hidden count, total, counts by kind, the page's rows)."""
        s = self.stored(sid)
        if s is None:
            return 0, 0, {}, []
        codex = s["product"] == "codex"
        creds = sorted({k for (k,) in self.snap.rows("credentials", _CREDENTIALS, {"sid": sid})})
        whole = self.cells(sid, None, None) if lo is None and hi is None else None
        cells = whole if whole is not None else self.cells(sid, lo, hi)
        if not codex:  # its analytics posts' credentials (the posts its calls' rows count)
            batch_cells = whole if whole is not None else self.cells(sid, None, None)
            creds = sorted(
                set(creds)
                | {
                    k.split("/", 1)[1]
                    for c in batch_cells
                    for m in (c.row["requests"], c.row["requests_hidden"])
                    for k in m
                    if k.startswith("analytics/")
                }
            )
        extras: list[Row] = []
        if codex:
            extras = [
                x
                for x in self.snap.dicts(
                    "extra_rows",
                    _EXTRA_ROWS,
                    {
                        "creds": creds,
                        "lo": s["start_ns"] // 1000 * 1000,
                        "hi": (s["last_ns"] + MINUTE) // 1000 * 1000 + 999,
                        "client": s["client"] or "",
                    },
                )
                if (lo is None or x["ts"] >= lo) and (hi is None or x["ts"] <= hi)
            ]
        kinds = list(kind or [])

        def counts_of(cell: Cell) -> Counter[tuple[str, bool]]:
            """The cell's rows by (kind, hidden), within the captured filter."""
            got: Counter[tuple[str, bool]] = Counter()
            if captured is not False:
                for col, hid in (("requests", False), ("requests_hidden", True)):
                    for k, n in cell.row[col].items():
                        kd, _, h = k.partition("/")
                        if h in creds:
                            got[kd, hid] += n
            return got

        extra_counts: Counter[tuple[str, bool]] = Counter()
        if captured is not False:
            for x in extras:
                extra_counts[x["kind"], x["hidden_kind"] is not None] += 1

        def keep(k: str, hid: bool) -> bool:
            return (hidden or not hid) and (not kinds or k in kinds)

        if q:
            rows = [r for r in self._request_rows(sid, s, creds, extras, lo, hi, captured, True, []) if _matches(r, q)]
            hidden_count = sum(r.hiddenKind is not None for r in rows)
            by_kind: Counter[str] = Counter(r.kind for r in rows if hidden or r.hiddenKind is None)
            shown = [r for r in rows if keep(r.kind, r.hiddenKind is not None)]
            total = len(shown)
            shown.sort(key=lambda r: (r.t, r.requestId), reverse=order == "desc")
            return (
                hidden_count,
                total,
                dict(sorted(by_kind.items())),
                shown[(page - 1) * size : page * size],
            )

        allc: Counter[tuple[str, bool]] = Counter(extra_counts)
        for c in cells:
            allc += counts_of(c)
        hidden_count = sum(n for (_, hid), n in allc.items() if hid)
        kind_counts: Counter[str] = Counter()
        for (k, hid), n in allc.items():
            if hidden or not hid:
                kind_counts[k] += n
        total = sum(n for (k, hid), n in allc.items() if keep(k, hid))
        skip = (page - 1) * size
        if size <= 0 or skip >= total:
            return hidden_count, total, dict(sorted((k, n) for k, n in kind_counts.items() if n)), []

        # The page: from the minute its first row lies in, as many minutes as hold its rows.
        extra_at = sorted(
            x["ts"] for x in extras if captured is not False and keep(x["kind"], x["hidden_kind"] is not None)
        )

        def count(cell: Cell) -> int:
            got = sum(n for (k, hid), n in counts_of(cell).items() if keep(k, hid))
            return got + bisect_right(extra_at, cell.end - 1) - bisect_right(extra_at, cell.start - 1)

        def refine(cell: Cell) -> list[Cell]:
            inside = [t for t in extra_at if cell.start <= t < cell.end]
            return _with_extras(self.minutes(sid, cell.start, cell.end - 1), inside)

        desc = order == "desc"
        span_cells = cells
        lo_all, hi_all = lo, hi
        if extras:  # the extras lie in the session's window; cells cover only its own minutes
            span_cells = _with_extras(cells, extra_at)
        found = _walk(span_cells, count, skip, refine, desc=desc)
        if found is None:
            return hidden_count, total, dict(sorted((k, n) for k, n in kind_counts.items() if n)), []
        at, inner = found
        need = inner + size
        window = self._window(span_cells, at, need, count, refine, desc=desc)
        a, b = window
        if lo_all is not None:
            a = max(a, lo_all)
        if hi_all is not None:
            b = min(b, hi_all + 1)
        rows = self._request_rows(sid, s, creds, extras, a, b - 1, captured, hidden, kinds)
        rows.sort(key=lambda r: (r.t, r.requestId), reverse=desc)
        rows = rows[inner : inner + size]
        return (
            hidden_count,
            total,
            dict(sorted((k, n) for k, n in kind_counts.items() if n)),
            rows,
        )

    @staticmethod
    def _window(
        cells: list[Cell], at: int, need: int, count: Callable[[Cell], int], refine: Callable[[Cell], list[Cell]],
        *, desc: bool,
    ) -> tuple[int, int]:  # fmt: skip
        """[a, b): from the landing minute `at` (descending: up to it), the minutes that hold `need` rows (an
        hour's minutes read as it's reached)."""
        got = 0
        if desc:
            a = at
            for c in reversed(cells):
                if c.start >= at:
                    continue
                for p in reversed(refine(c)) if c.hour else [c]:
                    if p.start >= at:
                        continue
                    got += count(p)
                    a = p.start
                    if got >= need:
                        return a, at
            return a, at
        b = at
        for c in cells:
            if c.end <= at:
                continue
            for p in refine(c) if c.hour else [c]:
                if p.start < at:
                    continue
                got += count(p)
                b = p.end
                if got >= need:
                    return at, b
        return at, b

    def _request_rows(
        self, sid: str, s: Row, creds: list[str], extras: list[Row], lo: int | None, hi: int | None,
        captured: bool | None, hidden: bool, kinds: list[str],
    ) -> list[RequestRow]:  # fmt: skip
        """The tab's rows in [lo, hi] (None: open) that the filters keep (but `q`)."""
        a = lo if lo is not None else -(1 << 62)
        b = hi + 1 if hi is not None else 1 << 62
        calls: list[Row] = []
        if captured is not False:
            calls = self.snap.dicts(
                "call_rows",
                _CALL_ROWS,
                {"sid": sid, "a": a, "b": b, "creds": creds, "hidden": hidden, "any_kind": not kinds, "kinds": kinds},
            )
            calls += [
                x
                for x in extras
                if a <= x["ts"] < b and (hidden or x["hidden_kind"] is None) and (not kinds or x["kind"] in kinds)
            ]
        pids = sorted({c["prompt_id"] for c in calls if c["prompt_id"]})
        index: dict[str, int] = {}
        if pids:
            index = dict(self.snap.rows("prompt_indexes", drawer.PROMPT_INDEXES, {"sid": sid, "pids": pids}))
        model_ids = [c["id"].replace("~", "#") for c in calls if c["kind"] == "model" and c["prompt_id"]]
        steps: dict[str, int] = {}
        if model_ids:
            steps = {
                cid.replace("#", "~"): n for cid, n in self.snap.rows("step_numbers", STEP_NUMBERS, {"ids": model_ids})
            }
        return [call_row(c, index, steps) for c in calls]

    def request_count(self, sid: str, lo: int | None, hi: int | None) -> int:
        """The Requests tab's total in [lo, hi] with every row shown (the state readout's)."""
        _, total, _, _ = self.requests(sid, lo, hi, None, None, None, 1, 0, "asc", True)
        return total


def _with_extras(cells: list[Cell], extra_at: list[int]) -> list[Cell]:
    """The cells, with a minute for each extra row's minute that has no cell of its own."""
    have = [(c.start, c.end) for c in cells]

    def covered(t: int) -> bool:
        return any(a <= t < b for a, b in have)

    out = list(cells)
    for m in sorted({t - t % MINUTE for t in extra_at if not covered(t)}):
        out.append(Cell(m, m + MINUTE, {"requests": {}, "requests_hidden": {}}))
    out.sort(key=lambda c: c.start)
    return out


def model_bins(cells: list[Cell], width: int) -> list[ModelCallBin]:
    """The model-call lane: the cells' calls and tokens per bin of `width` ns (bins with calls only)."""
    model: dict[int, Counter[str]] = defaultdict(Counter)
    known: dict[int, set[str]] = defaultdict(set)
    for c in cells:
        row = c.row
        if not row["model_calls"]:
            continue
        b = c.start - c.start % width
        m = model[b]
        for k in ("model_calls", "main_calls", "subagent_calls"):
            m[k] += row[k]
        for k in ("output_tokens", "context_sum"):
            if row[k] is not None:
                m[k] += row[k]
                known[b].add(k)
    out = []
    for b in sorted(model):
        m = model[b]
        n = m["model_calls"]
        out.append(
            ModelCallBin(
                t=dt(b),
                count=n,
                tokens=m["output_tokens"],
                byClass=CallClasses(
                    main=m["main_calls"],
                    subagent=m["subagent_calls"],
                    auxiliary=n - m["main_calls"] - m["subagent_calls"],
                ),
                tokensIn=m["context_sum"] if "context_sum" in known[b] else None,
                tokensOut=m["output_tokens"] if "output_tokens" in known[b] else None,
                notCaptured=0,
            )
        )
    return out


def _text(p: Row) -> PromptText:
    """A prompt's text and size; pastes and images were analytics': null."""
    return PromptText(
        retained=p["text"] is not None,
        text=p["text"],
        chars=p["text_chars"],
        pastedBytes=None,
        images=None,
    )


def _in_flight(p: Row, live: bool, now: int) -> bool:
    unfinished = p["unfinished_max_ns"]
    return live and (p["open_tail"] or (unfinished is not None and now - unfinished <= ss.STREAMING_WITHIN_NS))


def _duration(p: Row, live: bool, now: int) -> float | None:
    """The prompt's duration: to its last step's end, unless still in flight."""
    if p["ended_ns"] is None or _in_flight(p, live, now):
        return None
    return _round1((p["ended_ns"] - p["t_ns"]) / 1e6)


def _item(i: Row, steps: list[StepData], live: bool, now: int) -> PromptItem | BetweenItem:
    if i["type"] == "between":
        return BetweenItem(
            type="between",
            between=BetweenData(
                kind=i["between_kind"],
                t=dt(i["t_ns"]),
                detail=i["detail"],
                requestId=i["call_id"].replace("#", "~") if i["call_id"] else None,
                tokensFrom=None,
                tokensTo=None,
                waitMs=None,
            ),
        )
    counts = sorted((i["tool_counts"] or {}).items(), key=lambda x: (-x[1], x[0]))
    prompt = PromptData(
        promptId=i["prompt_id"],
        index=i["prompt_index"],
        t=dt(i["t_ns"]),
        durationMs=_duration(i, live, now),
        text=_text(i),
        steps=i["steps"],
        tokensIn=i["tokens_in"],
        tokensOut=i["tokens_out"],
        toolCounts=dict(counts),
        friction=PromptFriction(frustrated=False, interrupted=bool(i["interrupted"]), destructive=0),
        inFlight=_in_flight(i, live, now),
        turnReport=None,
        hidden=i["hidden"] or {},
    )
    return PromptItem(type="prompt", prompt=prompt, steps=steps)


def step_data(s: Row, live: bool, now: int) -> StepData:
    """A steps row as the API's StepData, with what depends on now (session.py _step). TTFT, the cache
    write split and cost were Claude Code's analytics: null."""
    end = s["end_ns"]
    if end is None:
        status = "streaming" if live and now - s["t_ns"] <= ss.STREAMING_WITHIN_NS else "error"
    else:
        status = "error" if s["failed"] else "done"
    return StepData(
        requestId=s["call_id"].replace("#", "~"),
        t=dt(s["t_ns"]),
        kind=s["kind"],
        model=s["model"],
        requestClass=s["request_class"],
        tokens=TokenCount(
            input=s["tokens_input"],
            cacheRead=s["tokens_cache_read"],
            cacheWrite=s["tokens_cache_write"],
            cacheWrite5m=None,
            cacheWrite1h=None,
            output=s["tokens_output"],
            accuracy="exact" if s["tokens_output"] is not None else "missing",
        ),
        durationMs=_round1((end - s["t_ns"]) / 1e6) if end is not None else None,
        ttftMs=None,
        stopReason=s["stop_reason"],
        costUsd=None,
        status=status,
        eventsPending=False,
        agentType=s["agent_type"],
        tools=s["tools"],
        does=s["does"],
        commands=list(s["commands"]),
        patches=s["patches"],
        text=s["text"],
        exitCodes=list(s["exit_codes"]),
        usageMissing=s["usage_missing"],
    )
