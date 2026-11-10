import type { CSSProperties, ReactNode } from "react";
import { Tooltip } from "../Tooltip/Tooltip";
import "./SummaryLabel.css";

export type SummaryLabelProps = {
  /** The block's name: `text`, `tool_use · exec_command`. */
  name: string;
  /** How many blocks share the name: `×2`, after the name; it never truncates. */
  count?: number | null;
  /** What the row says about it: `640 tokens · not retained`. */
  value: ReactNode;
  /** The label column: 220px in the 520px Summary column, 240px in a 640px drawer (§11L.9). */
  labelWidth?: 220 | 240;
};

/**
 * One Summary row's label and value (handoff §11L.9): a fixed label column where the name
 * truncates with an ellipsis and the count suffix stays at its visible end; the full label
 * is a tooltip on hover and focus. Rows never wrap.
 */
export function SummaryLabel({ name, count, value, labelWidth = 220 }: SummaryLabelProps) {
  const suffix = count ? `×${count}` : "";
  const full = suffix ? `${name} ${suffix}` : name;
  return (
    <div className="summary-label-row" style={{ "--summary-label": `${labelWidth}px` } as CSSProperties}>
      <Tooltip content={{ lead: full }}>
        {/* Focusable, so the full label opens from the keyboard too. */}
        {/* eslint-disable-next-line jsx-a11y/no-noninteractive-tabindex */}
        <span className="summary-label" tabIndex={0}>
          <span className="summary-label-name">{name}</span>
          <span className="summary-label-count text-ink-muted">{`\u00a0${suffix}`}</span>
        </span>
      </Tooltip>
      <span className="text-ink-muted">{value}</span>
    </div>
  );
}
