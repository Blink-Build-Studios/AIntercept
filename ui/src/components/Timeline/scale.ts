import { createContext, useContext } from "react";
import type { TooltipContent } from "../Tooltip/Tooltip";

/** The shared x-scale (handoff §6.4): tracks read it from context, never compute their own. */
export type TimeScale = { from: number; to: number; x: (t: number) => number; width: (a: number, b: number) => number };

export const makeScale = (from: number, to: number): TimeScale => {
  const span = Math.max(1, to - from);
  const x = (t: number) => Math.min(100, Math.max(0, ((t - from) / span) * 100));
  return { from, to, x, width: (a, b) => Math.max(0, x(b) - x(a)) };
};

export const ScaleContext = createContext<TimeScale>(makeScale(0, 1));
export const useTimeScale = () => useContext(ScaleContext);
export const ms = (iso: string) => Date.parse(iso);

/**
 * The tracks' width in px (the timeline less its 132px label column), measured by
 * Timeline: pixel rules (bar gaps, mark clusters, label fits, the bin choice) read it.
 */
export const TrackWidthContext = createContext(1210);
/** Whether tracks draw the axis's gridlines (§11G.A6): Timeline's `gridlines`. */
export const GridlinesContext = createContext(false);

/** A mark registered with its track (§11H.2): its tooltip and click, for snapping from empty space. */
export type TrackMark = { tip: () => TooltipContent | null; activate: (() => void) | null };
/** Set by an interactive Track: its marks register here by element. */
export const TrackMarksContext = createContext<Map<HTMLElement, TrackMark> | null>(null);
export const useTrackWidth = () => useContext(TrackWidthContext);
