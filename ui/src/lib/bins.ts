// The timeline's bin size (handoff §11G.A5): chosen by the client from the track's
// width and passed to the API as `bin=`.
import { useEffect, useState } from "react";

/** The ladder, in seconds: 1m · 5m · 15m · 1h · 3h · 6h · 12h · 1d. */
export const BIN_LADDER = [60, 300, 900, 3600, 10_800, 21_600, 43_200, 86_400];
/** The on-screen width a bin aims for, and the least it may be. */
const TARGET_PX = 4;
const MIN_PX = 2;

/**
 * The bin whose on-screen width is closest to 4px, never under 2px (ties to the
 * larger). Closeness is by ratio (2px and 8px are equally far from 4px): the
 * handoff's examples need it (a 2-day session takes 15m at 6.3px over 5m at 2.1px).
 */
export function pickBin(spanMs: number, trackPx: number): number {
  let best = 86_400;
  let bestDist = Infinity;
  for (const b of BIN_LADDER) {
    const px = ((b * 1000) / Math.max(1, spanMs)) * trackPx;
    if (px < MIN_PX) continue;
    const d = Math.abs(Math.log(px / TARGET_PX));
    if (d <= bestDist) {
      best = b;
      bestDist = d;
    }
  }
  return best;
}

/** The page's content width less its padding, the frame and the 132px label column. */
const estimate = () => (typeof window === "undefined" ? 1210 : Math.max(300, Math.min(window.innerWidth, 1440) - 64 - 2 - 132));

/** The timeline track's width, estimated from the window before the timeline renders; follows resizes (debounced 150 ms). */
export function useTrackPx(): number {
  const [px, setPx] = useState(estimate);
  useEffect(() => {
    let t: ReturnType<typeof setTimeout> | undefined;
    const on = () => {
      clearTimeout(t);
      t = setTimeout(() => setPx(estimate()), 150);
    };
    window.addEventListener("resize", on);
    return () => {
      clearTimeout(t);
      window.removeEventListener("resize", on);
    };
  }, []);
  return px;
}
