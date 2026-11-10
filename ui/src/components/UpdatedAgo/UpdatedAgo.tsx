import { useEffect, useState } from "react";
import { useDelayedFlag } from "../../lib/interaction";
import { Tooltip, type TooltipContent } from "../Tooltip/Tooltip";
import "./UpdatedAgo.css";

export type UpdatedAgoProps = {
  /** When the page's data last refreshed (ISO); null before the first. */
  at: string | null;
  /** A poll is running. */
  updating?: boolean;
  /** The last poll failed (it retries on the backoff; the banner says why). */
  failed?: boolean;
  /** Pins "now" (stories, tests); else the clock, ticking every 5 s. */
  now?: string;
  /** Forces the tooltip open (stories). */
  open?: boolean;
  /** Shows the polling dot at once, past its 150 ms delay (stories). */
  forcePolling?: boolean;
};

/** "12 s ago", "3 min ago", "2 h ago". */
export function agoText(at: string, now: number): string {
  const s = Math.max(0, Math.round((now - Date.parse(at)) / 1000));
  if (s < 60) return `${s} s ago`;
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  return `${Math.floor(s / 3600)} h ago`;
}

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
const pad = (n: number) => String(n).padStart(2, "0");
const clockTime = (d: Date) => `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;

/** "11:32:05" (24h, local); a day or more old, "Nov 7". */
export function updatedText(at: string, now: number): string {
  const d = new Date(at);
  return now - d.getTime() >= 86_400_000 ? `${MONTHS[d.getMonth()]} ${d.getDate()}` : clockTime(d);
}

export const updatedAgoTip = (at: string, now: number): TooltipContent => ({
  rows: [
    { key: "Updated", value: `${agoText(at, now)} · ${clockTime(new Date(at))}` },
    { key: "Polling", value: "every 5 s · 30 s when hidden" },
  ],
});

/**
 * The People page's freshness (handoff §11H.4): `updated 11:32:05` in a fixed 19ch
 * slot, so its width never changes. A 10px cell before the text holds a dot while a
 * poll runs (after 150 ms) or ✕ when the last one failed (the text keeps the last
 * success; Banner/refreshFailed says why). The tooltip gives the age.
 */
export function UpdatedAgo({ at, updating, failed, now, open, forcePolling }: UpdatedAgoProps) {
  const [tick, setTick] = useState(() => Date.now());
  useEffect(() => {
    if (now) return undefined;
    const t = setInterval(() => setTick(Date.now()), 5000);
    return () => clearInterval(t);
  }, [now]);
  const clock = now ? Date.parse(now) : tick;
  const delayed = useDelayedFlag(!!updating && !failed);
  const polling = !failed && (delayed || !!forcePolling);
  return (
    <Tooltip content={at ? updatedAgoTip(at, clock) : null} kind="cell" open={open}>
      <span className="updated-ago type-data text-ink-muted" aria-live="polite">
        <span className="updated-ago-cell">
          {failed ? (
            <span className="updated-ago-failed" role="img" aria-label="last update failed">
              ✕
            </span>
          ) : (
            polling && <span className="updated-ago-dot" role="img" aria-label="updating" />
          )}
        </span>
        <span>{at ? `updated ${updatedText(at, clock)}` : "updating…"}</span>
      </span>
    </Tooltip>
  );
}
