import type { ReactNode } from "react";
import { unknownAttrs, unknownTip } from "../../lib/unknown";
import { Marker } from "../Marker/Marker";
import { Tooltip, type TooltipContent } from "../Tooltip/Tooltip";
import "./HeaderStat.css";

export type HeaderStatProps = {
  label: string;
  value: ReactNode | null;
  live?: boolean;
  /** Shown after the dash ("— not extracted") and as its tooltip. */
  unknownReason?: string;
  qualifier?: ReactNode;
  /** "gap" when the qualifier reports a coverage gap ("+94 not captured"): danger, as on the canvas. */
  qualifierTone?: "muted" | "gap" | "hidden";
  /** Makes the qualifier a button ("+94 not captured" lists those calls; handoff §11E.2). */
  onQualifier?: () => void;
  /** How the number is computed and where it comes from (§6B.7): hover fill, cursor help, roving stop. */
  tip?: TooltipContent;
  /** Pins hover for stories. */
  "data-force"?: "hover";
};

/** A label above its value, in a page header (handoff §6.3). Null renders "— reason". */
export function HeaderStat({ label, value, live, unknownReason, qualifier, qualifierTone = "muted", onQualifier, tip, "data-force": force }: HeaderStatProps) {
  const unknown = value === null || value === undefined;
  const reason = unknownReason ?? "unknown";
  const content = tip ?? (unknown ? unknownTip(label, reason) : null);
  // "hidden": calls left out by default ("+6 prewarm hidden", §11J gap 2), faint at 400.
  const tone = qualifierTone === "gap" ? "text-danger" : qualifierTone === "hidden" ? "header-stat-qualifier-hidden text-ink-faint" : "text-ink-muted";
  return (
    <Tooltip content={content}>
    <div className={`header-stat ${content ? "header-stat-tip" : ""}`} data-roving={content ? "" : undefined} data-force={force}>
      <div className="type-key text-ink-faint">{label}</div>
      {unknown ? (
        <div className="header-stat-value text-ink-muted" {...unknownAttrs(reason)}>
          — {reason}
        </div>
      ) : (
        <div className="header-stat-value text-ink">
          {/* The dot alone; the value stays this element's text. */}
          {live && (
            <>
              <Marker kind="live" compact />{" "}
            </>
          )}
          {value}
          {qualifier && qualifierTone !== "hidden" && (
            <>
              {" "}
              {onQualifier ? (
                <button type="button" onClick={onQualifier} className={`header-stat-qualifier-button ${tone}`}>
                  {qualifier}
                </button>
              ) : (
                <span className={tone}>{qualifier}</span>
              )}
            </>
          )}
        </div>
      )}
      {/* "hidden": its own line under the value, 11/14 faint (CompProviders HeaderStatGroup/session). */}
      {!unknown && qualifier && qualifierTone === "hidden" && <div className="header-stat-qualifier-hidden text-ink-faint">{qualifier}</div>}
    </div>
    </Tooltip>
  );
}
