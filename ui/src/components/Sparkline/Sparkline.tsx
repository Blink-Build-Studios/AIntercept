import { AGENT_BG, type Agent } from "../AgentChip/AgentChip";
import type { Channel } from "../ChannelSwatch/ChannelSwatch";
import { FILL } from "../Timeline/BarTrack";
import "./Sparkline.css";

export type SparklineProps = {
  values: number[];
  color: Agent | Channel;
  /** Bar area height in px (default 22). */
  height?: number;
  /** A shared scale (the People table's busiest day), so rows compare; default: this line's own busiest day. */
  max?: number;
  "aria-label": string;
};

/** The shortest bar for a non-zero value, so one call still shows. */
const MIN_PX = 3;

/**
 * Daily activity as flexed bars (handoff §6.3): a non-zero day runs from 3px to
 * the full height against the busiest day (`max`); a zero day is a 1px `line` stub.
 */
export function Sparkline({ values, color, height = 22, max: shared, "aria-label": label }: SparklineProps) {
  const max = shared ?? Math.max(0, ...values);
  const fill = color in AGENT_BG ? AGENT_BG[color as Agent] : FILL[color as Channel];
  return (
    <span role="img" aria-label={label} className="sparkline" style={{ height }}>
      {values.map((v, i) => (
        <span
          key={i}
          className={`sparkline-bar ${v > 0 ? fill : "bg-line"}`}
          style={{ height: v > 0 ? MIN_PX + ((height - MIN_PX) * v) / max : 1 }}
        />
      ))}
    </span>
  );
}
