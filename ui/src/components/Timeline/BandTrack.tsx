import { Fragment, useMemo, type ReactNode } from "react";
import { formatDuration } from "../../lib/format";
import { partialCaptureTip } from "../PartialMark/PartialMark";
import { Tooltip, type TooltipContent } from "../Tooltip/Tooltip";
import { Mark, type MarkForce } from "./TimelineGuide";
import { Track } from "./Lane";
import { ms, useTimeScale } from "./scale";
import { readoutLine, type TrackHover } from "./trackHover";

export type Band = {
  id: string;
  from: string;
  to: string | null;
  label: string;
  row: number;
  live: boolean;
  preCapture?: boolean;
  /** The prompt capture began at (§11G.D1): the pre-capture fade's tooltip names it. */
  capturedFrom?: number | null;
};

/**
 * Band geometry from the canvas: bands that stack (CompTimeline) are 28px tall,
 * rows 34px apart from 6px down; bands that all fit one row (Main, the hover
 * sheets) are 26px from 8px down (`tl-band-single`).
 */
const ROW = 34;
const TOP = 6;
const SINGLE_TOP = 8;
const topOf = (b: Band, single: boolean) => (single ? SINGLE_TOP : TOP + b.row * ROW);
/** The pre-capture fade before a band seen mid-session, in % of the track. */
const FADE = 3;

/** Hover on the person page (§6B.4): a band explains its session and opens it. */
export type BandInteraction = {
  tip: (b: Band) => TooltipContent;
  onOpen?: (b: Band) => void;
  force?: { id: string; state: MarkForce };
};

/**
 * Session bands, overlapping sessions on separate rows (handoff §6.4). Live
 * bands run to `now` and carry a live dot at their right end; `preCapture`
 * fades in before the band's start. `inner` draws inside a band (session page only).
 * A band takes its hover style when a SessionRow points at it (§6B.5).
 */
export function BandTrack({ bands, now, inner, interact }: { bands: Band[]; now: string; inner?: (b: Band) => ReactNode; interact?: BandInteraction }) {
  const single = bands.every((b) => b.row === 0);
  const hover = useMemo<TrackHover>(() => ({ readout: (t) => sessionsReadout(bands, now, t) }), [bands, now]);
  return (
    <Track interactive={!!interact} hover={hover}>
      {bands.map((b) => (
        <Fragment key={b.id}>
          {b.preCapture && <BandFade band={b} single={single} />}
          <SessionBand band={b} now={now} interact={interact} single={single}>
            {inner?.(b)}
          </SessionBand>
        </Fragment>
      ))}
    </Track>
  );
}

/** A session id as the readout names it: `7a1e…`. */
const shortId = (id: string) => (id.length > 4 ? `${id.slice(0, 4)}…` : id);

/** The Sessions lane between bands (§11H.2): no session open, and the nearest one. */
export function sessionsReadout(bands: Band[], now: string, t: number) {
  let near: { b: Band; d: number; later: boolean } | null = null;
  for (const b of bands) {
    const a = ms(b.from);
    const e = ms(b.to ?? now);
    const n = a > t ? { b, d: a - t, later: true } : e < t ? { b, d: t - e, later: false } : null;
    if (n && (!near || n.d < near.d)) near = n;
  }
  const hint = near && `nearest: ${shortId(near.b.id)} ${near.later ? "starts" : "ended"} ${formatDuration(near.d)} ${near.later ? "later" : "earlier"}`;
  return readoutLine(t, "no session open", hint ?? undefined);
}

/** One band: `surface-raised` with a 2px agent edge, a mono label, and the live dot. */
export function SessionBand({
  band: b,
  now,
  interact,
  single = false,
  children,
}: {
  band: Band;
  now: string;
  interact?: BandInteraction;
  single?: boolean;
  children?: ReactNode;
}) {
  const scale = useTimeScale();
  const end = b.to ?? now;
  const style = { left: `${scale.x(ms(b.from))}%`, width: `${Math.max(scale.width(ms(b.from), ms(end)), 0.4)}%`, top: topOf(b, single) };
  const cls = `tl-band ${single ? "tl-band-single" : ""}`;
  const body = (
    <>
      {children}
      {b.label && <span className="tl-band-label">{b.label}</span>}
      {b.live && <span className="tl-band-live" />}
    </>
  );
  if (!interact) return <div className={cls} style={style}>{body}</div>;
  return (
    <Mark
      t={ms(b.from)}
      tip={interact.tip(b)}
      onActivate={interact.onOpen && (() => interact.onOpen?.(b))}
      link={{ source: "timeline", ids: [b.id], span: [b.from, end] }}
      linkedId={b.id}
      label={`Session ${b.label}`}
      className={cls}
      style={style}
      force={interact.force?.id === b.id ? interact.force.state : undefined}
    >
      {body}
    </Mark>
  );
}

/** The fade before a band first seen mid-session (`preCapture`). */
export function BandFade({ band: b, single = false }: { band: Band; single?: boolean }) {
  const scale = useTimeScale();
  const a = scale.x(ms(b.from));
  if (a <= 0) return null;
  const fade = (
    <div
      className={`tl-band-fade ${single ? "tl-band-single" : ""}`}
      style={{ left: `${Math.max(a - FADE, 0)}%`, width: `${Math.min(FADE, a)}%`, top: topOf(b, single) }}
    />
  );
  return b.capturedFrom ? <Tooltip content={partialCaptureTip(b.capturedFrom)}>{fade}</Tooltip> : fade;
}
