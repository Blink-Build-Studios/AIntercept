#!/usr/bin/env python3
"""Cross-provider tool-visibility report from extracted records (extract.py -o).

Answers: which providers/apps used which MCP servers and which tools, how often,
on which models. This is the queryable common-schema view."""
import collections
import json
import sys

recs = [json.loads(l) for l in open(sys.argv[1] if len(sys.argv) > 1 else "data/extracted.ndjson")]

by_server = collections.Counter()
by_tool = collections.Counter()
server_tools = collections.defaultdict(collections.Counter)
by_app = collections.Counter()
by_model = collections.Counter()
kind_counts = collections.Counter()
app_servers = collections.defaultdict(set)

for r in recs:
    app = r.get("client", {}).get("app") or "?"
    by_app[app] += 1
    by_model[r.get("model")] += 1
    # dedupe tool calls within a transaction's history so repeated context
    # replays don't multiply counts across turns of the same conversation.
    seen = set()
    for t in r.get("tool_use_history", []):
        key = (t.get("id"), t.get("name"))
        if key in seen:
            continue
        seen.add(key)
        name = t.get("name")
        server = t.get("server") or "(builtin/server)"
        kind = t.get("kind")
        by_tool[name] += 1
        by_server[server] += 1
        kind_counts[kind] += 1
        server_tools[server][name] += 1
        if t.get("kind") == "mcp":
            app_servers[app].add(server)

print(f"records: {len(recs)}\n")
print("APPS (by transaction count):")
for a, n in by_app.most_common():
    print(f"  {n:>4}  {a}")

print("\nMODELS:")
for m, n in by_model.most_common():
    print(f"  {n:>4}  {m}")

print("\nTOOL KINDS (unique invocations):")
for k, n in kind_counts.most_common():
    print(f"  {n:>6}  {k}")

print("\nSERVERS → TOOLS (every tool seen per server):")
for server, tools in sorted(server_tools.items(), key=lambda kv: -sum(kv[1].values())):
    print(f"  {server}  ({sum(tools.values())} calls, {len(tools)} distinct tools)")
    for tool, n in tools.most_common():
        print(f"      {n:>5}  {tool}")

print("\nMCP SERVERS USED PER APP:")
for app, servers in app_servers.items():
    print(f"  {app}: {sorted(servers)}")
