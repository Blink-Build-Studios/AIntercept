import { useQuery } from "@tanstack/react-query";
import { POLL_INTERVAL_S } from "../../lib/live";
import { getJson } from "../client";
import type { StatusResponse } from "../types";

export function useStatus() {
  return useQuery({
    queryKey: ["status"],
    queryFn: ({ signal }) => getJson<StatusResponse>("/status", signal),
    refetchInterval: POLL_INTERVAL_S * 1000,
  });
}
