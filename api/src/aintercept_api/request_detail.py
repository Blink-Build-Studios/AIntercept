"""The request drawer (handoff §5.2.10): one transaction's summary, raw wire data
and every known fact.

Sources, per transaction (correlation_id):
- request_headers / response_headers rows: headers (the observer already
  replaces credential values with a marker; they are redacted again here),
  status, client IP, key_hash;
- the complete row: model, usage, stop reason, request settings (effort,
  thinking, max_tokens, stream), the message-id chain, service tier, inference
  geo, the tool catalog and the tools the response used;
- body chunks: byte totals and timestamps (proxy timing); the bytes themselves
  only when the observer ran with full-body capture (`contents`).

Claude Code's analytics (tengu_api_success, its tool events, retries, stall
summaries) aren't in the database, so what only they said (its TTFT, cost, the
cache write split, content lengths, client timings, the cross-check, retries)
is null, and there are no reported-only (`reported:`) calls.

Envoy's own connection timings (accept, upstream connect, TLS) aren't recorded,
so proxy timing starts at the request headers.

What the drawer reads comes through `Reads`: v9's indexed reads of Postgres and one ranged GET of the
call's raw member (drawer.PgReads, docs/PIPELINE.md §6.2).
"""

import base64
import gzip
import json
import re
import threading
import zlib
from dataclasses import dataclass, field
from datetime import UTC, datetime
from typing import Any, Protocol

from aintercept_api import agents, rows
from aintercept_api import person as pp
from aintercept_api import session as ss
from aintercept_api.models.people import Cost, TokenCount
from aintercept_api.models.requests import (
    ContentRow,
    CrossCheck,
    DetailRow,
    DetailSection,
    FieldEntry,
    HeaderEntry,
    KeyStrip,
    Notice,
    RequestDetail,
    RequestDetails,
    RequestRaw,
    StreamEvent,
    Timing,
    TimingPhase,
    ToolCallDetail,
)

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


def wire_from_row(cid: str, r: dict[str, Any]) -> Wire:
    """The transaction from its request row (ROW_COLUMNS; its JSON columns as text): its headers as the
    processor keeps them, no bodies or stream."""
    complete = {
        k: v
        for k, v in {
            "model": r["model"],
            "stop_reason": r["stop_reason"],
            "message_id": r["message_id"],
            "input_tokens": r["input_tokens"],
            "output_tokens": r["output_tokens"],
            "cache_read_tokens": r["cache_read_tokens"],
            "cache_write_tokens": r["cache_write_tokens"],
            "error": r["error"],
            "session_id": r["session_id"],
            "device_id": r["device_id"],
            "client_metadata": json.loads(r["ext_json"]) if r["ext_json"] else None,
        }.items()
        if v is not None
    }
    status, resp_ts, end_ts = r["status"], r["response_start_ns"], r["end_ts"]
    return Wire(
        cid,
        r["ts"],
        r["method"],
        r["path"] or "",
        _json_obj(r["client_json"]),
        r["ip"],
        r["key_hash"],
        status=status,
        resp_ts=resp_ts,
        resp_headers=_json_obj(r["provider_json"]) if resp_ts is not None or status is not None else None,
        complete=complete if end_ts is not None else None,
        end_ts=end_ts,
        up=r["request_bytes"],
        down=r["response_bytes"],
        from_tables=True,
    )


def _json_obj(text: str | None) -> dict[str, str]:
    try:
        v = json.loads(text) if text else {}
    except ValueError:
        return {}
    return {str(k): str(x) for k, x in v.items()} if isinstance(v, dict) else {}


_WIRES: dict[tuple[int, str, object], Wire | None] = {}
_WIRES_LOCK = threading.Lock()  # the drawer's tabs and prefetches load in parallel threads


def wire_from_rows(cid: str, raw_rows: list[dict[str, Any]]) -> Wire | None:
    """The transaction from its raw rows; None without its request headers among them."""
    rows = sorted(raw_rows, key=lambda r: (r.get("seq") or 0, r.get("timestamp_ns") or 0))
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
    if w is None:
        return None
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


