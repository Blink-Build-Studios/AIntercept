import type { ReactNode } from "react";
import { formatCount } from "../../lib/format";
import { useRovingFocus } from "../../lib/rovingFocus";
import { unknownAttrs, unknownTip } from "../../lib/unknown";
import { Marker } from "../Marker/Marker";
import { Tooltip, type TooltipContent } from "../Tooltip/Tooltip";
import "./PersonHeader.css";

/**
 * One stat in a person-header row (handoff §11L.3): a key, its value (mono 500) and an
 * optional sub-line under it (the reset, the org id). A null value is one the agent
 * reports that we didn't get: "—" with `unknownReason` (§3.3). A stat an agent doesn't
 * report is left out, never shown as a placeholder.
 */
export type PersonStatData = {
  label: string;
  value: ReactNode | null;
  sub?: string | null;
  /** Explains the value (§6B.7): the stat becomes a stop in its row's roving focus. */
  tip?: TooltipContent | null;
  unknownReason?: string;
};

/** "Last seen" as the header shows it: `● now` (live), else the time in --ink-muted. */
export const lastSeenValue = (text: string, live: boolean): ReactNode =>
  live ? (
    <>
      <Marker kind="live" compact /> {text}
    </>
  ) : (
    <span className="text-ink-muted">{text}</span>
  );

/** Claude Code's Missed stat value: `▲ 94` (a coverage gap) or `0`. */
export const missedValue = (n: number): ReactNode => (n > 0 ? <Marker kind="coverageGap" inherit label={formatCount(n)} /> : "0");

export function PersonStat({ label, value, sub, tip, unknownReason = "unknown" }: PersonStatData) {
  const unknown = value === null;
  const content = tip ?? (unknown ? unknownTip(label, unknownReason) : null);
  return (
    <Tooltip content={content}>
      <div className={`person-stat ${content ? "person-stat-tip" : ""}`} data-roving={content ? "" : undefined}>
        <div className="type-key text-ink-faint">{label}</div>
        {unknown ? (
          <div className="person-stat-value value-unknown text-ink-muted" {...unknownAttrs(unknownReason)}>
            —
          </div>
        ) : (
          <div className="person-stat-value">{value}</div>
        )}
        {sub && <div className="person-stat-sub text-ink-faint">{sub}</div>}
      </div>
    </Tooltip>
  );
}

/** A row's stat area: the stats wrap within it, 8px between lines, at any width. */
export function PersonStats({ stats, label }: { stats: PersonStatData[]; label: string }) {
  const roving = useRovingFocus({ group: "stats", orientation: "horizontal" });
  return (
    <div ref={roving.ref} {...roving.props} role="group" aria-label={label} className="person-stats">
      {stats.map((s) => (
        <PersonStat key={s.label} {...s} />
      ))}
    </div>
  );
}

export type PersonHeaderRowProps = {
  /** The common stats every provider fills: Sessions, Prompts, Tokens, Devices, Last seen. */
  stats: PersonStatData[];
  /** The label column ("All agents"). */
  label?: string;
};

/**
 * The person header's first row (handoff §11L.3): a 200px label column, `ALL AGENTS`, and
 * the stats every provider fills, summed across the person's agents.
 */
export function PersonHeaderRow({ stats, label = "All agents" }: PersonHeaderRowProps) {
  return (
    <div className="person-header-row">
      <div className="person-header-label type-key text-ink-faint">{label}</div>
      <PersonStats stats={stats} label={label} />
    </div>
  );
}

/** The bordered block of rows, under the name. */
export function PersonHeaderBlock({ children }: { children: ReactNode }) {
  return <div className="person-header-block border border-line bg-surface">{children}</div>;
}
