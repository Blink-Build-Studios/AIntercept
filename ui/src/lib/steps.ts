// A long turn's steps (handoff §11L.7): what each step did, runs of command-only steps, and
// the rows the step list shows. Pure: PromptRow and its tests call these.
import type { StepData } from "../api/types";
import type { StepKind, StripStep } from "../components/StepStrip/StepStrip";
import type { StepRunPart } from "../components/StepRow/StepRun";
import { formatCount, formatDuration, formatTokens } from "./format";

/** A prompt with more steps than this gets a StepStrip, numbered steps and runs. */
export const LONG_TURN = 20;
/** Consecutive command-only steps collapse into a StepRun from this many. */
export const RUN_MIN = 5;

/**
 * The strip's colour for a step: exec, apply_patch, mcp, text only. A spawn or another tool
 * call reads as a command (ink); an interrupt as text.
 */
export function stripKind(s: StepData): StepKind {
  if (s.does === "patch" || s.does === "mcp" || s.does === "text") return s.does;
  return s.does === "interrupt" ? "text" : "exec";
}

/** The strip's bars: every step of the turn, its height its output tokens. */
export const stripSteps = (steps: StepData[]): StripStep[] => steps.map((s) => ({ kind: stripKind(s), output: s.tokens.output }));

/**
 * Whether a step only ran commands: these join a run. A step that changes files, spawns an
 * agent, is interrupted or writes text breaks a run and shows on its own.
 */
export const commandOnly = (s: StepData) => s.kind === "model" && s.does === "exec" && s.patches.length === 0 && !s.text;

const calls = (s: StepData) => Math.max(1, s.tools.length);

/** What a numbered step produced: `exec ×1`, `apply_patch src/timer.ts +12 −3`, `“Tests pass now…”`. */
export function stepProduced(s: StepData): string | null {
  switch (s.does) {
    case "exec":
      return `exec ×${calls(s)}`;
    case "patch": {
      const [f, ...rest] = s.patches;
      if (!f) return `apply_patch ×${calls(s)}`;
      return `apply_patch ${f.file} +${f.added} −${f.removed}${rest.length ? ` +${rest.length} ${rest.length === 1 ? "file" : "files"}` : ""}`;
    }
    case "mcp":
      return `mcp ×${calls(s)}`;
    case "spawn":
      return "spawn_agent";
    case "tool":
      return s.tools.length ? tally(s.tools.map((t) => t.name)) : "tool";
    case "text":
      // Without recorded bodies a text-only step reads `text` (§11M 3.1); its stop reason follows.
      return `${s.text ? `“${s.text}…”` : "text"}${s.stopReason ? ` · ${s.stopReason}` : ""}`;
    case "interrupt":
      return "interrupted";
  }
}

/** "npm test ×37, rg": most frequent first, ×N past one. */
function tally(xs: string[], top = Infinity): string {
  const n = new Map<string, number>();
  for (const x of xs) n.set(x, (n.get(x) ?? 0) + 1);
  return [...n.entries()]
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .slice(0, top)
    .map(([k, c]) => (c > 1 ? `${k} ×${formatCount(c)}` : k))
    .join(", ");
}

const end = (s: StepData) => Date.parse(s.t) + (s.durationMs ?? 0);
const sum = (xs: (number | null)[]) => (xs.some((x) => x !== null) ? xs.reduce<number>((a, x) => a + (x ?? 0), 0) : null);

/**
 * A run's summary (§11L.7): tool counts, notable commands, failures (in --danger), span,
 * tokens. Commands and exit codes come only with full interception; without them the run
 * reads as counts, span and tokens.
 */
export function runParts(run: StepData[]): StepRunPart[] {
  const first = run[0];
  const last = run[run.length - 1];
  if (!first || !last) return [];
  const execs = run.reduce((n, s) => n + calls(s), 0);
  const commands = tally(run.flatMap((s) => s.commands), 3);
  const failed = run.flatMap((s) => s.exitCodes.filter((c) => c !== 0));
  const fail = failed.length ? tally(failed.map((c) => `exit ${c}`), 2) : null;
  const input = sum(run.map((s) => sum([s.tokens.input, s.tokens.cacheRead, s.tokens.cacheWrite])));
  const output = sum(run.map((s) => s.tokens.output));
  const tail = [
    formatDuration(Math.max(0, end(last) - Date.parse(first.t))),
    input === null && output === null ? null : `${input === null ? "—" : formatTokens(input, "total")} → ${output === null ? "—" : formatTokens(output, "total")}`,
  ].filter((x): x is string => !!x);
  const after = tail.length ? ` · ${tail.join(" · ")}` : "";
  const head = `exec ×${formatCount(execs)}`;
  if (!commands && !fail) return [`${head}${after}`];
  if (!fail) return [`${head} (${commands})${after}`];
  return [`${head} (${commands ? `${commands}, ` : ""}`, { failed: fail }, `)${after}`];
}

/** A long turn's rows: a step on its own, or a run of command-only steps. `index` is 1-based. */
export type TurnRow = { kind: "step"; step: StepData; index: number } | { kind: "run"; steps: StepData[]; first: number; last: number };

/** Groups a long turn's steps into rows: runs of RUN_MIN or more command-only steps collapse. */
export function turnRows(steps: StepData[]): TurnRow[] {
  const out: TurnRow[] = [];
  let i = 0;
  while (i < steps.length) {
    let j = i;
    while (j < steps.length && commandOnly(steps[j] as StepData)) j++;
    if (j - i >= RUN_MIN) {
      out.push({ kind: "run", steps: steps.slice(i, j), first: i + 1, last: j });
      i = j;
    } else {
      const stop = Math.max(j, i + 1);
      for (let k = i; k < stop; k++) out.push({ kind: "step", step: steps[k] as StepData, index: k + 1 });
      i = stop;
    }
  }
  return out;
}

/** The last step the first `limit` rows reach, for `Showing steps 1–N`. */
export function stepsShown(rows: TurnRow[], limit: number): number {
  const r = rows.slice(0, limit).at(-1);
  return r ? (r.kind === "run" ? r.last : r.index) : 0;
}

/** "2h 4m": a turn's span, from its first step to its last step's end. */
export function turnSpan(steps: StepData[]): string {
  const first = steps[0];
  if (!first) return "0s";
  return formatDuration(Math.max(0, Math.max(...steps.map(end)) - Date.parse(first.t)));
}
