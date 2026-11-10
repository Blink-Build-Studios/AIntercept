import "../Chip/Chip.css";
import "./ChannelSwatch.css";

/**
 * Every product's request channels (Codex adds metrics, usage & account, catalog: handoff §11L.6), each on a Wire
 * channel colour. `other` is the shared bucket of a person's All agents requests (§11M 3.3): whatever isn't model,
 * analytics or account & usage, on --line-strong.
 */
export type Channel = "model" | "analytics" | "flags" | "count" | "account" | "skills" | "metrics" | "usage" | "catalog" | "other";

const COLOR: Record<Channel, string> = {
  model: "bg-ch-model",
  analytics: "bg-ch-analytics",
  flags: "bg-ch-flags",
  count: "bg-ch-count",
  account: "bg-ch-account",
  skills: "bg-ch-skills",
  metrics: "bg-ch-count",
  usage: "bg-ch-account",
  catalog: "bg-ch-flags",
  other: "bg-line-strong",
};

/** Text in a channel's color (titles, kind labels). */
export const CHANNEL_TEXT: Record<Channel, string> = {
  model: "text-ch-model",
  analytics: "text-ch-analytics",
  flags: "text-ch-flags",
  count: "text-ch-count",
  account: "text-ch-account",
  skills: "text-ch-skills",
  metrics: "text-ch-count",
  usage: "text-ch-account",
  catalog: "text-ch-flags",
  other: "text-ink-faint",
};

export type ChannelSwatchProps = { channel: Channel; size?: "regular" | "small"; label?: string };

/**
 * An 8px square naming the channel something came from (handoff §6.2). Always
 * next to a label: pass `label` to render the pair (mono data, 6px gap), or
 * place the bare square beside one.
 */
export function ChannelSwatch({ channel, size = "regular", label }: ChannelSwatchProps) {
  const square = (
    <span
      aria-hidden
      className={`inline-block flex-none rounded-sm ${COLOR[channel]} ${size === "small" ? "chip-dot" : "size-2"}`}
    />
  );
  if (label === undefined) return square;
  return (
    <span className="channel-swatch-label type-data text-ink">
      {square}
      {label}
    </span>
  );
}
