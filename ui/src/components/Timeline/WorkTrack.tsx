import { Fragment } from "react";
import type { TooltipContent } from "../Tooltip/Tooltip";
import { binSlots, GapRow, gapRowHover, type BinInteraction, type ForceAt, type Gap } from "./BarTrack";
import { Track } from "./Lane";
import { Mark } from "./TimelineGuide";
import { ms, useTimeScale, useTrackWidth } from "./scale";

/** One bin of work: steps that edited files, and everything else (commands, reads, …). */
export type WorkBin = { t: string; edits: number; other: number };

export type WorkTrackProps = {
  bins: WorkBin[];
  binSeconds: number;
  /** The not-captured gaps, in their row under the bars (unchanged from the model-call lane). */
  gaps?: Gap[];
  /** The bar area's full height in bin units (default: the tallest bin). */
  maxV?: number;
  interact?: BinInteraction;
  gapTip?: (g: Gap) => TooltipContent;
  onGap?: (g: Gap) => void;
  forceGap?: ForceAt;
};

/**
 * The Work lane (handoff §11R, CompTurns `WorkTrack/stacked`): per bin, commands and reads in
 * --ink-muted at the bottom and edits in --ink stacked on top, a bin wide less 1px; the
 * not-captured row under them is the model-call lane's. Replaces the model-call bars.
 */
export function WorkTrack({ bins, binSeconds, gaps = [], maxV, interact, gapTip, onGap, forceGap }: WorkTrackProps) {
  const scale = useTimeScale();
  const trackPx = useTrackWidth();
  const top = maxV ?? Math.max(1, ...bins.map((b) => b.edits + b.other));
  const binMs = binSeconds * 1000;
  const binPx = (binMs / (scale.to - scale.from)) * trackPx;
  const width = `${((binPx >= 3 ? binPx - 1 : binPx) / trackPx) * 100}%`;
  const pct = (v: number) => (v / top) * 100;
  const bar = (b: WorkBin) => {
    const left = `${scale.x(ms(b.t))}%`;
    return (
      <Fragment key={b.t}>
        {b.other > 0 && <span className="tl-abs bottom-0 bg-ink-muted" style={{ left, width, height: `${pct(b.other)}%` }} />}
        {b.edits > 0 && <span className="tl-abs bg-ink" style={{ left, width, bottom: `${pct(b.other)}%`, height: `${pct(b.edits)}%` }} />}
      </Fragment>
    );
  };
  return (
    <Track interactive={!!interact} hover={gapRowHover}>
      <div className="tl-bars">
        {interact &&
          binSlots(scale, bins.map((b) => ({ t: b.t, v: b.edits + b.other })), binSeconds).map((s) => {
            const a = ms(s.from);
            const z = ms(s.to);
            return (
              <Mark
                key={`col-${s.from}`}
                t={(a + z) / 2}
                tip={interact.tip(s)}
                onActivate={interact.onSelect && (() => interact.onSelect?.(s))}
                link={{ source: "timeline", span: [s.from, s.to] }}
                label={interact.label(s)}
                className="tl-col"
                style={{ left: `${scale.x(a)}%`, width: `${scale.width(a, z)}%` }}
                force={interact.force && ms(interact.force.t) >= a && ms(interact.force.t) < z ? interact.force.state : undefined}
              />
            );
          })}
        {bins.map(bar)}
      </div>
      <GapRow gaps={gaps} binSeconds={binSeconds} gapTip={gapTip} onGap={onGap} forceGap={forceGap} />
    </Track>
  );
}
