// A query-backed region's state, as a page hands it to a screen. Screens never
// see queries: they get Region<T> props and render them through RegionState.
import type { ApiError } from "../api/client";

export type RegionStatus = "loading" | "refetching" | "error" | "empty" | "ready";

export type Region<T> = {
  status: RegionStatus;
  data: T | undefined;
  error?: { message: string; queryId?: string };
  /** Set by the page once the request has run 8 s (§6A.1): "Still loading…" with this Cancel. */
  slow?: { onCancel: () => void };
};

/** Build a Region from a TanStack Query result (used in pages only). */
export function toRegion<T>(
  q: { data: T | undefined; isPending: boolean; isFetching: boolean; isError: boolean; isPlaceholderData?: boolean; error: unknown },
  isEmpty: (data: T) => boolean = () => false,
): Region<T> {
  const err = q.isError ? (q.error as ApiError | Error) : null;
  const error = err ? { message: err.message, queryId: (err as ApiError).queryId } : undefined;
  if (q.isError) return { status: "error", data: q.data, error };
  if (q.isPending && q.data === undefined) return { status: "loading", data: undefined };
  if (q.isFetching && (q.isPlaceholderData ?? false)) return { status: "refetching", data: q.data };
  if (q.data !== undefined && isEmpty(q.data)) return { status: "empty", data: q.data };
  return { status: "ready", data: q.data };
}

/** RegionState's slow notice for a region, naming what's loading. */
export const slowNotice = (r: Region<unknown>, reason: string) => (r.slow ? { reason, onCancel: r.slow.onCancel } : undefined);

/** A ready region from a sample (stories and tests). */
export const readyRegion = <T>(data: T): Region<T> => ({ status: "ready", data });
