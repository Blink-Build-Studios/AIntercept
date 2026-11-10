import { formatCount } from "../../lib/format";
import { Tooltip } from "../Tooltip/Tooltip";
import "../Chip/Chip.css";
import "./NewRowsPill.css";

export type NewRowsPillProps = {
  /** Rows that arrived while the viewer was scrolled away; 0 hides the pill. */
  count: number;
  /** Where the new rows are: below (`↓`, appended) or above (`↑`, prepended). */
  direction: "up" | "down";
  /** Scroll to the new rows; the owner clears the count. */
  onClick: () => void;
};

/**
 * "12 NEW ↓": live rows arrived past the edge the viewer scrolled away from
 * (handoff §5.3, §6A.9). Pinned to the table edge by its owner. It stays mounted
 * while hidden so its polite live region is in place before the count changes.
 */
export function NewRowsPill({ count, direction, onClick }: NewRowsPillProps) {
  const hidden = count <= 0;
  const where = direction === "down" ? "below" : "above";
  return (
    <Tooltip content={hidden ? null : { body: `Show ${formatCount(count)} new rows ${where}` }}>
    <button
      type="button"
      aria-live="polite"
      aria-hidden={hidden || undefined}
      tabIndex={hidden ? -1 : undefined}
      data-hidden={hidden || undefined}
      onClick={onClick}
      className="chip new-rows-pill rounded-sm border border-line-strong text-ink"
    >
      {hidden ? "" : `${formatCount(count)} NEW ${direction === "down" ? "↓" : "↑"}`}
    </button>
    </Tooltip>
  );
}
