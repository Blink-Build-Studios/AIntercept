import { useEffect, useState } from "react";

/**
 * Live appends while the viewer is away from the insert edge (handoff §5.3, §6A.9):
 * rows that arrive then are held back, not inserted, so nothing the viewer is
 * reading moves; `pending` feeds NewRowsPill and `reveal()` shows them.
 * `down`: new rows arrive at the end (oldest first); `up`: at the start.
 */
export function useHeldRows<T>(rows: readonly T[], following: boolean, direction: "up" | "down" = "down") {
  const [held, setHeld] = useState(rows.length);
  useEffect(() => {
    if (following) setHeld(rows.length);
  }, [following, rows.length]);
  const shown = following ? rows.length : Math.min(held, rows.length);
  return {
    rows: direction === "down" ? rows.slice(0, shown) : rows.slice(rows.length - shown),
    pending: rows.length - shown,
    reveal: () => setHeld(rows.length),
  };
}
