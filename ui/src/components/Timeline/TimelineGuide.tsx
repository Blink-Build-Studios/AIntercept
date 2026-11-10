// Timeline hover (handoff §6B.4): the guide through every lane, the axis
// readout, and the interactive marks (bins, bands, segments, ticks) that carry
// a tooltip and a click result. Marks are roving items of the timeline group (§6B.8).
import { createContext, useCallback, useContext, useRef, type CSSProperties, type ReactNode } from "react";
import { isLinked, linkHandlers, useLinkedSelect, useLinkedSet, type LinkedHover } from "../../lib/linkedHover";
import { Tooltip, type TooltipContent } from "../Tooltip/Tooltip";
import { TrackMarksContext, useTimeScale, type TrackMark } from "./scale";

export type Guide = { t: number; readout: string } | null;

/**
 * What marks call on hover. Stable for the timeline's life: the guide moving
 * re-renders the Timeline (its line and readout), not the marks reading this.
 */
type HoverApi = {
  setGuide: (g: Guide) => void;
  /** The guide readout for an instant ("Nov 3 10:15"). */
  readout: (t: number) => string;
};

export const TimelineHoverContext = createContext<HoverApi>({
  setGuide: () => {},
  readout: () => "",
});
export const useTimelineHover = () => useContext(TimelineHoverContext);

/**
 * The 1px guide at `t` through every lane (§6B.4); TimeAxis draws its readout.
 * No delay; snapping is the mark's job (bin centre, marker). Always mounted,
 * hidden without a guide: mounting it on each hover made the timeline's roving
 * focus rescan every mark.
 */
export function TimelineGuide({ t }: { t: number | null; readout?: string }) {
  const scale = useTimeScale();
  return <span className="tl-guide" aria-hidden hidden={t === null} style={{ "--at": t === null ? 0 : scale.x(t) / 100 } as CSSProperties} />;
}

/** A forced interaction state, for stories and parity specimens. */
export type MarkForce = "hover" | "focus" | "pressed" | "linked";

export type MarkProps = {
  /** Where the guide snaps (ms): the bin centre, the marker, the segment start. */
  t: number;
  tip?: TooltipContent | null;
  /** The click / Enter result (§6B.4); none for read-only marks. */
  onActivate?: () => void;
  /** What the linked-hover store gets while this mark is pointed at (§6B.5). */
  link?: Exclude<LinkedHover, null>;
  /** The id a table row points at to show this mark `linked` (§6B.5); `force` wins. */
  linkedId?: string;
  label: string;
  className: string;
  style?: CSSProperties;
  force?: MarkForce;
  children?: ReactNode;
};

/**
 * One interactive mark: a roving item of the timeline (Tab enters on it, arrows
 * move along its lane, ↑/↓ to the nearest mark in the next lane), its tooltip
 * (`mark` timing), the guide while pointed at or focused, and its click result
 * on click or Enter. Visual states are CSS
 * (`:hover`, `:active` = pressed in the same frame, `:focus-visible`) or `force`.
 */
export function Mark({ t, tip, onActivate, link, linkedId, label, className, style, force: forced, children }: MarkProps) {
  const { setGuide, readout } = useTimelineHover();
  const setLinked = useLinkedSet();
  const linked = useLinkedSelect((v) => !!linkedId && isLinked(v, "timeline", linkedId));
  const force = forced ?? (linked ? "linked" : undefined);
  const linkOn = link ? linkHandlers(setLinked, link) : undefined;
  // Registered with its track, so the pointer in empty space nearby can snap to it (§11H.2).
  const marks = useContext(TrackMarksContext);
  const latest = useRef({ tip, onActivate });
  latest.current = { tip, onActivate };
  const entry = useRef<TrackMark>({ tip: () => latest.current.tip ?? null, activate: null });
  entry.current.activate = onActivate ? () => latest.current.onActivate?.() : null;
  const registered = useRef<HTMLElement | null>(null);
  const ref = useCallback(
    (el: HTMLDivElement | null) => {
      if (registered.current) marks?.delete(registered.current);
      registered.current = el;
      if (el) marks?.set(el, entry.current);
    },
    [marks],
  );
  const on = () => {
    setGuide({ t, readout: readout(t) });
    linkOn?.onPointerEnter();
  };
  const off = () => {
    setGuide(null);
    linkOn?.onPointerLeave();
  };
  return (
    <Tooltip kind="mark" anchor="mark" content={tip ?? null} open={force === "hover" || force === "focus" ? true : undefined}>
      <div
        ref={ref}
        role="button"
        aria-label={label}
        tabIndex={-1}
        data-roving=""
        data-t={t}
        data-force={force}
        className={`tl-mark ${onActivate ? "tl-mark-active" : ""} ${className}`}
        style={style}
        onPointerEnter={on}
        onPointerLeave={off}
        onFocus={on}
        onBlur={off}
        onClick={onActivate}
        onKeyDown={(e) => {
          if (e.key === "Enter" && onActivate) {
            e.preventDefault();
            onActivate();
          }
        }}
      >
        {children}
      </div>
    </Tooltip>
  );
}
