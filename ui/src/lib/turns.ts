// Turns (handoff §11R): from the API's turns and work to what the Turns tab and the Work tab
// render: the Asked cell, a group's title and meta, a step's line. Pure: rows and tests call these.
import type { Schemas } from "../api/types";
import type { Agent } from "../components/AgentChip/AgentChip";
import type { OutcomeFact } from "../components/OutcomeSlot/OutcomeFacts";
import type { WorkSession } from "../components/RepoRow/WorkSessionRow";
import { formatCount, formatDuration, formatTokens } from "./format";
import { workLine, type WorkPart } from "./work";

/** One turn: a prompt and all the work it started. */
export type Turn = Schemas["TurnRow"];
export type TurnGroup = Schemas["WorkGroup"];
export type TurnStepData = Schemas["TurnStep"];

const plural = (n: number, one: string, many = `${one}s`) => `${formatCount(n)} ${n === 1 ? one : many}`;

/** The Asked cell: the prompt in quotes when recorded, else its size (`212 chars`). */
export function askedText(t: Pick<Turn, "asked">): { text: string; recorded: boolean } {
  const a = t.asked;
  if (a.retained && a.text !== null) return { text: `“${a.text}”`, recorded: true };
  return { text: a.chars === null ? "—" : `${formatCount(a.chars)} chars`, recorded: false };
}

/** Tokens in → out (`410k → 6.1k`); null when neither is known. */
export function inOut(tokensIn: number | null, tokensOut: number | null): string | null {
  if (tokensIn === null && tokensOut === null) return null;
  const f = (n: number | null) => (n === null ? "—" : formatTokens(n, "total"));
  return `${f(tokensIn)} → ${f(tokensOut)}`;
}

/** `.ts ×5, .md ×1`: files by type, most first. */
export function typesText(types: Record<string, number>): string | null {
  const e = Object.entries(types).filter(([, n]) => n > 0);
  if (!e.length) return null;
  return e
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .map(([t, n]) => `${t.startsWith(".") ? t : `.${t}`} ×${formatCount(n)}`)
    .join(", ");
}

/** A step under its group: one model call, opening the drawer. */
export type TurnStepView = { requestId: string; label: string; detail: string };
/** A group inside an open turn (WorkGroup), its steps, and a recorded reply's preview. */
export type TurnGroupView = {
  key: string;
  kind: TurnGroup["kind"];
  title: string;
  meta: string;
  exits: number[];
  steps: TurnStepView[];
  reply: { text: string; size: string } | null;
};
/** A subagent under the turn that spawned it. */
export type SubagentView = { key: string; name: string; work: WorkPart[]; tokens: string | null; sessionId: string | null; requestId: string | null };
/** A turn as the Turns tab renders it. */
export type TurnView = { turn: Turn; work: WorkPart[]; groups: TurnGroupView[]; subagents: SubagentView[] };

/** One step's line under its group: model · tokens · duration · its tool call (`Bash npm test · exit 1 · 18 s`). */
export function stepDetail(s: TurnStepData): string {
  const t = s.tools[0];
  const exit = s.exitCodes[0] ?? t?.exitCode ?? null;
  const tool = t
    ? [[t.name, t.program ?? s.commands[0] ?? null].filter(Boolean).join(" "), exit === null ? null : `exit ${exit}`, t.durationMs === null ? null : formatDuration(t.durationMs)]
    : [];
  const sent = s.tokens.input === null && s.tokens.cacheRead === null && s.tokens.cacheWrite === null ? null : (s.tokens.input ?? 0) + (s.tokens.cacheRead ?? 0) + (s.tokens.cacheWrite ?? 0);
  return [s.model, inOut(sent, s.tokens.output), s.durationMs === null ? null : formatDuration(s.durationMs), ...tool].filter((x): x is string => !!x).join(" · ");
}

/**
 * A group's title and meta (handoff §11R, CompTurns "Group line"): verb, count and target, then
 * types or paths, steps, time (`Read 5 files` · `.ts ×5 · 4 steps · 41 s`).
 */
export function groupLine(g: TurnGroup): { title: string; meta: string } {
  const w = g.work;
  const steps = plural(g.steps.length, "step");
  const time = g.durationMs === null ? null : formatDuration(g.durationMs);
  const meta = (...xs: (string | null)[]) => xs.filter((x): x is string => !!x).join(" · ");
  const paths = g.paths?.length ? g.paths.join(", ") : null;
  switch (g.kind) {
    case "read":
      return { title: `Read ${plural(w.readFiles ?? w.reads, "file")}`, meta: meta(paths ?? typesText(w.readTypes), steps, time) };
    case "edit": {
      const lines = w.linesAdded !== null && w.linesRemoved !== null ? `+${formatCount(w.linesAdded)} −${formatCount(w.linesRemoved)}` : null;
      return { title: `Edited ${plural(w.editedFiles ?? w.edits, "file")}`, meta: meta(lines, paths ?? typesText(w.editTypes), steps) };
    }
    case "run": {
      const runs = w.commands.reduce((a, c) => a + c.runs, 0) || g.steps.length;
      return { title: `Ran ${g.program ?? "commands"} ×${formatCount(runs)}`, meta: meta(steps, time) };
    }
    case "web":
      return { title: w.web.fetches && !w.web.searches ? `Fetched ${plural(w.web.fetches, "page")}` : `Searched ${formatCount(w.web.searches)}${w.web.fetches ? ` · fetched ${plural(w.web.fetches, "page")}` : ""}`, meta: meta(steps, time) };
    case "mcp":
      return { title: `MCP ${g.server || "server"} ×${formatCount(w.mcp.reduce((a, m) => a + m.calls, 0) || g.steps.length)}`, meta: meta(steps, time) };
    case "spawn":
      return { title: `Spawned ${plural(w.subagents.reduce((a, x) => a + x.count, 0) || g.steps.length, "subagent")}`, meta: meta(steps, time) };
    case "tool":
      return { title: `Used ${plural(w.otherTools || g.steps.length, "tool")}`, meta: meta(steps, time) };
    case "think":
      return { title: "Thinking", meta: meta(`${steps} with no tool`, time, inOut(g.tokensIn, g.tokensOut)) };
    case "reply":
      return { title: "Reply", meta: g.tokensOut === null ? "" : `${formatTokens(g.tokensOut, "total")} tokens` };
  }
}