# --- shared pieces ----------------------------------------------------------------------------


def _tokens(w: Wire) -> TokenCount:
    def num(key: str) -> int | None:
        return ss._int(w.c(key))

    out = num("output_tokens")
    return TokenCount(
        input=num("input_tokens"),
        cacheRead=num("cache_read_tokens"),
        cacheWrite=num("cache_write_tokens"),
        cacheWrite5m=None,
        cacheWrite1h=None,
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


def _tool_counts(w: Wire) -> list[str]:
    out = []
    for u in w.c("tool_uses") or []:
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
    delivered: int | None = None
    """The step its tool results are delivered in: the next call's."""


def _notice(w: Wire) -> Notice | None:
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


def _sent_shape(w: Wire) -> list[ContentRow]:
    return [
        ContentRow(label="request body", detail="not retained", size=w.up, unit="bytes" if w.up is not None else None)
    ]


def _received_shape(w: Wire) -> list[ContentRow]:
    """What the response held, from the observer's tool tally."""
    out = []
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


# --- what the drawer reads ------------------------------------------------------------------------


class Reads(Protocol):
    """What the drawer reads of one call."""

    def wire(self, cid: str) -> Wire | None:
        """The call as the observer recorded it (an analytics post with its scope session); None for no call."""
        ...

    def position(self, sid: str | None, w: Wire) -> Position: ...


# --- RequestDetail ------------------------------------------------------------------------------


@dataclass
class _Drawn:
    """A drawer's summary, and what its details read again."""

    d: RequestDetail
    sid: str | None
    w: Wire
    pos: Position


def detail(reads: Reads, cid: str) -> RequestDetail | None:
    drawn = _draw(reads, cid)
    return drawn.d if drawn else None


def _draw(reads: Reads, cid: str) -> _Drawn | None:
    w = reads.wire(cid)
    if w is None:
        return None
    sid = w.session_id
    pos = reads.position(sid, w)
    model = w.kind == "model"
    tokens = _tokens(w)
    status: int | str | None = w.status if w.status is not None else ("ERR" if w.end_ts is not None else None)
    retained = w.retained
    sent = (_sent_retained(w) if retained else None) or (_sent_shape(w) if model else [])
    received = [] if w.failed else (_received_retained(w) if retained else (_received_shape(w) if model else []))
    if w.failed:
        received = [ContentRow(label="no response", detail=w.error or f"HTTP {w.status}", size=None, unit=None)]
    proxy_ttft = _ms(w.ts, w.down_first or w.resp_ts) if not w.failed else None
    d = RequestDetail(
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
            model=w.c("model"),
            effort=w.c("effort"),
            thinking=_thinking(w.c("thinking_type")),
            tokens=tokens,
            cachedPct=_cached_pct(tokens),
            cost=Cost(usd=None, basis="api-equivalent", estimatedCalls=0),
            ttftMs=None,
            proxyTtftMs=proxy_ttft,
            ttftSource="proxy" if proxy_ttft is not None else None,
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
            streamEvents=len(sse_events(w)) or None,
            stopReason=w.c("stop_reason"),
            toolCalls=_tool_counts(w),
        ),
        crossCheck=CrossCheck(matched=False, costAgrees=None),
        notice=_notice(w),
        retry=None,
        sent=sent,
        received=received,
        contentRetained=retained,
    )
    return _Drawn(d, sid, w, pos)


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


def raw(reads: Reads, cid: str) -> RequestRaw | None:
    return wire_raw(reads.wire(cid))


def wire_raw(w: Wire | None) -> RequestRaw | None:
    """A captured call's raw view: its detail object (or, when that can't be read, its request row)."""
    if w is None:
        return None
    unavailable = None
    if WS_TURN in w.cid:
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


def _fields(w: Wire) -> list[FieldEntry]:
    out: list[FieldEntry] = []
    c = {k: v for k, v in (w.complete or {}).items() if k not in CONTENT_KEYS}
    for k in COMPLETE_FIELDS:
        c.setdefault(k, None)
    _flatten("", c, out)
    return out


