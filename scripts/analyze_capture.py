#!/usr/bin/env python3
"""Analyze a captured events.ndjson to map provider wire format and extract
every tool invocation. Dev analysis tool — operates on raw capture, prints a
structured report. Does not write anything sensitive to the repo."""
import json, collections, re, sys

PATH = sys.argv[1] if len(sys.argv) > 1 else "data/events.ndjson"

def load():
    by = collections.defaultdict(list)
    with open(PATH) as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            e = json.loads(line)
            by[e["correlation_id"]].append(e)
    return by

def reassemble(ev, phase):
    return "".join(x.get("body_full", "") for x in ev if x["phase"] == phase)

def req_meta(ev):
    h = [x for x in ev if x["phase"] == "request_headers"]
    rh = h[0] if h else {}
    s = [x for x in ev if x["phase"] == "response_headers"]
    return rh.get("method"), rh.get("path"), (s[0].get("status") if s else None)

def parse_sse(text):
    """Yield (event_type, data_obj) for each SSE frame."""
    for block in re.split(r"\n\n+", text):
        etype = None
        data_lines = []
        for ln in block.splitlines():
            if ln.startswith("event:"):
                etype = ln[6:].strip()
            elif ln.startswith("data:"):
                data_lines.append(ln[5:].strip())
        if data_lines:
            raw = "".join(data_lines)
            try:
                yield etype, json.loads(raw)
            except Exception:
                yield etype, None

def sse_tool_calls(resp_text):
    """Reconstruct tool_use blocks from a streamed SSE response."""
    calls = []  # (type, name, id)
    for etype, d in parse_sse(resp_text):
        if not d:
            continue
        if d.get("type") == "content_block_start":
            cb = d.get("content_block", {})
            if cb.get("type") in ("tool_use", "server_tool_use"):
                calls.append((cb["type"], cb.get("name"), cb.get("id", "")))
    return calls

def final_usage(resp_text):
    usage = {}
    model = None
    stop = None
    for etype, d in parse_sse(resp_text):
        if not d:
            continue
        if d.get("type") == "message_start":
            m = d.get("message", {})
            model = m.get("model")
            usage.update(m.get("usage", {}))
        if d.get("type") == "message_delta":
            usage.update(d.get("usage", {}))
            stop = d.get("delta", {}).get("stop_reason", stop)
    return model, usage, stop

def main():
    by = load()
    print(f"loaded {sum(len(v) for v in by.values())} events across {len(by)} transactions\n")

    # ---- 1. Available tools (declared per request) ----
    tool_decls = collections.Counter()
    tool_types = {}
    mcp_servers = collections.Counter()
    model_counts = collections.Counter()

    # ---- 2. Tool INVOCATIONS: from response SSE (new calls) + request history ----
    sse_invocations = collections.Counter()
    hist_invocations = collections.Counter()  # from prior-turn tool_use in requests
    tool_result_count = 0
    usage_totals = collections.Counter()
    stop_reasons = collections.Counter()

    for c, ev in by.items():
        method, path, status = req_meta(ev)
        if not (path or "").startswith("/v1/messages"):
            continue
        req = reassemble(ev, "request_body_chunk")
        resp = reassemble(ev, "response_body_chunk")
        try:
            rj = json.loads(req)
        except Exception:
            continue

        model_counts[rj.get("model")] += 1
        for t in rj.get("tools", []):
            nm = t.get("name", "?")
            tool_decls[nm] += 1
            tool_types[nm] = t.get("type", "custom")
            if nm.startswith("mcp__"):
                parts = nm.split("__")
                if len(parts) >= 3:
                    mcp_servers["__".join(parts[:2])] += 1

        # Tool calls the MODEL made, from the streamed response
        for (ttype, name, _id) in sse_tool_calls(resp):
            sse_invocations[name] += 1

        # Tool calls present in the conversation history (requests echo them)
        for msg in rj.get("messages", []):
            content = msg.get("content")
            if isinstance(content, list):
                for blk in content:
                    if isinstance(blk, dict):
                        if blk.get("type") == "tool_use":
                            hist_invocations[blk.get("name", "?")] += 1
                        elif blk.get("type") == "tool_result":
                            # counted, name is only in the paired tool_use
                            globals()  # noop

        model, usage, stop = final_usage(resp)
        for k, v in usage.items():
            if isinstance(v, int):
                usage_totals[k] += v
        if stop:
            stop_reasons[stop] += 1

    print("=" * 70)
    print("MODELS USED (by /v1/messages request count)")
    for m, n in model_counts.most_common():
        print(f"  {n:>4}  {m}")

    print("\n" + "=" * 70)
    print("TOOLS DECLARED AVAILABLE (how many requests offered each tool)")
    for nm, n in tool_decls.most_common():
        kind = "MCP" if nm.startswith("mcp__") else "builtin"
        print(f"  {n:>4}  [{kind:<7}] {nm}")

    print("\n" + "=" * 70)
    print("MCP SERVERS detected (from mcp__<server>__<tool> prefixes)")
    for s, n in mcp_servers.most_common():
        print(f"  {n:>4}  {s}")

    print("\n" + "=" * 70)
    print("TOOL INVOCATIONS BY THE MODEL (reconstructed from response SSE)")
    if sse_invocations:
        for nm, n in sse_invocations.most_common():
            print(f"  {n:>4}  {nm}")
    else:
        print("  (none found in responses)")

    print("\n" + "=" * 70)
    print("TOOL USES SEEN IN CONVERSATION HISTORY (request messages[].tool_use)")
    for nm, n in hist_invocations.most_common(40):
        print(f"  {n:>4}  {nm}")

    print("\n" + "=" * 70)
    print("TOKEN USAGE TOTALS (summed across all /v1/messages responses)")
    for k, v in usage_totals.most_common():
        print(f"  {k}: {v:,}")
    print("STOP REASONS:", dict(stop_reasons))

if __name__ == "__main__":
    main()
