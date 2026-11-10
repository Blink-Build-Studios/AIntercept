import { keepPreviousData, useQuery, type QueryKey } from "@tanstack/react-query";
import { getJson } from "../client";

/**
 * Every range-, filter- or page-driven query (handoff §6A.5):
 * - the key carries every parameter, so a new parameter set is a new query;
 * - the previous result stays on screen while the new one loads (keep and dim);
 * - a superseded request is aborted through the query's signal, and an
 *   out-of-order response can never overwrite newer state (it belongs to an old key).
 */
export function useScopedQuery<T>(key: QueryKey, path: string, options: { refetchIntervalMs?: number; enabled?: boolean } = {}) {
  return useQuery({
    queryKey: key,
    queryFn: ({ signal }) => getJson<T>(path, signal),
    placeholderData: keepPreviousData,
    refetchInterval: options.refetchIntervalMs,
    enabled: options.enabled,
  });
}
