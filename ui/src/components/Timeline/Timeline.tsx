import { useCallback, useLayoutEffect, useMemo, useRef, useState, type CSSProperties, type KeyboardEvent, type ReactNode } from "react";
import { useLinkedHover } from "../../lib/linkedHover";
import { useRovingFocus } from "../../lib/rovingFocus";
import { formatDayTime } from "../../lib/time";
import { Tooltip, type TooltipContent } from "../Tooltip/Tooltip";
import { Brush, type Range } from "./Brush";
import { TimelineGuide, TimelineHoverContext, type Guide } from "./TimelineGuide";
import { TimelineLoadingContext } from "./Lane";
import { GridlinesContext, ScaleContext, TrackWidthContext, makeScale, ms, useTimeScale } from "./scale";
import { TimeAxis } from "./TimeAxis";
import "./Timeline.css";

export type TimelineProps = {
  from: string;
  to: string;
  now?: string | null;
  selection?: Range | null;
  onSelect?: (r: Range | null) => void;
  /** live: the brush follows now (session page while live). */
  brushMode?: "idle" | "range" | "live";
  loading?: boolean;
  header?: ReactNode;
  label: string;
  /** The now line's tooltip ("Now · 11:32:10 · following live"). */
  nowTip?: TooltipContent;
  /** Forces the guide (stories): where it stands and the readout it shows. */
  guide?: Guide;
  /** Forces a brush state (stories). */
  brushForce?: "handleHover" | "dragging";
  /** Gridlines through the lanes (§11G.A6); off only for specimens drawn without them. */
  gridlines?: boolean;
  children: ReactNode;
};

const iso = (t: number) => new Date(t).toISOString();
const iso2 = (t: string) => new Date(ms(t)).toISOString();

/**
 * The activity timeline (handoff §6.4, §6B). Owns the x-scale; lanes and tracks
 * read it from context. Draws the brush and the now-line (when `now` is in
 * range) over the lanes, gridlines behind them, then the axis. Loading: lanes show
 * skeletons, with no brush, now-line or axis.
 *
 * Hover (§6B.4–6B.5): pointing at a mark draws the guide through every lane
 * with its readout on the axis; a row pointed at in a table shows its instant
 * (the guide) or span (a link fill) here. Keyboard (§6B.8): the marks are one
 * roving group: ←/→ along a lane, ↑/↓ between lanes, Home/End, Enter = click,
 * Shift+←/→ grows the selection from the focused mark, Esc (after the
 * tooltip's) clears the selection.
 */
