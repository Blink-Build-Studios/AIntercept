"""The request drawer over v9 (docs/PIPELINE.md §6.2): today's request_detail.py, its inputs read from
Postgres and S3 when the drawer opens instead of from a loaded session.

- The call: its `calls` row, by its primary key.
- The raw data: one ranged GET of the call's gzip member in its raw file (raw_key, raw_offset, raw_len),
  decoded as today (rows_of over its lines). A WebSocket turn has no member of its own (its frames
  are in its connection's), and a call that hasn't ended has none yet: their drawer is built from the
  row, as today's is.
- Step N of M: the call's `steps` row (its number counted at read, by (t_ns, ord)), its prompt's `prompts`
  row and its number (`prompt_items`).
- Previous and next: by message_id / parent_message_id, else the prompt's neighbouring step of the same
  request class.
- "Delivered in step N": the next call's step within its prompt (§8, difference 7).

Analytics events aren't in the database: there are no events about a call, no reported-only calls
(`reported:` ids) and no /events/{id}.

Each is an indexed lookup that reads a few rows, whatever the session's size: one call, one ranged GET,
a few statements. Nothing is cached between requests.
"""

import gzip
import json
from collections.abc import Callable
from typing import Any

from aintercept_api import request_detail as rd
from aintercept_api.reader import Snapshot


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

# A step {s}'s number in its prompt: 1 + its prompt's steps before it by (t_ns, ord), an indexed count on
# steps (prompt_id, session_id, t_ns, ord) (docs/PIPELINE.md §5.2: an ordinal is never stored).
STEP_NUMBER = """1 + (SELECT count(*) FROM steps o WHERE o.prompt_id = {s}.prompt_id AND o.session_id = {s}.session_id
  AND (o.t_ns, o.ord) < ({s}.t_ns, {s}.ord))"""
# Each listed call's step number in its prompt (%(ids)s: call ids).
STEP_NUMBERS = f"""SELECT s.call_id, {STEP_NUMBER.format(s="s")} AS step FROM steps s
WHERE s.call_id = ANY(%(ids)s)"""  # noqa: S608 (fixed text)
# A prompt's number: its place in its session (prompt_items.prompt_index), of session {s} and prompt {p}.
PROMPT_INDEX = """(SELECT i.prompt_index FROM prompt_items i WHERE i.session_id = {s} AND i.prompt_id = {p}
  AND i.prompt_index IS NOT NULL LIMIT 1)"""
# The listed prompts' numbers in a session.
PROMPT_INDEXES = """SELECT prompt_id, prompt_index FROM prompt_items
WHERE session_id = %(sid)s AND prompt_id = ANY(%(pids)s) AND prompt_index IS NOT NULL"""

# The call, as request_detail.wire_from_row reads a request row, with its raw member and what its place
# reads. A call that names no session (an analytics post) is its scope session's, when it has one.
_CALL = f"""SELECT c.call_id, c.correlation_id, c.session_id, c.prompt_id, c.request_class, c.message_id,
    c.parent_message_id, c.raw_key, c.raw_offset, c.raw_len, {VISIBLE} AS visible,
    {_AT} AS ts, c.method, c.path, c.ip, c.key_hash, c.status, c.response_start_ns, {_END} AS end_ts,
    c.client::text AS client_json, c.provider::text AS provider_json, c.model, c.stop_reason, c.input_tokens,
    c.output_tokens, c.cache_read_tokens, c.cache_write_tokens, c.request_bytes, c.response_bytes, c.error,
    c.device_id, c.ext::text AS ext_json, CASE WHEN c.session_id IS NULL THEN c.scope_session_id END AS batch_session
FROM calls c WHERE c.call_id = %(id)s"""  # noqa: S608 (fixed text)

# The call's place (request_detail.position): its step and its prompt's count and number; the previous
# and next calls by the message-id chain (the session's visible calls, a WebSocket turn not among them:
# today's chain reads them by correlation id; of several, the greatest id), else its prompt's
# neighbouring steps of its request class (by (t_ns, ord)); and the next call's message id and step.
_POSITION = f"""WITH me AS (
  SELECT s.*, {STEP_NUMBER.format(s="s")} AS step FROM steps s
  WHERE s.call_id = %(call)s AND s.prompt_id = %(prompt)s AND s.session_id = %(sid)s),
pr AS (SELECT steps FROM prompts WHERE prompt_id = %(prompt)s AND session_id = %(sid)s),
lp AS (SELECT s.call_id FROM steps s, me WHERE s.prompt_id = %(prompt)s AND s.session_id = %(sid)s
         AND (s.t_ns, s.ord) < (me.t_ns, me.ord) AND s.request_class IS NOT DISTINCT FROM %(cls)s
       ORDER BY s.t_ns DESC, s.ord DESC LIMIT 1),
ln AS (SELECT s.call_id FROM steps s, me WHERE s.prompt_id = %(prompt)s AND s.session_id = %(sid)s
         AND (s.t_ns, s.ord) > (me.t_ns, me.ord) AND s.request_class IS NOT DISTINCT FROM %(cls)s
       ORDER BY s.t_ns, s.ord LIMIT 1),
bm AS (SELECT c.call_id FROM calls c
       WHERE c.message_id = %(prev_mid)s AND c.session_id = %(sid)s AND strpos(c.call_id, '#') = 0 AND {VISIBLE}
       ORDER BY c.call_id COLLATE "C" DESC LIMIT 1),
bp AS (SELECT c.call_id FROM calls c
       WHERE c.parent_message_id = %(mid)s AND c.session_id = %(sid)s AND strpos(c.call_id, '#') = 0 AND {VISIBLE}
       ORDER BY c.call_id COLLATE "C" DESC LIMIT 1),
nx AS (SELECT coalesce((SELECT call_id FROM bp), (SELECT call_id FROM ln)) AS call_id)
SELECT (SELECT step FROM me) AS step, (SELECT steps FROM pr) AS steps,
  {PROMPT_INDEX.format(s="%(sid)s", p="%(prompt)s")} AS prompt_index,
  (SELECT call_id FROM bm) AS by_mid, (SELECT call_id FROM lp) AS lane_prev, nx.call_id AS next,
  (SELECT c.message_id FROM calls c WHERE c.call_id = nx.call_id AND strpos(c.call_id, '#') = 0) AS next_message,
  (SELECT {STEP_NUMBER.format(s="s")} FROM steps s WHERE s.call_id = nx.call_id AND s.session_id = %(sid)s) AS next_step
FROM nx"""  # noqa: S608 (fixed text)


class PgReads:
    """The drawer's reads (request_detail.Reads) of one request, in its snapshot."""

    def __init__(self, snap: Snapshot, read_range: ReadRange) -> None:
        self.snap, self.read_range = snap, read_range
        self._calls: dict[str, dict[str, Any] | None] = {}

    def _call(self, cid: str) -> dict[str, Any] | None:
        if cid not in self._calls:
            found = self.snap.dicts("call", _CALL, {"id": cid.replace(rd.WS_TURN, "#")})
            self._calls[cid] = found[0] if found else None
        return self._calls[cid]

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