/** A turn's groups, subagents and work line, as the Turns tab renders them. */
export function turnView(t: Turn): TurnView {
  return {
    turn: t,
    work: workLine(t.work),
    groups: t.groups.map((g, i) => {
      const { title, meta } = groupLine(g);
      return {
        key: `${t.promptId}:${i}`,
        kind: g.kind,
        title,
        meta,
        exits: g.exits,
        steps: g.steps.map((s) => ({ requestId: s.requestId, label: `step ${s.step}`, detail: stepDetail(s) })),
        reply: g.kind === "reply" && g.text ? { text: g.text, size: `${formatTokens(g.tokensOut ?? 0, "total")} tokens` } : null,
      };
    }),
    subagents: t.subagents.map((a, i) => {
      const work = workLine(a.work);
      return {
        key: `${t.promptId}:sub:${a.agentId ?? i}`,
        name: a.name ?? "subagent",
        work: [plural(a.steps, "step"), ...(work.length ? [" · ", ...work] : [])],
        tokens: inOut(a.tokensIn, a.tokensOut),
        sessionId: a.sessionId,
        requestId: a.groups[0]?.steps[0]?.requestId ?? null,
      };
    }),
  };
}

/** Rows show the day on the table's first row and on the first of each day. */
export function datedRows(ts: readonly string[]): boolean[] {
  return ts.map((t, i) => i === 0 || new Date(ts[i - 1] ?? t).toDateString() !== new Date(t).toDateString());
}

/** A repo on the person page's Work tab, its sessions newest first. */
export type RepoView = {
  key: string;
  repo: string | null;
  sub: string | null;
  agents: Agent[];
  sessions: number;
  turns: number | null;
  activeMs: number | null;
  work: WorkPart[];
  rows: { session: WorkSession; work: WorkPart[] }[];
};

/** The Work tab's repos (handoff §11R, CompTurns RepoRow): one session names itself after the repo. */
export function repoView(g: Schemas["RepoGroup"]): RepoView {
  const only = g.rows.length === 1 ? g.rows[0] : undefined;
  return {
    key: g.repo ?? "(none)",
    repo: g.repo,
    sub: only ? only.label.text : null,
    agents: g.agents,
    sessions: g.sessions,
    turns: g.turns,
    activeMs: g.activeMs,
    work: workLine(g.work),
    rows: g.rows.map((r) => ({
      session: { sessionId: r.sessionId, start: r.start, end: r.end, live: r.live, label: r.label.text, turns: r.turns, capturedFromPrompt: r.capturedFromPrompt, activeMs: r.activeMs, friction: r.friction },
      work: workLine(r.work),
    })),
  };
}

const head = (sha: string) => sha.slice(0, 4);

/**
 * The OutcomeSlot's facts (handoff §11R): commits (on the branch, with the head range), files
 * edited by type, the last test program's exit code, push and PRs. Zeros are left out.
 */
export function outcomeFacts(o: Schemas["OutcomeFacts"], where: { branch?: string | null }): OutcomeFact[] {
  const range = o.headFrom && o.headTo && o.headFrom !== o.headTo ? ` (${head(o.headFrom)} → ${head(o.headTo)})` : "";
  const commits = plural(o.commits, "commit");
  const types = typesText(o.editTypes);
  const facts: (OutcomeFact | null)[] = [
    o.commits ? (where.branch ? [`${commits} on `, { strong: where.branch }, range] : [`${commits}${range}`]) : null,
    o.editedFiles ? [`${plural(o.editedFiles, "file")} edited${types ? ` (${types})` : ""}`] : null,
    o.lastTest ? ["last ", { strong: o.lastTest.program }, ` exited ${o.lastTest.exitCode}`] : null,
    o.pushes ? ["pushed"] : null,
    ...o.prs.map((n) => [`PR #${n}`]),
  ];
  return facts.filter((f): f is OutcomeFact => f !== null);
}

/** A fact as plain text (tests, accessible names). */
export const factText = (f: OutcomeFact) => f.map((p) => (typeof p === "string" ? p : p.strong)).join("");
