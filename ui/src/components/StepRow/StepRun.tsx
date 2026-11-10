import { formatCount } from "../../lib/format";
import { formatTime } from "../../lib/time";
import { timeTip } from "../../lib/tips";
import { Tooltip } from "../Tooltip/Tooltip";
import "../DataTable/DataTable.css";
import "./StepRow.css";

/** A piece of a run's summary: plain text, or a failure count shown in --danger. */
export type StepRunPart = string | { failed: string };

export type StepRunProps = {
  /** The run's first step's start. */
  t: string;
  /** Step numbers, first and last (1-based, inclusive). */
  first: number;
  last: number;
  /**
   * The summary after the count, joined as given: tool counts, notable commands, failures,
   * span, tokens. E.g. `["exec ×81 (npm test ×37, ", { failed: "exit 1 ×31" }, ") · apply_patch ×9 · 52m 2s"]`.
   */
  parts: StepRunPart[];
  expanded: boolean;
  onToggle: () => void;
  "data-force"?: "hover" | "focus";
};

const clock = (iso: string) => formatTime(iso, new Date(iso), true);

/**
 * Consecutive command-only steps of a long turn, collapsed to one row (handoff §11L.7):
 * the range, how many steps, then tool counts, notable commands, failures in --danger,
 * the span and tokens. Only runs of 5 or more collapse; a step that changes files,
 * spawns an agent, is interrupted or writes text breaks a run. Counts as one row of the
 * step list's 120.
 */
export function StepRun({ t, first, last, parts, expanded, onToggle, "data-force": force }: StepRunProps) {
  const count = last - first + 1;
  return (
    <button
      type="button"
      role="row"
      tabIndex={-1}
      data-roving
      aria-expanded={expanded}
      data-force={force}
      onClick={onToggle}
      className="dt-row dt-click type-data text-ink step-row step-numbered step-run"
    >
      <Tooltip content={timeTip(t)}>
        <span role="cell" className="text-ink-faint">
          {clock(t)}
        </span>
      </Tooltip>
      <span role="cell" className="text-ink-muted">{`#${first}–${last}`}</span>
      <span role="cell">
        <span aria-hidden className="text-ink-muted">
          {expanded ? "▾" : "▸"}
        </span>{" "}
        <b>{`${formatCount(count)} steps`}</b>{" "}
        <span className="text-ink-muted">
          {"· "}
          {parts.map((p, i) => (typeof p === "string" ? p : <span key={i} className="step-run-failed">{p.failed}</span>))}
        </span>
      </span>
      <span role="cell" className="text-ink-muted">
        {expanded ? "collapse" : "expand"}
      </span>
    </button>
  );
}
