import type { ReactNode } from "react";
import { Button } from "../Button/Button";
import { KIND_COLOR, type RequestKind } from "../RequestRow/RequestRow";
import "./DrawerHeader.css";

export type DrawerKind = "normal" | "notCaptured" | "failed";

export type DrawerHeaderProps = {
  /** The request's channel (model, analytics, …): its label in the channel color. Unknown before the request loads. */
  channel: RequestKind | null;
  method: string | null;
  path: string;
  /** "200 · 9.3 s · 8.2 KB ↑ · 3.4 KB ↓", or the kind's own line. */
  status: string;
  kind: DrawerKind;
  /** Copy buttons, before the arrows. */
  actions?: ReactNode;
  onPrev?: () => void;
  onNext?: () => void;
  onClose: () => void;
  /** Why ↑ / ↓ is disabled at the ends of the range (§6A.4). */
  prevDisabledReason?: string;
  nextDisabledReason?: string;
};

const STATUS_TONE: Record<DrawerKind, string> = {
  normal: "text-ink-muted",
  notCaptured: "text-danger",
  failed: "text-ink",
};

/**
 * The drawer's top bar (handoff §6.6): channel, method and path, the status
 * line, then copy actions and ↑ ↓ × . The heading takes focus when the drawer opens.
 */
export function DrawerHeader({
  channel,
  method,
  path,
  status,
  kind,
  actions,
  onPrev,
  onNext,
  onClose,
  prevDisabledReason,
  nextDisabledReason,
}: DrawerHeaderProps) {
  return (
    <div className="drawer-header bg-surface-raised">
      {channel && <span className={`type-data ${KIND_COLOR[channel]}`}>{channel}</span>}
      <h2 id="drawer-heading" tabIndex={-1} className="type-data-strong text-ink outline-none">
        {method ? `${method} ${path}` : path}
      </h2>
      <span className={`type-data ${STATUS_TONE[kind]}`}>{status}</span>
      <span className="flex-1" />
      {actions}
      <Button variant="icon" aria-label="Previous request" onClick={onPrev} disabled={!onPrev} disabledReason={prevDisabledReason ?? "First request in range"}>
        ↑
      </Button>
      <Button variant="icon" aria-label="Next request" onClick={onNext} disabled={!onNext} disabledReason={nextDisabledReason ?? "Last request in range"}>
        ↓
      </Button>
      <Button variant="icon" aria-label="Close request detail" onClick={onClose}>
        ×
      </Button>
    </div>
  );
}
