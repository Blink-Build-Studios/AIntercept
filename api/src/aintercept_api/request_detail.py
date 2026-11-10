"""The request drawer (handoff §5.2.10): one transaction's summary, raw wire data
and every known fact, plus a reported-only (not captured) call built from
Claude Code's analytics alone.

Sources, per transaction (correlation_id):
- request_headers / response_headers rows: headers (the observer already
  replaces credential values with a marker; they are redacted again here),
  status, client IP, key_hash;
- the complete row: model, usage, stop reason, request settings (effort,
  thinking, max_tokens, stream), the message-id chain, service tier, inference
  geo, the tool catalog and the tools the response used;
- body chunks: byte totals and timestamps (proxy timing); the bytes themselves
  only when the observer ran with full-body capture (`contents`);
- Claude Code's analytics for the call: tengu_api_success by requestId (else
  clientRequestId), the tools it ran, tengu_api_retry and stall summaries.

Envoy's own connection timings (accept, upstream connect, TLS) aren't recorded,
so proxy timing starts at the request headers.
"""

import base64
import gzip
import json
import re
import threading
import zlib
from dataclasses import dataclass, field
from datetime import UTC, datetime
from typing import Any

from aintercept_api import agents, rows
from aintercept_api import claude_code as cc
from aintercept_api import person as pp
from aintercept_api import session as ss
from aintercept_api.models.people import Cost, TokenCount
from aintercept_api.models.requests import (
    ContentRow,
    CrossCheck,
    DetailRow,
    DetailSection,
    EventDetail,
    FieldEntry,
    HeaderEntry,
    KeyStrip,
    Notice,
    RequestDetail,
    RequestDetails,
    RequestRaw,
    RetryInfo,
    StreamEvent,
    Timing,
    TimingPhase,
    ToolCallDetail,
)
from aintercept_api.models.sessions import EventRow
from aintercept_api.store import Store

REPORTED = "reported:"
MARKER = "<redacted>"  # the observer's marker for a credential header (aintercept/internal/events)
# Header names that carry credentials, redacted whatever the observer did.
SECRET_HEADER = re.compile(r"authorization|cookie|api[-_]?key|token|secret|password|session[-_]?key", re.I)
# Credentials inside retained bodies (OAuth tokens in MCP results, API keys, JWTs, provider tokens).
SECRET_TEXT = re.compile(
    r"sk-ant-[A-Za-z0-9_\-]{8,}"
    r"|\bBearer\s+[A-Za-z0-9._~+/\-]{12,}=*"
    r"|\beyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}"
    r"|\bgh[pousr]_[A-Za-z0-9]{20,}"
    r"|\bxox[abpr]-[A-Za-z0-9\-]{10,}"
    r"|\bya29\.[A-Za-z0-9_\-]{20,}"
    r'|"(?:access_token|refresh_token|id_token|client_secret)"\s*:\s*"[^"]+"'
)
BODY_PREVIEW_CHARS = 4000  # a retained content row's body, at most
# Fields the observer can write on a complete row; absent ones are listed as "not on rows yet".
COMPLETE_FIELDS = (
    "model",
    "input_tokens",
    "output_tokens",
    "cache_read_tokens",
    "cache_write_tokens",
    "stop_reason",
    "message_id",
    "previous_message_id",
    "thread_type",
    "stream",
    "max_tokens",
    "effort",
    "thinking_type",
    "service_tier",
    "inference_geo",
    "speed",
    "tools_available",
    "tool_uses",
    "account_uuid",
    "device_id",
    "session_id",
    "provider_org_id",
)
CONTENT_KEYS = {"body_b64", "body_preview", "headers"}
REQUEST_EVENTS = ("tengu_api_success", "tengu_api_query", "tengu_api_retry", "tengu_streaming_stall_summary")


def redact(text: str) -> str:
    return SECRET_TEXT.sub("[redacted]", text)


def _scrub(v: Any) -> Any:
    if isinstance(v, str):
        return redact(v)
    if isinstance(v, dict):
        return {k: _scrub(x) for k, x in v.items()}
    if isinstance(v, list):
        return [_scrub(x) for x in v]
    return v


def _ms(a: int | None, b: int | None) -> float | None:
    return None if a is None or b is None else round((b - a) / 1e6, 1)


def _secs(ms: float) -> str:
    s = ms / 1000
    return f"{s:.1f} s" if s < 60 else f"{int(s // 60)}m {round(s % 60):02d}s"


# --- the wire -----------------------------------------------------------------------------------


@dataclass
class Wire:
    """One captured transaction's rows."""

    cid: str
    ts: int
    method: str | None
    path: str
    req_headers: dict[str, str]
    ip: str | None
    key_hash: str | None
    status: int | None = None
    resp_ts: int | None = None
    resp_headers: dict[str, str] | None = None
    complete: dict[str, Any] | None = None
    end_ts: int | None = None
    up: int | None = None
    down: int | None = None
    up_last: int | None = None
    down_first: int | None = None
    down_last: int | None = None
    req_chunks: list[tuple[int, bytes]] = field(default_factory=list)
    resp_chunks: list[tuple[int, bytes]] = field(default_factory=list)
    batch_session: str | None = None
    # Built from the shaped tables because its raw files couldn't be read: the
    # headers the processor keeps, no bodies or stream.
    from_tables: bool = False

    @property
    def kind(self) -> str:
        return pp.kind_of(self.path)

    @property
    def session_id(self) -> str | None:
        c = self.complete or {}
        return self.req_headers.get("x-claude-code-session-id") or c.get("session_id") or self.batch_session

    @property
    def prompt_id(self) -> str | None:
        return self.req_headers.get("x-claude-code-prompt-id")

    @property
    def request_class(self) -> str | None:
        return self.req_headers.get("x-claude-code-request-class")

    @property
    def client_request_id(self) -> str | None:
        return self.req_headers.get("x-client-request-id")

    @property
    def request_id(self) -> str | None:
        return (self.resp_headers or {}).get("request-id")

    @property
    def error(self) -> str | None:
        return (self.complete or {}).get("error")

    @property
    def failed(self) -> bool:
        return (self.status is None and self.end_ts is not None) or (self.status is not None and self.status >= 400)

    @property
    def retained(self) -> bool:
        return bool(self.req_chunks or self.resp_chunks)

    def c(self, key: str) -> Any:
        return (self.complete or {}).get(key)


WS_TURN = "~"  # a WebSocket turn's id is <connection>~<turn> (its call_id, <connection>#<turn>, URL-safe)


