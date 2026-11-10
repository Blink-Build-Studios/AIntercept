import { Fragment, useMemo } from "react";
import { formatCount } from "../../lib/format";
import type { Channel } from "../ChannelSwatch/ChannelSwatch";
import type { TooltipContent } from "../Tooltip/Tooltip";
import { Mark, type MarkForce } from "./TimelineGuide";
import { Track } from "./Lane";
import type { MarkLabel } from "./MarkerTrack";
import { ms, useTimeScale, useTrackWidth, type TimeScale } from "./scale";
import { readoutLine, SNAP_PX, type TrackHover } from "./trackHover";

export type Bin = { t: string; v: number; auxiliary?: boolean };
export type Gap = { from: string; to: string; count: number; /** Merged on screen (§11G.A4): the windows it holds. */ parts?: Gap[] };
/** One bin of the window, with its data when it has any (a missing bin is zero). */
export type BinSlot = { from: string; to: string; bin?: Bin };
/** Forces a mark's interaction state at an instant (stories, parity specimens). */
export type ForceAt = { t: string; state: MarkForce };

/** Clip a label at the next one's start (in % of the track) so close labels don't overprint. */
export const clipBefore = (left: number, next: number | undefined, pad = 8) =>
  next === undefined ? {} : { maxWidth: `max(calc(${next - left}% - ${pad}px), 0px)`, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" as const };

export const FILL: Record<Channel, string> = {
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

/** Bins across the window at `binSeconds`, aligned to the bin grid the API uses. */
export function binSlots(scale: TimeScale, bins: Bin[], binSeconds: number): BinSlot[] {
  const binMs = binSeconds * 1000;
  // Slots run on the bins' own grid (stepped back to cover the window's start); with no bins, on the epoch's.
  const anchor = bins.length ? Math.min(...bins.map((b) => ms(b.t))) : Math.floor(scale.from / binMs) * binMs;
  const first = anchor - Math.ceil(Math.max(0, anchor - scale.from) / binMs) * binMs;
  const n = Math.max(0, Math.ceil((scale.to - first) / binMs));
  const byIndex = new Map(bins.map((b) => [Math.round((ms(b.t) - first) / binMs), b]));
  return Array.from({ length: n }, (_, k) => ({
    from: new Date(first + k * binMs).toISOString(),
    to: new Date(first + (k + 1) * binMs).toISOString(),
    bin: byIndex.get(k),
  }));
}

/** Interactive bins (handoff §6B.4): a hit column per bin, its tooltip and its click result. */
export type BinInteraction = {
  /** The bin's tooltip; null for none. */
  tip: (s: BinSlot) => TooltipContent | null;
  /** The accessible name ("Nov 3 10:00–10:30: 38 model calls"). */
  label: (s: BinSlot) => string;
  /** Click / Enter: select the bin as the range. */
  onSelect?: (s: BinSlot) => void;
  force?: ForceAt;
};

const forcedAt = (force: ForceAt | undefined, s: BinSlot) =>
  force && ms(force.t) >= ms(s.from) && ms(force.t) < ms(s.to) ? force.state : undefined;

type BarsProps = {
  bins: Bin[];
  binSeconds: number;
  maxV?: number;
  className: (b: Bin) => string;
  headroom?: number;
  interact?: BinInteraction;
  /** A level track (context): the pointed bin's bar turns ink over a faint column. */
  level?: boolean;
};

/**
 * Bars from the bin's start, a bin wide less 1px (§11G.A5), height against
 * `maxV` (default: the tallest bin). `headroom` px stay clear above the tallest bar.
 * With `interact`, each bin of the window gets a hit column behind its bar.
 */
export function Bars({ bins, binSeconds, maxV, className, headroom = 0, interact, level = false }: BarsProps) {
  const scale = useTimeScale();
  const trackPx = useTrackWidth();
  const top = maxV ?? Math.max(1, ...bins.map((b) => b.v));
  const binMs = binSeconds * 1000;
  const bar = (b: Bin, hover?: boolean) => {
    const start = ms(b.t);
    // A bin's own width less a 1px gap (none below 3px a bin; §11G.A5), unclamped: a bar
    // running past the window's end is clipped by the track.
    const binPx = (binMs / (scale.to - scale.from)) * trackPx;
    const w = ((binPx >= 3 ? binPx - 1 : binPx) / trackPx) * 100;
    return (
      <span
        key={`bar-${b.t}`}
        // Hovered, the bar drops its fill utility (utilities outrank the component layer's ink).
        className={`tl-abs bottom-0 ${hover ? "tl-bar-hover" : className(b)}`}
        style={{
          left: `${scale.x(start)}%`,
          width: `${w}%`,
          height: headroom ? `calc((100% - ${headroom}px) * ${Math.max(b.v / top, 0.02)})` : `${Math.max((b.v / top) * 100, 2)}%`,
        }}
      />
    );
  };
  if (!interact) return <>{bins.map((b) => bar(b))}</>;
  // Columns first (behind every bar), then the bars. A level track inks the pointed bin's bar,
  // and draws each bin's column just before its bar (the Session sheet's order).
  const slots = binSlots(scale, bins, binSeconds);
  const active = slots.find((s) => forcedAt(interact.force, s) && forcedAt(interact.force, s) !== "pressed");
  // Pointed at, the bar is inked by CSS (its column is the sibling before it), so
  // hovering doesn't re-render the lane; `force` (stories) inks it here.
  const inked = (b: Bin) => level && b.t === active?.bin?.t;
  const column = (s: BinSlot) => {
    const a = ms(s.from);
    const b = ms(s.to);
    return (
          <Mark
            key={`col-${s.from}`}
            t={(a + b) / 2}
            tip={interact.tip(s)}
            onActivate={interact.onSelect && (() => interact.onSelect?.(s))}
            link={{ source: "timeline", span: [s.from, s.to] }}
            label={interact.label(s)}
            className={`tl-col ${level ? "tl-col-level" : ""}`}
            style={{ left: `${scale.x(a)}%`, width: `${scale.width(a, b)}%` }}
            force={forcedAt(interact.force, s)}
          />
    );
  };
  if (level) {
    const slotted = new Set(slots.flatMap((s) => (s.bin ? [s.bin.t] : [])));
    return (
      <>
        {slots.map((s) => (
          <Fragment key={s.from}>
            {column(s)}
            {s.bin && bar(s.bin, inked(s.bin))}
          </Fragment>
        ))}
        {bins.filter((b) => !slotted.has(b.t)).map((b) => bar(b))}
      </>
    );
  }
  return (
    <>
      {slots.map(column)}
      {bins.map((b) => bar(b))}
    </>
  );
}

export type BarTrackProps = {
  bins: Bin[];
  binSeconds: number;
  color: Channel;
  gaps?: Gap[];
  /**
   * The not-captured gaps in their own 16px row under the bars (§11G.A4; the model
   * calls lane): merged when under 4px apart, the row's count in the lane label
   * (`gapRowLabel`). The row stays when there are none.
   */
  gapRow?: boolean;
  maxV?: number;
  /**
   * A level rather than a count (the session page's context lane): bars at 70% ink
   * stop 4px under the top, as on the Session sheet.
   */
  level?: boolean;
  /** Ink labels from their time, over the bars ("⇣ compaction 903k → 64,133 · 1m 11s"). */
  labels?: MarkLabel[];
  interact?: BinInteraction;
  /** Interactive gaps: their tooltip and click result (list the not-captured calls). */
  gapTip?: (g: Gap) => TooltipContent;
  onGap?: (g: Gap) => void;
  /** Interactive labels (compactions): their tooltip and click result (jump to it in Prompts). */
  labelTip?: (l: MarkLabel) => TooltipContent | null;
  onLabel?: (l: MarkLabel) => void;
  forceGap?: ForceAt;
  /** Pins a state on the label at this time, for stories. */
  forceLabel?: ForceAt;
};

/** The gap row: the track's bottom 18px, under the bars. */
const GAP_ROW_PX = 18;

/** Gaps closer than this on screen merge (§11G.A4). */
export const GAP_MERGE_PX = 4;

/** Gaps merged on screen: a gap starting within GAP_MERGE_PX of the last one's end joins it, counts summed. */
export function mergeGaps(gaps: Gap[], binMs: number, px: (t: number) => number): Gap[] {
  const sorted = [...gaps].sort((a, b) => ms(a.from) - ms(b.from));
  const out: Gap[] = [];
  for (const g of sorted) {
    const prev = out[out.length - 1];
    if (prev && px(ms(g.from)) - px(ms(prev.to) + binMs) < GAP_MERGE_PX) {
      const parts = [...(prev.parts ?? [prev]), g];
      out[out.length - 1] = { from: prev.from, to: ms(g.to) > ms(prev.to) ? g.to : prev.to, count: prev.count + g.count, parts };
    } else out.push(g);
  }
  return out;
}

/** The gap row's label (§11G.A4): `▲ not captured N` in danger, or `not captured 0`. */
export function gapRowLabel(gaps: Gap[]) {
  const n = gaps.reduce((a, g) => a + g.count, 0);
  return <span className={`block ${n > 0 ? "text-danger" : "text-ink-muted"}`}>{n > 0 ? `▲ not captured ${formatCount(n)}` : "not captured 0"}</span>;
}

/**
 * Bars a bin wide less 1px (handoff §6.4, §11G.A5). Not-captured gaps (reported,
 * not captured) sit in their own row under the bars, hatched in danger with 1px
 * danger ends, at least 3px wide; the lane label carries their count.
 * Hover (§6B.4): bins, gaps and labels explain themselves and click through.
 */
export function BarTrack({ bins, binSeconds, color, gaps = [], gapRow = false, maxV, level = false, labels = [], interact, gapTip, onGap, labelTip, onLabel, forceGap, forceLabel }: BarTrackProps) {
  const scale = useTimeScale();
  const trackPx = useTrackWidth();
  const binMs = binSeconds * 1000;
  const sortedLabels = [...labels].sort((a, b) => ms(a.t) - ms(b.t));
  const at = (t: string | undefined) => (t === undefined ? undefined : scale.x(ms(t)));
  const px = (t: number) => (scale.x(t) / 100) * trackPx;
  const bars = (
    <Bars
      bins={bins}
      binSeconds={binSeconds}
      maxV={maxV}
      headroom={level ? 4 : 0}
      level={level}
      interact={interact}
      className={(b) => `${b.auxiliary ? "bg-line-strong" : FILL[color]} ${level ? "tl-bar-level" : ""}`}
    />
  );
  // The gap row (§11H.2): gaps within 16px snap; else every reported call was captured.
  // The bars need nothing: each bin's column answers, empty ones with 0.
  const hover = useMemo<TrackHover | undefined>(
    () =>
      gapRow
        ? {
            zone: (y, h) => (y >= h - GAP_ROW_PX ? { snapPx: SNAP_PX, band: [h - GAP_ROW_PX, Infinity] } : { snapPx: 0 }),
            readout: (t, y, h) => (y >= h - GAP_ROW_PX ? readoutLine(t, "all reported calls captured") : null),
          }
        : undefined,
    [gapRow],
  );
  return (
    <Track interactive={!!interact} hover={hover}>
      {gapRow ? <div className="tl-bars">{bars}</div> : bars}
      {gapRow && (
        <div className="tl-gaprow">
          {mergeGaps(gaps, binMs, px).map((g) => {
            const a = ms(g.from);
            const style = { left: `${scale.x(a)}%`, width: `max(3px, ${scale.width(a, ms(g.to) + binMs)}%)` };
            const label = `▲ ${formatCount(g.count)} reported, not captured`;
            return gapTip ? (
              <Mark
                key={g.from}
                t={a}
                tip={gapTip(g)}
                onActivate={onGap && (() => onGap(g))}
                link={{ source: "timeline", span: [g.from, g.to] }}
                label={label}
                className="tl-gap"
                style={style}
                force={forceGap && ms(forceGap.t) >= a && ms(forceGap.t) <= ms(g.to) ? forceGap.state : undefined}
              />
            ) : (
              <div key={g.from} className="tl-gap" style={style} />
            );
          })}
        </div>
      )}
      {sortedLabels.map((l, i) => {
        const left = scale.x(ms(l.t));
        const style = { left: `${left}%`, ...clipBefore(left, at(sortedLabels[i + 1]?.t)) };
        const tip = labelTip?.(l);
        if (tip) {
          return (
            <Mark key={l.t} t={ms(l.t)} tip={tip} onActivate={onLabel && (() => onLabel(l))} label={l.text}
              className="tl-bar-label tl-compaction"
              style={style}
              force={forceLabel && ms(forceLabel.t) === ms(l.t) ? forceLabel.state : undefined}
            >
              {l.text}
            </Mark>
          );
        }
        return (
          <span key={l.t} className="tl-bar-label" style={style}>
            {l.text}
          </span>
        );
      })}
    </Track>
  );
}
