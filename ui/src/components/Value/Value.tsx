import { unknownAttrs, unknownTip } from "../../lib/unknown";
import { Tooltip } from "../Tooltip/Tooltip";
import "./Value.css";

/** Unknown (the agent reports it, we didn't get it): "—" with a dotted underline and its reason (§3.3). */
export type ValueProps = { unknown: string; label?: string };

/**
 * A value that isn't there (handoff §3.3, §11L): unknown is "—" with its reason. A field the
 * product doesn't report has no placeholder at all (§11L retired the n/a form): leave it out.
 */
export function Value({ unknown, label }: ValueProps) {
  return (
    <Tooltip content={unknownTip(label ?? "Value", unknown)}>
      <span className="value-unknown type-data text-ink-muted" {...unknownAttrs(unknown)}>
        —
      </span>
    </Tooltip>
  );
}