def _wire_from_tables(store: Store, cid: str) -> Wire | None:
    """The transaction from its request row, when its raw files can't be read
    (not synced, or the object store unreachable): what the drawer can still say."""
    found = store.query(
        """SELECT ts, method, path, ip, key_hash, status, response_start_ns, end_ts, client_json, provider_json,
                  model, stop_reason, message_id, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
                  request_bytes, response_bytes, error, session_id, device_id, ext_json
           FROM calls WHERE call_id = replace(?, '~', '#') ORDER BY ts LIMIT 1""",
        [cid],
    )
    if not found:
        return None
    (ts, method, path, ip, kh, status, resp_ts, end_ts, client, provider, model, stop, msg,
     inp, out, read, write, up, down, error, sid, device, ext) = found[0]  # fmt: skip
    complete = {
        k: v
        for k, v in {
            "model": model,
            "stop_reason": stop,
            "message_id": msg,
            "input_tokens": inp,
            "output_tokens": out,
            "cache_read_tokens": read,
            "cache_write_tokens": write,
            "error": error,
            "session_id": sid,
            "device_id": device,
            "client_metadata": json.loads(ext) if ext else None,
        }.items()
        if v is not None
    }
    w = Wire(
        cid,
        ts,
        method,
        path or "",
        _json_obj(client),
        ip,
        kh,
        status=status,
        resp_ts=resp_ts,
        resp_headers=_json_obj(provider) if resp_ts is not None or status is not None else None,
        complete=complete if end_ts is not None else None,
        end_ts=end_ts,
        up=up,
        down=down,
        from_tables=True,
    )
    if w.kind == "analytics":
        found = store.query("SELECT any_value(session_id) FROM cc_events WHERE correlation_id = ?", [cid])
        w.batch_session = found[0][0] if found else None
    return w


def _json_obj(text: str | None) -> dict[str, str]:
    try:
        v = json.loads(text) if text else {}
    except ValueError:
        return {}
    return {str(k): str(x) for k, x in v.items()} if isinstance(v, dict) else {}


_WIRES: dict[tuple[int, str, object], Wire | None] = {}
_WIRES_LOCK = threading.Lock()  # the drawer's tabs and prefetches load in parallel threads


def load_wire(store: Store, cid: str) -> Wire | None:
    """The transaction as the observer recorded it: its raw rows, read from the
    files its lineage names (the shaped tables keep no headers or bodies).
    Cached per commit: the drawer's tabs read the same request's raw files."""
    key = (id(store), cid, store.commit)
    with _WIRES_LOCK:
        if key in _WIRES:
            return _WIRES[key]
    w = _load_wire(store, cid)
    with _WIRES_LOCK:
        while len(_WIRES) >= 8:  # bodies can be MBs in capture mode
            _WIRES.pop(next(iter(_WIRES)))
        _WIRES[key] = w
    return w


def _load_wire(store: Store, cid: str) -> Wire | None:
    if WS_TURN in cid:  # one turn of a WebSocket: its row says what the drawer shows
        return _wire_from_tables(store, cid)
    rows = sorted(store.raw_rows(cid), key=lambda r: (r.get("seq") or 0, r.get("timestamp_ns") or 0))
    if not rows:
        return _wire_from_tables(store, cid)
    w: Wire | None = None
    disconnect: dict[str, Any] | None = None
    for r in rows:
        phase, ts, status = r.get("phase"), r.get("timestamp_ns"), r.get("status")
        if phase == "request_headers" and w is None and isinstance(ts, int):
            w = Wire(
                cid,
                ts,
                r.get("method"),
                r.get("path") or "",
                dict(r.get("headers") or {}),
                r.get("ip"),
                r.get("key_hash"),
            )
        elif w is not None and phase == "response_headers" and w.resp_ts is None:
            w.status, w.resp_ts, w.resp_headers = status, ts, dict(r.get("headers") or {})
        elif w is not None and phase == "complete" and w.complete is None and r.get("transport") != "websocket":
            w.complete, w.end_ts = r, ts
            if w.status is None and status is not None and r.get("error") is None:
                w.status = status
        elif w is not None and phase == "disconnect" and disconnect is None:
            disconnect = r
    if w is None:  # its request headers are in a file the lineage doesn't reach
        return _wire_from_tables(store, cid)
    if w.complete is None and disconnect is not None:  # it ended in a disconnect, as its row says
        w.complete, w.end_ts = disconnect, disconnect.get("timestamp_ns")
    for phase in ("request_body_chunk", "response_body_chunk"):
        chunks = [r for r in rows if r.get("phase") == phase]
        if not chunks:
            continue
        total = sum(int(r.get("bytes") or 0) for r in chunks)
        times = [r["timestamp_ns"] for r in chunks if r.get("timestamp_ns")]
        if phase == "request_body_chunk":
            w.up, w.up_last = total, max(times, default=None)
        else:
            w.down, w.down_first, w.down_last = total, min(times, default=None), max(times, default=None)
        for r in chunks:
            if r.get("body_b64") is None:
                continue
            try:
                chunk = base64.b64decode(r["body_b64"])
            except ValueError:
                continue
            (w.req_chunks if phase == "request_body_chunk" else w.resp_chunks).append(
                (int(r.get("timestamp_ns") or 0), chunk)
            )
    if w.kind == "analytics":
        sid = store.query("SELECT any_value(session_id) FROM cc_events WHERE correlation_id = ?", [cid])
        w.batch_session = sid[0][0] if sid else None
    return w


def _decode(raw: bytes, encoding: str | None) -> str | None:
    """Body bytes as text, per content-encoding; None when it can't be decoded here (br, zstd)."""
    enc = (encoding or "identity").lower().strip()
    try:
        if enc in ("identity", ""):
            data = raw
        elif enc in ("gzip", "x-gzip"):
            data = gzip.decompress(raw)
        elif enc == "deflate":
            data = zlib.decompress(raw)
        else:
            return None
    except (OSError, zlib.error, EOFError):
        return None
    return data.decode("utf-8", errors="replace")


def _header(h: dict[str, str] | None, name: str) -> str | None:
    for k, v in (h or {}).items():
        if k.lower() == name:
            return v
    return None


def request_text(w: Wire) -> str | None:
    if not w.req_chunks:
        return None
    text = _decode(b"".join(c for _, c in w.req_chunks), _header(w.req_headers, "content-encoding"))
    return redact(text) if text is not None else None


def response_text(w: Wire) -> str | None:
    if not w.resp_chunks:
        return None
    text = _decode(b"".join(c for _, c in w.resp_chunks), _header(w.resp_headers, "content-encoding"))
    return redact(text) if text is not None else None


def _json(text: str | None) -> Any:
    if text is None:
        return None
    try:
        return json.loads(text)
    except ValueError:
        return None


def _is_stream(w: Wire) -> bool:
    return "event-stream" in (_header(w.resp_headers, "content-type") or "")


@dataclass
class Sse:
    offset_ms: float | None
    type: str
    data: dict[str, Any]


