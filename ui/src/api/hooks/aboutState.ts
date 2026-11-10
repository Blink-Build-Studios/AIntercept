import { useQuery } from "@tanstack/react-query";
import type { AboutProductData, AboutState } from "../../components/AboutThisData/AboutThisData";
import type { Agent } from "../../components/AgentChip/AgentChip";
import type { UrlState } from "../../pages/useUrlState";
import { getJson } from "../client";
import type { Schemas } from "../types";

/** The products About this data lists (§11M 3.2): one per agent in `capabilities`, with its sessions in the window. */
export function aboutProducts(o: Schemas["Overview"] | undefined): AboutProductData[] | null {
  if (!o) return null;
  return Object.keys(o.capabilities).map((agent) => {
    const a = o.agents.find((x) => x.agent === agent);
    return { agent: agent as Agent, tokenAccuracy: a?.tokenAccuracy ?? "exact", sessions: a?.sessions ?? 0 };
  });
}

/** The overview's products, fetched only while About this data is open. */
export function useAboutProducts(open: boolean): AboutProductData[] | null {
  const q = useQuery({
    queryKey: ["overview", "about"],
    queryFn: ({ signal }) => getJson<Schemas["OverviewResponse"]>("/overview", signal),
    enabled: open,
    staleTime: 60_000,
  });
  return aboutProducts(q.data?.data);
}

/** "About this data" for a screen: open from the URL (`about=1`), the mode from /status, the products from the overview. */
export function aboutState(open: boolean, set: (patch: Partial<UrlState>) => void, status: Schemas["Status"] | undefined, products?: AboutProductData[] | null): AboutState {
  return { open, contentRetained: status?.contentRetained ?? null, products, onOpen: () => set({ about: true }), onClose: () => set({ about: false }) };
}
