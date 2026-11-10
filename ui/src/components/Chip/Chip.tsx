import { Children, type ReactNode } from "react";
import type { Channel } from "../ChannelSwatch/ChannelSwatch";
import { ChannelSwatch } from "../ChannelSwatch/ChannelSwatch";
import { Tooltip, type TooltipContent } from "../Tooltip/Tooltip";
import "./Chip.css";

export type ChipTone = "neutral" | "muted" | "range" | "notRetained" | "selected";

const TONE: Record<ChipTone, { border: string; rest: string }> = {
  neutral: { border: "border-line-strong", rest: "text-ink" },
  muted: { border: "border-line", rest: "text-ink-muted" },
  range: { border: "border-focus", rest: "text-focus" },
  notRetained: { border: "border-line", rest: "text-ink-faint" },
  selected: { border: "border-ink", rest: "chip-selected text-bg bg-ink" },
};

export type ChipProps = {
  tone?: ChipTone;
  swatch?: Channel;
  children: ReactNode;
  className?: string;
  /** What the chip means (§6B.7: NOT RETAINED, posture, range); it then takes cursor help. */
  tip?: TooltipContent;
};

/**
 * A non-interactive label (handoff §6.2). Micro caps: string children are
 * upper-cased here, so callers pass plain words. Color only through `swatch`;
 * a swatch chip takes the hairline border (CompChips "with swatch").
 */
export function Chip({ tone = "neutral", swatch, children, className = "", tip }: ChipProps) {
  const t = TONE[tone];
  return (
    <Tooltip content={tip ?? null}>
      <span className={`chip rounded-sm border ${swatch ? "border-line" : t.border} ${t.rest} ${tip ? "cursor-help" : ""} ${className}`}>
        {swatch && <ChannelSwatch channel={swatch} size="small" />}
        {Children.map(children, (c) => (typeof c === "string" ? c.toUpperCase() : c))}
      </span>
    </Tooltip>
  );
}
