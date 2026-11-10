import type { ReactNode } from "react";
import { formatCount } from "../../lib/format";
import { unknownAttrs, unknownTip } from "../../lib/unknown";
import type { Channel } from "../ChannelSwatch/ChannelSwatch";
import { ChannelSwatch } from "../ChannelSwatch/ChannelSwatch";
import { SkeletonBar } from "../Skeleton/Skeleton";
import { Tooltip, type TooltipContent } from "../Tooltip/Tooltip";
import "./StatTile.css";

export type StatTileProps = {
  label: string;
  /** A number is formatted (§3.3); null renders "—" with `unknownReason`, never 0. */
  value: number | string | null;
  sub?: ReactNode;
  swatch?: Channel;
  href?: string;
  tone?: "default" | "gap";
  loading?: boolean;
  unknownReason?: string;
  /** How the figure is computed and where it comes from (§6B.7): hover fill and cursor help. */
  tip?: TooltipContent;
  /** Pins the hover state for stories. */
  "data-force"?: "hover";
};

/** One figure in a ribbon (handoff §6.3). `gap` shows a coverage gap in danger with ▲. */
export function StatTile({ label, value, sub, swatch, href, tone = "default", loading, unknownReason, tip, ...rest }: StatTileProps) {
  const reason = unknownReason ?? "unknown";
  const body = loading ? (
    <>
      <SkeletonBar width={70} height={10} />
      <SkeletonBar width={90} height={20} />
      <SkeletonBar width={130} height={10} faint />
    </>
  ) : (
    <>
      <span className={`type-key text-ink-faint ${swatch ? "stat-tile-key-swatch flex items-center" : ""}`}>
        {swatch && <ChannelSwatch channel={swatch} />}
        {label}
      </span>
      {value === null ? (
        <span className="type-num-xl text-ink-muted" {...unknownAttrs(reason)}>
          —
        </span>
      ) : (
        <span className={`type-num-xl ${tone === "gap" ? "text-danger" : "text-ink"}`}>
          {`${tone === "gap" ? "▲ " : ""}${typeof value === "number" ? formatCount(value) : value}`}
        </span>
      )}
      {(sub ?? (value === null && unknownReason)) && <span className="type-body text-ink-muted">{sub ?? unknownReason}</span>}
    </>
  );
  const content = loading ? null : (tip ?? (value === null ? unknownTip(label, reason) : null));
  const cls = `stat-tile flex min-w-0 flex-col ${loading ? "stat-tile-loading" : ""} ${content && !href ? "stat-tile-tip" : ""}`;
  return (
    <Tooltip content={content}>
      {href && !loading ? (
        <a href={href} data-force={rest["data-force"]} className={`${cls} transition-colors duration-fast`}>
          {body}
        </a>
      ) : (
        <div aria-busy={loading || undefined} data-force={rest["data-force"]} className={cls}>
          {body}
        </div>
      )}
    </Tooltip>
  );
}
