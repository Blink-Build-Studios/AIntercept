// Timing rules from handoff §6A.1, implemented once and used by every indicator.
import { useEffect, useRef, useState } from "react";

export const SHOW_AFTER_MS = 150;
export const MIN_VISIBLE_MS = 400;
export const SLOW_AFTER_MS = 8_000;

/**
 * True while `pending` has lasted at least `delay` ms, and then for at least
 * `minVisible` ms in total, so fast responses never flash an indicator and
 * slow ones never flicker.
 */
export function useDelayedFlag(pending: boolean, { delay = SHOW_AFTER_MS, minVisible = MIN_VISIBLE_MS } = {}): boolean {
  const [shown, setShown] = useState(false);
  const shownAt = useRef<number | null>(null);
  useEffect(() => {
    if (pending && !shown) {
      const t = setTimeout(() => {
        shownAt.current = Date.now();
        setShown(true);
      }, delay);
      return () => clearTimeout(t);
    }
    if (!pending && shown) {
      const left = Math.max(0, minVisible - (Date.now() - (shownAt.current ?? 0)));
      const t = setTimeout(() => {
        shownAt.current = null;
        setShown(false);
      }, left);
      return () => clearTimeout(t);
    }
    return undefined;
  }, [pending, shown, delay, minVisible]);
  return shown;
}

/** True once `pending` has lasted `after` ms ("Still loading…", §6A.1). */
export function useSlowFlag(pending: boolean, after = SLOW_AFTER_MS): boolean {
  const [slow, setSlow] = useState(false);
  useEffect(() => {
    if (!pending) {
      setSlow(false);
      return undefined;
    }
    const t = setTimeout(() => setSlow(true), after);
    return () => clearTimeout(t);
  }, [pending, after]);
  return slow;
}
