"""The request drawer over v9 (docs/PIPELINE.md §6.2): today's request_detail.py, its inputs read from
Postgres and S3 when the drawer opens instead of from a loaded session.

- The call: its `calls` row, by its primary key.
- The raw data: one ranged GET of the call's gzip member in its raw file (raw_key, raw_offset, raw_len),
  decoded as today (rows_of over its lines). A WebSocket turn has no member of its own (its frames
  are in its connection's), and a call that hasn't ended has none yet: their drawer is built from the
  row, as today's is.
- Step N of M: the call's `steps` row and its prompt's `prompts` row.
- Previous and next: by message_id / parent_message_id, else the prompt's neighbouring step of the same
  request class.
- "Delivered in step N": the next call's step within its prompt (§8, difference 7).
- Analytics around the call: `events` by request id, client request id and tool use id; its stall
  summary, and its prompt's query and retries, by name and time.
- A `reported:` id: its report, by request id, client request id or time.
- /events/{id}: the `events` row, and its raw row from its batch's member.

Each is an indexed lookup that reads a few rows, whatever the session's size: one call, one ranged GET,
a few statements. Nothing is cached between requests.
"""

import gzip
import hashlib
import json
import re
from collections.abc import Callable
from typing import Any

from aintercept_api import claude_code as cc
from aintercept_api import person as pp
from aintercept_api import request_detail as rd
from aintercept_api import rows
from aintercept_api import session as ss
from aintercept_api.models.requests import EventDetail
from aintercept_api.models.sessions import EventRow
from aintercept_api.reader import Snapshot

# A repeat of an identical event row is keyed <hash>-2, -3 (the observer's eventCopies).
_COPY = re.compile(r"^(h[0-9a-f]{16})-\d+$")


def lines(gz: bytes | None) -> list[bytes]:
    """A raw member's non-empty lines (none when it's missing or truncated)."""
    if gz is None:
        return []
    try:
        return [ln.strip() for ln in gzip.decompress(gz).split(b"\n") if ln.strip()]
    except (OSError, EOFError):
        return []


def rows_of(lines: list[bytes], correlation_id: str) -> list[dict[str, Any]]:
    """The raw rows among lines that are the transaction's."""
    out = []
    for line in lines:
        if correlation_id.encode() not in line:
            continue
        try:
            r = json.loads(line)
        except ValueError:
            continue
        if r.get("correlation_id") == correlation_id:
            out.append(r)
    return out


def event_row(lines: list[bytes], correlation_id: str, event_key: str) -> dict[str, Any] | None:
    """The client_event row among lines that event_key names."""
    want = event_key.split("/", 1)[1] if "/" in event_key else ""
    # A repeat of an identical row is keyed <hash>-2, -3 (the processor's eventCopies):
    # any of the identical rows is it.
    want = _COPY.sub(r"\1", want)
    for line in lines:
        try:
            r = json.loads(line)
        except ValueError:
            continue
        if r.get("correlation_id") != correlation_id or r.get("phase") != "client_event":
            continue
        ce = r.get("client_event") or {}
        ident = ce.get("id") or ("h" + hashlib.sha256(line).hexdigest()[:16])
        if ident == want and isinstance(r, dict):
            return r
    return None


# A ranged GET of (key, offset, length); None when the object can't be read.
ReadRange = Callable[[str, int, int], bytes | None]

# The API's cc_calls over calls c: a coding agent's model call with a session and no hidden kind, as the
# rollups have it (aintercept/internal/refresh/sql.go `visible`: a Codex call that never really ended
# names no session today).
VISIBLE = """(c.hidden_kind IS NULL AND NOT c.synthetic AND c.session_id IS NOT NULL
  AND NOT (c.source = 'codex' AND (c.end_ns IS NULL OR c.swept)) AND (
    (coalesce(c.source, 'claude_code') = 'claude_code' AND c.host = 'api.anthropic.com'
     AND starts_with(c.path, '/v1/messages') AND NOT starts_with(c.path, '/v1/messages/count_tokens'))
    OR (c.source = 'codex' AND c.host = 'chatgpt.com' AND c.path = '/backend-api/codex/responses'
     AND (c.ws_turn IS NOT NULL OR c.method = 'POST'))))"""
