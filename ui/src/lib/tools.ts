// Tool names and counts (handoff §11S, CompTurns "Work line vocabulary · content-free"): what a
// turn, a session or a person did when no content was recorded, exactly as the requests carry
// it. Nothing is translated into verbs; Task / spawn_agent calls are subagents, MCP calls are
// grouped by server (`mcp__<server>__<tool>` → `mcp:<server>`).
import { formatCount } from "./format";

/** Calls per tool name, as sent (`Bash`, `exec_command`, `mcp__linear__list_issues`). */
export type ToolCounts = Readonly<Record<string, number>>;

/** The tools that spawn a subagent: Claude Code's Task, Codex's spawn_agent. */
const SPAWN = new Set(["Task", "spawn_agent"]);
const MCP = /^mcp__(.+?)__(.+)$/;

/** Where a tool comes from: `Built-in` or `mcp:<server>`, and its own name. */
export function toolSource(name: string): { source: string; tool: string } {
  const m = MCP.exec(name);
  return m ? { source: `mcp:${m[1] ?? ""}`, tool: m[2] ?? "" } : { source: "Built-in", tool: name };
}

/** Most first; ties by name, so the order is stable. */
const byCount = (a: [string, number], b: [string, number]) => b[1] - a[1] || a[0].localeCompare(b[0]);

/**
 * The content-free work line: tool names with `×N`, most used first (at most `top`), then
 * `N subagents`, then `mcp:<server> ×N`, joined with ` · `; `no tools` when none was called.
 */
export function toolLine(counts: ToolCounts, top = Infinity): string {
  const tools: [string, number][] = [];
  const servers = new Map<string, number>();
  let subagents = 0;
  for (const [name, n] of Object.entries(counts)) {
    if (n <= 0) continue;
    if (SPAWN.has(name)) subagents += n;
    else if (MCP.test(name)) {
      const { source } = toolSource(name);
      servers.set(source, (servers.get(source) ?? 0) + n);
    } else tools.push([name, n]);
  }
  const parts = [
    ...tools.sort(byCount).slice(0, top).map(([name, n]) => `${name} ×${formatCount(n)}`),
    ...(subagents ? [`${formatCount(subagents)} ${subagents === 1 ? "subagent" : "subagents"}`] : []),
    ...[...servers.entries()].sort(byCount).map(([s, n]) => `${s} ×${formatCount(n)}`),
  ];
  return parts.length ? parts.join(" · ") : "no tools";
}

/** One ToolsOfferedUsed row: a source, its tools offered and used, and its top calls. */
export type ToolSourceRow = { source: string; offered: number; used: number; calls: string | null };

/**
 * The ToolsOfferedUsed rows (handoff §11S): one per source, Built-in first, then each MCP server
 * by calls; servers never called sink to the bottom. Offered = distinct tools in the requests'
 * tool lists (a tool called but never listed still counts); Used = distinct tools called; Calls
 * = the top 4 with their counts.
 */
export function toolsOffered(offered: readonly string[], calls: ToolCounts): ToolSourceRow[] {
  const rows = new Map<string, { offered: Set<string>; calls: Map<string, number> }>();
  const row = (source: string) => {
    let r = rows.get(source);
    if (!r) rows.set(source, (r = { offered: new Set(), calls: new Map() }));
    return r;
  };
  for (const name of offered) {
    const { source, tool } = toolSource(name);
    row(source).offered.add(tool);
  }
  for (const [name, n] of Object.entries(calls)) {
    if (n <= 0) continue;
    const { source, tool } = toolSource(name);
    const r = row(source);
    r.offered.add(tool);
    r.calls.set(tool, (r.calls.get(tool) ?? 0) + n);
  }
  const total = (r: { calls: Map<string, number> }) => [...r.calls.values()].reduce((a, b) => a + b, 0);
  return [...rows.entries()]
    .sort(([a, ra], [b, rb]) => (a === "Built-in" ? -1 : b === "Built-in" ? 1 : total(rb) - total(ra) || a.localeCompare(b)))
    .map(([source, r]) => {
      const top = [...r.calls.entries()].sort(byCount);
      const shown = top.slice(0, 4).map(([t, n]) => `${t} ${formatCount(n)}`);
      return { source, offered: r.offered.size, used: r.calls.size, calls: top.length ? [...shown, ...(top.length > 4 ? ["…"] : [])].join(" · ") : null };
    });
}

/**
 * At most `max` rows: when there are more, the servers never called past the first `max - 1`
 * rows fold into one `N more MCP servers` row (their tools offered summed, none used).
 */
export function collapseUnused(rows: readonly ToolSourceRow[], max: number): ToolSourceRow[] {
  if (rows.length <= max) return [...rows];
  const keep = rows.slice(0, max - 1);
  const rest = rows.slice(max - 1);
  const used = rest.filter((r) => r.used > 0);
  const unused = rest.filter((r) => r.used === 0);
  if (unused.length < 2) return [...rows];
  return [...keep, ...used, { source: `${formatCount(unused.length)} more MCP servers`, offered: unused.reduce((a, r) => a + r.offered, 0), used: 0, calls: null }];
}
