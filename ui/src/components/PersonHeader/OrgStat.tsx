import type { TooltipContent } from "../Tooltip/Tooltip";
import { PersonStat, type PersonStatData } from "./PersonHeaderRow";

/**
 * A Claude Code account's organization (handoff §11L.4), against the configured customer
 * org ids: one of them (`customer`), another (`outside`: a personal org or another
 * company's), or no ids configured (`unset`: we can't tell). Codex has no org stat.
 */
export type OrgInfo =
  | { kind: "customer"; id: string }
  | { kind: "outside"; id: string | null; personal: boolean }
  | { kind: "unset"; id: string };

/** The deployment setting that names the customer's org ids. */
export const ORG_IDS_SETTING = "CUSTOMER_ORG_IDS";

/** `Tooltip/orgUnset`: why the org can't be placed, and the setting that fixes it. */
export const orgUnsetTip = (id: string): TooltipContent => ({
  lead: `Organization ${id}`,
  note: (
    <>
      AIntercept can’t tell the customer organization’s accounts from outside ones until it’s given the customer org IDs. Set them in deployment
      config (<span className="text-ink">{ORG_IDS_SETTING}</span>).
    </>
  ),
});

/** The Organization stat for a Claude Code AgentRow. */
export function orgStat(org: OrgInfo): PersonStatData {
  switch (org.kind) {
    case "customer":
      return { label: "Organization", value: "customer org", sub: org.id };
    case "outside":
      return { label: "Organization", value: "outside org", sub: [org.personal ? "personal" : null, org.id].filter(Boolean).join(" · ") || null };
    case "unset":
      return { label: "Organization", value: org.id, sub: "ours? set org IDs", tip: orgUnsetTip(org.id) };
  }
}

/** The Organization stat on its own (handoff §11L.4, CompProviders "OrgStat/*"). */
export function OrgStat({ org }: { org: OrgInfo }) {
  return <PersonStat {...orgStat(org)} />;
}
