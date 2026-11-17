"""The tools a range's requests offered against the ones its calls used (handoff §11S, ToolsOfferedUsed),
from its key rows (docs/PIPELINE.md §5.2 session_minute_keys): `offer` and `offer_main`, the tool lists its
requests carried (a hash each; the lists themselves are tool_lists rows, a few per session), and
`call_builtin` and `call_mcp`, the tools its calls named. Pure: the readers pass the rows."""

import json
import re
from collections import Counter, defaultdict
from collections.abc import Iterable, Mapping, Sequence

from aintercept_api.models.work import ToolsInRange, ToolSource

KINDS = ["offer", "offer_main", "call_builtin", "call_mcp"]
"""The key kinds tools_in_range reads."""

LISTS = "SELECT hash, names FROM tool_lists WHERE hash = ANY(%(hashes)s)"
"""The tool lists a range's `offer` keys name."""

_MCP = re.compile(r"^mcp__(.+?)__(.+)$")
BUILT_IN = "Built-in"


def source(name: str) -> tuple[str, str]:
    """A tool name as the request sends it (`mcp__linear__list_issues`) as its source and bare name."""
    m = _MCP.match(name)
    return (f"mcp:{m[1]}", m[2]) if m else (BUILT_IN, name)


def hashes(keys: Iterable[tuple[str, str, int]]) -> list[str]:
    """The tool lists the key rows name (to read from tool_lists)."""
    return sorted({k for kind, k, _ in keys if kind == "offer"})


def tools_in_range(keys: Sequence[tuple[str, str, int]], lists: Mapping[str, Sequence[str]]) -> ToolsInRange:
    """The range's tools by source: offered (its lists' names; a called tool no list names counts too, once
    any list is known), used (distinct tools called) and calls by tool."""
    offered: dict[str, set[str]] = defaultdict(set)
    calls: dict[str, Counter[str]] = defaultdict(Counter)
    by_name: Counter[str] = Counter()
    have_list = False
    main: set[str] = set()
    for kind, key, n in keys:
        if kind == "offer" and key in lists:
            have_list = True
            for name in lists[key]:
                src, tool = source(name)
                offered[src].add(tool)
        elif kind == "offer_main":
            main.add(key)
        elif kind == "call_builtin":
            calls[BUILT_IN][key] += n
            by_name[key] += n
        elif kind == "call_mcp":
            server, tool = json.loads(key)
            calls[f"mcp:{server}"][tool] += n
            by_name[f"mcp__{server}__{tool}"] += n
    for src, c in calls.items():
        if have_list:
            offered[src].update(c)

    def order(src: str) -> tuple[int, int, str]:
        return (0 if src == BUILT_IN else 1, -sum(calls[src].values()), src)

    sources = [
        ToolSource(
            source=src,
            offered=len(offered[src]) if have_list else None,
            used=len(calls[src]),
            calls=dict(sorted(calls[src].items(), key=lambda x: (-x[1], x[0]))),
        )
        for src in sorted(set(offered) | set(calls), key=order)
    ]
    return ToolsInRange(
        sources=sources,
        offered=sum(len(v) for v in offered.values()) if have_list else None,
        used=sum(len(c) for c in calls.values()),
        calls=sum(by_name.values()),
        byName=dict(sorted(by_name.items(), key=lambda x: (-x[1], x[0]))),
        listChanges=len(main) - 1 if main else None,
    )