def sse_events(w: Wire) -> list[Sse]:
    """The response's server-sent events, each at the time its chunk arrived (identity encoding only)."""
    if not w.resp_chunks or not _is_stream(w):
        return []
    enc = (_header(w.resp_headers, "content-encoding") or "identity").lower()
    timed = enc in ("identity", "")
    if timed:
        text, bounds, n = "", [], 0
        for ts, chunk in w.resp_chunks:
            part = chunk.decode("utf-8", errors="replace")
            text += part
            n += len(part)
            bounds.append((n, ts))
    else:
        text, bounds = response_text(w) or "", []
    out: list[Sse] = []
    pos = 0
    for block in re.split(r"\r?\n\r?\n", text):
        start = pos
        pos += len(block) + 2
        name, data = None, []
        for line in block.splitlines():
            if line.startswith("event:"):
                name = line[6:].strip()
            elif line.startswith("data:"):
                data.append(line[5:].strip())
        if not name and not data:
            continue
        payload = _json("\n".join(data))
        payload = payload if isinstance(payload, dict) else {}
        at = next((ts for end, ts in bounds if start < end), None) if timed else None
        out.append(Sse(_ms(w.ts, at), name or str(payload.get("type") or "message"), _scrub(payload)))
    return out


@dataclass
class Block:
    """One content block of the response: thinking, text or tool_use."""

    index: int
    type: str
    text: str = ""
    name: str | None = None
    id: str | None = None
    input: Any = None


def response_blocks(w: Wire) -> list[Block]:
    """The response's content blocks, from the stream or a plain JSON body (retained content only)."""
    if _is_stream(w):
        blocks: dict[int, Block] = {}
        partial: dict[int, str] = {}
        for e in sse_events(w):
            d, i = e.data, e.data.get("index")
            if e.type == "content_block_start" and isinstance(i, int):
                cb = d.get("content_block") or {}
                blocks[i] = Block(i, str(cb.get("type") or "?"), name=cb.get("name"), id=cb.get("id"))
            elif e.type == "content_block_delta" and isinstance(i, int) and i in blocks:
                delta = d.get("delta") or {}
                if "partial_json" in delta:
                    partial[i] = partial.get(i, "") + str(delta["partial_json"])
                blocks[i].text += str(delta.get("text") or delta.get("thinking") or "")
        for i, j in partial.items():
            blocks[i].input = _json(j) if j else {}
        return [blocks[i] for i in sorted(blocks)]
    body = _json(response_text(w))
    content = body.get("content") if isinstance(body, dict) else None
    out = []
    for i, cb in enumerate(content if isinstance(content, list) else []):
        if isinstance(cb, dict):
            text = str(cb.get("text") or cb.get("thinking") or "")
            out.append(Block(i, str(cb.get("type") or "?"), text, cb.get("name"), cb.get("id"), cb.get("input")))
    return out


# --- analytics for the call ----------------------------------------------------------------------


@dataclass
class Facts:
    """Claude Code's analytics around one call."""

    success: ss.Event | None
    query: ss.Event | None
    stall: ss.Event | None
    tools: list[ss.Event]
    retry: ss.Event | None
    joins: Any
    events: list[ss.Event]


def _near_query(evs: list[ss.Event], prompt: str | None, ts: int) -> ss.Event | None:
    """The tengu_api_query just before the call (it carries no request id): same prompt, within 60 s."""
    best = None
    for e in evs:
        if e.name == "tengu_api_query" and e.prompt_id == prompt and ts - 60 * cc.NS <= e.ts <= ts + 2 * cc.NS:
            best = e
    return best


def facts(store: Store, sid: str | None, rid: str | None, crid: str | None, w: Wire | None) -> Facts:
    if not sid:
        return Facts(None, None, None, [], None, None, [])
    # Only this call's events: its prompt's (the query and retries carry no request
    # id), those naming its ids, then the Bash events its tool uses point at.
    prompt_id = w.prompt_id if w else None
    ids = [x for x in (rid, crid) if x]
    evs = ss.events(store, sid, about=(prompt_id, ids, []))
    tool_ids = [t for e in evs if _str_id(e.md.get("requestId")) in ids and (t := _str_id(e.md.get("toolUseID")))]
    if tool_ids:
        seen = {e.id for e in evs}
        evs = sorted(
            evs + [e for e in ss.events(store, sid, about=(None, [], tool_ids)) if e.id not in seen],
            key=lambda e: (e.ts, e.id),
        )
    j = ss._joins(evs)
    succ = (j.success.get(rid) if rid else None) or (j.success.get(crid) if crid else None)
    stall = next(
        (e for e in evs if e.name == "tengu_streaming_stall_summary" and rid and e.md.get("request_id") == rid), None
    )
    tools = j.tools.get(rid, []) if rid else []
    ts = w.ts if w else (succ.ts if succ else 0)
    prompt = w.prompt_id if w else (succ.prompt_id if succ else None)
    query = _near_query(evs, prompt, ts)
    retry = None
    if w is not None and w.failed:
        retry = retry_event([e for e in evs if e.name == "tengu_api_retry"], w.ts, w.end_ts, w.prompt_id)
    mine = [e for e in (query, succ, retry, stall) if e is not None] + tools
    return Facts(succ, query, stall, tools, retry, j, sorted(mine, key=lambda e: (e.ts, e.id)))


def _str_id(v: Any) -> str | None:
    return None if v is None or isinstance(v, dict | list) else str(v)


def retry_event(retries: list[ss.Event], ts: int, end_ts: int | None, prompt: str | None) -> ss.Event | None:
    return pp.nearest_retry(retries, lambda e: (e.ts, e.prompt_id), ts, end_ts, prompt)


# --- shared pieces ----------------------------------------------------------------------------


def _tokens(w: Wire | None, md: dict[str, Any]) -> TokenCount:
    def num(key: str) -> int | None:
        return ss._int(w.c(key)) if w else None

    out = num("output_tokens") if w else ss._int(md.get("outputTokens"))
    if w is None:  # reported only: Claude Code's own counts
        cached = ss._int(md.get("cachedInputTokens"))
        uncached = ss._int(md.get("uncachedInputTokens"))
        return TokenCount(
            input=uncached,
            cacheRead=cached,
            cacheWrite=None,
            cacheWrite5m=ss._int(md.get("cache_creation_5m_input_tokens")),
            cacheWrite1h=ss._int(md.get("cache_creation_1h_input_tokens")),
            output=out,
            accuracy="estimated" if out is not None else "missing",
        )
    return TokenCount(
        input=num("input_tokens"),
        cacheRead=num("cache_read_tokens"),
        cacheWrite=num("cache_write_tokens"),
        cacheWrite5m=ss._int(md.get("cache_creation_5m_input_tokens")),
        cacheWrite1h=ss._int(md.get("cache_creation_1h_input_tokens")),
        output=out,
        accuracy="exact" if out is not None else "missing",
    )


