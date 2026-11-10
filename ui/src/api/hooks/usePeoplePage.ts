import { TYPING_MS, useDebounced } from "./useDebounced";
import { useSlow } from "./useSlow";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { toRegion } from "../../lib/region";
import { getJson } from "../client";
import type { Schemas } from "../types";
import { useLive, useRefresh } from "./useLive";

const HOUR = 3_600_000;
const SPAN: Record<string, number | null> = { "24h": 24 * HOUR, "7d": 7 * 24 * HOUR, "14d": 14 * 24 * HOUR, all: null };

/**
 * A window preset as a range. It starts on the hour, so the query key (and the
 * table) stays put between polls; `to` is open (now). "all" has no range.
 */
export function windowRange(win: string, now: number): { from?: string } {
  const span = SPAN[win] ?? SPAN["14d"] ?? null;
  return span === null ? {} : { from: new Date(Math.floor((now - span) / HOUR) * HOUR).toISOString() };
}

const qs = (o: Record<string, string | string[] | number | undefined>) => {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(o)) {
    if (v === undefined || v === "") continue;
    for (const x of Array.isArray(v) ? v : [v]) p.append(k, String(x));
  }
  const s = p.toString();
  return s ? `?${s}` : "";
};

/** The People page's queries (handoff §7.1), polled with the page's live state. */
export function usePeoplePage({ win, agent, q, page }: { win: string; agent: string[]; q: string; page: number }) {
  const typedQ = useDebounced(q, TYPING_MS);
  const live = useLive();
  const range = windowRange(win, Date.now());
  const overviewQ = useQuery({
    queryKey: ["overview", range],
    queryFn: ({ signal }) => getJson<Schemas["OverviewResponse"]>(`/overview${qs(range)}`, signal),
    placeholderData: keepPreviousData,
    refetchInterval: live.refetchInterval,
    retry: false,
  });
  const peopleQ = useQuery({
    queryKey: ["people", range, agent, typedQ, page],
    queryFn: ({ signal }) => getJson<Schemas["PeopleResponse"]>(`/people${qs({ ...range, agent, q: typedQ.trim(), page })}`, signal),
    placeholderData: keepPreviousData,
    refetchInterval: live.refetchInterval,
    retry: false,
  });
  const refresh = useRefresh([overviewQ, peopleQ]);
  const slowOverview = useSlow(overviewQ.isFetching, ["overview"]);
  const slowPeople = useSlow(peopleQ.isFetching, ["people"]);
  // The page's freshness, in place of LIVE (§11G.C1): the people list's last refresh.
  const updated = {
    at: peopleQ.dataUpdatedAt ? new Date(peopleQ.dataUpdatedAt).toISOString() : null,
    updating: peopleQ.isFetching || overviewQ.isFetching,
    failed: refresh.failed,
  };
  return {
    live,
    updated,
    range,
    overview: { ...toRegion({ ...overviewQ, data: overviewQ.data?.data }), slow: slowOverview },
    overviewMeta: overviewQ.data?.meta,
    people: { ...toRegion(peopleQ, (r) => r.data.length === 0), slow: slowPeople },
    refresh,
    retry: { overview: () => void overviewQ.refetch(), people: () => void peopleQ.refetch() },
  };
}
