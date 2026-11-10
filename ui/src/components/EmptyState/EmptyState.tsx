import type { ReactNode } from "react";
import "./EmptyState.css";

export type EmptyStateProps = {
  chip?: ReactNode;
  title: string;
  body?: string;
  actions?: ReactNode;
  /** The tighter "no rows match the filter" form: title and one action. */
  compact?: boolean;
  /** Inside a region that draws the frame and centres it (RegionState): no frame or padding. */
  bare?: boolean;
};

/** Shown only after a query succeeded with nothing to show (handoff §6.7, §6A.3). */
export function EmptyState({ chip, title, body, actions, compact, bare }: EmptyStateProps) {
  return (
    <div
      className={`empty-state flex flex-col items-center ${compact ? "empty-state-compact" : ""} ${bare ? "empty-state-bare" : "border border-line bg-surface"}`}
    >
      {chip}
      <div className="empty-state-title text-ink">{title}</div>
      {body && <div className="empty-state-body text-ink-muted">{body}</div>}
      {actions && (compact ? actions : <div className="empty-state-actions flex gap-2">{actions}</div>)}
    </div>
  );
}
