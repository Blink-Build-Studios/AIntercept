import { useMemo, useRef, useState } from "react";
import type { PromptData, PromptItem, StepData } from "../../api/types";
import { LONG_TURN, runParts, stepProduced, stepsShown, stripSteps, turnRows, turnSpan } from "../../lib/steps";
import { formatBytes, formatCount, formatDuration, formatTokens } from "../../lib/format";
import { formatDayTime } from "../../lib/time";
import { useRowLink } from "../../lib/linkedHover";
import { formatExact } from "../../lib/format";
import { frictionTip, fullText, spanTip, timeTip } from "../../lib/tips";
import { unknownAttrs, unknownTip } from "../../lib/unknown";
import { Marker } from "../Marker/Marker";
import { StepList, STEP_PAGE } from "../StepRow/StepList";
import { StepRow } from "../StepRow/StepRow";
import { StepRun } from "../StepRow/StepRun";
import { StepStrip } from "../StepStrip/StepStrip";
import { Tooltip, unknownValue } from "../Tooltip/Tooltip";
import type { Column } from "../DataTable/DataTable";
// The table row styles (.dt-row: hover, focus, pending, dimming), also when drawn outside a DataTable.
import "../DataTable/DataTable.css";
import { HiddenCallsNote } from "../HiddenCallsNote/HiddenCallsNote";
import { TurnReport } from "../TurnReport/TurnReport";
import { turnReportParts } from "../../lib/agents";
import "./PromptRow.css";

export type Prompt = PromptData;
export type { PromptItem };

// The Prompts table (handoff §11B.4, Session.dc.html): the header and every PromptRow use these widths.
export const PROMPT_MIN_WIDTH = 1100;
export const PROMPT_COLUMNS: Column[] = [
  { id: "twisty", header: "", width: 18 },
  { id: "index", header: "#", width: 40 },
  { id: "t", header: "Time", width: 92 },
  { id: "prompt", header: "Prompt", width: 240 },
  { id: "steps", header: "Steps", width: 70, definition: "Model calls made for the prompt, subagents included." },
  { id: "dur", header: "Dur", width: 64 },
  { id: "tokens", header: "Tokens", width: 110, definition: "Input → output, summed over the prompt's calls." },
  { id: "tools", header: "Tools", width: "flex" },
  { id: "friction", header: "Friction", width: 80 },
];

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

/**
 * The prompt cell: `“text”` when bodies are recorded, else its length (`412 chars`), then any
 * paste. Without bodies the text closes up to what the call carries (§11M 3.1): the mode is
 * said once, in About this data, not on every row.
 */
export function promptShape(p: Prompt): string {
  const t = p.text;
  if (t.retained && t.text !== null) return `“${t.text}”`;
  const parts = [t.chars === null ? null : `${formatCount(t.chars)} chars`, t.pastedBytes ? `pasted ${formatBytes(t.pastedBytes)}` : null];
  return parts.filter((x) => x !== null).join(" · ");
}

/** Total tokens in → out (§11B.3 totals): `39k → 2.6k`; null when neither is known. */
export function promptTokens(p: Prompt): string | null {
  if (p.tokensIn === null && p.tokensOut === null) return null;
  const f = (n: number | null) => (n === null ? "—" : formatTokens(n, "total"));
  return `${f(p.tokensIn)} → ${f(p.tokensOut)}`;
}

/** "Bash·19 Read·2": most used first. */
export const toolTally = (counts: Prompt["toolCounts"]) =>
  Object.entries(counts)
    .sort(([a, x], [b, y]) => y - x || a.localeCompare(b))
    .map(([name, n]) => `${name}·${n}`)
    .join(" ");

/**
 * The expanded prompt's detail line, when it adds to the prompt cell: chars · pasted · images
 * (the known ones). Empty when the cell already says it all (no paste, no images).
 */
export function promptDetail(p: Prompt): string {
  const t = p.text;
  if (!t.pastedBytes && !t.images) return "";
  return [t.chars === null ? null : `${formatCount(t.chars)} chars`, t.pastedBytes ? `pasted ${formatBytes(t.pastedBytes)}` : null, t.images ? plural(t.images, "image") : null]
    .filter((x) => x !== null)
    .join(" · ");
}

type StepGroup = { key: string; step: StepData } | { key: string; subagent: StepData[]; agentType: string | null };

/** A prompt's steps as rows: model calls, and each subagent's consecutive calls as one run (keyed by its first call). */
export function groupSteps(steps: StepData[]): StepGroup[] {
  const out: StepGroup[] = [];
  for (const s of steps) {
    const last = out[out.length - 1];
    if (s.kind !== "subagent") out.push({ key: s.requestId, step: s });
    else if (last && "subagent" in last && last.agentType === s.agentType) last.subagent.push(s);
    else out.push({ key: s.requestId, subagent: [s], agentType: s.agentType });
  }
  return out;
}

