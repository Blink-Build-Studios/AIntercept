import type { CSSProperties } from "react";
import type { Guide } from "./TimelineGuide";
import { useTimeScale, useTrackWidth } from "./scale";
import { ticksFor } from "./ticks";

/** Mono 11px: about 6.6px a character, to keep the edge labels inside the track. */
const CHAR_PX = 6.6;

/**
 * The time axis (handoff §11G.A6): ticks hang from its top at round local times,
 * minor 3px, major 6px, day boundary 9px; labels on majors only, midnights as the
 * date in ink. The first and last labels align inward when centring would overflow.
 * `readout`: the guide's time, in an ink chip at the guide (§6B.4).
 */
export function TimeAxis({ readout }: { readout?: Guide }) {
  const scale = useTimeScale();
  const trackPx = useTrackWidth();
  const { minor, major, day, labels } = ticksFor(scale.from, scale.to, trackPx);
  const at = (t: number) => `${scale.x(t)}%`;
  const align = (t: number, text: string, i: number) => {
    const x = (scale.x(t) / 100) * trackPx;
    const half = (text.length * CHAR_PX) / 2;
    if (i === 0 && x < half) return "translateX(0)";
    if (i === labels.length - 1 && x > trackPx - half) return "translateX(-100%)";
    return "translateX(-50%)";
  };
  return (
    <div className="tl-axis" aria-hidden>
      <div className="tl-axis-gutter" />
      <div className="tl-axis-track">
        {minor.map((t) => (
          <div key={`n${t}`} className="tl-tick-minor" style={{ left: at(t) }} />
        ))}
        {major.map((t) => (
          <div key={`j${t}`} className="tl-tick-major" style={{ left: at(t) }} />
        ))}
        {day.map((t) => (
          <div key={`d${t}`} className="tl-tick-day" style={{ left: at(t) }} />
        ))}
        {labels.map((l, i) => (
          <span key={`l${l.t}`} className={`tl-axis-label ${l.midnight ? "tl-axis-midnight" : ""}`} style={{ left: at(l.t), transform: align(l.t, l.text, i) }}>
            {l.text}
          </span>
        ))}
        {readout && (
          <span className="tl-readout" style={{ "--at": scale.x(readout.t) / 100 } as CSSProperties}>
            {readout.readout}
          </span>
        )}
      </div>
    </div>
  );
}

/** Gridlines in a track, behind its marks (§11G.A6): majors `--line`, day boundaries `--line-strong`. Every track draws them, so they run through every lane. */
export function Gridlines() {
  const scale = useTimeScale();
  const trackPx = useTrackWidth();
  const { major, day } = ticksFor(scale.from, scale.to, trackPx);
  const days = new Set(day);
  // Majors, then day boundaries over them (as the sheet layers them).
  return (
    <>
      {major
        .filter((t) => !days.has(t))
        .map((t) => (
          <div key={t} aria-hidden className="tl-grid-major" style={{ left: `${scale.x(t)}%` }} />
        ))}
      {major
        .filter((t) => days.has(t))
        .map((t) => (
          <div key={t} aria-hidden className="tl-grid-day" style={{ left: `${scale.x(t)}%` }} />
        ))}
    </>
  );
}