# A call's time (its start, else its end), and its end unless the sweep closed it (today's expired row has
# none).
_AT = "coalesce(c.start_ns, c.end_ns)"
_END = "CASE WHEN c.swept THEN NULL ELSE c.end_ns END"

# The call, as request_detail.wire_from_row reads a request row, with its raw member and what its place
# reads. An analytics batch's session is its earliest event's (the assembler's batch session, which
# scope_session_id holds when the call names none: a resent batch's events are its first copy's).
_CALL = f"""SELECT c.call_id, c.correlation_id, c.session_id, c.prompt_id, c.request_class, c.message_id,
    c.parent_message_id, c.raw_key, c.raw_offset, c.raw_len, {VISIBLE} AS visible,
    {_AT} AS ts, c.method, c.path, c.ip, c.key_hash, c.status, c.response_start_ns, {_END} AS end_ts,
    c.client::text AS client_json, c.provider::text AS provider_json, c.model, c.stop_reason, c.input_tokens,
    c.output_tokens, c.cache_read_tokens, c.cache_write_tokens, c.request_bytes, c.response_bytes, c.error,
    c.device_id, c.ext::text AS ext_json,
    coalesce((SELECT e.session_id FROM events e WHERE e.batch_call_id = c.correlation_id AND e.session_id <> ''
              ORDER BY e.ts_ns, e.session_id COLLATE "C" LIMIT 1),
             CASE WHEN c.session_id IS NULL THEN c.scope_session_id END) AS batch_session
FROM calls c WHERE c.call_id = %(id)s"""  # noqa: S608 (fixed text)

# An event as session.Event has it (its id <batch>:<rank among the batch's Claude Code events>), with
# the Events tab's summary, internal and marker, stored at insert (process/eventrows.go).
_EVENT = """e.batch_call_id || ':' || e.batch_cc_n AS id, e.ts_ns, coalesce(e.name, '(unnamed)') AS name,
    e.device_id, e.head_sha, e.metadata::text AS md, e.prompt_id, e.summary, e.internal, e.marker"""
# Today's order of a session's events: time, batch, place in the batch.
_ORDER = """ORDER BY ts_ns, split_part(id, ':', 1) COLLATE "C", split_part(id, ':', 2)::bigint"""

# The events about one call (request_detail.facts): those naming its ids, its stall summary (its
# request id is only in the metadata), and its prompt's query (60 s before it to 2 s after) and retries
# (from its start to 30 s after its end), each through an index.
_CALL_EVENTS = f"""SELECT * FROM (
  SELECT {_EVENT} FROM events e
  WHERE e.request_id = ANY(%(ids)s) AND e.session_id = %(sid)s AND e.source = 'claude_code'
  UNION
  SELECT {_EVENT} FROM events e
  WHERE e.client_request_id = ANY(%(ids)s) AND e.session_id = %(sid)s AND e.source = 'claude_code'
  UNION
  SELECT {_EVENT} FROM events e
  WHERE e.session_id = %(sid)s AND e.name = 'tengu_streaming_stall_summary' AND e.source = 'claude_code'
    AND e.metadata ->> 'request_id' = %(rid)s
  UNION
  SELECT {_EVENT} FROM events e
  WHERE e.session_id = %(sid)s AND e.name = 'tengu_api_query' AND e.ts_ns BETWEEN %(q_lo)s AND %(q_hi)s
    AND e.prompt_id = %(prompt)s AND e.source = 'claude_code'
  UNION
  SELECT {_EVENT} FROM events e
  WHERE %(failed)s AND e.session_id = %(sid)s AND e.name = 'tengu_api_retry'
    AND e.ts_ns BETWEEN %(r_lo)s AND %(r_hi)s AND e.prompt_id = %(prompt)s AND e.source = 'claude_code'
) x {_ORDER}"""  # noqa: S608 (fixed text)
_TOOL_EVENTS = f"""SELECT {_EVENT} FROM events e
WHERE e.tool_use_id = ANY(%(tools)s) AND e.session_id = %(sid)s AND e.source = 'claude_code'
ORDER BY e.ts_ns, e.batch_call_id COLLATE "C", e.batch_cc_n"""  # noqa: S608

