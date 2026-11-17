// The sheets' own ToolsOfferedUsed rows (handoff §11S): CompTurns (session, in range) and Main (person).
import type { ToolSourceRow } from "../../lib/tools";

export const sessionTools: ToolSourceRow[] = [
  { source: "Built-in", offered: 23, used: 9, calls: "Bash 488 · Read 402 · Edit 171 · Grep 96 · …" },
  { source: "mcp:linear", offered: 41, used: 2, calls: "list_issues 2 · get_issue 1" },
  { source: "mcp:sentry", offered: 22, used: 0, calls: null },
];
export const personTools: ToolSourceRow[] = [
  { source: "Built-in", offered: 23, used: 11, calls: "Bash 2,104 · Read 1,630 · Edit 712 · Grep 288 · …" },
  { source: "mcp:linear", offered: 41, used: 2, calls: "list_issues 14 · get_issue 6" },
  { source: "mcp:playwright", offered: 21, used: 3, calls: "browser_navigate 40 · browser_click 31" },
  { source: "mcp:github", offered: 38, used: 1, calls: "get_pull_request 3" },
  { source: "7 more MCP servers", offered: 258, used: 0, calls: null },
];
