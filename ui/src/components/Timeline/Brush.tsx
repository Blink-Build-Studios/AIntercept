import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import { formatRange } from "../../lib/time";
import { FloatingTooltip, type TooltipControls } from "../Tooltip/Tooltip";
import { ms, useTimeScale, useTrackWidth } from "./scale";
import { brushTip } from "./timelineTips";

export type Range = { from: string; to: string };

const iso = (t: number) => new Date(t).toISOString();
/** Pointer travel (px) before a press on the tracks becomes a drag rather than a click on a mark. */
const DRAG_PX = 3;

type Drag = { kind: "new" | "move" | "start" | "end"; x0: number; a0: number; b0: number; a: number; b: number; moved: boolean; onMark?: boolean };

/**
 * Select a range by dragging over the tracks (handoff §6.4, §6A.4, §6B.4). The
 * brush doesn't cover the marks: a press anywhere on the tracks that travels
 * more than 3px starts a new selection (crosshair), and a press that doesn't is
 * the mark's click. Inside the selection: grab to move it; 6px either side of an
 * edge: ew-resize (the handle grows). While dragging, a readout tooltip rides 8px
 * above the moving edge; the selection commits on release. Clearing (§11G.A8): the
 * × in the selection's corner, a click on empty track (not a mark, under 3px of
 * travel), Esc, or Delete/Backspace on the brush. Keyboard: arrows move it,
 * Shift+arrows resize it (the timeline's marks also brush with Shift+←/→).
 */