# The call's place (request_detail.position): its step and its prompt's count and number; the previous
# and next calls by the message-id chain (the session's visible calls, a WebSocket turn not among them:
# today's chain reads them by correlation id; of several, the greatest id), else its prompt's
# neighbouring steps of its request class; and the next call's message id and step.
_POSITION = f"""WITH me AS (
  SELECT step FROM steps WHERE call_id = %(call)s AND prompt_id = %(prompt)s AND session_id = %(sid)s),
pr AS (SELECT steps, prompt_index FROM prompts WHERE prompt_id = %(prompt)s AND session_id = %(sid)s),
lp AS (SELECT s.call_id FROM steps s, me WHERE s.prompt_id = %(prompt)s AND s.session_id = %(sid)s
         AND s.step < me.step AND s.request_class IS NOT DISTINCT FROM %(cls)s ORDER BY s.step DESC LIMIT 1),
ln AS (SELECT s.call_id FROM steps s, me WHERE s.prompt_id = %(prompt)s AND s.session_id = %(sid)s
         AND s.step > me.step AND s.request_class IS NOT DISTINCT FROM %(cls)s ORDER BY s.step LIMIT 1),
bm AS (SELECT c.call_id FROM calls c
       WHERE c.message_id = %(prev_mid)s AND c.session_id = %(sid)s AND strpos(c.call_id, '#') = 0 AND {VISIBLE}
       ORDER BY c.call_id COLLATE "C" DESC LIMIT 1),
bp AS (SELECT c.call_id FROM calls c
       WHERE c.parent_message_id = %(mid)s AND c.session_id = %(sid)s AND strpos(c.call_id, '#') = 0 AND {VISIBLE}
       ORDER BY c.call_id COLLATE "C" DESC LIMIT 1),
nx AS (SELECT coalesce((SELECT call_id FROM bp), (SELECT call_id FROM ln)) AS call_id)
SELECT (SELECT step FROM me) AS step, (SELECT steps FROM pr) AS steps, (SELECT prompt_index FROM pr) AS prompt_index,
  (SELECT call_id FROM bm) AS by_mid, (SELECT call_id FROM lp) AS lane_prev, nx.call_id AS next,
  (SELECT c.message_id FROM calls c WHERE c.call_id = nx.call_id AND strpos(c.call_id, '#') = 0) AS next_message,
  (SELECT s.step FROM steps s WHERE s.call_id = nx.call_id AND s.session_id = %(sid)s LIMIT 1) AS next_step
FROM nx"""  # noqa: S608 (fixed text)

# A failed call's retry: the first call after it of its prompt and request class.
_NEXT_ATTEMPT = f"""SELECT replace(c.call_id, '#', '~') AS id, {_AT} AS ts, c.status, {_END} AS end_ts
FROM steps s JOIN calls c ON c.call_id = s.call_id
WHERE s.prompt_id = %(prompt)s AND s.session_id = %(sid)s AND s.request_class IS NOT DISTINCT FROM %(cls)s
  AND s.t_ns > %(ts)s
ORDER BY s.step LIMIT 1"""  # noqa: S608
_NEXT_ATTEMPT_NO_PROMPT = f"""SELECT replace(c.call_id, '#', '~') AS id, {_AT} AS ts, c.status, {_END} AS end_ts
FROM calls c
WHERE c.session_id = %(sid)s AND (c.prompt_id IS NULL OR c.hidden_kind IS NOT NULL) AND c.prompt_id IS NULL
  AND {VISIBLE} AND c.request_class IS NOT DISTINCT FROM %(cls)s AND {_AT} > %(ts)s
ORDER BY {_AT}, replace(c.call_id, '#', '~') COLLATE "C" LIMIT 1"""  # noqa: S608