def _cached_pct(t: TokenCount) -> float | None:
    parts = [t.input, t.cacheRead, t.cacheWrite]
    total = sum(p or 0 for p in parts)
    if t.cacheRead is None or total == 0:
        return None
    return round(t.cacheRead / total * 100, 1)


def _thinking(v: Any) -> bool | None:
    if v in (None, ""):
        return None
    return str(v) != "disabled"


def _tool_counts(w: Wire | None) -> list[str]:
    out = []
    for u in (w.c("tool_uses") if w else None) or []:
        if isinstance(u, dict) and u.get("name"):
            n = ss._int(u.get("count")) or 1
            out.append(str(u["name"]) if n == 1 else f"{u['name']} ×{n}")
    return out


@dataclass
class Position:
    prompt_index: int | None = None
    step_index: int | None = None
    step_count: int | None = None
    prev: str | None = None
    next: str | None = None
    prev_message: str | None = None
    next_message: str | None = None
    calls: list[ss.Step] = field(default_factory=list)


def position(store: Store, s: cc.Session | None, w: Wire) -> Position:
    """The call's place in its prompt and thread: steps by prompt; prev/next by the message-id chain, else
    the neighbouring call of the same prompt and request class."""
    if s is None or w.kind != "model":
        return Position()
    steps = ss.steps(store, s.id)
    index = cc.prompt_index(s)
    chain = {
        cid: (mid, prev)
        for cid, mid, prev in store.query(
            """SELECT correlation_id, message_id, previous_message_id
               FROM calls WHERE correlation_id IN (SELECT unnest(?::VARCHAR[]))""",
            [[x.correlation_id for x in steps]],
        )
    }
    mid, prev_mid = chain.get(w.cid, (None, None))
    by_mid = {m: cid for cid, (m, _) in chain.items() if m}
    by_prev = {p: cid for cid, (_, p) in chain.items() if p}
    same = [x for x in steps if x.prompt_id == w.prompt_id] if w.prompt_id else []
    lane = [x for x in same if x.request_class == w.request_class]
    at = next((i for i, x in enumerate(lane) if x.correlation_id == w.cid), None)
    prev = by_mid.get(prev_mid or "") or (lane[at - 1].correlation_id if at else None)
    nxt = by_prev.get(mid or "") if mid else None
    if nxt is None and at is not None and at + 1 < len(lane):
        nxt = lane[at + 1].correlation_id
    step = next((i + 1 for i, x in enumerate(same) if x.correlation_id == w.cid), None)
    return Position(
        prompt_index=index.get(w.prompt_id or ""),
        step_index=step,
        step_count=len(same) or None,
        prev=prev,
        next=nxt,
        prev_message=prev_mid,
        next_message=chain.get(nxt or "", (None, None))[0],
        calls=steps,
    )


def _retry_info(store: Store, w: Wire, f: Facts, pos: Position) -> RetryInfo | None:
    if f.retry is None:
        return None
    attempt = ss._int(f.retry.md.get("attempt")) or 1
    nxt = None
    for x in pos.calls:
        if x.ts > w.ts and x.prompt_id == w.prompt_id and x.request_class == w.request_class:
            nxt = x
            break
    status: int | str | None = None
    if nxt is not None:
        status = nxt.status if nxt.status is not None else ("ERR" if nxt.end_ts is not None else None)
    done = nxt is not None and isinstance(status, int) and status < 400
    return RetryInfo(
        attempt=attempt,
        of=attempt + 1 if done else None,
        backoffMs=ss._float(f.retry.md.get("delayMs")),
        nextRequestId=nxt.correlation_id if nxt else None,
        nextStatus=status,
        nextAt=rows.dt(nxt.ts) if nxt else None,
    )


def _notice(w: Wire, retry: RetryInfo | None) -> Notice | None:
    if not w.failed:
        return None
    took = _ms(w.ts, w.end_ts)
    if w.status is None:
        title = f"✕ {w.error or 'Connection failed'}"
        body = "The request was sent but no response arrived"
        body += f": the connection failed after {_secs(took)}." if took is not None else "."
    else:
        phrase = rows._phrase(w.status)
        title = f"✕ {w.status}{f' {phrase}' if phrase else ''}"
        body = f"Anthropic answered {w.status}" + (f" after {_secs(took)}." if took is not None else ".")
    if retry:
        wait = f" waited {_secs(retry.backoffMs)} and" if retry.backoffMs is not None else ""
        outcome = (
            "the retry succeeded."
            if retry.of is not None
            else "the retry failed too."
            if retry.nextStatus is not None
            else "no retry was captured."
        )
        body += f" Claude Code{wait} sent the request again (tengu_api_retry, attempt {retry.attempt}); {outcome}"
    return Notice(tone="neutral", title=title, body=body)


def _lines(text: str) -> str:
    n = text.count("\n") + 1 if text else 0
    return f"{n} line{'s' if n != 1 else ''}"


def _preview(text: str) -> str:
    if len(text) <= BODY_PREVIEW_CHARS:
        return text
    rest = text[BODY_PREVIEW_CHARS:].count("\n")
    return text[:BODY_PREVIEW_CHARS] + f"\n… {rest} more lines"


def _block_text(content: Any) -> str:
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return "\n".join(str(c.get("text") or "") for c in content if isinstance(c, dict))
    return ""


def _sent_retained(w: Wire) -> list[ContentRow] | None:
    """The messages new in this request: everything after the last assistant message."""
    body = _json(request_text(w))
    msgs = body.get("messages") if isinstance(body, dict) else None
    if not isinstance(msgs, list):
        return None
    names: dict[str, str] = {}
    last = -1
    for i, m in enumerate(msgs):
        if isinstance(m, dict) and m.get("role") == "assistant":
            last = i
            content = m.get("content")
            for cb in content if isinstance(content, list) else []:
                if isinstance(cb, dict) and cb.get("type") == "tool_use" and cb.get("id"):
                    names[str(cb["id"])] = str(cb.get("name") or "")
    out = []
    for m in msgs[last + 1 :]:
        content = m.get("content") if isinstance(m, dict) else None
        blocks = [{"type": "text", "text": content}] if isinstance(content, str) else content or []
        for cb in blocks if isinstance(blocks, list) else []:
            if not isinstance(cb, dict):
                continue
            kind = str(cb.get("type") or "?")
            if kind == "tool_result":
                text = _block_text(cb.get("content"))
                tool = names.get(str(cb.get("tool_use_id") or ""))
                label = f"tool_result · {tool}" if tool else "tool_result"
                detail = _lines(text) + (" · error" if cb.get("is_error") else "")
                size = len(text.encode())
                out.append(ContentRow(label=label, detail=detail, size=size, unit="bytes", body=_preview(text)))
            elif kind == "text":
                text = str(cb.get("text") or "")
                out.append(
                    ContentRow(
                        label="text", detail=f"{len(text)} chars", size=len(text), unit="chars", body=_preview(text)
                    )
                )
            else:
                out.append(ContentRow(label=kind, detail="", size=None, unit=None))
    return out


