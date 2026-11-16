"""The session pages over v9 (docs/PIPELINE.md §6.1): what each page shows, read from the facts and the
rollups through a few indexed statements in the request's snapshot, and shaped by today's code.

- /sessions/{id}: its `sessions` row. What a page computes from the stored values: the agent, bypass %,
  live, Codex's limits (agents.codex_limits of the stored headers), the children (`session_edges`,
  §5.4) and the label (its own title, else the earliest title call of its accounts 0–5 minutes after
  its start).
- /timeline: the model-call and analytics lanes from `session_minutes` (bins under an hour) or
  `session_hours`, the top event names from `session_minute_keys` or `session_keys`; the not-captured
  windows from the row's coverage windows; friction from the friction minutes' events, each with its
  prompt's number from `prompts`; typed prompts from `prompts`; compactions from `prompt_items`;
  context points from `calls` by (session, time), one per main-thread call (what the chart draws).
- /state: counts from `session_minutes` in the brush (hours inside it, minutes at its edges); heads,
  PRs, commits and pushes from `session_keys` the same way; capabilities over the whole session from
  its `session_keys`; recaps and the latest counts by event name.
- /events: a page of `events` by (session, time), found through the minutes' counts; prompt numbers
  and `linkedTo` by join (§5.4); totals from `session_minutes`.
- /requests: a page of `calls` by (scope session, time) with the session's `missed_reports`, and for a
  Codex thread its client's sessionless calls in its window by (credential, time); the page is found
  through the minutes' counts per kind and credential; step numbers from `steps`, TTFT and cache from
  the report (§5.4).
- /prompts: a page of `prompt_items` by (session, seq), with each prompt's `prompts` row and `steps`.

Text search (`q`) scans the session's rows in the range (§6.1). Nothing is cached between requests.
"""

import json
import math
import re
from bisect import bisect_right
from collections import Counter, defaultdict
from collections.abc import Callable, Sequence
from dataclasses import dataclass
from datetime import UTC, datetime
from decimal import ROUND_HALF_UP, Decimal, InvalidOperation
from http import HTTPStatus
from typing import Any

from aintercept_api import agents, drawer
from aintercept_api import claude_code as cc
from aintercept_api import person as pp
from aintercept_api import session as ss
from aintercept_api import timeline as tl
from aintercept_api.models.people import (
    AnalyticsBin,
    CallClasses,
    EventCount,
    FrictionMark,
    GapWindow,
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
    Compaction,
    ContextPoint,
    EventRow,
    PromptData,
    PromptFriction,
    PromptItem,
    RangeCounts,
    Recap,
    RepoInRange,
    SessionState,
    SessionTimeline,
    StepData,
    ToolTally,
    TurnReport,
)
from aintercept_api.models.work import TurnBlock, WorkBin
from aintercept_api.reader import Snapshot
from aintercept_api.rows import dt, usage_missing

Row = dict[str, Any]

NS = cc.NS
MINUTE = 60 * NS
HOUR = 60 * MINUTE
TITLE_WITHIN_NS = 5 * MINUTE
RETRY_AFTER_NS = 30 * NS

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
  c.output_tokens, c.ws_turn, c.tool_uses, c.detail -> 'text' AS text, c.request_id, c.client_request_id,
  c.batch_events, c.batch_name, c.key_hash"""


# --- small helpers ------------------------------------------------------------------------------------


def _round1(x: float) -> float:
    return round(x, 1)


_NUMERIC = re.compile(r"^[+-]?([0-9]+\.?[0-9]*|\.[0-9]+)([eE][+-]?[0-9]+)?$")


def json_int(v: Any) -> int | None:
    """TRY_CAST(json_extract_string(v) AS BIGINT): a number or numeric string, rounded half away from zero."""
    if v is None or isinstance(v, bool):
        return None
    if isinstance(v, int):
        return v
    text = repr(v) if isinstance(v, float) else str(v).strip() if isinstance(v, str) else None
    if text is None or not _NUMERIC.match(text):
        return None
    try:
        return int(Decimal(text).to_integral_value(rounding=ROUND_HALF_UP))
    except (InvalidOperation, ValueError, OverflowError):
        return None


def json_double(v: Any) -> float | None:
    """TRY_CAST(json_extract_string(v) AS DOUBLE)."""
    if v is None or isinstance(v, bool):
        return None
    if isinstance(v, int | float):
        return float(v)
    if isinstance(v, str) and _NUMERIC.match(v.strip()):
        return float(v.strip())
    return None


def _json_str(v: Any) -> str | None:
    """json_extract_string: a string's value, another scalar's text; None for null, an object or a list."""
    if v is None or isinstance(v, dict | list):
        return None
    if isinstance(v, bool):
        return "true" if v else "false"
    return v if isinstance(v, str) else json.dumps(v)


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
    stored values (the agent, bypass %, live, Codex's limits), its children and its label (§6.1: its own
    title, else its title call's)."""
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
    r["bypass_pct"] = _round1(100 * s["bypassed"] / s["queries"]) if s["queries"] else None
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