# The listed events' links (session._link: an event's request is the newest visible call of the
# session with its request id, §5.4) and prompt numbers (Claude Code's from its input, a Codex turn's
# place).
_LINKED = f"""SELECT DISTINCT ON (c.request_id) c.request_id, replace(c.call_id, '#', '~') AS id, c.request_class,
    c.model, c.stop_reason
FROM calls c WHERE c.request_id = ANY(%(rids)s) AND c.session_id = %(sid)s AND {VISIBLE}
ORDER BY c.request_id, {_AT} DESC, replace(c.call_id, '#', '~') COLLATE "C" DESC"""  # noqa: S608
_PROMPT_INDEXES = """SELECT prompt_id, prompt_index FROM prompt_items
WHERE session_id = %(sid)s AND prompt_id = ANY(%(pids)s) AND prompt_index IS NOT NULL"""

# The newest Bash event's sandbox setting up to a time (the first such event, newest first, through
# the (session, name, time) index).
_SANDBOX = """SELECT e.metadata -> 'sandbox_enabled' AS v FROM events e
WHERE e.session_id = %(sid)s AND e.name = 'tengu_bash_tool_command_executed' AND e.ts_ns <= %(until)s
  AND e.source = 'claude_code'
  AND (jsonb_typeof(e.metadata -> 'sandbox_enabled') IN ('number', 'boolean')
       OR jsonb_typeof(e.metadata -> 'sandbox_enabled') = 'string' AND e.metadata ->> 'sandbox_enabled' <> '')
ORDER BY e.ts_ns DESC, e.batch_call_id COLLATE "C" DESC, e.batch_cc_n DESC LIMIT 1"""

# A report by its key: its request id, client request id or time; the first, in today's order.
_REPORT = f"""SELECT e.session_id, {_EVENT} FROM events e
WHERE e.source = 'claude_code' AND e.name = 'tengu_api_success' AND e.session_id IS NOT NULL
  AND (e.request_id = %(key)s OR e.client_request_id = %(key)s OR e.ts_ns = %(ts)s)
ORDER BY e.ts_ns, e.batch_call_id COLLATE "C", e.batch_cc_n LIMIT 1"""  # noqa: S608
_PROMPT_INDEX = "SELECT prompt_index FROM prompts WHERE prompt_id = %(prompt)s AND session_id = %(sid)s"

# /events/{id}: the event by its batch and rank, and its batch's raw member.
_ONE_EVENT = """SELECT e.ts_ns, coalesce(e.name, '(unnamed)') AS name, e.session_id, e.event_key,
    c.raw_key, c.raw_offset, c.raw_len
FROM events e LEFT JOIN calls c ON c.call_id = e.batch_call_id
WHERE e.batch_call_id = %(batch)s AND e.batch_cc_n = %(n)s AND e.source = 'claude_code'"""


def _event(r: dict[str, Any]) -> ss.Event:
    return ss.Event(r["id"], r["ts_ns"], r["name"], r["device_id"], r["head_sha"], r["md"], r["prompt_id"])