def _received_retained(w: Wire) -> list[ContentRow]:
    out = []
    for b in response_blocks(w):
        if b.type == "tool_use":
            text = json.dumps(_scrub(b.input), indent=2) if b.input is not None else ""
            out.append(
                ContentRow(
                    label=f"tool_use · {b.name or '?'}",
                    detail=b.id or "",
                    size=len(text.encode()),
                    unit="bytes",
                    tone="tool",
                    body=_preview(text),
                )
            )
        else:
            out.append(
                ContentRow(
                    label=b.type, detail=f"{len(b.text)} chars", size=len(b.text), unit="chars", body=_preview(b.text)
                )
            )
    return out


def _sent_shape(w: Wire, md: dict[str, Any]) -> list[ContentRow]:
    n = ss._int(md.get("messageCount"))
    detail = f"{n} messages · not retained" if n is not None else "not retained"
    return [ContentRow(label="request body", detail=detail, size=w.up, unit="bytes" if w.up is not None else None)]


def _received_shape(w: Wire, md: dict[str, Any]) -> list[ContentRow]:
    """What the response held, from Claude Code's content lengths and the observer's tool tally."""
    out = []
    thinking, text = ss._int(md.get("thinkingContentLength")), ss._int(md.get("textContentLength"))
    if thinking:
        out.append(ContentRow(label="thinking", detail="not retained", size=thinking, unit="chars"))
    if text:
        out.append(ContentRow(label="text", detail="not retained", size=text, unit="chars"))
    for name in _tool_counts(w):
        out.append(ContentRow(label=f"tool_use · {name}", detail="not retained", size=None, unit=None, tone="tool"))
    if not out:
        size = w.down
        out.append(
            ContentRow(
                label="response body", detail="not retained", size=size, unit="bytes" if size is not None else None
            )
        )
    return out


# --- RequestDetail ------------------------------------------------------------------------------


def detail(store: Store, cid: str) -> RequestDetail | None:
    if cid.startswith(REPORTED):
        return reported_detail(store, cid)
    w = load_wire(store, cid)
    if w is None:
        return None
    sid = w.session_id
    s = cc.load_session(store, sid) if sid else None
    f = facts(store, sid, w.request_id, w.client_request_id, w)
    pos = position(store, s, w)
    md = f.success.md if f.success else {}
    model = w.kind == "model"
    tokens = _tokens(w, md)
    retry = _retry_info(store, w, f, pos)
    status: int | str | None = w.status if w.status is not None else ("ERR" if w.end_ts is not None else None)
    retained = w.retained
    sent = (_sent_retained(w) if retained else None) or (_sent_shape(w, md) if model else [])
    received = [] if w.failed else (_received_retained(w) if retained else (_received_shape(w, md) if model else []))
    if w.failed:
        received = [ContentRow(label="no response", detail=w.error or f"HTTP {w.status}", size=None, unit=None)]
    client_ttft = ss._float(md.get("ttftMs"))
    proxy_ttft = _ms(w.ts, w.down_first or w.resp_ts) if not w.failed else None
    return RequestDetail(
        requestId=w.cid,
        kind="failed" if w.failed else "normal",
        t=rows.dt(w.ts),
        method=w.method,
        path=w.path,
        status=status,
        durationMs=_ms(w.ts, w.end_ts),
        bytesUp=w.up,
        bytesDown=w.down,
        sessionId=sid,
        promptIndex=pos.prompt_index,
        stepIndex=pos.step_index,
        stepCount=pos.step_count,
        requestClass=w.request_class,
        prevRequestId=pos.prev,
        nextRequestId=pos.next,
        keyStrip=KeyStrip(
            model=w.c("model") or ss._str(md.get("model")),
            effort=w.c("effort") or ss._str(md.get("effort_level")),
            thinking=_thinking(w.c("thinking_type")),
            tokens=tokens,
            cachedPct=_cached_pct(tokens),
            cost=Cost(
                usd=ss._float(md.get("costUSD")),
                basis="api-equivalent",
                estimatedCalls=1 if model and f.success is None and not w.failed else 0,
            ),
            ttftMs=client_ttft,
            proxyTtftMs=proxy_ttft,
            ttftSource="client" if client_ttft is not None else "proxy" if proxy_ttft is not None else None,
            reasoningTokens=ss._int(w.c("reasoning_tokens")),
            usageMissing=rows.usage_missing(
                tokens.output,
                w.status if isinstance(w.status, int) else None,
                w.c("stop_reason"),
                1 if WS_TURN in w.cid else None,
                w.down,
                None,
            )
            if model
            else None,
            streamEvents=ss._int(md.get("streamEventCount")) or (len(sse_events(w)) or None),
            stopReason=w.c("stop_reason") or ss._str(md.get("stop_reason")),
            toolCalls=_tool_counts(w),
        ),
        crossCheck=CrossCheck(matched=f.success is not None, costAgrees=None),
        notice=_notice(w, retry),
        retry=retry,
        sent=sent,
        received=received,
        contentRetained=retained,
    )


def find_report(store: Store, cid: str) -> tuple[str, ss.Event] | None:
    """A `reported:<requestId | clientRequestId | ts>` id -> (session id, its tengu_api_success)."""
    key = cid.removeprefix(REPORTED)
    found = store.query(
        """SELECT session_id FROM cc_events WHERE name = 'tengu_api_success'
             AND (json_extract_string(md, '$.requestId') = ? OR json_extract_string(md, '$.clientRequestId') = ?
                  OR CAST(ts AS VARCHAR) = ?)
           LIMIT 1""",
        [key, key, key],
    )
    if not found or not found[0][0]:
        return None
    sid = found[0][0]
    for e in ss.events(store, sid, ["tengu_api_success"]):
        if key in (ss._str(e.md.get("requestId")), ss._str(e.md.get("clientRequestId")), str(e.ts)):
            return sid, e
    return None


