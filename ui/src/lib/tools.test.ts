import { describe, expect, it } from "vitest";
import { collapseUnused, sourceRows, toolLine, toolSource, toolsOffered } from "./tools";

// CompTurns → "Work line vocabulary · content-free" (handoff §11S): tool names and counts.
describe("toolLine (handoff §11S, CompTurns content-free vocabulary)", () => {
  it("tool names with ×N, most used first", () => {
    expect(toolLine({ Bash: 6, Read: 31, Grep: 2 })).toBe("Read ×31 · Bash ×6 · Grep ×2");
    expect(toolLine({ Edit: 1 })).toBe("Edit ×1");
  });

  it("ties keep a stable order: by name", () => {
    expect(toolLine({ Write: 2, Bash: 2 })).toBe("Bash ×2 · Write ×2");
  });

  it("Task / spawn_agent calls read N subagents, after the tools", () => {
    expect(toolLine({ Read: 4, Edit: 2, Bash: 3, Task: 1 })).toBe("Read ×4 · Bash ×3 · Edit ×2 · 1 subagent");
    expect(toolLine({ Bash: 488, Read: 402, Task: 6 })).toBe("Bash ×488 · Read ×402 · 6 subagents");
    expect(toolLine({ spawn_agent: 2 })).toBe("2 subagents");
  });

  it("MCP calls by server, mcp:<server> ×N, last", () => {
    expect(toolLine({ mcp__linear__list_issues: 2, mcp__linear__get_issue: 1, Read: 61, Edit: 12 })).toBe("Read ×61 · Edit ×12 · mcp:linear ×3");
    expect(toolLine({ Bash: 12, Edit: 8, Read: 31, Task: 2, mcp__linear__list_issues: 3 })).toBe("Read ×31 · Bash ×12 · Edit ×8 · 2 subagents · mcp:linear ×3");
  });

  it("Codex names as sent", () => {
    expect(toolLine({ exec_command: 141, apply_patch: 18, mcp__linear__list_issues: 9 })).toBe("exec_command ×141 · apply_patch ×18 · mcp:linear ×9");
  });

  it("no tools: a turn whose steps called none", () => {
    expect(toolLine({})).toBe("no tools");
    expect(toolLine({ Read: 0 })).toBe("no tools");
  });

  it("at most `top` names; the rest are left out (subagents and MCP stay)", () => {
    expect(toolLine({ Bash: 488, Read: 402, Edit: 171, Grep: 96, Glob: 3, Task: 6 }, 4)).toBe("Bash ×488 · Read ×402 · Edit ×171 · Grep ×96 · 6 subagents");
  });

  it("counts are grouped like every count (1,630)", () => {
    expect(toolLine({ Bash: 2104, Read: 1630, Edit: 712 })).toBe("Bash ×2,104 · Read ×1,630 · Edit ×712");
  });
});

describe("toolSource: Built-in or mcp:<server>", () => {
  it("splits mcp__<server>__<tool>", () => {
    expect(toolSource("mcp__linear__list_issues")).toEqual({ source: "mcp:linear", tool: "list_issues" });
    expect(toolSource("mcp__claude_ai_Gmail__search")).toEqual({ source: "mcp:claude_ai_Gmail", tool: "search" });
    expect(toolSource("Bash")).toEqual({ source: "Built-in", tool: "Bash" });
  });
});

describe("toolsOffered: the ToolsOfferedUsed rows", () => {
  it("one row per source, Built-in first, then servers by calls; unused servers sink", () => {
    const rows = toolsOffered(
      ["Bash", "Read", "Edit", "Grep", "Glob", "mcp__linear__list_issues", "mcp__linear__get_issue", "mcp__linear__create_issue", "mcp__sentry__a", "mcp__sentry__b"],
      { Bash: 488, Read: 402, Edit: 171, Grep: 96, mcp__linear__list_issues: 2, mcp__linear__get_issue: 1 },
    );
    expect(rows).toEqual([
      { source: "Built-in", offered: 5, used: 4, calls: "Bash 488 · Read 402 · Edit 171 · Grep 96" },
      { source: "mcp:linear", offered: 3, used: 2, calls: "list_issues 2 · get_issue 1" },
      { source: "mcp:sentry", offered: 2, used: 0, calls: null },
    ]);
  });

  it("a tool called but not in any list still counts as offered", () => {
    expect(toolsOffered([], { Bash: 1 })).toEqual([{ source: "Built-in", offered: 1, used: 1, calls: "Bash 1" }]);
  });

  it("calls list the top 4, then …", () => {
    const [row] = toolsOffered([], { A: 5, B: 4, C: 3, D: 2, E: 1 });
    expect(row?.calls).toBe("A 5 · B 4 · C 3 · D 2 · …");
  });
});

describe("collapseUnused: servers never called fold into `N more MCP servers`", () => {
  const row = (source: string, offered: number, used = 0) => ({ source, offered, used, calls: used ? "x 1" : null });
  it("keeps up to `max` rows; the unused servers past it fold into one", () => {
    const rows = [row("Built-in", 23, 9), row("mcp:linear", 41, 2), row("mcp:github", 38, 1), row("mcp:sentry", 22), row("mcp:a", 100), row("mcp:b", 180)];
    expect(collapseUnused(rows, 5).map((r) => [r.source, r.offered, r.used])).toEqual([
      ["Built-in", 23, 9],
      ["mcp:linear", 41, 2],
      ["mcp:github", 38, 1],
      ["mcp:sentry", 22, 0],
      ["2 more MCP servers", 280, 0],
    ]);
  });
  it("leaves a short list alone", () => {
    const rows = [row("Built-in", 3, 1), row("mcp:sentry", 22)];
    expect(collapseUnused(rows, 5)).toEqual(rows);
  });
});

describe("sourceRows: the API's tool sources as ToolsOfferedUsed rows (handoff §11S)", () => {
  it("calls read as the top 4 with counts, then …; none reads null; past 5 rows unused servers fold", () => {
    const rows = sourceRows(
      [
        { source: "Built-in", offered: 23, used: 9, calls: { Bash: 488, Read: 402, Edit: 171, Grep: 96, Glob: 31 } },
        { source: "mcp:linear", offered: 41, used: 2, calls: { list_issues: 2, get_issue: 1 } },
        { source: "mcp:github", offered: 38, used: 1, calls: { get_pull_request: 1 } },
        { source: "mcp:sentry", offered: 22, used: 0, calls: {} },
        { source: "mcp:a", offered: 100, used: 0, calls: {} },
        { source: "mcp:b", offered: 180, used: 0, calls: {} },
      ],
      5,
    );
    expect(rows.map((r) => [r.source, r.offered, r.used, r.calls])).toEqual([
      ["Built-in", 23, 9, "Bash 488 · Read 402 · Edit 171 · Grep 96 · …"],
      ["mcp:linear", 41, 2, "list_issues 2 · get_issue 1"],
      ["mcp:github", 38, 1, "get_pull_request 1"],
      ["mcp:sentry", 22, 0, null],
      ["2 more MCP servers", 280, 0, null],
    ]);
  });

  it("an unknown offered count stays unknown", () => {
    expect(sourceRows([{ source: "Built-in", offered: null, used: 1, calls: { Bash: 2 } }], 5)[0]?.offered).toBeNull();
  });
});
