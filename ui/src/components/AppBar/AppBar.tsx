import type { ReactNode } from "react";
import { Breadcrumb, type Crumb } from "../Breadcrumb/Breadcrumb";
import { Button } from "../Button/Button";
import { LiveBadge, type LiveBadgeProps } from "../LiveBadge/LiveBadge";
import { SegmentedControl, type SegmentedControlProps } from "../SegmentedControl/SegmentedControl";
import { Wordmark } from "../Wordmark/Wordmark";
import "../Button/Button.css";
import "./AppBar.css";

export type { Crumb };

export type AppBarProps = {
  breadcrumb: Crumb[];
  window?: SegmentedControlProps;
  /** Names the window control (§11G.C1: "Active in the last"), which it labels. */
  windowLabel?: string;
  /** In place of LIVE (the People page, §11G.C1). */
  updated?: ReactNode;
  live?: LiveBadgeProps;
  onPause?: () => void;
  paused?: boolean;
  actions?: ReactNode;
  loading?: boolean;
  /** Opens "About this data" (handoff §11E.1); the button sits last. */
  onAbout?: () => void;
};

/**
 * The top bar (handoff §6.7): global chrome only, no agent chips (§11L.2; the
 * breadcrumb's person and session crumbs carry the agent dots). Renders immediately; only crumbs whose name is
 * still loading show a skeleton word (§6A.3). While loading the window control
 * is disabled with a reason.
 */
export function AppBar({ breadcrumb, window, windowLabel, updated, live, onPause, paused, actions, loading, onAbout }: AppBarProps) {
  return (
    <header className={`app-bar flex flex-wrap items-center gap-x-4 ${windowLabel ? "gap-y-3" : "gap-y-4"} border-b border-line bg-surface`}>
      <Wordmark />
      <Breadcrumb crumbs={breadcrumb} />
      <span className="flex-1" />
      {window && windowLabel && (
        <span id="app-bar-window-label" className="type-key text-ink-faint">
          {windowLabel}
        </span>
      )}
      {window && (
        <SegmentedControl
          {...window}
          labelledBy={windowLabel ? "app-bar-window-label" : undefined}
          disabled={loading || window.disabled}
          disabledReason={loading ? "Connecting…" : window.disabledReason}
        />
      )}
      {updated ?? (live && <LiveBadge {...(loading ? { status: "connecting" as const } : live)} />)}
      {onPause && (
        <button
          type="button"
          aria-pressed={paused}
          onPointerDown={onPause}
          onKeyDown={(e) => (e.key === "Enter" || e.key === " ") && onPause()}
          className="btn type-label rounded-sm"
        >
          {paused ? "Resume" : "Pause"}
        </button>
      )}
      {actions}
      {onAbout && <Button onClick={onAbout}>About this data</Button>}
    </header>
  );
}
