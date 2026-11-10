import type { StatusResponse } from "../api/types";

/** Poll interval and "delayed" threshold, tuned to the observer's 60 s flush (BUILD_PLAN §2). */
export const POLL_INTERVAL_S = 15;
export const DELAYED_AFTER_MS = 3 * 60_000;

export type LiveStatus = "live" | "connecting" | "waiting" | "delayed" | "retrying" | "paused";

/** The badge status for a settled /status response (connecting, retrying and paused come from query state). */
export function liveStatusFrom(res: StatusResponse): { status: LiveStatus; delayedMs?: number } {
  if (!res.data.firstRequestAt) return { status: "waiting" };
  const last = res.meta.lastDataAt ? Date.parse(res.meta.lastDataAt) : null;
  if (last === null) return { status: "delayed" };
  const age = Date.parse(res.meta.asOf) - last;
  return age > DELAYED_AFTER_MS ? { status: "delayed", delayedMs: age } : { status: "live" };
}
