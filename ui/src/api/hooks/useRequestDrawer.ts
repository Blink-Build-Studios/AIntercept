import { useSlow } from "./useSlow";
import { keepPreviousData, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";
import { curl } from "../../components/RequestDrawer/drawerData";
import type { DrawerTab } from "../../components/RequestDrawer/DrawerTabs";
import { toRegion } from "../../lib/region";
import { getJson } from "../client";
import type { Schemas } from "../types";

type Part = "raw" | "details";
const key = (id: string, part?: Part) => ["request", id, ...(part ? [part] : [])];
const path = (id: string, part?: Part) => `/requests/${encodeURIComponent(id)}${part ? `/${part}` : ""}`;
const fetcher =
  <T>(id: string, part?: Part) =>
  ({ signal }: { signal: AbortSignal }) =>
    getJson<T>(path(id, part), signal);

/**
 * The request drawer's data (handoff §6.6, §6A.4): `/requests/:id` at once
 * (the previous request stays, dimmed, while ↑ / ↓ load the next); `/raw` and
 * `/details` only when their tab opens; neighbours and Raw (on hover) prefetched.
 * `enabled` is false while no request is open (pages call this hook unconditionally).
 */
export function useRequestDrawer(requestId: string, tab: DrawerTab, enabled = true) {
  const client = useQueryClient();
  const detailQ = useQuery({ queryKey: key(requestId), queryFn: fetcher<Schemas["RequestResponse"]>(requestId), placeholderData: keepPreviousData, enabled });
  const d = detailQ.data?.data;
  const fresh = d?.requestId === requestId;
  const rawQ = useQuery({ queryKey: key(requestId, "raw"), queryFn: fetcher<Schemas["RequestRawResponse"]>(requestId, "raw"), enabled: enabled && tab === "raw" && fresh });
  const detailsQ = useQuery({
    queryKey: key(requestId, "details"),
    queryFn: fetcher<Schemas["RequestDetailsResponse"]>(requestId, "details"),
    enabled: enabled && tab === "all" && fresh,
  });
  useEffect(() => {
    for (const id of [d?.prevRequestId, d?.nextRequestId]) if (id) void client.prefetchQuery({ queryKey: key(id), queryFn: fetcher(id) });
  }, [client, d?.prevRequestId, d?.nextRequestId]);
  const slowDetail = useSlow(detailQ.isFetching, key(requestId), true);
  const slowRaw = useSlow(rawQ.isFetching, key(requestId, "raw"));
  const slowDetails = useSlow(detailsQ.isFetching, key(requestId, "details"));
  const loadRaw = () => client.fetchQuery({ queryKey: key(requestId, "raw"), queryFn: fetcher<Schemas["RequestRawResponse"]>(requestId, "raw") });
  return {
    detail: { ...toRegion({ ...detailQ, data: d }), slow: slowDetail },
    raw: { ...toRegion({ ...rawQ, data: rawQ.data?.data }), slow: slowRaw },
    details: { ...toRegion({ ...detailsQ, data: detailsQ.data?.data }), slow: slowDetails },
    retry: { detail: () => void detailQ.refetch(), raw: () => void rawQ.refetch(), details: () => void detailsQ.refetch() },
    prefetchRaw: () => void loadRaw(),
    curl: async () => curl((await loadRaw()).data),
  };
}