export type PromptRowProps = {
  item: PromptItem;
  /** Steps shown inline under the row (controlled; steps arrive with /prompts, so there is no wait). */
  expanded: boolean;
  onToggle: (promptId: string) => void;
  /** Opens the request drawer on a step. */
  onOpenStep?: (requestId: string) => void;
  selectedRequestId?: string | null;
  pendingRequestId?: string | null;
  /** Expanded subagent runs, by their first call's requestId. */
  expandedSubagents?: readonly string[];
  onToggleSubagent?: (requestId: string) => void;
  /** The reference "now" for same-day times and in-flight durations (defaults to the clock). */
  now?: string;
  /** A timeline click landed here (§11H.6): selected until the next pointer-down, tab or page change, or Esc. */
  arrived?: boolean;
  /** Shows the session's hidden calls (prewarm, title) in the Requests tab; with it, an expanded
   * prompt that made some ends with a HiddenCallsNote (§11J gap 2). */
  onShowHidden?: () => void;
  /** Pins a state for stories; "linked": the timeline points at this prompt (§6B.5). */
  "data-force"?: "hover" | "focus" | "linked";
};

/**
 * One prompt in the Prompts tab (handoff §6.5, columns §11B.4): twisty · # · time · prompt ·
 * steps · duration · tokens · tools · friction. A `<button aria-expanded>`; expanded, it is
 * followed by a detail line and its StepRows. In flight it counts `N so far` and `● elapsed`;
 * friction is `↯` frustrated, `✕` interrupted, `▲` destructive.
 */
export function PromptRow({
  item,
  expanded,
  onToggle,
  onOpenStep,
  selectedRequestId,
  pendingRequestId,
  expandedSubagents = [],
  onToggleSubagent,
  now,
  arrived,
  onShowHidden,
  "data-force": force,
}: PromptRowProps) {
  const p = item.prompt;
  const ref = now ? new Date(now) : new Date();
  const f = p.friction;
  // Friction (handoff §3.2): ↯ frustrated and ✕ interrupted in ink; destructive ▲ in danger.
  const marks = [f.frustrated && (["↯", "frustrated"] as const), f.interrupted && (["✕", "interrupted"] as const)].filter((m) => !!m);
  const glyphs = marks.map((m) => m[0]).join(" ");
  const words = [...marks.map((m) => m[1]), f.destructive > 0 ? plural(f.destructive, "destructive command") : null].filter(Boolean).join(", ");
  const tokens = promptTokens(p);
  const detail = promptDetail(p);
  const shape = promptShape(p);
  // Hover and focus point the timeline at the prompt's tick; its tick hovered marks the row.
  const { linked, handlers } = useRowLink({ source: "table", ids: [p.promptId], t: p.t }, { id: p.promptId });
  const tokensTip = tokens === null
    ? unknownTip("Tokens", "no captured model calls")
    : { rows: [{ key: "Input", value: p.tokensIn === null ? unknownValue("no usage reported") : formatExact(p.tokensIn) }, { key: "Output", value: p.tokensOut === null ? unknownValue("no usage reported") : formatExact(p.tokensOut) }] };
  return (
    <>
      <button
        type="button"
        role="row"
        tabIndex={-1}
        data-roving
        aria-expanded={expanded}
        data-inflight={p.inFlight || undefined}
        data-prompt-index={p.index ?? undefined}
        data-linked={linked || undefined}
        data-arrived={arrived || undefined}
        data-force={force}
        onClick={() => onToggle(p.promptId)}
        // On the row (§6B.8): → expands, ← collapses.
        onKeyDown={(e) => {
          if (e.target !== e.currentTarget) return;
          if ((e.key === "ArrowRight" && !expanded) || (e.key === "ArrowLeft" && expanded)) {
            e.preventDefault();
            e.stopPropagation();
            onToggle(p.promptId);
          }
        }}
        {...handlers}
        className="dt-row type-data text-ink prompt-row"
      >
        <span role="cell" aria-hidden>
          {expanded ? "▾" : "▸"}
        </span>
        <Tooltip content={p.index === null ? unknownTip("Prompt", "prompt not captured") : null}>
          <span role="cell" className="type-data-strong" {...(p.index === null ? unknownAttrs("prompt not captured") : {})}>
            {p.index === null ? "#—" : `#${p.index}`}
          </span>
        </Tooltip>
        <Tooltip content={timeTip(p.t)}>
          <span role="cell" className="text-ink-muted">
            {formatDayTime(p.t)}
          </span>
        </Tooltip>
        <Tooltip kind="truncate" content={fullText(shape)}>
          <span role="cell" className="text-ink-muted">
            {shape}
          </span>
        </Tooltip>
        <span role="cell">{p.inFlight ? `${p.steps} so far` : p.steps}</span>
        {p.inFlight ? (
          <span role="cell" className="text-ink-muted">{`● ${formatDuration(Math.max(0, ref.getTime() - Date.parse(p.t)))}`}</span>
        ) : (
          <Tooltip content={p.durationMs === null ? unknownTip("Duration", "no turn end in analytics") : spanTip(p.durationMs)}>
            <span role="cell" className="text-ink-muted" {...(p.durationMs === null ? unknownAttrs("no turn end in analytics") : {})}>
              {p.durationMs === null ? "—" : formatDuration(p.durationMs)}
            </span>
          </Tooltip>
        )}
        <Tooltip content={tokensTip}>
          <span role="cell" {...(tokens === null ? unknownAttrs("no captured model calls") : {})}>
            {tokens ?? "—"}
          </span>
        </Tooltip>
        <span role="cell" className="text-ink-muted">
          {toolTally(p.toolCounts)}
        </span>
        <Tooltip content={frictionTip({ frustrated: f.frustrated ? 1 : 0, interrupted: f.interrupted ? 1 : 0, destructive: f.destructive })}>
          <span role="cell" className="text-ink" aria-label={words || undefined}>
            {glyphs}
            {f.destructive > 0 && (
              <>
                {glyphs ? " " : ""}
                <Marker kind="destructive" compact label={plural(f.destructive, "destructive command")} />
              </>
            )}
          </span>
        </Tooltip>
      </button>
      {expanded && p.turnReport && <TurnReport agent="codex" parts={turnReportParts(p.turnReport)} />}
      {expanded && detail && (
        <div role="row" className="dt-row type-data text-ink-faint prompt-detail">
          <span role="cell">{detail}</span>
        </div>
      )}
      {expanded && item.steps.length > LONG_TURN && <LongTurn steps={item.steps} onOpenStep={onOpenStep} selectedRequestId={selectedRequestId} pendingRequestId={pendingRequestId} />}
      {expanded &&
        item.steps.length <= LONG_TURN &&
        groupSteps(item.steps).map((g) =>
          "step" in g ? (
            <StepRow
              key={g.key}
              step={g.step}
              depth={0}
              onOpen={onOpenStep}
              selectedRequestId={selectedRequestId}
              pendingRequestId={pendingRequestId}
            />
          ) : (
            <StepRow
              key={g.key}
              subagent={g.subagent}
              depth={0}
              expanded={expandedSubagents.includes(g.key)}
              onToggle={() => onToggleSubagent?.(g.key)}
              onOpen={onOpenStep}
              selectedRequestId={selectedRequestId}
              pendingRequestId={pendingRequestId}
            />
          ),
        )}
      {expanded && onShowHidden && Object.keys(p.hidden).length > 0 && (
        <div role="row" className="dt-row prompt-hidden">
          <span role="cell">
            <HiddenCallsNote counts={p.hidden} onShow={onShowHidden} />
          </span>
        </div>
      )}
    </>
  );
}

