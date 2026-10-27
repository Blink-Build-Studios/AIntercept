#!/usr/bin/env python3
"""Sanitize extracted records into committable example fixtures.

The goal of a fixture is to show the SHAPE of the data (schema, tool
attribution, which fields exist) WITHOUT leaking prompt content, file paths,
credentials, or user/device identity. We therefore:

  - drop tool_call `input` and any free-text content entirely (keep names only)
  - null out provider_raw.metadata (contains device/account/session IDs)
  - redact user-agent to the app family + major version
  - keep structural/aggregate fields: provider, endpoint, model, tool names,
    server attribution, kind, counts, usage shape

Fixtures are for schema design and regression tests, not for data analysis.
"""
import collections
import json
import re
import sys


def redact_ua(ua):
    if not ua:
        return None
    m = re.match(r"([a-zA-Z0-9_-]+)/(\d+)", ua)
    return f"{m.group(1)}/{m.group(2)}.x" if m else "redacted"


def sanitize(rec):
    # Collapse tool history to name/server/kind only (no inputs, no ids).
    def slim(t):
        return {"name": t.get("name"), "server": t.get("server"), "kind": t.get("kind")}

    hist = [slim(t) for t in rec.get("tool_use_history", [])]
    calls = [slim(t) for t in rec.get("tool_calls", [])]

    pr = dict(rec.get("provider_raw", {}))
    pr["metadata"] = None  # device/account/session identity removed
    return {
        "schema_version": rec.get("schema_version"),
        "provider": rec.get("provider"),
        "endpoint": rec.get("endpoint"),
        "correlation_id": "REDACTED",
        "client": {
            "user_agent": redact_ua(rec.get("client", {}).get("user_agent")),
            "app": rec.get("client", {}).get("app"),
        },
        "model": rec.get("model"),
        "stream": rec.get("stream"),
        "status": rec.get("status"),
        "tools_available": rec.get("tools_available"),  # names/servers only, no secrets
        "tool_calls": calls,
        "tool_use_history": hist,
        "usage": rec.get("usage"),  # token counts only, no content
        "stop_reason": rec.get("stop_reason"),
        "provider_raw": pr,
    }


def main():
    src = sys.argv[1] if len(sys.argv) > 1 else "data/extracted.ndjson"
    recs = [json.loads(l) for l in open(src)]
    # Pick a small, diverse slice: a few models, with and without MCP tools.
    picked = []
    seen_models = collections.Counter()
    for r in recs:
        has_mcp = any(t.get("kind") == "mcp" for t in r.get("tool_use_history", []))
        key = (r.get("model"), has_mcp)
        if seen_models[key] < 1:
            seen_models[key] += 1
            picked.append(sanitize(r))
        if len(picked) >= 8:
            break
    for p in picked:
        print(json.dumps(p))


if __name__ == "__main__":
    main()