def reported_detail(store: Store, cid: str) -> RequestDetail | None:
    """A call Claude Code reported that the proxy never saw: everything comes from its tengu_api_success."""
    hit = find_report(store, cid)
    if hit is None:
        return None
    sid, e = hit
    s = cc.load_session(store, sid)
    md = e.md
    tokens = _tokens(None, md)
    index = cc.prompt_index(s) if s else {}
    inp, out = ss._int(md.get("inputTokens")), ss._int(md.get("outputTokens"))
    cached = ss._int(md.get("cachedInputTokens"))
    usage = (
        f"{inp:,} in" + (f" ({cached:,} cached)" if cached is not None else "") + f" → {out:,} out"
        if inp is not None and out is not None
        else "usage not reported"
    )
    return RequestDetail(
        requestId=cid,
        kind="notCaptured",
        t=rows.dt(e.ts),
        method=None,
        path="/v1/messages",
        status=None,
        durationMs=ss._float(md.get("durationMs")),
        bytesUp=None,
        bytesDown=None,
        sessionId=sid,
        promptIndex=index.get(e.prompt_id or ""),
        stepIndex=None,
        stepCount=None,
        requestClass=None,
        prevRequestId=None,
        nextRequestId=None,
        keyStrip=KeyStrip(
            model=ss._str(md.get("model")),
            effort=ss._str(md.get("effort_level")),
            thinking=None,
            tokens=tokens,
            cachedPct=_cached_pct(tokens),
            cost=Cost(usd=ss._float(md.get("costUSD")), basis="api-equivalent", estimatedCalls=0),
            ttftMs=ss._float(md.get("ttftMs")),
            proxyTtftMs=None,
            ttftSource="client" if ss._float(md.get("ttftMs")) is not None else None,
            reasoningTokens=None,
            usageMissing=None,
            streamEvents=ss._int(md.get("streamEventCount")),
            stopReason=ss._str(md.get("stop_reason")),
            toolCalls=[],
        ),
        crossCheck=CrossCheck(matched=False, costAgrees=None),
        notice=Notice(
            tone="danger",
            title="▲ This call bypassed the proxy",
            body=(
                "Claude Code’s own analytics (tengu_api_success) report a model call the proxy never saw. "
                "Tokens, cost and timing come from Claude Code’s report; there is no request or response to show."
            ),
        ),
        retry=None,
        sent=[ContentRow(label="messages", detail="not captured", size=None, unit=None)],
        received=[ContentRow(label="usage", detail=usage, size=out, unit="tokens" if out is not None else None)],
        contentRetained=False,
    )


# --- raw --------------------------------------------------------------------------------------


def _headers(h: dict[str, str] | None) -> list[HeaderEntry]:
    out = []
    for name in sorted(h or {}, key=str.lower):
        value = (h or {})[name]
        secret = value == MARKER or bool(SECRET_HEADER.search(name))
        out.append(HeaderEntry(name=name, value=None if secret else redact(value), redacted=secret))
    return out


def _fold(events: list[Sse]) -> list[StreamEvent]:
    out: list[StreamEvent] = []
    for e in events:
        d = e.data
        if e.type == "content_block_delta":
            kind = str((d.get("delta") or {}).get("type") or "delta")
            last = out[-1] if out else None
            if last and last.type == "content_block_delta" and last.detail.startswith(f"[{d.get('index')}]"):
                last.count = (last.count or 1) + 1
                if kind not in last.detail:
                    last.detail += f" · {kind}"
                continue
            out.append(StreamEvent(tOffsetMs=e.offset_ms, type=e.type, detail=f"[{d.get('index')}] {kind}", count=1))
            continue
        if e.type == "message_start":
            m = d.get("message") or {}
            detail = " · ".join(x for x in (f"id {m.get('id')}" if m.get("id") else "", str(m.get("model") or "")) if x)
        elif e.type == "content_block_start":
            cb = d.get("content_block") or {}
            detail = " · ".join(str(x) for x in (f"[{d.get('index')}]", cb.get("type"), cb.get("name")) if x)
        elif e.type == "message_delta":
            delta, usage = d.get("delta") or {}, d.get("usage") or {}
            parts = [f"stop_reason {delta['stop_reason']}" if delta.get("stop_reason") else ""]
            parts.append(f"output_tokens {usage['output_tokens']}" if "output_tokens" in usage else "")
            detail = " · ".join(p for p in parts if p)
        elif e.type == "error":
            err = d.get("error") or {}
            detail = " · ".join(str(x) for x in (err.get("type"), err.get("message")) if x)
        else:
            detail = ""
        out.append(StreamEvent(tOffsetMs=e.offset_ms, type=e.type, detail=detail, count=None))
    for x in out:
        if x.count == 1:
            x.count = None
    return out


def raw(store: Store, cid: str) -> RequestRaw | None:
    if cid.startswith(REPORTED):
        if find_report(store, cid) is None:
            return None
        return RequestRaw(
            unavailable=(
                "No wire data: this call never passed through the proxy, so there are no headers, body or stream."
            ),
            requestLine=None,
            requestHeaders=[],
            requestBody=None,
            responseLine=None,
            responseHeaders=[],
            responseBody=None,
            stream=None,
            keyHash=None,
            contentRetained=False,
        )
    w = load_wire(store, cid)
    if w is None:
        return None
    unavailable = None
    if WS_TURN in cid:
        unavailable = (
            "This is one turn of a WebSocket connection: its frames are in the connection's raw rows, "
            "which aren't split per turn here. The summary and details come from the processed tables."
        )
    elif w.from_tables:
        unavailable = (
            "The raw files for this request can't be read here, so its full headers, body and stream aren't shown; "
            "the summary and details come from the processed tables."
        )
    elif w.resp_headers is None:
        took = _ms(w.ts, w.end_ts)
        unavailable = "The request was sent but no response arrived" + (
            f": {w.error or 'the connection failed'} after {_secs(took)}." if took is not None else "."
        )
    phrase = rows._phrase(w.status) if w.status is not None else None
    stream = _fold(sse_events(w)) if w.retained and _is_stream(w) else None
    return RequestRaw(
        unavailable=unavailable,
        requestLine=f"{w.method or '—'} {w.path}",
        requestHeaders=_headers(w.req_headers),
        requestBody=request_text(w),
        responseLine=f"{w.status}{f' {phrase}' if phrase else ''}" if w.resp_headers is not None else None,
        responseHeaders=_headers(w.resp_headers),
        responseBody=response_text(w) if w.retained and not _is_stream(w) else None,
        stream=stream,
        keyHash=w.key_hash,
        contentRetained=w.retained,
    )


# --- details ------------------------------------------------------------------------------------


def _row(k: str, v: Any, unit: str | None = None, *, tone: str | None = None, note: str | None = None) -> DetailRow:
    return DetailRow(k=k, v=v, unit=unit, tone=tone, note=note)


def _limit(h: dict[str, str] | None, name: str) -> str | None:
    return _header(h, f"anthropic-ratelimit-unified-{name}")


def _pct(v: str | None) -> float | None:
    f = ss._float(v)
    return round(f * 100, 1) if f is not None else None