/**
 * A long turn's steps (handoff §11L.7, more than LONG_TURN): the StepStrip under the prompt
 * row, then numbered steps with runs of command-only steps collapsed, 120 rows at a time
 * (a run counts as one). The strip jumps the list, showing more rows when it has to.
 */
function LongTurn({ steps, onOpenStep, selectedRequestId, pendingRequestId }: { steps: StepData[]; onOpenStep?: (id: string) => void; selectedRequestId?: string | null; pendingRequestId?: string | null }) {
  const rows = useMemo(() => turnRows(steps), [steps]);
  const [limit, setLimit] = useState(STEP_PAGE);
  const [open, setOpen] = useState<ReadonlySet<number>>(() => new Set());
  const listRef = useRef<HTMLDivElement>(null);
  const jump = (i: number) => {
    const at = rows.findIndex((r) => (r.kind === "run" ? r.last : r.index) >= i + 1);
    if (at >= limit) setLimit(Math.ceil((at + 1) / STEP_PAGE) * STEP_PAGE);
    requestAnimationFrame(() => listRef.current?.querySelector(`[data-step-row="${at}"]`)?.scrollIntoView?.({ block: "nearest" }));
  };
  const toggle = (first: number) => setOpen((o) => { const n = new Set(o); if (n.has(first)) n.delete(first); else n.add(first); return n; });
  const numbered = (st: StepData, index: number, key: string) => (
    <StepRow key={key} numbered={{ step: st, index, produced: stepProduced(st) }} onOpen={onOpenStep} selectedRequestId={selectedRequestId} pendingRequestId={pendingRequestId} />
  );
  const nodes = rows.map((r, i) =>
    r.kind === "step" ? (
      <div key={r.step.requestId} data-step-row={i} className="contents">
        {numbered(r.step, r.index, r.step.requestId)}
      </div>
    ) : (
      <div key={`run-${r.first}`} data-step-row={i} className="contents">
        <StepRun t={r.steps[0]?.t ?? ""} first={r.first} last={r.last} parts={runParts(r.steps)} expanded={open.has(r.first)} onToggle={() => toggle(r.first)} />
        {open.has(r.first) && r.steps.map((st, k) => numbered(st, r.first + k, st.requestId))}
      </div>
    ),
  );
  return (
    <>
      <div role="row" className="dt-row step-strip-row">
        <div role="cell" className="step-strip-cell">
          <StepStrip steps={stripSteps(steps)} span={turnSpan(steps)} onJump={jump} />
        </div>
      </div>
      <div ref={listRef} className="contents">
        <StepList rows={nodes} limit={limit} stepsShown={stepsShown(rows, limit)} totalSteps={steps.length} onMore={() => setLimit((l) => l + STEP_PAGE)} />
      </div>
    </>
  );
}
