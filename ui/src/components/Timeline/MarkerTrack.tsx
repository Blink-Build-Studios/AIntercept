import { useMemo } from "react";
import { formatDuration } from "../../lib/format";
import { formatDayTime } from "../../lib/time";
import type { TooltipContent } from "../Tooltip/Tooltip";
import { Mark, type MarkForce } from "./TimelineGuide";
import { Track } from "./Lane";
import { ms, useTimeScale } from "./scale";
import { readoutLine, type TrackHover } from "./trackHover";

/** `id`: what linked hover matches on (the prompt id); `hoverLabel`: shown on hover ("#19"). */
export type TimelineMark = { t: string; glyph?: string; tone?: "ink" | "danger" | "muted"; id?: string; hoverLabel?: string };
export type MarkLabel = { t: string; text: string };

/** Hover (§6B.4): a marker explains itself and jumps to its prompt. */
export type MarkerInteraction = {
  tip: (m: TimelineMark) => TooltipContent | null;
  onActivate?: (m: TimelineMark) => void;
  force?: { t: string; state: MarkForce };
};

const TONE = { ink: "", danger: "text-danger", muted: "text-ink-muted" };

const hm = (t: number) => formatDayTime(new Date(t).toISOString()).slice(-5);

/** The prompt ticks between ticks (§11H.2): no prompt, and the previous one (else the next). */
export function promptsReadout(markers: TimelineMark[], t: number) {
  let prev: TimelineMark | undefined;
  let next: TimelineMark | undefined;
  for (const m of markers) {
    const at = ms(m.t);
    if (at <= t && (!prev || at > ms(prev.t))) prev = m;
    if (at > t && (!next || at < ms(next.t))) next = m;
  }
  const name = (m: TimelineMark) => m.hoverLabel ?? "prompt";
  const hint = prev
    ? `previous: ${name(prev)} at ${hm(ms(prev.t))} · ${formatDuration(t - ms(prev.t))} before`
    : next && `next: ${name(next)} at ${hm(ms(next.t))} · ${formatDuration(ms(next.t) - t)} after`;
  return readoutLine(t, "no prompt", hint || undefined);
}

/**
 * Prompt ticks (1px ink, 16px), friction glyphs from their time, and optional
 * labels ("#1") (handoff §6.4). Interactive ticks get a 13px hit box (the guide
 * snaps to a marker within 6px); on hover, or when a PromptRow points at
 * them (§6B.5), they widen to 3px and show their `hoverLabel`.
 */
export function MarkerTrack({ markers, labels = [], interact }: { markers: TimelineMark[]; labels?: MarkLabel[]; interact?: MarkerInteraction }) {
  const scale = useTimeScale();
  const hover = useMemo<TrackHover>(() => ({ readout: (t) => promptsReadout(markers, t) }), [markers]);
  return (
    <Track interactive={!!interact} hover={hover}>
      {markers.map((m, i) => {
        const left = `${scale.x(ms(m.t))}%`;
        const key = `${m.t}-${i}`;
        if (!interact) {
          if (!m.glyph) return <span key={key} className="tl-tick" style={{ left }} />;
          return (
            <span key={key} className={`tl-glyph ${TONE[m.tone ?? "ink"]}`} style={{ left }}>
              {m.glyph}
            </span>
          );
        }
        const common = {
          t: ms(m.t),
          tip: interact.tip(m),
          onActivate: interact.onActivate && (() => interact.onActivate?.(m)),
          link: m.id ? { source: "timeline" as const, ids: [m.id], t: m.t } : undefined,
          linkedId: m.id,
          force: interact.force?.t === m.t ? interact.force.state : undefined,
        };
        if (m.glyph) {
          return (
            <Mark key={key} {...common} label={m.glyph} className={`tl-glyph ${TONE[m.tone ?? "ink"]}`} style={{ left }}>
              {m.glyph}
            </Mark>
          );
        }
        return (
          <Mark key={key} {...common} label={m.hoverLabel ?? "prompt"} className="tl-hit" style={{ left }}>
            <span className="tl-tick" />
            {m.hoverLabel && <span className="tl-tick-label">{m.hoverLabel}</span>}
          </Mark>
        );
      })}
      {labels.map((l) => (
        <span key={l.t} className="tl-mark-label" style={{ left: `${scale.x(ms(l.t))}%` }}>
          {l.text}
        </span>
      ))}
    </Track>
  );
}
