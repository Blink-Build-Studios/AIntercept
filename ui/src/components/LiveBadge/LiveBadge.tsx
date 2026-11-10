import { formatAge, formatCount } from "../../lib/format";
import type { LiveStatus } from "../../lib/live";
import { Tooltip, type TooltipContent } from "../Tooltip/Tooltip";
import "./LiveBadge.css";

export type LiveBadgeProps = {
  status: LiveStatus;
  intervalS?: number;
  /** How far behind the data is (status `delayed`). */
  delayedMs?: number;
  newCount?: number;
  /** Increment on each successful poll: the live dot blips once (handoff §6A.7). */
  heartbeat?: number;
  className?: string;
  /** Last poll, newest data and the poll interval (§6B.7). */
  tip?: TooltipContent;
  /** Pins the heartbeat's dimmest frame for stories. */
  "data-force"?: "midBeat";
};

function label({ status, intervalS, delayedMs, newCount }: LiveBadgeProps): string {
  switch (status) {
    case "live":
      return intervalS ? `LIVE · ${intervalS}s` : "LIVE";
    case "delayed":
      return delayedMs !== undefined ? `DELAYED · ${formatAge(delayedMs)}` : "DELAYED";
    case "paused":
      return newCount ? `PAUSED · ${formatCount(newCount)} NEW` : "PAUSED";
    default:
      return status.toUpperCase();
  }
}

/**
 * Live/poll state for a page (handoff §6.2). Only `live` uses the live color.
 * Not a live region: the delayed Banner and the "N new" pill announce changes (§3.4).
 */
export function LiveBadge(props: LiveBadgeProps) {
  const live = props.status === "live";
  return (
    <Tooltip content={props.tip ?? null}>
    <span
      data-force={props["data-force"]}
      className={`live-badge rounded-sm border ${
        live ? "border-live text-live" : "border-line-strong text-ink-muted"
      } ${props.className ?? ""}`}
    >
      <span
        aria-hidden
        key={live ? props.heartbeat : undefined}
        className={`live-badge-dot rounded-pill ${
          live
            ? `bg-live ${props.heartbeat ? "live-badge-beat" : ""}`
            : props.status === "paused"
              ? "border border-ink-faint"
              : "bg-ink-faint"
        }`}
      />
      {label(props)}
    </span>
    </Tooltip>
  );
}
