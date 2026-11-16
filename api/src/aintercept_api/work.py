"""Work (handoff §11R): the rollups store a turn's, a step's and a session's work as TurnWork's JSON
(aintercept/internal/refresh/work.go builds it in SQL). The pages add stored works together: a group's
from its steps', a subagent's from its steps', a repo's from its sessions'. Adding is the only thing done
here; what counts as which kind is decided once, in the SQL."""

from collections import Counter
from collections.abc import Iterable, Mapping
from typing import Any

from aintercept_api.models.work import CommandRuns, McpUse, SubagentSpawn, TurnWork, WebUse

EMPTY: dict[str, Any] = {
    "reads": 0,
    "readFiles": None,
    "readTypes": {},
    "edits": 0,
    "editedFiles": None,
    "editTypes": {},
    "linesAdded": None,
    "linesRemoved": None,
    "commands": [],
    "subagents": [],
    "web": {"searches": 0, "fetches": 0},
    "mcp": [],
    "otherTools": 0,
    "commits": 0,
    "pushes": 0,
    "prs": [],
}


def _opt_sum(a: int | None, b: int | None) -> int | None:
    return b if a is None else a if b is None else a + b


def add(works: Iterable[Mapping[str, Any] | None]) -> dict[str, Any]:
    """The sum of stored works (None: nothing). Distinct file counts add up (each work's files are its
    own: a group's, a session's), a command's last exit is the last work's that ran it."""
    out: dict[str, Any] = {k: v for k, v in EMPTY.items()}
    read_types: Counter[str] = Counter()
    edit_types: Counter[str] = Counter()
    commands: dict[str | None, dict[str, Any]] = {}
    subagents: Counter[str | None] = Counter()
    mcp: Counter[str] = Counter()
    prs: set[int] = set()
    web = Counter[str]()
    for w in works:
        if not w:
            continue
        for k in ("reads", "edits", "otherTools", "commits", "pushes"):
            out[k] += w.get(k) or 0
        for k in ("readFiles", "editedFiles", "linesAdded", "linesRemoved"):
            out[k] = _opt_sum(out[k], w.get(k))
        read_types.update(w.get("readTypes") or {})
        edit_types.update(w.get("editTypes") or {})
        for c in w.get("commands") or []:
            got = commands.setdefault(c["program"], {"program": c["program"], "runs": 0, "failed": 0, "lastExit": None})
            got["runs"] += c["runs"]
            got["failed"] += c["failed"]
            if c.get("lastExit") is not None:
                got["lastExit"] = c["lastExit"]
        for s in w.get("subagents") or []:
            subagents[s["name"]] += s["count"]
        for m in w.get("mcp") or []:
            mcp[m["server"]] += m["calls"]
        web.update(w.get("web") or {})
        prs.update(w.get("prs") or [])
    out["readTypes"] = dict(sorted(read_types.items(), key=lambda x: (-x[1], x[0])))
    out["editTypes"] = dict(sorted(edit_types.items(), key=lambda x: (-x[1], x[0])))
    out["commands"] = sorted(commands.values(), key=lambda c: (-c["runs"], c["program"] or ""))
    out["subagents"] = [
        {"name": k, "count": n} for k, n in sorted(subagents.items(), key=lambda x: (-x[1], x[0] or ""))
    ]
    out["web"] = {"searches": web["searches"], "fetches": web["fetches"]}
    out["mcp"] = [{"server": k, "calls": n} for k, n in sorted(mcp.items(), key=lambda x: (-x[1], x[0]))]
    out["prs"] = sorted(prs)
    return out


KINDS = [
    "w:read",
    "w:read_file",
    "w:edit",
    "w:edit_file",
    "w:lines_added",
    "w:lines_removed",
    "w:run",
    "w:run_failed",
    "w:spawn",
    "w:web",
    "w:mcp",
    "w:tool",
    "w:commit",
    "w:push",
    "w:pr",
]
"""The session key kinds work is kept in, per minute and hour (refresh/work.go workItems): each is
(kind, key, n), key '' where the kind has none."""


def from_keys(rows: Iterable[tuple[str, str, int]]) -> dict[str, Any]:
    """A work from its key rows (session_minute_keys, session_keys: refresh/work.go workJSON, the same
    rules): counts summed, files counted distinct (their keys are hashed paths)."""
    n: dict[str, Counter[str]] = {k: Counter() for k in KINDS}
    for kind, key, count in rows:
        if kind in n:
            n[kind][key] += count
    read_types = Counter({k: v for k, v in n["w:read"].items() if k})
    edit_types = Counter({k: v for k, v in n["w:edit"].items() if k})
    runs = n["w:run"]
    out = {
        **EMPTY,
        "reads": sum(n["w:read"].values()),
        "readFiles": len(n["w:read_file"]) if n["w:read_file"] else None,
        "readTypes": dict(read_types),
        "edits": sum(n["w:edit"].values()),
        "editedFiles": len(n["w:edit_file"]) if n["w:edit_file"] else None,
        "editTypes": dict(edit_types),
        "linesAdded": sum(n["w:lines_added"].values()) if n["w:lines_added"] else None,
        "linesRemoved": sum(n["w:lines_removed"].values()) if n["w:lines_removed"] else None,
        "commands": [
            {"program": k or None, "runs": v, "failed": n["w:run_failed"][k], "lastExit": None} for k, v in runs.items()
        ],
        "subagents": [{"name": k or None, "count": v} for k, v in n["w:spawn"].items()],
        "web": {"searches": n["w:web"]["search"], "fetches": n["w:web"]["fetch"]},
        "mcp": [{"server": k, "calls": v} for k, v in n["w:mcp"].items()],
        "otherTools": sum(n["w:tool"].values()),
        "commits": sum(n["w:commit"].values()),
        "pushes": sum(n["w:push"].values()),
        "prs": sorted(int(k) for k in n["w:pr"] if k.isdigit()),
    }
    return add([out])


def turn_work(w: Mapping[str, Any] | None) -> TurnWork:
    """A stored work as the API's TurnWork (None: nothing done)."""
    w = add([w])
    return TurnWork(
        reads=w["reads"],
        readFiles=w["readFiles"],
        readTypes=w["readTypes"],
        edits=w["edits"],
        editedFiles=w["editedFiles"],
        editTypes=w["editTypes"],
        linesAdded=w["linesAdded"],
        linesRemoved=w["linesRemoved"],
        commands=[CommandRuns(**c) for c in w["commands"]],
        subagents=[SubagentSpawn(**s) for s in w["subagents"]],
        web=WebUse(**w["web"]),
        mcp=[McpUse(**m) for m in w["mcp"]],
        otherTools=w["otherTools"],
        commits=w["commits"],
        pushes=w["pushes"],
        prs=w["prs"],
    )


def edits_and_other(w: Mapping[str, Any]) -> tuple[int, int]:
    """The Work lane's two stacks: edits, and every other kind of work."""
    other = (
        w["reads"]
        + sum(c["runs"] for c in w["commands"])
        + w["web"]["searches"]
        + w["web"]["fetches"]
        + sum(m["calls"] for m in w["mcp"])
        + sum(s["count"] for s in w["subagents"])
        + w["otherTools"]
    )
    return w["edits"], other