_CELL_COLUMNS = """model_calls, main_calls, subagent_calls, output_tokens, context_sum, missed, events, events_shown,
    frustrated, interrupted, destructive, requests, requests_hidden"""


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
# Each bin's event names: the hours' or minutes' name keys.
_HOUR_NAMES = "SELECT hour_ns AS t, key, n FROM session_keys WHERE session_id = %(sid)s AND kind = 'name'"
_MINUTE_NAMES = "SELECT minute_ns AS t, key, n FROM session_minute_keys WHERE session_id = %(sid)s AND kind = 'name'"

# The typed prompts (the timeline's marks): a prompt with its input, or a Codex thread's turn.
_TYPED = """SELECT i.seq, i.t_ns, i.prompt_id, i.prompt_index, i.open_tail, i.pasted_bytes, i.images, p.steps,
    p.text, p.text_chars, p.tokens_in, p.tokens_out, p.turn_end_ns, p.turn_report, p.unfinished_max_ns
FROM prompt_items i JOIN prompts p ON p.prompt_id = i.prompt_id AND p.session_id = i.session_id
WHERE i.session_id = %(sid)s AND i.type = 'prompt' AND (p.has_input OR %(codex)s)
ORDER BY i.t_ns / 1000, i.seq"""
_FRICTION_MINUTES = """SELECT minute_ns FROM session_minutes WHERE session_id = %(sid)s
  AND (frustrated > 0 OR interrupted > 0 OR destructive > 0) ORDER BY minute_ns"""
# The friction minutes' events (claude_code.py friction_marks), each with its prompt's number (the
# input's own, else its prompt's from `prompts`).
_FRICTION = """SELECT e.ts_ns, e.name, e.metadata -> 'is_negative' AS negative, e.metadata -> 'prompt_index' AS idx,
    e.metadata -> 'destructive_category' AS category, e.metadata -> 'destructive_target_scope' AS scope,
    (SELECT p.prompt_index FROM prompts p WHERE p.prompt_id = e.metadata ->> 'cc_prompt_id' AND p.session_id = %(sid)s
       AND p.has_input) AS prompt_index
FROM unnest(%(minutes)s::bigint[]) m(t), LATERAL (
  SELECT * FROM events e WHERE e.session_id = %(sid)s
    AND e.name IN ('tengu_input_prompt', 'tengu_cancel', 'tengu_bash_tool_command_executed')
    AND e.ts_ns >= m.t AND e.ts_ns < m.t + 60000000000 AND e.source = 'claude_code') e
ORDER BY e.ts_ns, e.batch_call_id COLLATE "C", e.batch_cc_n"""
# Not-captured windows' cross-checks: the reports in each, and whether any call was captured in it.
_WINDOW = f"""SELECT (SELECT count(*) FROM events e WHERE e.session_id = %(sid)s AND e.name = 'tengu_api_success'
      AND e.ts_ns BETWEEN %(a)s AND %(b)s AND e.source = 'claude_code') AS reported,
  NOT EXISTS (SELECT 1 FROM calls c WHERE c.session_id = %(sid)s AND {_AT} BETWEEN %(a)s AND %(b)s AND {VISIBLE})
    AS bypassed"""  # noqa: S608
# Context points: one per main-thread visible call that sent context.
_CONTEXT = f"""SELECT {_ID} AS id, {_AT} AS t, c.model,
    nullif(coalesce(c.input_tokens, 0) + coalesce(c.cache_read_tokens, 0) + coalesce(c.cache_write_tokens, 0), 0)
      AS tokens,
    (SELECT p.prompt_index FROM prompts p
     WHERE p.prompt_id = c.prompt_id AND p.session_id = c.session_id AND p.has_input)
      AS prompt_index
FROM calls c WHERE c.session_id = %(sid)s AND c.request_class = 'main' AND {VISIBLE}
  AND (c.input_tokens IS NOT NULL OR c.cache_read_tokens IS NOT NULL OR c.cache_write_tokens IS NOT NULL)
ORDER BY {_AT}, {_ID} COLLATE "C\""""  # noqa: S608
_COMPACTIONS = """SELECT t_ns, between_kind, tokens_from, tokens_to, attempt_ns FROM prompt_items
WHERE session_id = %(sid)s AND type = 'between' AND between_kind IN ('compaction', 'idle-compaction')
  AND (%(lo)s::bigint IS NULL OR t_ns BETWEEN %(lo)s AND %(hi)s)
ORDER BY seq"""

# State: the range's prompts and compactions, the head at each end, the recaps, the latest counts.
_ITEM_COUNTS = """SELECT count(*) FILTER (WHERE type = 'prompt') AS prompts,
    count(*) FILTER (WHERE between_kind IN ('compaction', 'idle-compaction')) AS compactions
FROM prompt_items WHERE session_id = %(sid)s AND t_ns BETWEEN %(lo)s AND %(hi)s"""
_HEADS = """SELECT
  (SELECT head_sha FROM events WHERE session_id = %(sid)s AND ts_ns BETWEEN %(lo)s AND %(hi)s AND head_sha IS NOT NULL
     AND source = 'claude_code' ORDER BY ts_ns, head_sha COLLATE "C" LIMIT 1) AS head_from,
  (SELECT head_sha FROM events WHERE session_id = %(sid)s AND ts_ns BETWEEN %(lo)s AND %(hi)s AND head_sha IS NOT NULL
     AND source = 'claude_code' ORDER BY ts_ns DESC, head_sha COLLATE "C" DESC LIMIT 1) AS head_to"""
