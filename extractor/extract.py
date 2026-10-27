#!/usr/bin/env python3
"""
extractor — turn captured proxy telemetry (events.ndjson) into normalized,
cross-provider usage records focused on TOOL VISIBILITY.

Input:  an NDJSON capture from the observer (one event per line, grouped by
        correlation_id). Request bodies are plaintext JSON; response bodies are
        base64 of raw wire bytes (possibly gzip/br/zstd) — decoded best-effort.

Output: one JSON record per intercepted LLM API transaction, in a COMMON schema
        that is the same across providers, plus a `provider_raw` blob holding
        provider-specific fields we don't (yet) normalize.

The common schema (v0) — see extractor/SCHEMA.md:
  {
    "schema_version": "0",
    "provider": "anthropic",
    "endpoint": "/v1/messages",
    "ts": <unix_ns of request_headers>,
    "correlation_id": "...",
    "client": { "user_agent": "...", "app": "claude-code"|... },
    "model": "claude-...",
    "stream": true,
    "status": 200,
    "tools_available": [ {"name","server","kind"} ],   # declared this request
    "tool_calls": [ {"name","server","kind","id","input","ts_seq"} ],  # model-issued
    "usage": { "input_tokens", "output_tokens", ... } | null,
    "stop_reason": "..." | null,
    "provider_raw": { ...provider-specific... }
  }

Tool attribution: a tool name like `mcp__playwright__browser_navigate` is split
into server=`playwright`, tool=`browser_navigate`, kind=`mcp`. Builtins (Bash,
Edit, ...) get server=null, kind=`builtin`. Anthropic server tools (web_search_*)
get kind=`server`.
"""
import argparse
import base64
import collections
import gzip
import json
import sys
import zlib

SCHEMA_VERSION = "0"


def iter_transactions(path):
    by = collections.defaultdict(list)
    order = []
    with open(path) as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            e = json.loads(line)
            cid = e["correlation_id"]
            if cid not in by:
                order.append(cid)
            by[cid].append(e)
    for cid in order:
        yield cid, by[cid]


def phase_body_text(ev, phase):
    """Reassemble a plaintext body (request side)."""
    return "".join(x.get("body_full", "") for x in ev if x["phase"] == phase)


def phase_body_bytes(ev, phase):
    """Reassemble raw bytes from base64 chunks (response side), best-effort
    decompress by content-encoding."""
    raw = b""
    for x in ev:
        if x["phase"] == phase and x.get("body_b64"):
            try:
                raw += base64.b64decode(x["body_b64"])
            except Exception:
                pass
    return raw


def decompress(raw, encoding):
    if not raw:
        return b""
    enc = (encoding or "").lower()
    try:
        if enc == "gzip":
            return gzip.decompress(raw)
        if enc in ("deflate", "zlib"):
            return zlib.decompress(raw)
        if enc == "br":
            import brotli  # optional
            return brotli.decompress(raw)
        if enc == "zstd":
            import zstandard  # optional
            return zstandard.ZstdDecompressor().decompress(raw)
    except Exception:
        return raw  # return as-is; caller handles
    return raw


def headers_for(ev, phase):
    h = [x for x in ev if x["phase"] == phase]
    return h[0].get("headers", {}) if h else {}


def meta_for(ev):
    rh = [x for x in ev if x["phase"] == "request_headers"]
    sh = [x for x in ev if x["phase"] == "response_headers"]
    rhx = rh[0] if rh else {}
    return {
        "ts": rhx.get("timestamp_ns"),
        "method": rhx.get("method"),
        "path": rhx.get("path"),
        "status": (sh[0].get("status") if sh else None),
    }


def classify_tool(name):
    """Return (server, tool, kind)."""
    if not name:
        return (None, name, "unknown")
    if name.startswith("mcp__"):
        parts = name.split("__")
        # mcp__<server>__<tool...>
        if len(parts) >= 3:
            return (parts[1], "__".join(parts[2:]), "mcp")
        return (None, name, "mcp")
    # Anthropic server-side tools look like web_search / web_fetch / bash_* etc.
    if name in ("web_search", "web_fetch", "code_execution"):
        return (None, name, "server")
    return (None, name, "builtin")


def client_name(user_agent):
    ua = (user_agent or "").lower()
    if "claude-code" in ua or "claude code" in ua:
        return "claude-code"
    if "anthropic" in ua:
        return "anthropic-sdk"
    if "curl" in ua:
        return "curl"
    return ua.split("/")[0] if ua else None


def parse_sse(text):
    import re
    for block in re.split(r"\n\n+", text):
        etype = None
        data = []
        for ln in block.splitlines():
            if ln.startswith("event:"):
                etype = ln[6:].strip()
            elif ln.startswith("data:"):
                data.append(ln[5:].strip())
        if data:
            try:
                yield etype, json.loads("".join(data))
            except Exception:
                yield etype, None


