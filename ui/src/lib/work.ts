// The work line (handoff §11R, CompTurns "Work line vocabulary"): what a turn, a session or a
// repo did, in plain words, from metadata. One function over the API's structured work; the
// rows render its parts (WorkText), the tooltip its text.
import type { Schemas } from "../api/types";
import { formatCount } from "./format";

/** The structured work the API sends for a turn, a group, a session, a repo or a bin (`TurnWork`). */
export type Work = Schemas["TurnWork"];

export const EMPTY_WORK: Work = {
  reads: 0,
  readFiles: null,
  readTypes: {},
  edits: 0,
  editedFiles: null,
  editTypes: {},
  linesAdded: null,
  linesRemoved: null,
  commands: [],
  subagents: [],
  web: { searches: 0, fetches: 0 },
  mcp: [],
  otherTools: 0,
  commits: 0,
  pushes: 0,
  prs: [],
};

/** A run of the line: plain text, or a failure count (its own span, in --ink: failures are work, not friction). */
export type WorkPart = string | { failed: string };

const n = (count: number, one: string, many = `${one}s`) => `${formatCount(count)} ${count === 1 ? one : many}`;

/** The command phrase: one program with its outcome, several with their counts, or a bare count. */
function commandParts(cmds: Work["commands"]): WorkPart[] {
  const runs = cmds.reduce((a, c) => a + c.runs, 0);
  if (!runs) return [];
  const failed = cmds.reduce((a, c) => a + c.failed, 0);
  const [only] = cmds;
  if (cmds.length === 1 && only?.program) {
    const head = `ran ${only.program} ×${formatCount(only.runs)}`;
    if (only.failed > 0) return only.lastExit === 0 ? [`${head}: `, { failed: `${formatCount(only.failed)} failed` }, ", then passed"] : [`${head}: `, { failed: `${formatCount(only.failed)} failed` }];
    return [only.lastExit === 0 ? `${head}: passed` : head];
  }
  const named = cmds.filter((c) => c.program);
  const tally = named.length === cmds.length ? ` (${named.map((c) => `${c.program} ×${formatCount(c.runs)}`).join(", ")})` : "";
  const head = `ran ${n(runs, "command")}${tally}`;
  return failed > 0 ? [`${head}: `, { failed: `${formatCount(failed)} failed` }] : [head];
}

/** The line's phrases, in order (reads, edits, commands, subagents, web, MCP, git); zero kinds left out. */
export function workLine(w: Work): WorkPart[] {
  const phrases: WorkPart[][] = [];
  // Files where the agent names them (distinct), else the calls.
  const reads = w.readFiles ?? w.reads;
  const edits = w.editedFiles ?? w.edits;
  if (reads) phrases.push([`read ${n(reads, "file")}`]);
  if (edits) phrases.push([`edited ${n(edits, "file")}${w.linesAdded !== null && w.linesRemoved !== null ? ` +${formatCount(w.linesAdded)} −${formatCount(w.linesRemoved)}` : ""}`]);
  const cmd = commandParts(w.commands);
  if (cmd.length) phrases.push(cmd);
  const spawned = w.subagents.reduce((a, x) => a + x.count, 0);
  if (spawned) phrases.push([`spawned ${n(spawned, "subagent")}`]);
  if (w.web.searches) phrases.push([`searched ${formatCount(w.web.searches)}`]);
  if (w.web.fetches) phrases.push([`fetched ${n(w.web.fetches, "page")}`]);
  // A call whose server the agent didn't name reads `used MCP ×N`.
  for (const m of w.mcp) if (m.calls) phrases.push([`used ${m.server || "MCP"} ×${formatCount(m.calls)}`]);
  if (w.commits) phrases.push([n(w.commits, "commit")]);
  if (w.pushes) phrases.push(["pushed"]);
  for (const pr of w.prs) phrases.push([`PR #${pr}`]);
  return phrases.flatMap((p, i) => (i === 0 ? p : [" · ", ...p])).reduce<WorkPart[]>((out, part) => {
    // Adjacent text runs join, so the line is as few spans as it has failures.
    const last = out[out.length - 1];
    if (typeof part === "string" && typeof last === "string") out[out.length - 1] = last + part;
    else out.push(part);
    return out;
  }, []);
}

/** The line as plain text (tooltips, accessible names). */
export const workText = (parts: readonly WorkPart[]) => parts.map((p) => (typeof p === "string" ? p : p.failed)).join("");