_RECAPS = """SELECT ts_ns, metadata ? 'hadRecap' AS has,
    metadata -> 'hadRecap' IN ('true'::jsonb, '"true"'::jsonb) AS recap
FROM events WHERE session_id = %(sid)s AND name = 'tengu_return_to_session' AND source = 'claude_code'
  AND jsonb_typeof(metadata) = 'object'
ORDER BY ts_ns, batch_call_id COLLATE "C", batch_cc_n"""
_LATEST = """SELECT (SELECT e.metadata -> 'mcpToolCount' FROM events e WHERE e.session_id = %(sid)s
     AND e.name = 'tengu_tool_search_mode_decision' AND e.source = 'claude_code'
     ORDER BY e.ts_ns DESC, e.batch_call_id COLLATE "C" DESC, e.batch_cc_n DESC LIMIT 1) AS mcp,
  (SELECT e.metadata -> 'total' FROM events e WHERE e.session_id = %(sid)s
     AND e.name = 'tengu_skills_sync_success' AND e.source = 'claude_code'
     ORDER BY e.ts_ns DESC, e.batch_call_id COLLATE "C" DESC, e.batch_cc_n DESC LIMIT 1) AS skills"""

# Prompts: the range's items (seq follows time, so they're a run of seqs), a page of them, and their steps.
_ITEM_SPAN = """SELECT count(*) AS total, min(seq) AS first FROM prompt_items
WHERE session_id = %(sid)s AND t_ns BETWEEN %(lo)s AND %(hi)s"""
_PROMPT_AT = """SELECT min(seq) FROM prompt_items WHERE session_id = %(sid)s AND t_ns BETWEEN %(lo)s AND %(hi)s
  AND type = 'prompt' AND prompt_index = %(n)s"""
_ITEMS = """SELECT i.*, p.has_input, p.text, p.text_chars, p.frustrated, p.steps, p.unfinished_max_ns, p.turn_end_ns,
    p.tokens_in, p.tokens_out, p.tool_counts, p.interrupted, p.destructive, p.turn_report
FROM prompt_items i LEFT JOIN prompts p ON p.prompt_id = i.prompt_id AND p.session_id = i.session_id
WHERE i.session_id = %(sid)s AND i.seq BETWEEN %(a)s AND %(b)s ORDER BY i.seq"""
_STEPS = """SELECT * FROM steps WHERE session_id = %(sid)s AND prompt_id = ANY(%(pids)s) ORDER BY prompt_id, step"""

# Events: a page from a minute on, in today's order (time, batch, place in the batch).
_EVENT_PAGE = f"""SELECT {drawer._EVENT} FROM events e
WHERE e.session_id = %(sid)s AND e.ts_ns >= %(from)s AND e.ts_ns <= %(hi)s AND e.source = 'claude_code'
  AND (%(internal)s OR NOT e.internal)
  AND (%(q)s = '' OR strpos(lower(coalesce(e.name, '(unnamed)')), %(q)s) > 0 OR strpos(lower(e.summary), %(q)s) > 0)
ORDER BY e.ts_ns, e.batch_call_id COLLATE "C", e.batch_cc_n LIMIT %(n)s OFFSET %(skip)s"""  # noqa: S608
_EVENT_COUNTS = """SELECT count(*) FILTER (WHERE (%(internal)s OR NOT e.internal)
    AND (strpos(lower(coalesce(e.name, '(unnamed)')), %(q)s) > 0 OR strpos(lower(e.summary), %(q)s) > 0))
FROM events e WHERE e.session_id = %(sid)s AND e.ts_ns BETWEEN %(lo)s AND %(hi)s AND e.source = 'claude_code'"""

# Requests: the session's credentials; its rows (a span of its calls, its missed reports); a Codex
# thread's client's sessionless calls in its window; the joins a row reads.
_CREDENTIALS = "SELECT key_hash FROM session_credentials WHERE session_id = %(sid)s"
_ROW_PREDICATE = f"""c.key_hash = ANY(%(creds)s) AND NOT {_UNNAMED} AND {_TRANSACTION}"""
_CALL_ROWS = f"""SELECT {_ROW_COLUMNS} FROM calls c
WHERE c.scope_session_id = %(sid)s AND (c.start_ns >= %(a)s AND c.start_ns < %(b)s
    OR c.start_ns IS NULL AND c.end_ns >= %(a)s AND c.end_ns < %(b)s)
  AND {_ROW_PREDICATE} AND (%(hidden)s OR c.hidden_kind IS NULL) AND (%(any_kind)s OR c.kind = ANY(%(kinds)s))"""  # noqa: S608