def _step(
    cid: str, ts: int = 0, *, end_ts: int | None = None, status: int | None = None, request_class: str | None = None,
    model: str | None = None, stop_reason: str | None = None,
) -> ss.Step:  # fmt: skip
    """A session.Step with what the drawer reads of it (its id, time, status, end; its link's label)."""
    return ss.Step(
        correlation_id=cid, ts=ts, end_ts=end_ts, status=status, prompt_id=None, request_class=request_class,
        agent_type=None, request_id=None, client_request_id=None, model=model, input=None, output=None,
        cache_read=None, cache_write=None, stop_reason=stop_reason, ip=None, tool_uses_json=None, ws_turn=None,
        response_bytes=None, detail_json=None,
    )  # fmt: skip


class PgReads:
    """The drawer's reads (request_detail.Reads) of one request, in its snapshot."""

    def __init__(self, snap: Snapshot, read_range: ReadRange) -> None:
        self.snap, self.read_range = snap, read_range
        self._calls: dict[str, dict[str, Any] | None] = {}
        self._facts: dict[str, dict[str, Any]] = {}  # event id -> its stored summary, internal and marker

    def _call(self, cid: str) -> dict[str, Any] | None:
        if cid not in self._calls:
            found = self.snap.dicts("call", _CALL, {"id": cid.replace(rd.WS_TURN, "#")})
            self._calls[cid] = found[0] if found else None
        return self._calls[cid]

    def _events(self, name: str, sql: str, params: dict[str, Any]) -> list[ss.Event]:
        out = []
        for r in self.snap.dicts(name, sql, params):
            self._facts[r["id"]] = r
            out.append(_event(r))
        return out

    def member(self, row: dict[str, Any]) -> list[bytes]:
        """The lines of a call's gzip member in its raw file: one ranged GET (none when it has no member)."""
        if row["raw_key"] is None:
            return []
        return lines(self.read_range(row["raw_key"], row["raw_offset"], row["raw_len"]))

    def wire(self, cid: str) -> rd.Wire | None:
        row = self._call(cid)
        if row is None:
            return None
        w = None
        if rd.WS_TURN not in cid:  # a turn's frames are its connection's: its row says what the drawer shows
            w = rd.wire_from_rows(cid, rows_of(self.member(row), row["correlation_id"]))
        if w is None:  # no member yet (it hasn't ended), or it couldn't be read
            w = rd.wire_from_row(cid, row)
        if w.kind == "analytics":
            w.batch_session = row["batch_session"]
        return w

    def call_events(self, sid: str, w: rd.Wire | None, ids: list[str]) -> list[ss.Event]:
        ts = w.ts if w else 0
        stop = (w.end_ts or w.ts) if w else 0
        params = {
            "sid": sid,
            "ids": ids,
            "rid": w.request_id if w else None,
            "prompt": w.prompt_id if w else None,
            "q_lo": ts - 60 * cc.NS,
            "q_hi": ts + 2 * cc.NS,
            "failed": bool(w and w.failed),
            "r_lo": ts,
            "r_hi": stop + pp.RETRY_AFTER_NS,
        }
        return self._events("call_events", _CALL_EVENTS, params)

    def tool_events(self, sid: str, tool_ids: list[str]) -> list[ss.Event]:
        return self._events("tool_events", _TOOL_EVENTS, {"sid": sid, "tools": tool_ids})

    def position(self, sid: str | None, w: rd.Wire) -> rd.Position:
        if not sid or w.kind != "model":
            return rd.Position()
        row = self._call(w.cid) or {}
        # Its own message ids, when the chain holds it: one of the session's visible calls, not a turn.
        chained = bool(row.get("visible")) and row.get("session_id") == sid and rd.WS_TURN not in w.cid
        (r,) = self.snap.dicts(
            "position",
            _POSITION,
            {
                "sid": sid,
                "prompt": w.prompt_id,
                "cls": w.request_class,
                "call": w.cid.replace(rd.WS_TURN, "#"),
                "mid": row.get("message_id") if chained else None,
                "prev_mid": row.get("parent_message_id") if chained else None,
            },
        )

        def turn(cid: str | None) -> str | None:
            return cid.replace("#", rd.WS_TURN) if cid else None

        prev_mid = row.get("parent_message_id") if chained else None
        return rd.Position(
            prompt_index=r["prompt_index"] if w.prompt_id else None,
            step_index=r["step"] if w.prompt_id else None,
            step_count=(r["steps"] or None) if w.prompt_id else None,
            prev=turn(r["by_mid"]) or turn(r["lane_prev"]),
            next=turn(r["next"]),
            prev_message=prev_mid,
            next_message=r["next_message"],
            delivered=r["next_step"],
        )

    def next_attempt(self, sid: str, w: rd.Wire) -> ss.Step | None:
        params = {"sid": sid, "prompt": w.prompt_id, "cls": w.request_class, "ts": w.ts}
        found = self.snap.dicts("next_attempt", _NEXT_ATTEMPT if w.prompt_id else _NEXT_ATTEMPT_NO_PROMPT, params)
        if not found:
            return None
        r = found[0]
        return _step(r["id"], r["ts"], end_ts=r["end_ts"], status=r["status"])

    def event_rows(self, sid: str, evs: list[ss.Event]) -> list[EventRow]:
        rids = sorted({rid for e in evs if (rid := ss._str(e.md.get("requestId")))})
        pids = sorted({e.prompt_id for e in evs if e.prompt_id})
        steps: dict[str, ss.Step] = {}
        if rids:
            for r in self.snap.dicts("linked", _LINKED, {"sid": sid, "rids": rids}):
                steps[r["request_id"]] = _step(
                    r["id"], request_class=r["request_class"], model=r["model"], stop_reason=r["stop_reason"]
                )
        index: dict[str, int] = {}
        if pids:
            index = {p: n for p, n in self.snap.rows("prompt_indexes", _PROMPT_INDEXES, {"sid": sid, "pids": pids})}
        out = []
        for e in evs:
            f = self._facts.get(e.id) or {}
            out.append(
                EventRow(
                    eventId=e.id,
                    t=rows.dt(e.ts),
                    name=e.name,
                    promptIndex=index.get(e.prompt_id) if e.prompt_id else None,
                    linkedTo=ss._link(e, sid, steps, index),
                    summary=f.get("summary", ""),
                    marker=f.get("marker") or None,
                    internal=bool(f.get("internal")),
                )
            )
        return out

    def sandbox(self, sid: str, until_ns: int) -> str | None:
        found = self.snap.rows("sandbox", _SANDBOX, {"sid": sid, "until": until_ns})
        return ss._str(found[0][0]) if found else None

    def report(self, key: str) -> tuple[str, ss.Event] | None:
        ts = int(key) if key.isdigit() and int(key) < 1 << 63 else None  # a time: a bigint
        found = self.snap.dicts("report", _REPORT, {"key": key, "ts": ts})
        if not found:
            return None
        self._facts[found[0]["id"]] = found[0]
        return found[0]["session_id"], _event(found[0])

    def prompt_index(self, sid: str, prompt_id: str | None) -> int | None:
        if not prompt_id:
            return None
        found = self.snap.rows("prompt_index", _PROMPT_INDEX, {"sid": sid, "prompt": prompt_id})
        return found[0][0] if found else None

    def event(self, eid: str) -> EventDetail | None:
        """`<batch>:<n>`: the batch's n-th Claude Code event, and its raw row from the batch's member."""
        batch, _, n = eid.rpartition(":")
        if not batch or not n.isdigit() or len(n) > 9:
            return None
        found = self.snap.dicts("event", _ONE_EVENT, {"batch": batch, "n": int(n)})
        if not found:
            return None
        r = found[0]
        body = event_row(self.member(r), batch, r["event_key"]) or {}
        ev = body.get("client_event")
        return EventDetail(
            eventId=eid,
            t=rows.dt(r["ts_ns"]),
            name=r["name"],
            sessionId=r["session_id"],
            raw=rd._scrub(ev if isinstance(ev, dict) else {}),
        )