export function Timeline(p: TimelineProps) {
  const { from, to, now, selection = null, onSelect, brushMode = "idle", loading = false, header, label, children } = p;
  const scale = useMemo(() => makeScale(ms(from), ms(to)), [from, to]);
  const nowMs = now ? ms(now) : null;
  const [guide, setGuide] = useState<Guide>(null);
  const [linked] = useLinkedHover();
  const readout = useCallback((t: number) => formatDayTime(iso(t)), []);
  // Stable: the marks read it, and they shouldn't re-render as the guide moves.
  const hover = useMemo(() => ({ setGuide, readout }), [readout]);
  // The tracks' width in px, for the pixel rules (bar gaps, clusters, label fits, ticks).
  const inner = useRef<HTMLDivElement>(null);
  const [trackPx, setTrackPx] = useState(1210);
  useLayoutEffect(() => {
    const el = inner.current;
    if (!el || typeof ResizeObserver === "undefined") return undefined;
    const measure = () => {
      const w = el.getBoundingClientRect().width - 132;
      if (w > 0) setTrackPx(Math.round(w * 100) / 100);
    };
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  // A table row pointed at: its instant as the guide, its span as a link fill (§6B.5).
  const fromTable = linked?.source === "table" ? linked : null;
  const shownGuide: Guide = (p.guide ?? guide) ?? (fromTable?.t ? { t: ms(fromTable.t), readout: readout(ms(fromTable.t)) } : null);

  const roving = useRovingFocus({
    group: "timeline",
    orientation: "horizontal",
    initial: (items) => {
      const start = selection ? ms(selection.from) : null;
      const at = start === null ? -1 : items.findIndex((el) => Number(el.dataset.t) >= start);
      return at >= 0 ? at : items.length - 1;
    },
    onKey: (e: KeyboardEvent, index: number, items: HTMLElement[]) => {
      const el = items[index];
      if (!el) return undefined;
      const laneOf = (x: HTMLElement) => x.closest(".tl-lane");
      const lanes = [...new Set(items.map(laneOf))];
      const lane = laneOf(el);
      const inLane = items.filter((x) => laneOf(x) === lane);
      const i = inLane.indexOf(el);
      const t = Number(el.dataset.t);
      if (e.shiftKey && (e.key === "ArrowLeft" || e.key === "ArrowRight") && onSelect) {
        // The keyboard brush: grow or shrink the selection from the focused mark.
        const next = inLane[i + (e.key === "ArrowLeft" ? -1 : 1)];
        if (!next) return null;
        const anchor = selection ? ms(selection.from) : t;
        const edge = Number(next.dataset.t);
        onSelect({ from: iso(Math.min(anchor, edge)), to: iso(Math.max(anchor, edge)) });
        return items.indexOf(next);
      }
      if (e.key === "ArrowLeft" || e.key === "ArrowRight") {
        const next = inLane[i + (e.key === "ArrowLeft" ? -1 : 1)];
        return next ? items.indexOf(next) : null;
      }
      if (e.key === "Home" || e.key === "End") {
        const end = e.key === "Home" ? inLane[0] : inLane[inLane.length - 1];
        return end ? items.indexOf(end) : null;
      }
      if (e.key === "ArrowUp" || e.key === "ArrowDown") {
        const target = lanes[lanes.indexOf(lane) + (e.key === "ArrowUp" ? -1 : 1)];
        if (!target) return null;
        const cands = items.filter((x) => laneOf(x) === target);
        const nearest = cands.reduce((a, b) => (Math.abs(Number(b.dataset.t) - t) < Math.abs(Number(a.dataset.t) - t) ? b : a));
        return items.indexOf(nearest);
      }
      if (e.key === "Escape" && selection && onSelect) {
        onSelect(null);
        return null;
      }
      return undefined;
    },
  });

  return (
    <section aria-label={label} aria-busy={loading || undefined} className="border border-line bg-surface">
      {header && <div className="flex items-center gap-3 border-b border-line px-3 py-2">{header}</div>}
      <ScaleContext.Provider value={scale}>
        <TimelineLoadingContext.Provider value={loading}>
          <TimelineHoverContext.Provider value={hover}>
            <TrackWidthContext.Provider value={trackPx}>
            <GridlinesContext.Provider value={p.gridlines ?? true}>
              <div className="tl">
                <div className="tl-inner" ref={inner}>
                  <div ref={roving.ref} className={`tl-body ${loading ? "tl-loading" : ""}`} data-brush={onSelect && !loading ? "" : undefined} {...(loading ? {} : roving.props)}>
                    {!loading && onSelect && (
                      <div className="tl-overlay">
                        <Brush value={selection} onChange={onSelect} mode={brushMode} force={p.brushForce} />
                      </div>
                    )}
                    {!loading && fromTable?.span && <LinkSpan from={iso2(fromTable.span[0])} to={iso2(fromTable.span[1])} />}
                    {!loading && now && nowMs !== null && nowMs >= scale.from && nowMs <= scale.to && <NowLine t={now} tip={p.nowTip} />}
                    {!loading && <TimelineGuide t={shownGuide?.t ?? null} />}
                    {children}
                  </div>
                  {!loading && <TimeAxis readout={shownGuide} />}
                </div>
              </div>
            </GridlinesContext.Provider>
            </TrackWidthContext.Provider>
          </TimelineHoverContext.Provider>
        </TimelineLoadingContext.Provider>
      </ScaleContext.Provider>
    </section>
  );
}

/** The 1px live line at `t` (handoff §6.4), over the tracks; Timeline draws it when `now` is in range. */
export function NowLine({ t, tip }: { t: string; tip?: TooltipContent }) {
  const scale = useTimeScale();
  const style = { "--at": scale.x(ms(t)) / 100 } as CSSProperties;
  if (!tip) return <span className="tl-now" aria-hidden style={style} />;
  return (
    <Tooltip kind="mark" content={tip}>
      <span className="tl-now tl-now-hit" style={style} />
    </Tooltip>
  );
}

/** A table row's span on the timeline (§6B.5): link fill with muted edges across every lane. */
function LinkSpan({ from, to }: { from: string; to: string }) {
  const scale = useTimeScale();
  const a = ms(from);
  const style = { left: `calc(132px + (100% - 132px) * ${scale.x(a) / 100})`, width: `calc((100% - 132px) * ${scale.width(a, ms(to)) / 100})` };
  return <span className="tl-link-span" aria-hidden style={style} />;
}