export function Brush({
  value,
  onChange,
  mode,
  force,
}: {
  value: Range | null;
  onChange: (r: Range | null) => void;
  mode: "idle" | "range" | "live";
  /** Pins a state for stories: the start handle hovered, or a drag in progress on the end edge. */
  force?: "handleHover" | "dragging";
}) {
  const scale = useTimeScale();
  const trackPx = useTrackWidth();
  const ref = useRef<HTMLDivElement>(null);
  const [drag, setDrag] = useState<Drag | null>(null);
  const span = scale.to - scale.from;
  const nudge = span / 100;
  const atX = (clientX: number) => {
    const box = ref.current?.getBoundingClientRect();
    if (!box || box.width === 0) return scale.from;
    return scale.from + Math.min(1, Math.max(0, (clientX - box.left) / box.width)) * span;
  };
  const clamp = (t: number) => Math.min(scale.to, Math.max(scale.from, t));

  // Presses on the tracks (marks included) may start a new selection.
  const latest = useRef({ value, onChange, mode, atX, clamp });
  latest.current = { value, onChange, mode, atX, clamp };
  const startRef = useRef<(d: Drag) => void>(() => {});
  useEffect(() => {
    const body = ref.current?.closest(".tl-body");
    if (!(body instanceof HTMLElement)) return undefined;
    const down = (e: PointerEvent) => {
      if (e.button !== 0 || (e.target as Element).closest(".tl-sel, .tl-sel-clear")) return;
      const t = latest.current.atX(e.clientX);
      const onMark = !!(e.target as Element).closest(".tl-mark");
      startRef.current({ kind: "new", x0: e.clientX, a0: t, b0: t, a: t, b: t, moved: false, onMark });
    };
    body.addEventListener("pointerdown", down);
    return () => body.removeEventListener("pointerdown", down);
  }, []);

  const start = (d: Drag) => {
    let cur = d;
    const move = (e: PointerEvent) => {
      if (!cur.moved && Math.abs(e.clientX - cur.x0) < DRAG_PX) return;
      const { atX: at, clamp: c, mode: m } = latest.current;
      const dt = at(e.clientX) - at(cur.x0);
      const t = at(e.clientX);
      if (cur.kind === "new") cur = { ...cur, a: cur.a0, b: m === "live" ? scale.to : t };
      else if (cur.kind === "move") {
        const w = cur.b0 - cur.a0;
        const a = c(cur.a0 + dt);
        cur = { ...cur, a: Math.min(a, scale.to - w), b: Math.min(a, scale.to - w) + w };
      } else if (cur.kind === "start") cur = { ...cur, a: c(t), b: cur.b0 };
      else cur = { ...cur, a: cur.a0, b: m === "live" ? scale.to : c(t) };
      cur = { ...cur, moved: true };
      setDrag(cur);
    };
    const up = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
      if (cur.moved) {
        // The press was a drag: swallow the click it would otherwise give the mark under it.
        window.addEventListener("click", (e) => e.stopPropagation(), { capture: true, once: true });
        const [a, b] = [Math.min(cur.a, cur.b), Math.max(cur.a, cur.b)];
        if (b - a > nudge / 4) latest.current.onChange({ from: iso(a), to: iso(b) });
      } else if (cur.kind === "new" && !cur.onMark && latest.current.value) {
        latest.current.onChange(null); // a click on empty track clears (§11G.A8)
      }
      setDrag(null);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
  };
  startRef.current = start;

  const committed = value ? ([ms(value.from), ms(value.to)] as [number, number]) : null;
  const forced: Drag | null =
    force === "dragging" && committed ? { kind: "end", x0: 0, a0: committed[0], b0: committed[1], a: committed[0], b: committed[1], moved: true } : null;
  const live = drag?.moved ? drag : forced;
  const sel = live ? ([Math.min(live.a, live.b), Math.max(live.a, live.b)] as [number, number]) : committed;
  const [a, b] = sel ?? [0, 0];
  const edge = live ? (live.kind === "start" || (live.kind === "new" && live.b < live.a) ? "start" : "end") : undefined;

  const onKey = (e: KeyboardEvent) => {
    if (e.key === "Escape" || e.key === "Delete" || e.key === "Backspace") {
      e.preventDefault();
      return onChange(null);
    }
    if (!value || (e.key !== "ArrowLeft" && e.key !== "ArrowRight")) return;
    e.preventDefault();
    const d = (e.key === "ArrowLeft" ? -1 : 1) * nudge;
    const from = ms(value.from);
    const to = ms(value.to);
    const [nextFrom, nextTo] = e.shiftKey ? [from, Math.max(from + nudge, to + d)] : [from + d, mode === "live" ? to : to + d];
    onChange({ from: iso(Math.max(scale.from, nextFrom)), to: iso(Math.min(scale.to, nextTo)) });
  };

  const grab = (kind: Drag["kind"]) => (e: React.PointerEvent) => {
    if (e.button !== 0 || !committed) return;
    e.stopPropagation();
    start({ kind, x0: e.clientX, a0: committed[0], b0: committed[1], a: committed[0], b: committed[1], moved: false });
  };

  // The × (§11G.A8) sits in the selection's corner but beside the slider, not in it:
  // a slider may not contain another control.
  const clearable = !!sel && !live && ((scale.width(a, b) / 100) * trackPx) >= 40;
  return (
    <>
    <div
      ref={ref}
      role="slider"
      tabIndex={-1}
      aria-label="Range"
      aria-valuetext={value ? formatRange(value.from, value.to) : "whole window"}
      aria-valuemin={scale.from}
      aria-valuemax={scale.to}
      aria-valuenow={value ? ms(value.from) : scale.from}
      className={`tl-brush ${live ? "tl-brush-dragging" : ""}`}
      onKeyDown={onKey}
    >
      {sel && (
        <>
          <BrushSelection
            from={a}
            to={b}
            live={mode === "live"}
            dragEdge={edge}
            hoverStart={force === "handleHover"}
            onGrab={grab("move")}
            onEdge={(which) => grab(which)}
            withClear={false}
          />
          <span className="tl-dim" style={{ left: 0, width: `${scale.x(a)}%` }} />
          <span className="tl-dim" style={{ left: `${scale.x(b)}%`, right: 0 }} />
          {live && <DragReadout el={ref.current} at={edge === "start" ? a : b} from={a} to={b} />}
        </>
      )}
    </div>
    {clearable && (
      <button
        type="button"
        className="tl-sel-clear"
        aria-label="Clear range"
        style={{ left: `calc(${scale.x(b)}% - 20px)`, right: "auto" }}
        onPointerDown={(e) => e.stopPropagation()}
        onClick={() => onChange(null)}
      >
        ×
      </button>
    )}
    </>
  );
}

