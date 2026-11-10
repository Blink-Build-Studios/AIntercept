import type { ReactNode } from "react";
import { formatCount } from "../../lib/format";
import "../DataTable/DataTable.css";
import "./StepRow.css";

/** The step list renders this many rows (a StepRun counts as one) before `show N more` (§11L.7). */
export const STEP_PAGE = 120;

export type StepListProps = {
  /** One node per row: StepRows and StepRuns, in order. */
  rows: ReactNode[];
  /** How many rows to show; the caller grows it by STEP_PAGE with each `show N more`. */
  limit?: number;
  /** The last step the shown rows reach (a run covers many), for `Showing steps 1–120`. */
  stepsShown: number;
  /** The turn's steps, so the link says how many are left. */
  totalSteps: number;
  onMore: () => void;
};

/**
 * A long turn's steps (handoff §11L.7): the first `limit` rows, then `Showing steps 1–120 ·
 * show 83 more`. The StepStrip above always covers the whole turn.
 */
export function StepList({ rows, limit = STEP_PAGE, stepsShown, totalSteps, onMore }: StepListProps) {
  const more = rows.length > limit ? totalSteps - stepsShown : 0;
  return (
    <>
      {rows.slice(0, limit)}
      {more > 0 && (
        <div role="row" className="dt-row type-data text-ink-muted step-list-more">
          <span role="cell">{`Showing steps 1–${formatCount(stepsShown)} · `}</span>
          {/* A cell for the table's roles, with no box of its own: the link sits in the row. */}
          <span role="cell" className="contents">
            <button type="button" className="step-list-more-link" onClick={onMore}>
              {`show ${formatCount(more)} more`}
            </button>
          </span>
        </div>
      )}
    </>
  );
}
