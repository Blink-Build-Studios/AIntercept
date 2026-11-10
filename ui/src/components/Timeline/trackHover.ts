// Hover over empty track space (handoff §11H.2, option c): within 16px the nearest
// mark snaps (its hover style, its tooltip, its click); otherwise a one-line
// readout at the guide's time, with a hint naming the nearest mark. The guide
// follows the pointer either way.
import { createElement, useCallback, useMemo, useRef, useState, type PointerEvent, type ReactNode } from "react";
import { formatDayTime } from "../../lib/time";
import { FloatingTooltip, markAnchor, useTooltipControls, type TooltipContent } from "../Tooltip/Tooltip";
import { useTimelineHover } from "./TimelineGuide";
import { useTimeScale, type TrackMark } from "./scale";

/** Snap distance (§11H.2). */
export const SNAP_PX = 16;

/** What empty space says: a line, an optional hint (`key`: their text, to skip re-renders). */
export type LaneReadout = { key: string; content: TooltipContent; at?: number; onActivate?: () => void };

export type TrackHover = {
  /**
   * Which marks may snap at this height (px from the track's top): the snap
   * distance, and for multi-row lanes the hovered row's band (only marks in it).
   */
  zone?: (y: number, height: number) => { snapPx: number; band?: [number, number] };
  /** The readout at `t` (and `y` in a track `height` tall) when nothing snaps. */
  readout?: (t: number, y: number, height: number) => LaneReadout | null;
};

/** Multi-row lanes (§11G.A2): the 16px row under `y` (clamped to the rows there are), as a snap zone. */
export function rowZone(rows: number, top: number, height: number) {
  return (y: number) => {
    const r = Math.min(Math.max(Math.floor((y - top) / height), 0), Math.max(rows - 1, 0));
    return { row: r, snapPx: SNAP_PX, band: [top + r * height, top + (r + 1) * height] as [number, number] };
  };
}

/** `Nov 3 14:20 · no session open`, muted, and its hint (Tooltip/laneReadout). */
export function readoutLine(t: number, text: string, hint?: string): LaneReadout {
  const line = `${formatDayTime(new Date(t).toISOString())} · ${text}`;
  return { key: `${line}|${hint ?? ""}`, content: { body: createElement("span", { className: "text-ink-muted" }, line), hint } };
}

type Shown = { kind: "snap"; el: HTMLElement; mark: TrackMark; key: string; content: TooltipContent | null } | { kind: "readout"; at: number; key: string; readout: LaneReadout } | null;

const same = (a: Shown, b: Shown) =>
  a === b || (!!a && !!b && a.kind === b.kind && a.key === b.key && (a.kind !== "snap" || (b.kind === "snap" && a.el === b.el)));

/** The track's pointer handlers and the tooltip they open. */
export function useTrackHover(marks: Map<HTMLElement, TrackMark>, hover: TrackHover | undefined) {
  const scale = useTimeScale();
  const { setGuide, readout: guideText } = useTimelineHover();
  const controls = useTooltipControls("mark");
  const [shown, setShown] = useState<Shown>(null);
  const current = useRef<Shown>(null);
  const track = useRef<HTMLDivElement | null>(null);

  const unsnap = () => {
    const s = current.current;
    if (s?.kind === "snap") {
      s.el.removeAttribute("data-snapped");
      if (s.el.dataset.force === "hover") delete s.el.dataset.force;
    }
  };
  const show = (next: Shown) => {
    if (same(current.current, next)) return;
    unsnap();
    current.current = next;
    const el = track.current;
    if (next?.kind === "snap") {
      next.el.setAttribute("data-snapped", "");
      if (!next.el.dataset.force) next.el.dataset.force = "hover";
    }
    // A press here does something (a snapped mark, an empty bin): the brush mustn't clear on it.
    if (el) {
      const clicks = next?.kind === "snap" ? !!next.mark.activate : !!next?.readout.onActivate;
      if (clicks) el.setAttribute("data-hover-click", "");
      else el.removeAttribute("data-hover-click");
    }
    setShown(next);
    if (next) controls.hoverStart();
    else controls.hoverEnd();
  };

  const onPointerMove = (e: PointerEvent<HTMLDivElement>) => {
    const el = track.current;
    if (!hover || !el || e.pointerType === "touch") return;
    // On a mark itself: the mark answers.
    if ((e.target as Element).closest(".tl-mark") && !(e.target as Element).closest("[data-snapped]")) return show(null);
    const box = el.getBoundingClientRect();
    const x = e.clientX - box.left;
    const y = e.clientY - box.top;
    const t = scale.from + (Math.min(Math.max(x, 0), box.width) / box.width) * (scale.to - scale.from);
    setGuide({ t, readout: guideText(t) });
    const zone = hover.zone?.(y, box.height) ?? { snapPx: SNAP_PX };
    let best: { el: HTMLElement; mark: TrackMark; score: number } | null = null;
    for (const [m, mark] of marks) {
      if (!el.contains(m)) continue;
      const r = m.getBoundingClientRect();
      const l = r.left - box.left;
      const rr = r.right - box.left;
      const top = r.top - box.top;
      const bottom = r.bottom - box.top;
      if (zone.band && (bottom <= zone.band[0] || top >= zone.band[1])) continue;
      const dx = x < l ? l - x : x > rr ? x - rr : 0;
      if (dx > zone.snapPx) continue;
      const dy = y < top ? top - y : y > bottom ? y - bottom : 0;
      const score = dx * 1000 + dy;
      if (!best || score < best.score) best = { el: m, mark, score };
    }
    if (best) {
      if (current.current?.kind === "snap" && current.current.el === best.el) return;
      return show({ kind: "snap", el: best.el, mark: best.mark, key: "", content: best.mark.tip() });
    }
    const r = hover.readout?.(t, y, box.height) ?? null;
    show(r ? { kind: "readout", at: r.at ?? t, key: r.key, readout: r } : null);
  };
  const onPointerLeave = () => {
    if (!current.current) return;
    show(null);
    setGuide(null);
  };
  const onClick = () => {
    const s = current.current;
    if (s?.kind === "snap") s.mark.activate?.();
    else if (s?.kind === "readout") s.readout.onActivate?.();
  };

  // The readout sits over the timeline at its instant, as a mark's tooltip does.
  const reference = useMemo(() => {
    if (!shown) return null;
    if (shown.kind === "snap") return markAnchor(shown.el);
    const el = track.current;
    if (!el) return null;
    const at = shown.at;
    return {
      contextElement: el,
      getBoundingClientRect: () => {
        const box = el.getBoundingClientRect();
        const tl = (el.closest(".tl") ?? el).getBoundingClientRect();
        const x = box.left + (scale.x(at) / 100) * box.width;
        return { x, y: tl.top, left: x, right: x, top: tl.top, bottom: tl.bottom, width: 0, height: tl.height };
      },
    };
  }, [shown, scale]);
  const content = shown === null ? null : shown.kind === "snap" ? shown.content : shown.readout.content;
  const tooltip: ReactNode = content && reference ? createElement(FloatingTooltip, { controls, content, kind: "mark", reference }) : null;
  const ref = useCallback((el: HTMLDivElement | null) => {
    track.current = el;
  }, []);
  return { ref, handlers: hover ? { onPointerMove, onPointerLeave, onClick } : {}, tooltip };
}