/** The drag readout: a tooltip 8px above the moving edge, open only while dragging. */
function DragReadout({ el, at, from, to }: { el: HTMLElement | null; at: number; from: number; to: number }) {
  const scale = useTimeScale();
  const controls = { id: "brush-drag", open: true, surfaceHandlers: {} } as TooltipControls;
  const reference = el && {
    getBoundingClientRect: () => {
      const box = el.getBoundingClientRect();
      const x = box.left + (scale.x(at) / 100) * box.width;
      return { x, y: box.top, left: x, right: x, top: box.top, bottom: box.top, width: 0, height: 0 };
    },
  };
  return <FloatingTooltip controls={controls} content={brushTip(iso(from), iso(to))} kind="mark" reference={reference} />;
}

/**
 * The selected span: focus edges and 8x22 handles; live has no right handle (its
 * edge is now). Interactive: the body grabs (moves), 6px edge zones resize and
 * grow their handle to 10x28 with a `--focus-halo` outline; while dragging, the
 * moving edge's handle is grown (no halo).
 */
export function BrushSelection({
  from,
  to,
  live,
  dragEdge,
  hoverStart,
  onGrab,
  onEdge,
  onClear,
  clearForce,
  withClear = true,
}: {
  from: number;
  to: number;
  live?: boolean;
  dragEdge?: "start" | "end";
  hoverStart?: boolean;
  onGrab?: (e: React.PointerEvent) => void;
  onEdge?: (which: "start" | "end") => (e: React.PointerEvent) => void;
  onClear?: () => void;
  /** Pins the ×'s state (stories). */
  clearForce?: "hover" | "focus";
  /** Draw the × inside (a standalone selection); the brush draws its own beside the slider. */
  withClear?: boolean;
}) {
  const scale = useTimeScale();
  const trackPx = useTrackWidth();
  const interactive = !!onGrab;
  // The × shows once the selection is 40px wide (§11G.A8).
  const clearable = (scale.width(from, to) / 100) * trackPx >= 40;
  return (
    // Pointer-only: the keyboard brushes with the slider's arrows and Shift+←/→ on the timeline's marks.
    <span
      className={`tl-sel ${interactive ? "tl-sel-active" : ""} ${dragEdge ? "tl-sel-dragging" : ""}`}
      style={{ left: `${scale.x(from)}%`, width: `${scale.width(from, to)}%` }}
      onPointerDown={onGrab}
    >
      {interactive && !dragEdge && <span className="tl-edge tl-edge-start" onPointerDown={onEdge?.("start")} />}
      {interactive && !dragEdge && !live && <span className="tl-edge tl-edge-end" onPointerDown={onEdge?.("end")} />}
      <span className={`tl-handle tl-handle-start ${hoverStart ? "tl-handle-grow tl-handle-halo" : dragEdge === "start" ? "tl-handle-grow" : ""}`} />
      {!live && <span className={`tl-handle tl-handle-end ${dragEdge === "end" ? "tl-handle-grow" : ""}`} />}
      {withClear && clearable && !dragEdge && (
        <button
          type="button"
          className="tl-sel-clear"
          aria-label="Clear range"
          data-force={clearForce}
          onPointerDown={(e) => e.stopPropagation()}
          onClick={(e) => {
            e.stopPropagation();
            onClear?.();
          }}
        >
          ×
        </button>
      )}
    </span>
  );
}
