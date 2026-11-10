import { useRef, useState, type KeyboardEvent, type PointerEvent } from "react";
import { formatCount } from "../../lib/format";
import "./StepStrip.css";

/** What a step did (handoff §11L.7): ran commands, changed files, called an MCP tool, or only wrote text. */
export type StepKind = "exec" | "patch" | "mcp" | "text";

export type StripStep = { kind: StepKind; /** Output tokens: the bar's height. */ output: number | null };

const KIND_LABEL: Record<StepKind, string> = { exec: "exec", patch: "apply_patch", mcp: "mcp", text: "text only" };
const KINDS: StepKind[] = ["exec", "patch", "mcp", "text"];
/** The bars' height in px; a step with no usage still shows 2px. */
const HEIGHT = 22;
const MIN_PX = 2;

export type StepStripProps = {
  /** Every step of the turn, in order: the strip always covers the whole turn. */
  steps: StripStep[];
  /** "2h 4m": the turn's span, after the step count. */
  span: string;
  /** Counts by kind, when the API has them (tool calls, not steps); else counted from `steps`. */
  totals?: Partial<Record<StepKind, number>>;
  /** The output that fills the strip's height; default the turn's largest. */
  max?: number;
  /** Jumps the step list to a step (0-based), on click, drag and the arrow keys. */
  onJump: (index: number) => void;
};

/**
 * A long turn at a glance (handoff §11L.7, more than 20 steps): one bar per step, 1px
 * apart, coloured by what the step did (exec --ink, apply_patch --ch-count, mcp
 * --ch-flags, text only --ch-model), its height set by output tokens. Totals by kind
 * sit beside it. Click or drag jumps the list; it is a slider for the keyboard.
 */
export function StepStrip({ steps, span, totals, max, onJump }: StepStripProps) {
  const bars = useRef<HTMLDivElement>(null);
  const [at, setAt] = useState(0);
  const dragging = useRef(false);
  const top = max ?? Math.max(1, ...steps.map((s) => s.output ?? 0));
  const counts = totals ?? Object.fromEntries(KINDS.map((k) => [k, steps.filter((s) => s.kind === k).length]));
  const jump = (i: number) => {
    const clamped = Math.max(0, Math.min(steps.length - 1, i));
    setAt(clamped);
    onJump(clamped);
  };
  const fromPointer = (e: PointerEvent) => {
    const box = bars.current?.getBoundingClientRect();
    if (!box || !steps.length) return;
    jump(Math.floor(((e.clientX - box.left) / box.width) * steps.length));
  };
  const onKeyDown = (e: KeyboardEvent) => {
    const step = { ArrowRight: 1, ArrowLeft: -1, PageDown: 10, PageUp: -10 }[e.key];
    if (step !== undefined) {
      e.preventDefault();
      jump(at + step);
    } else if (e.key === "Home" || e.key === "End") {
      e.preventDefault();
      jump(e.key === "Home" ? 0 : steps.length - 1);
    }
  };
  return (
    <div className="step-strip">
      <div
        ref={bars}
        role="slider"
        tabIndex={0}
        aria-label="Jump to step"
        aria-valuemin={1}
        aria-valuemax={steps.length}
        aria-valuenow={at + 1}
        aria-valuetext={`step ${at + 1} of ${steps.length}`}
        className="step-strip-bars"
        onPointerDown={(e) => {
          dragging.current = true;
          e.currentTarget.setPointerCapture?.(e.pointerId);
          fromPointer(e);
        }}
        onPointerMove={(e) => dragging.current && fromPointer(e)}
        onPointerUp={() => {
          dragging.current = false;
        }}
        onKeyDown={onKeyDown}
      >
        {steps.map((s, i) => (
          <span
            key={i}
            className={`step-strip-bar step-kind-${s.kind}`}
            style={{ height: s.output === null ? MIN_PX : Math.max(MIN_PX, Math.round((HEIGHT * s.output) / top)) }}
          />
        ))}
      </div>
      <div className="step-strip-legend text-ink-muted">
        <span>{`${formatCount(steps.length)} ${steps.length === 1 ? "step" : "steps"} · ${span}`}</span>
        {KINDS.filter((k) => (counts[k] ?? 0) > 0).map((k) => (
          <span key={k} className="step-strip-key">
            <span aria-hidden className={`step-strip-swatch step-kind-${k}`} />
            {`${KIND_LABEL[k]} ${formatCount(counts[k] ?? 0)}`}
          </span>
        ))}
        <span className="flex-1" />
        <span>click or drag to jump</span>
      </div>
    </div>
  );
}