def _event_rows(store: Store, sid: str | None, evs: list[ss.Event]) -> list[EventRow]:
    if not sid:
        return []
    steps = {x.request_id: x for x in ss.steps(store, sid) if x.request_id}
    index = pp.prompt_indexes(store, [sid])
    return [
        EventRow(
            eventId=e.id,
            t=rows.dt(e.ts),
            name=e.name,
            promptIndex=index.get(e.prompt_id) if e.prompt_id else None,
            linkedTo=ss._link(e, sid, steps, index),
            summary=ss._summary(e.md),
            marker=ss._marker(e),
            internal=ss.is_internal(e.name),
            collapsedCount=None,
        )
        for e in evs
    ]


def _flatten(prefix: str, v: Any, out: list[FieldEntry]) -> None:
    if isinstance(v, dict):
        for k in sorted(v):
            _flatten(f"{prefix}.{k}" if prefix else k, v[k], out)
    elif isinstance(v, list):
        out.append(FieldEntry(key=prefix, value=redact(json.dumps(v, separators=(",", ":")))))
    else:
        out.append(
            FieldEntry(
                key=prefix, value=None if v is None else redact(str(v).lower() if isinstance(v, bool) else str(v))
            )
        )


def _fields(w: Wire | None, md: dict[str, Any]) -> list[FieldEntry]:
    out: list[FieldEntry] = []
    if w is not None:
        c = {k: v for k, v in (w.complete or {}).items() if k not in CONTENT_KEYS}
        for k in COMPLETE_FIELDS:
            c.setdefault(k, None)
        _flatten("", c, out)
    if md:
        _flatten("analytics", md, out)
    return out


def _tool_details(w: Wire | None, f: Facts, pos: Position) -> list[ToolCallDetail]:
    inputs = {b.id: b for b in response_blocks(w) if b.type == "tool_use"} if w and w.retained else {}
    nxt = next((i for i, x in enumerate(pos.calls) if x.correlation_id == pos.next), None)
    out = []
    for e in f.tools:
        tool = ss._tool(e, f.joins)
        block = inputs.get(tool.toolUseId)
        params = block.input if block is not None and isinstance(block.input, dict) else None
        out.append(
            ToolCallDetail(
                toolUseId=tool.toolUseId,
                name=tool.name,
                mcpServer=tool.mcpServer,
                input=[_input_row(k, v) for k, v in params.items()] if params else [],
                execution=[
                    _row("status", tool.status),
                    _row("duration", tool.durationMs, "ms"),
                    _row("PreToolUse hook", ss._float(e.md.get("preToolHookDurationMs")), "ms"),
                    _row("permission wait", ss._float(e.md.get("permissionDurationMs")), "ms"),
                    *([_row("exit code", tool.exitCode)] if tool.exitCode is not None else []),
                    *([_row("destructive", tool.destructive.category)] if tool.destructive else []),
                ],
                result=[
                    _row("delivered in", f"step {nxt + 1}" if nxt is not None else None, note=pos.next),
                    _row("size", tool.resultBytes, "bytes"),
                    _row("is_error", tool.status == "error"),
                ],
            )
        )
    if not out and w is not None:  # the observer's tally only: names, no execution facts
        for name in _tool_counts(w):
            out.append(ToolCallDetail(toolUseId=None, name=name, mcpServer=None, input=[], execution=[], result=[]))
    return out


def _input_row(k: str, v: Any) -> DetailRow:
    text = v if isinstance(v, str) else json.dumps(_scrub(v))
    text = redact(text)
    if len(text) <= 120 and "\n" not in text:
        return _row(k, text)
    return _row(k, f"{_lines(text)} · {len(text)} chars", tone="muted")