def _tool_details(w: Wire) -> list[ToolCallDetail]:
    """The observer's tally of the tools the response asked to run: names only (their execution facts were
    Claude Code's analytics)."""
    return [
        ToolCallDetail(toolUseId=None, name=name, mcpServer=None, input=[], execution=[], result=[])
        for name in _tool_counts(w)
    ]


def details(reads: Reads, cid: str) -> RequestDetails | None:
    drawn = _draw(reads, cid)
    if drawn is None:
        return None
    d, w, pos = drawn.d, drawn.w, drawn.pos
    k = d.keyStrip
    rh = w.resp_headers
    t = k.tokens
    mcp = sorted(
        {str(x.get("server")) for x in w.c("tools_available") or [] if isinstance(x, dict) and x.get("server")}
    )
    tools_available = w.c("tools_available")
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
                _row("thinking", None, "chars"),
                _row("costUSD (Claude Code)", None, "usd"),
            ],
        ),
        DetailSection(
            title="Request settings",
            accent=None,
            rows=[
                _row("model", k.model),
                _row("max_tokens", ss._int(w.c("max_tokens")), "tokens"),
                _row("thinking", w.c("thinking_type")),
                _row("effort", k.effort),
                _row("stream", w.c("stream") if w.complete and "stream" in w.complete else None),
                _row("tools", len(tools_available) if tools_available else None),
            ],
        ),
        DetailSection(
            title="Thread position",
            accent=None,
            rows=[
                _row("previous step", pos.prev_message or pos.prev),
                _row("this step", w.c("message_id")),
                _row("next step", pos.next_message or pos.next),
                _row("thread type", w.c("thread_type")),
                _row("messages", None),
                _row("query depth", None),
            ],
        ),
        DetailSection(
            title="What the model could use",
            accent="skills",
            rows=[
                _row("tools", len(tools_available) if tools_available else None),
                _row("deferred MCP tools", None),
                _row("MCP servers", len(mcp) if tools_available else None, note=", ".join(mcp) or None),
                _row("system prompt", None, "chars"),
                _row("tool schemas", None, "chars"),
                _row("skills", None, note="not extracted yet"),
            ],
        ),
        DetailSection(
            title="Response service",
            accent=None,
            rows=[
                _row("service tier", w.c("service_tier")),
                _row("inference geo", w.c("inference_geo")),
                _row("speed", w.c("speed")),
                _row("status", w.status),
                _row("request-id", w.request_id),
                _row("retries", None),
                _row("streaming stalls", None),
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
                _row("client", w.ip),
                _row("device", w.c("device_id")),
                _row("TLS", "intercepted by the proxy"),
                _row("upstream connect", None, "ms", note="not recorded by the proxy"),
                _row("bytes up", d.bytesUp, "bytes"),
                _row("bytes down", d.bytesDown, "bytes"),
                _row("credential", w.key_hash, tone="muted", note="key_hash (refreshes; not identity)"),
            ],
        ),
        DetailSection(
            title="Cross-check & posture",
            accent=None,
            rows=[
                _row("analytics match", None, note="analytics aren't stored"),
                _row("cost agreement", None, note="no price table"),
                _row("ttft (Claude Code)", None, "ms"),
                _row("ttft (proxy)", k.proxyTtftMs, "ms"),
                _row("permissionMode", None),
                _row("sandbox", None),
                _row(
                    "content retention",
                    "full content (experimentation mode)" if d.contentRetained else "shape only",
                    tone="muted",
                ),
            ],
        ),
    ]
    if isinstance(w.c("client_metadata"), dict):
        sections.append(_codex_section(w))
    proxy: list[TimingPhase] = []
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
    return RequestDetails(
        composition=t,
        sections=sections,
        tool=_tool_details(w),
        timing=Timing(proxy=proxy, client=[]),
        events=[],
        fields=_fields(w),
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
