import { useQueryClient, type QueryKey } from "@tanstack/react-query";
import { useEffect, useState } from "react";

/** A request this long gets "Still loading…" with Cancel (handoff §6A.1). */
export const SLOW_MS = 8000;

/**
 * A region's slow state: set once its query has been fetching for SLOW_MS, with
 * a Cancel that stops it (cancelling every query under `keyPrefix`, the region's).
 * A cancelled first load goes back to idle; a refresh keeps the data it had.
 */
export function useSlow(fetching: boolean, keyPrefix: QueryKey, exact = false): { onCancel: () => void } | undefined {
  const client = useQueryClient();
  const [slow, setSlow] = useState(false);
  useEffect(() => {
    setSlow(false);
    if (!fetching) return;
    const t = setTimeout(() => setSlow(true), SLOW_MS);
    return () => clearTimeout(t);
  }, [fetching]);
  return slow && fetching ? { onCancel: () => void client.cancelQueries({ queryKey: keyPrefix, exact }) } : undefined;
}
