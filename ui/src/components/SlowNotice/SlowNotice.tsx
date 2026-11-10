import { Button } from "../Button/Button";
import "./SlowNotice.css";

export type SlowNoticeProps = {
  /** What's loading: "the timeline", "the summary", "capabilities", "sessions", "this request"… */
  reason: string;
  /** Its size when already known (span · rows), never estimated: "2 days · 21,305 events". */
  size?: string;
  onCancel: () => void;
  /** Centred over its region (default), or a full-width row (CompR11's specimens). */
  layout?: "overlay" | "row";
};

/**
 * "Still loading the timeline…", then the size in --ink-muted when known, and a
 * Cancel (handoff §6A.1 at 8 s, copy §11H.5). The overlay centres itself over the
 * nearest positioned ancestor (the region).
 */
export function SlowNotice({ reason, size, onCancel, layout = "overlay" }: SlowNoticeProps) {
  return (
    <div role="status" aria-live="polite" className={`${layout === "row" ? "slow-notice-row" : "slow-notice"} text-ink`}>
      <span className="type-data">
        Still loading {reason}…{size && <> <span className="text-ink-muted">{size}</span></>}
      </span>
      {layout === "row" && <span className="flex-1" />}
      <Button onClick={onCancel}>Cancel</Button>
    </div>
  );
}