def details(store: Store, cid: str) -> RequestDetails | None:
    d = detail(store, cid)
    if d is None:
        return None
    if cid.startswith(REPORTED):
        hit = find_report(store, cid)
        sid, e = hit if hit else (None, None)
        md = e.md if e else {}
        f = Facts(e, None, None, [], None, None, [e] if e else [])
        w: Wire | None = None
        pos = Position()
    else:
        w = load_wire(store, cid)
        if w is None:  # gone between the two reads
            return None
        sid = w.session_id
        f = facts(store, sid, w.request_id, w.client_request_id, w)
        md = f.success.md if f.success else {}
        pos = position(store, cc.load_session(store, sid) if sid else None, w)
    k = d.keyStrip
    rh = w.resp_headers if w else None
    t = k.tokens
    stalls = ss._int(f.stall.md.get("stall_count")) if f.stall else (0 if f.success else None)
    sandbox = None
    if sid:
        for e in ss.events(store, sid, ["tengu_bash_tool_command_executed"]):
            if e.ts <= cc.to_ns(d.t) + cc.NS:
                sandbox = ss._str(e.md.get("sandbox_enabled")) or sandbox
    mcp = sorted(
        {
            str(x.get("server"))
            for x in (w.c("tools_available") if w else None) or []
            if isinstance(x, dict) and x.get("server")
        }
    )
    tools_available = w.c("tools_available") if w else None
    sections = [
        DetailSection(
            title="Tokens & cost",
            accent="count",
            rows=[
                _row("input · uncached", t.input, "tokens"),
                _row("cache read", t.cacheRead, "tokens"),
                _row("cache write", t.cacheWrite, "tokens"),
                _row("cache write 5m", t.cacheWrite5m, "tokens"),
                _row("cache write 1h", t.cacheWrite1h, "tokens"),
                _row("output", t.output, "tokens"),
                _row("thinking", ss._int(md.get("thinkingContentLength")), "chars"),
                _row("costUSD (Claude Code)", k.cost.usd, "usd", tone="ok" if d.crossCheck.matched else None),
            ],
        ),
        DetailSection(
            title="Request settings",
            accent=None,
            rows=[
                _row("model", k.model),
                _row("max_tokens", ss._int(w.c("max_tokens")) if w else None, "tokens"),
                _row("thinking", w.c("thinking_type") if w else None),
                _row("effort", k.effort),
                _row("stream", w.c("stream") if w and w.complete and "stream" in w.complete else None),
                _row(
                    "tools",
                    ss._int(md.get("toolsCount")) if md else (len(tools_available) if tools_available else None),
                ),
            ],
        ),
        DetailSection(
            title="Thread position",
            accent=None,
            rows=[
                _row("previous step", pos.prev_message or pos.prev),
                _row("this step", w.c("message_id") if w else None),
                _row("next step", pos.next_message or pos.next),
                _row("thread type", w.c("thread_type") if w else None),
                _row("messages", ss._int(md.get("messageCount"))),
                _row("query depth", ss._int(md.get("queryDepth"))),
            ],
        ),
        DetailSection(
            title="What the model could use",
            accent="skills",
            rows=[
                _row("tools", len(tools_available) if tools_available else ss._int(md.get("toolsCount"))),
                _row("deferred MCP tools", ss._int(md.get("deferredToolsCount"))),
                _row("MCP servers", len(mcp) if tools_available else None, note=", ".join(mcp) or None),
                _row("system prompt", ss._int(md.get("systemCharLength")), "chars"),
                _row("tool schemas", ss._int(md.get("toolsCharLength")), "chars"),
                _row("skills", None, note="not extracted yet"),
            ],
        ),
        DetailSection(
            title="Response service",
            accent=None,
            rows=[
                _row("service tier", w.c("service_tier") if w else None),
                _row("inference geo", w.c("inference_geo") if w else None),
                _row("speed", w.c("speed") if w else None),
                _row("status", w.status if w else None),
                _row("request-id", (w.request_id if w else None) or ss._str(md.get("requestId"))),
                _row("retries", (ss._int(md.get("attempt")) or 1) - 1 if md.get("attempt") is not None else None),
                _row("streaming stalls", stalls),
            ],
        ),
        DetailSection(
            title="Account limits at response",
            accent=None,
            rows=[
                _row("unified status", _limit(rh, "status")),
                _row("7-day utilization", _pct(_limit(rh, "7d-utilization")), "pct"),
                _row("representative claim", _limit(rh, "representative-claim")),
                _row("overage", _limit(rh, "overage-status")),
                _row("overage disabled reason", _limit(rh, "overage-disabled-reason")),
                _row("fallback percentage", _pct(_limit(rh, "fallback-percentage")), "pct"),
            ],
        ),
        DetailSection(
            title="Network & proxy",
            accent=None,
            rows=[
                _row("client", w.ip if w else None),
                _row("device", w.c("device_id") if w else None),
                _row("TLS", "intercepted by the proxy" if w else None),
                _row("upstream connect", None, "ms", note="not recorded by the proxy"),
                _row("bytes up", d.bytesUp, "bytes"),
                _row("bytes down", d.bytesDown, "bytes"),
                _row("credential", w.key_hash if w else None, tone="muted", note="key_hash (refreshes; not identity)"),
            ],
        ),
        DetailSection(
            title="Cross-check & posture",
            accent=None,
            rows=[
                _row("analytics match", d.crossCheck.matched, tone="ok" if d.crossCheck.matched else None),
                _row("cost agreement", None, note="no price table (costUSD is Claude Code’s own)"),
                _row("ttft (Claude Code)", k.ttftMs, "ms"),
                _row("ttft (proxy)", k.proxyTtftMs, "ms"),
                _row("permissionMode", ss._str(md.get("permissionMode"))),
                _row("sandbox", {"true": "on", "false": "off"}.get(sandbox or "")),
                _row(
                    "content retention",
                    "full content (experimentation mode)" if d.contentRetained else "shape only",
                    tone="muted",
                ),
            ],
        ),
    ]
    if w is not None and isinstance(w.c("client_metadata"), dict):
        sections.append(_codex_section(w))
    proxy: list[TimingPhase] = []
    if w is not None:
        end = w.end_ts or w.down_last
        # The proxy doesn't record these: their rows stay, with no bar (handoff §11D).
        proxy.append(TimingPhase(label="accept + TLS", startMs=None, durMs=None))
        proxy.append(TimingPhase(label="upstream connect", startMs=None, durMs=None))
        first = w.down_first or w.resp_ts
        if first:
            proxy.append(TimingPhase(label="first byte", startMs=0, durMs=_ms(w.ts, first) or 0))
            if end and end > first:
                proxy.append(TimingPhase(label="streaming", startMs=_ms(w.ts, first) or 0, durMs=_ms(first, end) or 0))
        if end:
            proxy.append(TimingPhase(label="total", startMs=0, durMs=_ms(w.ts, end) or 0))
    client = [
        _row(
            key,
            ss._float(md.get(key)) if key != "attempt" else ss._int(md.get(key)),
            "ms" if key != "attempt" else None,
        )
        for key in (
            "requestPrepareMs",
            "ttftMs",
            "firstContentMs",
            "durationMs",
            "durationMsIncludingRetries",
            "attempt",
        )
        if md.get(key) is not None
    ]
    return RequestDetails(
        composition=t,
        sections=sections,
        tool=_tool_details(w, f, pos),
        timing=Timing(proxy=proxy, client=client),
        events=_event_rows(store, sid, f.events),
        fields=_fields(w, md),
    )


def _codex_section(w: Wire) -> DetailSection:
    """Codex · response (handoff §11J.2): what only Codex reports on a turn, after the common sections."""
    md: dict[str, Any] = w.c("client_metadata") or {}
    limits = agents.codex_limits(json.dumps(w.resp_headers or {}), datetime.fromtimestamp(w.ts / 1e9, UTC))
    shown = [x for x in (limits.windows if limits else []) if x.usedPct is not None]
    windows = " · ".join(f"{x.name} {x.usedPct:g}%" for x in shown)
    return DetailSection(
        title="Codex · response",
        accent=None,
        rows=[
            _row("request kind", ss._str(md.get("request_kind"))),
            _row("thread source", ss._str(md.get("thread_source"))),
            _row("agent", ss._str(md.get("agent_name"))),
            _row("history", ss._str(md.get("history_initialization"))),
            _row("trigger", ss._str(md.get("turn_trigger"))),
            _row("sandbox", ss._str(md.get("sandbox_mode"))),
            _row("limits", windows or None),
            _row("plan", limits.plan if limits else None),
        ],
    )


# --- one event -------------------------------------------------------------------------------


def event(store: Store, event_id: str) -> EventDetail | None:
    """`<batch correlation id>:<n>`, the event's position in its whole batch (as session.events numbers it)."""
    cid, _, n = event_id.rpartition(":")
    if not cid or not n.isdigit():
        return None
    found = store.query(
        """SELECT ts, name, session_id, event_key, source_file FROM (
             SELECT ts, coalesce(name, '(unnamed)') AS name, session_id, event_key, source_file,
                    row_number() OVER (PARTITION BY correlation_id ORDER BY seq, row_ts, ts, name, md::VARCHAR) AS n
             FROM cc_events WHERE correlation_id = ?)
           WHERE n = ?""",
        [cid, int(n)],
    )
    if not found:
        return None
    ts, name, sid, key, source = found[0]
    body = store.raw_event(cid, key, source) or {}
    ev = body.get("client_event") if isinstance(body, dict) else None
    return EventDetail(
        eventId=event_id, t=rows.dt(ts), name=name, sessionId=sid, raw=_scrub(ev if isinstance(ev, dict) else {})
    )
