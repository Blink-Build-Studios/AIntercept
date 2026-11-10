import { useEffect, useState } from "react";

/** `value`, once it has stopped changing for `ms` (a brush settling, typing pausing).
 * Changes are by identity: pass a string or number, since an object built each render
 * never stops changing (the timer restarts, its update renders again, and so on). */
export function useDebounced<T>(value: T, ms: number): T {
  const [v, setV] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setV(value), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return v;
}

/** Text filters query once typing pauses this long; the input itself updates at once. */
export const TYPING_MS = 250;
