// Live behaviour shared by every page (handoff §5.3, BUILD_PLAN §2): poll 15 s while
// the tab is visible and 30 s while hidden; Pause stops it; a failed poll keeps the
// data and backs off 5 s → 10 s → 30 s with a Retry now.
import { useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useState, useSyncExternalStore } from "react";
import { DELAYED_AFTER_MS, liveStatusFrom, POLL_INTERVAL_S, type LiveStatus } from "../../lib/live";
import { liveBadgeTip } from "../../lib/tips";
import { useStatus } from "./useStatus";

export const HIDDEN_POLL_S = 30;
export const BACKOFF_S = [5, 10, 30];

const subscribe = (cb: () => void) => {
  document.addEventListener("visibilitychange", cb);
  return () => document.removeEventListener("visibilitychange", cb);
};
export const usePageVisible = () => useSyncExternalStore(subscribe, () => document.visibilityState !== "hidden");

/** What the backoff reads from a query's state (a Query's `state`, or a query result). */
export type FailureState = { status: string; errorUpdateCount: number };
/** What a poll interval reads from a query (any query's `state` fits). */
export type PolledQuery = { state: FailureState };

/**
 * Consecutive failed fetches, per query. TanStack's fetchFailureCount restarts
 * at every fetch (with retry: false it's always 1), so the streak is counted
 * here: errorUpdateCount only grows, and a success ends the streak.
 */
const streaks = new WeakMap<object, { errors: number; streak: number }>();
export function failureStreak(key: object, s: FailureState): number {
  const seen = streaks.get(key);
  if (s.status !== "error") {
    streaks.set(key, { errors: s.errorUpdateCount, streak: 0 });
    return 0;
  }
  if (seen && s.errorUpdateCount === seen.errors) return seen.streak;
  const streak = seen && s.errorUpdateCount > seen.errors ? seen.streak + (s.errorUpdateCount - seen.errors) : 1;
  streaks.set(key, { errors: s.errorUpdateCount, streak });
  return streak;
}

const backoffS = (streak: number) => BACKOFF_S[Math.min(Math.max(streak, 1), BACKOFF_S.length) - 1] ?? 5;

/** The poll interval for one query: off when paused; backing off after failures; slower while hidden. */
export function pollInterval(query: PolledQuery, paused: boolean, visible: boolean): number | false {
  const streak = failureStreak(query, query.state);
  if (paused) return false;
  if (query.state.status === "error") return backoffS(streak) * 1000;
  return (visible ? POLL_INTERVAL_S : HIDDEN_POLL_S) * 1000;
}

/** Polled region queries: whether the last refresh failed, the time of the data still shown, and Retry now. */
export type Refresh = { failed: boolean; since?: string; retryInS?: number; retry: () => void; retrying: boolean };

export function useRefresh(
  queries: { status: string; isError: boolean; data: unknown; dataUpdatedAt: number; errorUpdateCount: number; isFetching: boolean; refetch: () => unknown }[],
): Refresh {
  const failed = queries.filter((q) => q.isError && q.data !== undefined);
  const oldest = failed.length ? Math.min(...failed.map((q) => q.dataUpdatedAt)) : 0;
  // Each query's streak is keyed by its refetch (stable for the query's observer).
  const worst = Math.max(0, ...queries.map((q) => failureStreak(q.refetch, q)));
  return {
    failed: failed.length > 0,
    since: oldest ? new Date(oldest).toISOString() : undefined,
    retryInS: failed.length ? backoffS(worst) : undefined,
    retry: () => failed.forEach((q) => void q.refetch()),
    retrying: failed.some((q) => q.isFetching),
  };
}

/** Data is delayed when the scope is live and its newest data is older than the threshold (§5.3). */
export function delayedSince(meta: { asOf: string; lastDataAt: string | null } | undefined, live: boolean): string | undefined {
  if (!meta?.lastDataAt || !live) return undefined;
  return Date.parse(meta.asOf) - Date.parse(meta.lastDataAt) > DELAYED_AFTER_MS ? meta.lastDataAt : undefined;
}

/**
 * The page's live state: Pause, the interval every polled query uses, and the
 * LiveBadge (from /status, which polls too): its dot blips on each successful
 * status poll, and its tooltip names the last poll, the newest data and the intervals.
 */
export function useLive() {
  const [paused, setPaused] = useState(false);
  const visible = usePageVisible();
  const status = useStatus();
  const client = useQueryClient();
  const [polls, setPolls] = useState(0);
  useEffect(() => {
    if (status.isSuccess) setPolls((n) => n + 1);
  }, [status.dataUpdatedAt, status.isSuccess]);
  // Resume refetches at once, so the page catches up.
  const togglePause = useCallback(() => {
    setPaused((p) => {
      if (p) void client.invalidateQueries();
      return !p;
    });
  }, [client]);
  const refetchInterval = useCallback((q: PolledQuery) => pollInterval(q, paused, visible), [paused, visible]);
  let badge: { status: LiveStatus; delayedMs?: number } = { status: "connecting" };
  if (paused) badge = { status: "paused" };
  else if (status.isError) badge = { status: "retrying" };
  else if (status.data) badge = liveStatusFrom(status.data);
  return {
    paused,
    togglePause,
    refetchInterval,
    status: status.data,
    badge: {
      ...badge,
      intervalS: visible ? POLL_INTERVAL_S : HIDDEN_POLL_S,
      heartbeat: polls,
      tip: liveBadgeTip(status.data?.meta.asOf ?? null, status.data?.meta.lastDataAt ?? null, POLL_INTERVAL_S, HIDDEN_POLL_S),
    },
  };
}