def tool_calls_from_response(resp_text):
    """Model-issued tool calls reconstructed from a streamed SSE response."""
    calls = []
    for etype, d in parse_sse(resp_text):
        if d and d.get("type") == "content_block_start":
            cb = d.get("content_block", {})
            if cb.get("type") in ("tool_use", "server_tool_use"):
                server, tool, kind = classify_tool(cb.get("name"))
                calls.append({
                    "name": cb.get("name"), "server": server, "tool": tool,
                    "kind": kind, "id": cb.get("id"), "input": None,
                    "source": "response_stream",
                })
    return calls


def usage_from_response(resp_text):
    usage, model, stop = {}, None, None
    for etype, d in parse_sse(resp_text):
        if not d:
            continue
        if d.get("type") == "message_start":
            m = d.get("message", {})
            model = m.get("model", model)
            usage.update(m.get("usage", {}))
        if d.get("type") == "message_delta":
            usage.update(d.get("usage", {}))
            stop = d.get("delta", {}).get("stop_reason", stop)
    return model, (usage or None), stop


def extract_anthropic(cid, ev):
    m = meta_for(ev)
    req_text = phase_body_text(ev, "request_body_chunk")
    try:
        rj = json.loads(req_text)
    except Exception:
        return None  # not a JSON messages request (e.g. telemetry batch)

    req_hdrs = headers_for(ev, "request_headers")
    resp_hdrs = headers_for(ev, "response_headers")

    # Tools available (declared on the request)
    tools_available = []
    for t in rj.get("tools", []):
        server, tool, kind = classify_tool(t.get("name"))
        tools_available.append({"name": t.get("name"), "server": server,
                                "tool": tool, "kind": kind})

    # Model-issued tool calls: prefer the response stream; fall back to the
    # LAST assistant turn in the request history (the most recent model output
    # echoed back). Both are captured so the extractor is robust to the
    # response-body corruption in older captures.
    resp_bytes = phase_body_bytes(ev, "response_body_chunk")
    resp_text = decompress(resp_bytes, resp_hdrs.get("content-encoding")).decode("utf-8", "replace") if resp_bytes else ""
    tool_calls = tool_calls_from_response(resp_text)

    # Full tool-use history (every call made across the whole conversation),
    # with inputs — this is the richest tool-visibility signal.
    history = []
    for msg in rj.get("messages", []):
        content = msg.get("content")
        if not isinstance(content, list):
            continue
        for blk in content:
            if isinstance(blk, dict) and blk.get("type") == "tool_use":
                server, tool, kind = classify_tool(blk.get("name"))
                history.append({
                    "name": blk.get("name"), "server": server, "tool": tool,
                    "kind": kind, "id": blk.get("id"), "input": blk.get("input"),
                    "source": "request_history",
                })

    r_model, r_usage, r_stop = usage_from_response(resp_text)

    return {
        "schema_version": SCHEMA_VERSION,
        "provider": "anthropic",
        "endpoint": (m["path"] or "").split("?")[0],
        "ts": m["ts"],
        "correlation_id": cid,
        "client": {
            "user_agent": req_hdrs.get("user-agent"),
            "app": client_name(req_hdrs.get("user-agent")),
        },
        "model": rj.get("model") or r_model,
        "stream": bool(rj.get("stream")),
        "status": m["status"],
        "tools_available": tools_available,
        "tool_calls": tool_calls,            # model's NEW calls this turn
        "tool_use_history": history,         # all calls across the conversation
        "usage": r_usage,
        "stop_reason": r_stop,
        "provider_raw": {
            "metadata": rj.get("metadata"),
            "anthropic_beta": req_hdrs.get("anthropic-beta"),
            "n_messages": len(rj.get("messages", [])),
            "n_system_blocks": len(rj.get("system", []) if isinstance(rj.get("system"), list) else []),
            "max_tokens": rj.get("max_tokens"),
        },
    }


def is_llm_request(ev):
    m = meta_for(ev)
    return (m["path"] or "").startswith("/v1/messages") and \
           "count_tokens" not in (m["path"] or "")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("capture", nargs="?", default="data/events.ndjson")
    ap.add_argument("-o", "--out", help="write records as NDJSON here")
    args = ap.parse_args()

    out = open(args.out, "w") if args.out else None
    n = 0
    for cid, ev in iter_transactions(args.capture):
        if not is_llm_request(ev):
            continue
        rec = extract_anthropic(cid, ev)
        if rec is None:
            continue
        n += 1
        line = json.dumps(rec)
        if out:
            out.write(line + "\n")
        else:
            sys.stdout.write(line + "\n")
    if out:
        out.close()
    sys.stderr.write(f"extracted {n} records\n")


if __name__ == "__main__":
    main()