_MISSED_ROWS = """SELECT event_key, ts_ns, request_id, client_request_id FROM missed_reports
WHERE session_id = %(sid)s AND ts_ns >= %(a)s AND ts_ns < %(b)s"""
_EXTRA_ROWS = f"""SELECT {_ROW_COLUMNS} FROM calls c
WHERE c.key_hash = ANY(%(creds)s) AND c.start_ns BETWEEN %(lo)s AND %(hi)s
  AND (c.scope_session_id IS NULL OR {_UNNAMED}) AND c.source = 'codex' AND c.kind <> 'model'
  AND (CASE WHEN c.end_ns IS NULL THEN '' ELSE coalesce(c.client_mode, '') END) = %(client)s
  AND {_TRANSACTION}"""  # noqa: S608
# The first report of each id (as its request or client request id), in today's order.
_REPORTS = """SELECT k, r.metadata -> 'ttftMs' AS ttft, r.metadata -> 'cache_creation_5m_input_tokens' AS c5,
    r.metadata -> 'cache_creation_1h_input_tokens' AS c1
FROM unnest(%(keys)s::text[]) k, LATERAL (
  SELECT r.metadata FROM events r WHERE (r.request_id = k OR r.client_request_id = k) AND r.session_id = %(sid)s
    AND r.source = 'claude_code' AND r.name = 'tengu_api_success'
  ORDER BY r.ts_ns, r.event_key COLLATE "C" LIMIT 1) r"""
_STEP_NUMBERS = "SELECT call_id, step FROM steps WHERE call_id = ANY(%(ids)s) AND session_id = %(sid)s"
_RETRIES = """SELECT ts_ns, metadata -> 'cc_prompt_id' AS prompt, metadata -> 'attempt' AS attempt FROM events
WHERE session_id = %(sid)s AND name = 'tengu_api_retry' AND source = 'claude_code' AND ts_ns BETWEEN %(a)s AND %(b)s
  AND jsonb_typeof(metadata) = 'object'
ORDER BY ts_ns, event_key COLLATE "C\""""
_NEXT_ATTEMPT = f"""SELECT {_AT} AS ts, c.status FROM calls c
WHERE c.scope_session_id = %(sid)s AND c.kind = 'model' AND {_ROW_PREDICATE}
  AND c.prompt_id IS NOT DISTINCT FROM %(prompt)s AND c.request_class IS NOT DISTINCT FROM %(cls)s
  AND (c.start_ns >= %(ts)s OR c.start_ns IS NULL AND c.end_ns >= %(ts)s)
  AND ({_AT} > %(ts)s OR {_AT} = %(ts)s AND {_ID} COLLATE "C" > %(id)s)
ORDER BY {_AT}, {_ID} COLLATE "C" LIMIT 1"""  # noqa: S608


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
    if kind == "analytics":
        n, name = c["batch_events"] or 0, c["batch_name"] or ""
        if c["source"] == "codex" and name:
            return name if n == 1 else f"{name} · {n} events"
        return f"{n} events"
    if kind == "flags":
        return "feature flags"
    if kind == "count":
        return "count_tokens"
    path = c["path"] or ""
    p = path.split("?", 1)[0]
    if kind in ("usage", "catalog", "metrics"):
        return p.removeprefix("/backend-api/").lstrip("/") or p
    return p[p.rfind("/") + 1 :] or path


@dataclass
class _Report:
    ttft: float | None
    cache_5m: int | None
    cache_1h: int | None


@dataclass
class _Attempt:
    retries: int | None
    succeeded_at: int | None


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


