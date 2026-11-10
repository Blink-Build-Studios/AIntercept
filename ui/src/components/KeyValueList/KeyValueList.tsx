import type { ReactNode } from "react";
import { unknownAttrs, unknownTip } from "../../lib/unknown";
import { CHANNEL_TEXT, type Channel } from "../ChannelSwatch/ChannelSwatch";
import { SkeletonBar } from "../Skeleton/Skeleton";
import { Tooltip, type TooltipContent } from "../Tooltip/Tooltip";
import "./KeyValueList.css";

export type KeyValueRow = {
  k: string;
  d?: string;
  v: ReactNode | null;
  unknownReason?: string;
  /** Where the value comes from and how often it was seen (§6B.7): `Source`, `Seen`. */
  tip?: TooltipContent;
  /** Pins hover on the row, for stories. */
  force?: "hover";
};

export type KeyValueListProps = {
  title: string;
  /** Colors the title in its channel. */
  accent?: Channel;
  action?: { label: string; href: string };
  rows: KeyValueRow[];
  footer?: string;
  loading?: boolean;
};

/** A titled list of facts: capabilities, footprint, session state (handoff §6.3). */
export function KeyValueList({ title, accent, action, rows, footer, loading }: KeyValueListProps) {
  if (loading) {
    return (
      <section aria-label={title} aria-busy className="kv-loading border border-line bg-surface">
        <SkeletonBar width="60%" height={10} />
        <SkeletonBar width="90%" faint />
        <SkeletonBar width="80%" faint />
        <SkeletonBar width="85%" faint />
      </section>
    );
  }
  return (
    <section aria-label={title} className="border border-line bg-surface">
      <header className="kv-row justify-between">
        <h3 className={`type-key ${accent ? CHANNEL_TEXT[accent] : "text-ink-faint"}`}>{title}</h3>
        {action && (
          <a href={action.href} className="type-data text-ch-model">
            {action.label}
          </a>
        )}
      </header>
      <dl className="kv-rows type-data">
        {rows.map((r) => (
          <KeyValueRowView key={r.k} row={r} data-force={r.force} />
        ))}
      </dl>
      {footer && <p className="border-t border-line px-3 py-2 type-body text-ink-faint">{footer}</p>}
    </section>
  );
}

/** One fact; with a `tip` (or an unknown value) the whole row explains itself on hover (§6B.7). */
export function KeyValueRowView({ row: r, "data-force": force }: { row: KeyValueRow; "data-force"?: "hover" }) {
  const unknown = r.v === null || r.v === undefined;
  const content = r.tip ?? (unknown ? unknownTip(r.k, r.unknownReason ?? "unknown") : null);
  return (
    <Tooltip content={content}>
      <div className={`kv-row ${content ? "kv-row-tip" : ""}`} data-force={force}>
        <dt className="kv-key type-data-strong text-ink">{r.k}</dt>
        <dd className="kv-desc flex-1 text-ink-muted">{r.d}</dd>
        {unknown ? (
          <dd className="text-ink-muted" {...unknownAttrs(r.unknownReason ?? "unknown")}>
            —
          </dd>
        ) : (
          <dd className="text-ink">{r.v}</dd>
        )}
      </div>
    </Tooltip>
  );
}
