import { createContext, useContext, useState, type ReactNode } from "react";
import { Tooltip } from "../Tooltip/Tooltip";
import { GridlinesContext, TrackMarksContext, type TrackMark } from "./scale";
import { useTrackHover, type TrackHover } from "./trackHover";
import { Gridlines } from "./TimeAxis";

export type LaneProps = {
  label: string;
  sublabel?: string;
  height: number;
  summary: string;
  /** Default: the Timeline's `loading`. */
  loading?: boolean;
  /** `bg` behind the track, so raised bands stand out (the sessions lane). */
  sunken?: boolean;
  /** What the lane shows (§6B.6 copy): the label gets a dotted underline and explains itself. */
  definition?: string;
  /** Forces the definition open (stories). */
  definitionOpen?: boolean;
  /** A multi-row lane's row names, each aligned to its 16px row (§11G.A3). */
  rows?: ReactNode[];
  /** Pinned to the label column's foot: the gap row's `▲ not captured N` (§11G.A4). */
  footer?: ReactNode;
  children?: ReactNode;
};

/** Lane heights by type (§11G.A3). */
export const LANE_HEIGHT = {
  single: 28,
  singleWithSublabel: 44,
  bars: 60,
  barsWithGapRow: 76,
  rows: (n: number) => 6 + 14 + 16 * n + 6,
};

/** Set by Timeline: every lane shows its skeleton while the timeline loads. */
export const TimelineLoadingContext = createContext(false);
const LaneContext = createContext({ label: "", summary: "" });

/**
 * One labelled track row (handoff §6.4): a 132px label and one track. `summary`
 * is the track's accessible description. Loading keeps the label and height and
 * fills the track with a skeleton bar, inset 8px and at most 40px tall.
 */
export function Lane({ label, sublabel, height, summary, loading, sunken, definition, definitionOpen, rows, footer, children }: LaneProps) {
  const timelineLoading = useContext(TimelineLoadingContext);
  const isLoading = loading ?? timelineLoading;
  return (
    <div className={`tl-lane ${sunken ? "bg-bg" : ""}`} style={{ height }}>
      <div className="tl-label">
        {definition ? (
          <Tooltip content={{ title: label, hint: definition }} open={definitionOpen}>
            <span className="tl-key tl-key-term block">{label}</span>
          </Tooltip>
        ) : (
          <Tooltip kind="truncate" content={{ body: label }}>
            <div className="tl-key">{label}</div>
          </Tooltip>
        )}
        {sublabel && <div className="tl-sublabel">{sublabel}</div>}
        {rows?.map((r, i) => (
          <div key={i} className="tl-rowlabel">
            {r}
          </div>
        ))}
        {footer && <div className="tl-label-footer">{footer}</div>}
      </div>
      {isLoading ? (
        <div className="tl-track tl-track-loading" role="img" aria-label={`${label}: loading`}>
          <div className="tl-skeleton" style={{ height: Math.min(height - 16, 40) }} />
        </div>
      ) : (
        <LaneContext.Provider value={{ label, summary }}>{children}</LaneContext.Provider>
      )}
    </div>
  );
}

/**
 * The positioned track area every track renders into; named by its lane. Interactive
 * tracks hold focusable marks, so they are a group, not an image. With `hover`, empty
 * space answers too (§11H.2): the nearest mark within 16px snaps, else a readout.
 */
export function Track({ children, interactive = false, hover }: { children: ReactNode; interactive?: boolean; hover?: TrackHover }) {
  const { label, summary } = useContext(LaneContext);
  const grid = useContext(GridlinesContext);
  const [marks] = useState(() => new Map<HTMLElement, TrackMark>());
  const h = useTrackHover(marks, interactive ? hover : undefined);
  return (
    <div ref={h.ref} className="tl-track" role={interactive ? "group" : "img"} aria-label={`${label}: ${summary}`} {...h.handlers}>
      {grid && <Gridlines />}
      <TrackMarksContext.Provider value={marks}>{children}</TrackMarksContext.Provider>
      {h.tooltip}
    </div>
  );
}
