import type { ReactNode } from "react";
import { formatCount, formatDuration, formatExact } from "../../lib/format";
import { useRowLink } from "../../lib/linkedHover";
import { formatDayTime, formatTime } from "../../lib/time";
import { fullText, spanTip, timeTip, turnFrictionTip } from "../../lib/tips";
import { askedText, inOut, type Turn } from "../../lib/turns";
import { unknownAttrs, unknownTip } from "../../lib/unknown";
import { workText, type WorkPart } from "../../lib/work";
import type { Column } from "../DataTable/DataTable";
import { Tooltip, unknownValue } from "../Tooltip/Tooltip";
import { WorkText } from "./WorkText";
// The table row styles (.dt-row: hover, focus, dimming), also when drawn outside a DataTable.
import "../DataTable/DataTable.css";
import "./TurnRow.css";

export type { Turn };

// The Turns table (handoff §11R, §11S, CompTurns TurnRow): ▸ 14 · Turn 40 · Started 92 · Asked 240 ·
// Work done (flex) · Steps 50 · Time 64 · Tokens 110 · friction 40; min width 1100. No Cost column.
export const TURN_MIN_WIDTH = 1100;
export const TURN_COLUMNS: Column[] = [
  { id: "twisty", header: "", width: 14 },
  { id: "turn", header: "Turn", width: 40 },
  { id: "t", header: "Started", width: 92 },
  { id: "asked", header: "Asked", width: 240, definition: "The prompt: its size without recorded content, its text with it." },
  { id: "work", header: "Work done", width: "flex", definition: "What the turn did: its tool calls by name, or in plain words when content was recorded." },
  { id: "steps", header: "Steps", width: 50, align: "right", definition: "Model calls made for the turn, subagents included." },
  { id: "time", header: "Time", width: 64, align: "right", definition: "Prompt to last step." },
  { id: "tokens", header: "Tokens", width: 110, align: "right", definition: "Input → output, summed over the turn's steps." },
  { id: "friction", header: "", width: 40 },
];

const plural = (n: number, one: string) => `${formatCount(n)} ${one}${n === 1 ? "" : "s"}`;

export type TurnRowProps = {
  turn: Turn;
  /** The work line (lib/work.ts `workLine`). */
  work: readonly WorkPart[];
  /** Shows the day with the time: the table's first row, and the first of each day. */
  dated?: boolean;
  expanded: boolean;
  onToggle: (turnId: string) => void;
  /** The reference "now" for a live turn's elapsed time (defaults to the clock). */
  now?: string;
  /** Landed on from the timeline (§11H.6). */
  arrived?: boolean;
  /** The expanded turn's WorkGroups and subagent rows. */
  children?: ReactNode;
  /** Pins a state for stories; "linked": the timeline points at this turn (§6B.5). */
  "data-force"?: "hover" | "focus" | "linked";
};

/**
 * One turn in the Turns tab (handoff §11R, CompTurns): a `<button aria-expanded>` row; the whole
 * row toggles, expanded it is --surface-raised and followed by its work groups. A live turn's
 * time is `● 1m 4s` in --live. Friction is `✕` interrupted and `!` failed calls, both --ink
 * (§11S); a failed command is work, not friction.
 */
export function TurnRow({ turn: t, work, dated = false, expanded, onToggle, now, arrived, children, "data-force": force }: TurnRowProps) {
  const ref = now ? new Date(now) : new Date();
  const asked = askedText(t);
  const tokens = inOut(t.tokensIn, t.tokensOut);
  const line = workText(work);
  const { interrupted, failedCalls: failed } = t.friction;
  const glyphs = [interrupted ? "✕" : null, failed > 0 ? "!" : null].filter(Boolean).join(" ");
  const words = [interrupted ? "interrupted" : null, failed > 0 ? plural(failed, "failed call") : null].filter(Boolean).join(", ");
  const { linked, handlers } = useRowLink({ source: "table", ids: [t.promptId], t: t.startedAt }, { id: t.promptId });
  const tokensTip = tokens === null
    ? unknownTip("Tokens", "no captured model calls")
    : { rows: [{ key: "Input", value: t.tokensIn === null ? unknownValue("no usage reported") : formatExact(t.tokensIn) }, { key: "Output", value: t.tokensOut === null ? unknownValue("no usage reported") : formatExact(t.tokensOut) }] };
  return (
    <>
      <button
        type="button"
        role="row"
        tabIndex={-1}
        data-roving
        aria-expanded={expanded}
        data-turn-index={t.n ?? undefined}
        data-linked={linked || undefined}
        data-arrived={arrived || undefined}
        data-force={force}
        onClick={() => onToggle(t.promptId)}
        // On the row: → opens, ← closes.
        onKeyDown={(e) => {
          if (e.target !== e.currentTarget) return;
          if ((e.key === "ArrowRight" && !expanded) || (e.key === "ArrowLeft" && expanded)) {
            e.preventDefault();
            e.stopPropagation();
            onToggle(t.promptId);
          }
        }}
        {...handlers}
        className="dt-row type-data text-ink turn-row"
      >
        <span role="cell" aria-hidden className="text-ink-muted">
          {expanded ? "▾" : "▸"}
        </span>
        <span role="cell" className="type-data-strong" {...(t.n === null ? unknownAttrs("prompt not captured") : {})}>
          {t.n === null ? "#—" : `#${t.n}`}
        </span>
        <Tooltip content={timeTip(t.startedAt)}>
          <span role="cell" className="text-ink-faint">
            {dated ? formatDayTime(t.startedAt) : formatTime(t.startedAt, new Date(t.startedAt))}
          </span>
        </Tooltip>
        <Tooltip kind="truncate" content={fullText(asked.text)}>
          <span role="cell" className={asked.recorded ? "text-ink" : "text-ink-faint"}>
            {asked.text}
          </span>
        </Tooltip>
        <Tooltip kind="truncate" content={line ? fullText(line) : null}>
          <span role="cell">
            <WorkText parts={work} />
          </span>
        </Tooltip>
        <span role="cell" className="dt-right">
          {t.steps}
        </span>
        {t.live ? (
          <span role="cell" className="dt-right text-ink-muted">
            <span className="text-live">{`● ${formatDuration(Math.max(0, ref.getTime() - Date.parse(t.startedAt)))}`}</span>
          </span>
        ) : (
          <Tooltip content={t.durationMs === null ? unknownTip("Time", "no last step seen") : spanTip(t.durationMs)}>
            <span role="cell" className="dt-right text-ink-muted" {...(t.durationMs === null ? unknownAttrs("no last step seen") : {})}>
              {t.durationMs === null ? "—" : formatDuration(t.durationMs)}
            </span>
          </Tooltip>
        )}
        <Tooltip content={tokensTip}>
          <span role="cell" className="dt-right" {...(tokens === null ? unknownAttrs("no captured model calls") : {})}>
            {tokens ?? "—"}
          </span>
        </Tooltip>
        <Tooltip content={turnFrictionTip(interrupted ? 1 : 0, failed)}>
          <span role="cell" className="turn-friction" aria-label={words || undefined}>
            {glyphs}
          </span>
        </Tooltip>
      </button>
      {expanded && children}
    </>
  );
}