def call_row(
    c: Row, index: dict[str, int], reports: dict[str, _Report], steps: dict[str, int], attempt: _Attempt | None
) -> RequestRow:
    """A captured transaction's row (process/requestrows.go requestRow)."""
    if c["pending"]:
        c = {**c, **_NOT_YET}
    kind = c["kind"]
    model = kind == "model"
    rep = None
    if model:
        rep = (reports.get(c["request_id"]) if c["request_id"] else None) or (
            reports.get(c["client_request_id"]) if c["client_request_id"] else None
        )
    status = c["status"] or 0
    end = c["end_ns"]
    duration = _round1((end - c["ts"]) / 1e6) if end is not None else None
    hidden = c["hidden_kind"] if c["hidden_kind"] in ("prewarm", "title") else None
    prewarm = c["hidden_kind"] == "prewarm"
    output = c["output_tokens"] or 0
    tokens = None
    if model:
        tokens = TokenCount(
            input=c["input_tokens"] or None,
            cacheRead=c["cache_read_tokens"] or None,
            cacheWrite=c["cache_write_tokens"] or None,
            cacheWrite5m=rep.cache_5m if rep else None,
            cacheWrite1h=rep.cache_1h if rep else None,
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
            retries=attempt.retries if attempt else None,
            retrySucceededAt=dt(attempt.succeeded_at) if attempt and attempt.succeeded_at is not None else None,
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
        retries=attempt.retries if attempt else None,
        bytesUp=c["request_bytes"] or None,
        bytesDown=c["response_bytes"] or None,
        background=kind in ("count", "analytics") or c["request_class"] in ("auxiliary", "compaction"),
        captured=True,
        statusText=_phrase(status) if status else (c["error"] or None),
        stopReason=(c["stop_reason"] or None) if model else None,
        ttftMs=rep.ttft if rep else None,
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


def missed_row(m: Row, sid: str, reports: dict[str, _Report]) -> RequestRow:
    """A call Claude Code reported that the proxy never saw (process/requestrows.go missedRows)."""
    rid, crid = m["request_id"], m["client_request_id"]
    rep = (reports.get(rid) if rid else None) or (reports.get(crid) if crid else None)
    return RequestRow(
        requestId="reported:" + (rid or crid or str(m["ts_ns"])),
        t=dt(m["ts_ns"]),
        kind="model",
        agent="claude-code",
        client=None,
        hiddenKind=None,
        sessionId=sid,
        sessionLabel=None,
        promptIndex=None,
        method=None,
        path="/v1/messages",
        detail="reported by Claude Code · not captured",
        status=None,
        durationMs=None,
        retries=None,
        bytesUp=None,
        bytesDown=None,
        background=False,
        captured=False,
        statusText=None,
        stopReason=None,
        ttftMs=rep.ttft if rep else None,
        tokens=TokenCount(
            input=None,
            cacheRead=None,
            cacheWrite=None,
            cacheWrite5m=rep.cache_5m if rep else None,
            cacheWrite1h=rep.cache_1h if rep else None,
            output=None,
            accuracy="missing",
        ),
        contentEncoding=None,
        error=None,
        model=None,
        effort=None,
        stepIndex=None,
        toolCalls=[],
        usageMissing=None,
        text=None,
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
        self._failed: dict[str, Row] = {}  # the failed model calls a requests read built rows of, by row id

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
        (turnpages.TurnReads, §11R)."""
        width = bin_s * NS
        hourly = width % HOUR == 0
        cells = self.cells(sid, None, None) if hourly else self.minutes(sid, None, None)
        model: dict[int, Counter[str]] = defaultdict(Counter)
        known: dict[int, set[str]] = defaultdict(set)
        events: Counter[int] = Counter()
        for c in cells:
            b = c.start - c.start % width
            row = c.row
            if row["model_calls"] or row["missed"]:
                m = model[b]
                for k in ("model_calls", "main_calls", "subagent_calls", "missed"):
                    m[k] += row[k]
                for k in ("output_tokens", "context_sum"):
                    if row[k] is not None:
                        m[k] += row[k]
                        known[b].add(k)
            events[b] += row["events"]
        model_bins = []
        for b in sorted(model):
            m = model[b]
            n = m["model_calls"]
            model_bins.append(
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
                    notCaptured=m["missed"],
                )
            )
        names: dict[int, Counter[str]] = defaultdict(Counter)
        for t, key, n in self.snap.rows("names", _HOUR_NAMES if hourly else _MINUTE_NAMES, {"sid": sid}):
            names[t - t % width][key] += n
        analytics = []
        for b in sorted(k for k, n in events.items() if n):
            got = names[b]
            unnamed = events[b] - sum(got.values())
            if unnamed > 0:
                got["(unnamed)"] += unnamed
            top = sorted(got.items(), key=lambda x: (-x[1], x[0]))[: tl.TOP_EVENTS]
            analytics.append(AnalyticsBin(t=dt(b), count=events[b], top=[EventCount(name=k, count=n) for k, n in top]))
        return SessionTimeline(
            from_=dt(lo),
            to=dt(hi),
            now=now_dt if r["live"] else None,
            binSeconds=bin_s,
            sessions=[tl.band_from_table(r, 0, alone=True)],
            prompts=self.typed_prompts(sid, r, now),
            modelCalls=model_bins,
            analytics=analytics,
            notCaptured=self.gaps(sid, r),
            friction=self.friction(sid, width),
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
            compactions=[
                Compaction(
                    t=dt(c["t_ns"]),
                    from_=c["tokens_from"],
                    to=c["tokens_to"],
                    idle=c["between_kind"] == "idle-compaction",
                    waitMs=_round1((c["t_ns"] - c["attempt_ns"]) / 1e6) if c["attempt_ns"] is not None else None,
                    messages=None,
                )
                for c in self.snap.dicts("compactions", _COMPACTIONS, {"sid": sid, "lo": None, "hi": None})
            ],
            turns=turns,
            work=work(width, {b: m["model_calls"] for b, m in model.items()}),
        )

    def typed_prompts(self, sid: str, r: Row, now: int) -> list[TimelinePrompt]:
        live = r["live"]
        out = []
        for p in self.snap.dicts("typed", _TYPED, {"sid": sid, "codex": r["product"] == "codex"}):
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

    def gaps(self, sid: str, r: Row) -> list[GapWindow]:
        out = []
        for a, b, n in zip(r["cov_window_from"], r["cov_window_to"], r["cov_window_count"], strict=True):
            ((reported, bypassed),) = self.snap.rows("window", _WINDOW, {"sid": sid, "a": a, "b": b})
            out.append(
                GapWindow(
                    from_=dt(a),
                    to=dt(b),
                    count=n,
                    reported=reported,
                    captured=reported - n,
                    cause=tl.BYPASS_CAUSE if bypassed else None,
                )
            )
        return out

    def friction(self, sid: str, width: int) -> list[FrictionMark]:
        minutes = [m for (m,) in self.snap.rows("friction_minutes", _FRICTION_MINUTES, {"sid": sid})]
        if not minutes:
            return []
        marks: dict[tuple[int, str], list[tuple[int | None, str | None]]] = defaultdict(list)
        for e in self.snap.dicts("friction", _FRICTION, {"sid": sid, "minutes": minutes}):
            b = e["ts_ns"] - e["ts_ns"] % width
            if e["name"] == "tengu_input_prompt":
                if _json_str(e["negative"]) == "true":
                    marks[b, "frustrated"].append((json_int(e["idx"]), None))
            elif e["name"] == "tengu_cancel":
                marks[b, "interrupted"].append((e["prompt_index"], None))
            else:
                cat = _json_str(e["category"])
                if cat and cat != "none":
                    scope = _json_str(e["scope"])
                    detail = " · ".join(p for p in (cat, scope) if p)
                    marks[b, "destructive"].append((e["prompt_index"], detail))
        out = []
        for (b, kind), xs in sorted(marks.items()):
            details = list(dict.fromkeys(d for _, d in xs if d is not None))
            out.append(
                FrictionMark(
                    t=dt(b),
                    kind=kind,
                    count=len(xs),
                    promptIndex=xs[0][0],
                    detail=", ".join(details) if kind == "destructive" else tl.FRICTION_DETAIL[kind],
                    tool="Bash" if kind == "destructive" else None,
                )
            )
        return out

    # --- state ----------------------------------------------------------------------------------

    def state(self, sid: str, lo: int, hi: int, requests: int) -> SessionState:
        s = self.stored(sid) or {}
        has_events = bool(s.get("events"))
        cells = self.cells(sid, lo, hi)
        model_calls = sum(c.row["model_calls"] for c in cells)
        n_events = sum(c.row["events"] for c in cells)
        (counts,) = self.snap.dicts("item_counts", _ITEM_COUNTS, {"sid": sid, "lo": lo, "hi": hi})
        ranged: dict[str, Counter[str]] = defaultdict(Counter)
        for kind, key, n in self.keys(sid, lo, hi, ["head", "pr", "git", "name"]):
            if kind != "name" or key == "tengu_deferred_tools_pool_change":
                ranged[kind][key] += n
        heads = len(ranged["head"])
        head_from = head_to = None
        if heads:
            ((head_from, head_to),) = self.snap.rows("heads", _HEADS, {"sid": sid, "lo": lo, "hi": hi})
        prs = sorted({n for n in map(_pr, ranged["pr"]) if n is not None})
        whole: dict[str, set[str]] = defaultdict(set)
        for kind, key, _ in self.keys(
            sid, None, None, ["hook", "event_builtin", "event_mcp", "call_builtin", "call_mcp"]
        ):
            whole[kind].add(key)
        builtin = len(whole["event_builtin"] | whole["call_builtin"])
        servers: dict[str, set[str]] = defaultdict(set)
        for key in whole["event_mcp"] | whole["call_mcp"]:
            server, tool = json.loads(key)
            servers[server].add(tool)
        mcp = sorted((k, len(v)) for k, v in servers.items())
        recaps = self.snap.dicts("recaps", _RECAPS, {"sid": sid})
        reports_recaps = any(x["has"] for x in recaps)
        (latest,) = self.snap.dicts("latest", _LATEST, {"sid": sid})
        return SessionState(
            capabilitiesAt=CapabilitiesAt(
                builtin=ToolTally(total=None, used=builtin if has_events else None),
                mcpServers=[McpServer(name=k, tools=None, toolsUsed=n, riskyVerbs=[]) for k, n in mcp],
                mcpTools=ToolTally(
                    total=json_int(latest["mcp"]), used=sum(n for _, n in mcp) if has_events or mcp else None
                ),
                skills=json_int(latest["skills"]),
                hooks=len(whole["hook"]) if has_events else None,
                poolChangesInRange=ranged["name"]["tengu_deferred_tools_pool_change"] if has_events else None,
            ),
            repoInRange=RepoInRange(
                branch=None,
                headFrom=head_from,
                headTo=head_to,
                headChanges=heads - 1 if heads else None,
                commits=ranged["git"]["commit"],
                pushed=ranged["git"]["push"] > 0,
                cwdTrail=None,
                prs=prs,
            ),
            recapsInRange=[Recap(t=dt(x["ts_ns"]), text=None) for x in recaps if x["recap"] and lo <= x["ts_ns"] <= hi]
            if reports_recaps
            else None,
            recapsTotal=sum(bool(x["recap"]) for x in recaps) if reports_recaps else None,
            experimentsAt=None,
            flagFetchesInRange=None,
            flagsChangedInRange=None,
            counts=RangeCounts(
                prompts=counts["prompts"],
                modelCalls=model_calls,
                requests=requests,
                events=n_events,
                compactions=counts["compactions"],
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
        last_event = (self.stored(sid) or {}).get("prompt_event_last_ns")
        steps: dict[str, list[StepData]] = defaultdict(list)
        if pids:
            for s in self.snap.dicts("steps", _STEPS, {"sid": sid, "pids": pids}):
                steps[s["prompt_id"]].append(_step(s, live, now, last_event))
        return [_item(i, steps[i["prompt_id"]] if i["type"] == "prompt" else [], live, now) for i in items], total, page

    # --- events ---------------------------------------------------------------------------------

    def events(
        self, sid: str, lo: int, hi: int, internal: bool, q: str | None, page: int, size: int
    ) -> tuple[int, int, list[EventRow]]:
        needle = q.lower() if q else ""
        cells = self.cells(sid, lo, hi)
        total = sum(c.row["events"] for c in cells)
        col = "events" if internal else "events_shown"
        params: dict[str, Any] = {"sid": sid, "hi": hi, "internal": internal, "q": needle, "n": size}
        skip = (page - 1) * size
        if needle:
            (shown,) = self.snap.rows(
                "event_counts", _EVENT_COUNTS, {"sid": sid, "lo": lo, "hi": hi, "internal": internal, "q": needle}
            )[0]
            params.update({"from": lo, "skip": skip})
        else:
            shown = sum(c.row[col] for c in cells)
            found = _walk(
                cells, lambda c: c.row[col], skip, lambda c: self.minutes(sid, c.start, c.end - 1), desc=False
            )
            if found is None:
                return total, shown, []
            params.update({"from": found[0], "skip": found[1]})
        reads = drawer.PgReads(self.snap, lambda *_: None)
        evs = reads._events("event_page", _EVENT_PAGE, params)
        return total, shown, reads.event_rows(sid, evs)

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
        if not codex:  # the analytics posts that carried its events (process/requestrows.go corrHashes)
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
            if captured is not True and cell.row["missed"]:
                got["model", False] += cell.row["missed"]
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
                self._retried(sid, creds, shown[(page - 1) * size : page * size]),
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
        rows = self._retried(sid, creds, rows[inner : inner + size])
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
        missed: list[Row] = []
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
        if captured is not True and (not kinds or "model" in kinds):
            missed = self.snap.dicts("missed_rows", _MISSED_ROWS, {"sid": sid, "a": a, "b": b})
        keys = sorted(
            {k for c in calls if c["kind"] == "model" for k in (c["request_id"], c["client_request_id"]) if k}
            | {k for m in missed for k in (m["request_id"], m["client_request_id"]) if k}
        )
        reports: dict[str, _Report] = {}
        if keys:
            for k, ttft, c5, c1 in self.snap.rows("reports", _REPORTS, {"keys": keys, "sid": sid}):
                reports[k] = _Report(json_double(ttft), json_int(c5), json_int(c1))
        pids = sorted({c["prompt_id"] for c in calls if c["prompt_id"]})
        index: dict[str, int] = {}
        if pids:
            index = dict(self.snap.rows("prompt_indexes", drawer._PROMPT_INDEXES, {"sid": sid, "pids": pids}))
        model_ids = [c["id"].replace("~", "#") for c in calls if c["kind"] == "model" and c["prompt_id"]]
        steps: dict[str, int] = {}
        if model_ids:
            steps = {
                cid.replace("#", "~"): n
                for cid, n in self.snap.rows("step_numbers", _STEP_NUMBERS, {"ids": model_ids, "sid": sid})
            }
        out = []
        for c in calls:
            if c["kind"] == "model" and _failed(c) and not c["unnamed"] and c["scope_session_id"] == sid:
                self._failed[c["id"]] = c
            out.append(call_row(c, index, reports, steps, None))
        out += [missed_row(m, sid, reports) for m in missed]
        return out

    def _retried(self, sid: str, creds: list[str], rows: list[RequestRow]) -> list[RequestRow]:
        """The page's rows, each failed model call's with its retry (read for the page's rows alone)."""
        out = []
        for r in rows:
            c = self._failed.get(r.requestId)
            attempt = self._attempt(sid, c, creds) if c is not None else None
            if attempt is not None and r.error is not None:
                at = dt(attempt.succeeded_at) if attempt.succeeded_at is not None else None
                error = r.error.model_copy(update={"retries": attempt.retries, "retrySucceededAt": at})
                r = r.model_copy(update={"retries": attempt.retries, "error": error})
            out.append(r)
        return out

    def _attempt(self, sid: str, c: Row, creds: list[str]) -> _Attempt | None:
        """A failed call Claude Code retried: its tengu_api_retry (nearest its end, within 30 s after it),
        and the next attempt (process/requestrows.go attempts)."""
        stop = c["end_ns"] if c["end_ns"] is not None else c["ts"]
        best: tuple[int, int, Any] | None = None
        for r in self.snap.dicts("retries", _RETRIES, {"sid": sid, "a": c["ts"], "b": stop + RETRY_AFTER_NS}):
            prompt = _json_str(r["prompt"])
            if c["prompt_id"] and prompt is not None and prompt != c["prompt_id"]:
                continue
            dist = abs(r["ts_ns"] - stop)
            if best is None or dist < best[0]:
                best = (dist, r["ts_ns"], r["attempt"])
        if best is None:
            return None
        found = self.snap.dicts(
            "next_attempt",
            _NEXT_ATTEMPT,
            {
                "sid": sid,
                "creds": creds,
                "prompt": c["prompt_id"],
                "cls": c["request_class"],
                "ts": c["ts"],
                "id": c["id"],
            },
        )
        succeeded = None
        if found and found[0]["status"] and found[0]["status"] < 400:
            succeeded = found[0]["ts"]
        retries = json_int(best[2])
        if retries is not None and not -(1 << 31) <= retries < 1 << 31:
            retries = None
        return _Attempt(retries, succeeded)

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
        out.append(Cell(m, m + MINUTE, {"requests": {}, "requests_hidden": {}, "missed": 0}))
    out.sort(key=lambda c: c.start)
    return out


def _pr(key: str) -> int | None:
    """A PR number as event_rows.pr read it (pyIntOf: int(float(v)))."""
    try:
        v = float(key)
    except ValueError:
        return None
    return int(v) if math.isfinite(v) else None


def _text(p: Row) -> PromptText:
    return PromptText(
        retained=p["text"] is not None,
        text=p["text"],
        chars=p["text_chars"],
        pastedBytes=p["pasted_bytes"],
        images=p["images"],
    )


def _in_flight(p: Row, live: bool, now: int) -> bool:
    unfinished = p["unfinished_max_ns"]
    return live and (p["open_tail"] or (unfinished is not None and now - unfinished <= ss.STREAMING_WITHIN_NS))


def _duration(p: Row, live: bool, now: int) -> float | None:
    """The prompt's duration (session.py _DURATION): its turn's, unless still in flight; else its report's."""
    turn = _round1((p["turn_end_ns"] - p["t_ns"]) / 1e6) if p["turn_end_ns"] is not None else None
    if turn is not None and not _in_flight(p, live, now):
        return turn
    report = p["turn_report"]
    return report.get("durationMs") if isinstance(report, dict) else None


def _item(i: Row, steps: list[StepData], live: bool, now: int) -> PromptItem | BetweenItem:
    if i["type"] == "between":
        return BetweenItem(
            type="between",
            between=BetweenData(
                kind=i["between_kind"],
                t=dt(i["t_ns"]),
                detail=i["detail"],
                requestId=i["call_id"].replace("#", "~") if i["call_id"] else None,
                tokensFrom=i["tokens_from"],
                tokensTo=i["tokens_to"],
                waitMs=_round1((i["t_ns"] - i["attempt_ns"]) / 1e6) if i["attempt_ns"] is not None else None,
            ),
        )
    counts = sorted((i["tool_counts"] or {}).items(), key=lambda x: (-x[1], x[0]))
    report = i["turn_report"]
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
        friction=PromptFriction(frustrated=i["frustrated"], interrupted=i["interrupted"], destructive=i["destructive"]),
        inFlight=_in_flight(i, live, now),
        turnReport=TurnReport.model_validate(report) if report else None,
        hidden=i["hidden"] or {},
    )
    return PromptItem(type="prompt", prompt=prompt, steps=steps)


def _step(s: Row, live: bool, now: int, last_event: int | None) -> StepData:
    """A steps row as the API's StepData, with what depends on now (session.py _step)."""
    end = s["end_ns"]
    if end is None:
        status = "streaming" if live and now - s["t_ns"] <= ss.STREAMING_WITHIN_NS else "error"
    else:
        status = "error" if s["failed"] else "done"
    pending = not s["reported"] and (last_event is None or last_event <= (end if end is not None else s["t_ns"]))
    content_live = live and s["content_live"]
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
            cacheWrite5m=s["tokens_cache_write_5m"],
            cacheWrite1h=s["tokens_cache_write_1h"],
            output=s["tokens_output"],
            accuracy="exact" if s["tokens_output"] is not None else "missing",
        ),
        durationMs=_round1((end - s["t_ns"]) / 1e6) if end is not None else None,
        ttftMs=s["ttft_ms"],
        stopReason=s["stop_reason"],
        costUsd=s["cost_usd"],
        status=status,
        eventsPending=live and pending,
        agentType=s["agent_type"],
        tools=s["tools_live"] if live and s["tools_live"] is not None else s["tools"],
        does=s["does"],
        commands=list(s["commands_live"] if content_live else s["commands"]),
        patches=s["patches"],
        text=s["text"],
        exitCodes=list(s["exit_codes_live"] if content_live else s["exit_codes"]),
        usageMissing=s["usage_missing"],
    )
