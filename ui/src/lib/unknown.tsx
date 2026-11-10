// Unknown values (handoff §3.3, §6B.1): a dash with a reason, never 0.
import { Tooltip, unknownValue, type TooltipContent } from "../components/Tooltip/Tooltip";

export type UnknownReason = "not extracted yet" | "not sent by this surface" | "no response" | "not captured" | string;

/** The tooltip for a value that shows "—": `<label>  —  <reason>` (§6B.6 "unknown"). */
export const unknownTip = (label: string, reason: UnknownReason): TooltipContent => ({
  rows: [{ key: label, value: unknownValue(reason) }],
});

/** The accessible name for an element that itself shows "—"; pair it with `unknownTip`. */
export const unknownAttrs = (reason: UnknownReason) => ({ "aria-label": `unknown: ${reason}` });

/** "—" in the surrounding text's color, with its reason in a tooltip. Where the dash is a whole cell, put the tooltip on the cell instead. */
export function Dash({ reason, label = "Value" }: { reason: UnknownReason; label?: string }) {
  return (
    <Tooltip content={unknownTip(label, reason)}>
      <span {...unknownAttrs(reason)}>—</span>
    </Tooltip>
  );
}
