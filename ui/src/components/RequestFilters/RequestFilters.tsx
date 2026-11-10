import { useId } from "react";
import { formatCount } from "../../lib/format";
import type { Channel } from "../ChannelSwatch/ChannelSwatch";
import { ToggleChip } from "../ToggleChip/ToggleChip";
import "./RequestFilters.css";

/** A channel the product declares (handoff §11L.6), with its count under the other filters. */
export type RequestChannel = { channel: Channel; label: string; count: number };

/** Codex's channels, in order (§11L.6): model calls, analytics, metrics (OTel), usage & account, catalog. */
export const CODEX_CHANNELS: { channel: Channel; label: string }[] = [
  { channel: "model", label: "model" },
  { channel: "analytics", label: "analytics" },
  { channel: "metrics", label: "metrics" },
  { channel: "usage", label: "usage & account" },
  { channel: "catalog", label: "catalog" },
];

export type RequestFiltersProps = {
  /** The product's declared channels; only those with a count above 0 get a chip. */
  channels: RequestChannel[];
  /** The selected channels; empty is ALL. */
  value: Channel[];
  onChange: (channels: Channel[]) => void;
  /** Prewarm and title calls left out by default (§11J gap 2): the `show hidden calls (N)` checkbox. */
  hidden?: { count: number; shown: boolean; onChange: (shown: boolean) => void } | null;
};

/**
 * The Requests tab's filters (handoff §11L.6): ALL with the total, then a chip per channel
 * the product declares, only where its count is above 0. Hidden prewarm and title calls are
 * a checkbox at the end, not a chip.
 */
export function RequestFilters({ channels, value, onChange, hidden }: RequestFiltersProps) {
  const id = useId();
  const shown = channels.filter((c) => c.count > 0);
  const total = shown.reduce((n, c) => n + c.count, 0);
  const toggle = (c: Channel) => onChange(value.includes(c) ? value.filter((v) => v !== c) : [...value, c]);
  return (
    <div role="group" aria-label="Request channels" className="request-filters">
      <ToggleChip label="all" count={total} pressed={value.length === 0} onChange={() => onChange([])} />
      {shown.map((c) => (
        <ToggleChip key={c.channel} label={c.label} swatch={c.channel} count={c.count} pressed={value.includes(c.channel)} onChange={() => toggle(c.channel)} />
      ))}
      {hidden && hidden.count > 0 && <span className="flex-1" />}
      {hidden && hidden.count > 0 && (
        <label htmlFor={`${id}-hidden`} className="request-filters-hidden type-data text-ink-muted">
          <input id={`${id}-hidden`} type="checkbox" className="request-filters-check" checked={hidden.shown} onChange={(e) => hidden.onChange(e.target.checked)} />
          {`show hidden calls (${formatCount(hidden.count)})`}
        </label>
      )}
    </div>
  );
}
