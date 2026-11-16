import { useMemo } from "react";
import { formatDuration } from "../../lib/format";
import { formatDayTime } from "../../lib/time";
import type { TooltipContent } from "../Tooltip/Tooltip";
import { Track } from "./Lane";
import { Mark, type MarkForce } from "./TimelineGuide";
import { ms, useTimeScale } from "./scale";
import { readoutLine, type TrackHover } from "./trackHover";

/** One turn on the session timeline: from its prompt to its last step (`to` null while it runs). */
export type TurnBlock = { id: string; index: number | null; from: string; to: string | null };

/** Hover (§6B.4): a block explains itself and opens its turn. */
export type TurnInteraction = {
  tip: (b: TurnBlock) => TooltipContent | null;
  onActivate?: (b: TurnBlock) => void;
  force?: { id: string; state: MarkForce };
};

const hm = (t: number) => formatDayTime(new Date(t).toISOString()).slice(-5);
const name = (b: TurnBlock) => (b.index === null ? "turn" : `turn #${b.index}`);

/** Empty space between blocks (§11H.2): the turn running here, else the previous one (or the next). */
export function turnsReadout(blocks: TurnBlock[], t: number, now: number) {
  let prev: TurnBlock | undefined;
  let next: TurnBlock | undefined;
  for (const b of blocks) {
    const a = ms(b.from);
    if (a <= t && (!prev || a > ms(prev.from))) prev = b;
    if (a > t && (!next || a < ms(next.from))) next = b;
  }
  if (prev && t <= (prev.to === null ? now : ms(prev.to))) return readoutLine(t, name(prev));
  const end = prev && (prev.to === null ? now : ms(prev.to));
  const hint =
    prev && end !== undefined
      ? `previous: ${name(prev)} ended ${hm(end)} · ${formatDuration(t - end)} before`
      : next && `next: ${name(next)} at ${hm(ms(next.from))} · ${formatDuration(ms(next.from) - t)} after`;
  return readoutLine(t, "no turn", hint || undefined);
}

/**
 * The session timeline's Turns lane (handoff §11R, CompTurns `TurnTrack/blocks`): one block per
 * turn from its prompt to its last step, --surface-raised with a 2px --ink left edge, 16px
 * tall at 5px, at least 3px wide. A running turn reaches `now`. Replaces the prompt ticks.
 */
export function TurnTrack({ blocks, now, interact }: { blocks: TurnBlock[]; now: string; interact?: TurnInteraction }) {
  const scale = useTimeScale();
  const end = ms(now);
  const hover = useMemo<TrackHover>(() => ({ readout: (t) => turnsReadout(blocks, t, end) }), [blocks, end]);
  return (
    <Track interactive={!!interact} hover={hover}>
      {blocks.map((b) => {
        const a = ms(b.from);
        const style = { left: `${scale.x(a)}%`, width: `max(3px, ${scale.width(a, b.to === null ? end : ms(b.to))}%)` };
        if (!interact) return <span key={b.id} className="tl-turn" style={style} />;
        return (
          <Mark
            key={b.id}
            t={a}
            tip={interact.tip(b)}
            onActivate={interact.onActivate && (() => interact.onActivate?.(b))}
            link={{ source: "timeline", ids: [b.id], t: b.from }}
            linkedId={b.id}
            label={name(b)}
            className="tl-turn"
            style={style}
            force={interact.force?.id === b.id ? interact.force.state : undefined}
          />
        );
      })}
    </Track>
  );
}
