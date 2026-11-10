import "./ProgressBar.css";

export type ProgressBarProps = { active: boolean; scope: "global" | "region"; className?: string };

/**
 * A 2px indeterminate bar (handoff §6A.10) along the top of the nearest positioned
 * ancestor (region) or the viewport (global). Callers gate `active` through useDelayedFlag.
 */
export function ProgressBar({ active, scope, className = "" }: ProgressBarProps) {
  return (
    <div
      role="progressbar"
      aria-hidden={!active}
      aria-busy={active}
      aria-label="Loading"
      data-active={active}
      className={`progress ${scope === "global" ? "progress-global" : ""} ${className}`}
    />
  );
}
