import { useMemo } from "react";
import type { Schemas } from "../../api/types";
import { formatCount } from "../../lib/format";
import type { TooltipContent } from "../Tooltip/Tooltip";
import { Mark, type MarkForce } from "./TimelineGuide";
import { Track } from "./Lane";
import { ROW_HEIGHT, ROWS_TOP } from "./RowTrack";
import { ms, useTimeScale, useTrackWidth } from "./scale";
import { readoutLine, rowZone, type TrackHover } from "./trackHover";

/** A friction kind: the API's, and Codex's own `steered` (handoff §11J, a turn's steer count). */
export type FrictionKind = Schemas["FrictionMark"]["kind"] | "steered";
export type Friction = Omit<Schemas["FrictionMark"], "kind"> & { kind: FrictionKind };
type Kind = FrictionKind;
export type FrictionKindRow = { kind: Kind; glyph: string; name: string };

/** The rows, always in this order (§11G.A1). */
export const FRICTION_KINDS: FrictionKindRow[] = [
  { kind: "frustrated", glyph: "↯", name: "frustrated" },
  { kind: "interrupted", glyph: "✕", name: "interrupted" },
  { kind: "destructive", glyph: "▲", name: "destructive" },
];

/** Codex's rows (§11J): interrupted and destructive, then its own steered (in --ink); it reports no frustration. */
export const CODEX_FRICTION_KINDS: FrictionKindRow[] = [
  { kind: "interrupted", glyph: "✕", name: "interrupted" },
  { kind: "destructive", glyph: "▲", name: "destructive" },
  { kind: "steered", glyph: "↻", name: "steered" },
];

/** Marks closer than this merge into a cluster (§11G.A1). */
export const CLUSTER_PX = 14;
/** Mono 11px: about 6.6px a character (a cluster's text width, for the overlap pass). */
const CHAR_PX = 6.6;

/** One drawn mark: one incident or several merged, drawn at its first instant `t`. */
export type FrictionCluster = { kind: Kind; t: string; marks: Friction[]; count: number };

/** Hover and click (§11G.A1): a cluster explains itself; a click selects its span and jumps to its first prompt. */
export type FrictionInteraction = {
  tip: (c: FrictionCluster) => TooltipContent | null;
  onActivate?: (c: FrictionCluster) => void;
  /** Forces the cluster starting at `t` into a state (stories). */
  force?: { t: string; state: MarkForce };
};

/**
 * One kind's marks merged by distance: a mark closer than CLUSTER_PX to the previous
 * one joins its cluster; then clusters whose text would overlap merge again. Never
 * binned: a lone incident always shows.
 */
export function clusters(marks: Friction[], x: (t: number) => number): FrictionCluster[] {
  const sorted = [...marks].sort((a, b) => ms(a.t) - ms(b.t));
  type Run = { first: Friction; at: number; last: number; marks: Friction[] };
  const out: Run[] = [];
  for (const m of sorted) {
    const px = x(ms(m.t));
    const cur = out.at(-1);
    if (cur && px - cur.last < CLUSTER_PX - 1e-6) {
      cur.marks.push(m);
      cur.last = px;
    } else out.push({ first: m, at: px, last: px, marks: [m] });
  }
  const count = (r: Run) => r.marks.reduce((a, m) => a + Math.max(1, m.count), 0);
  const chars = (r: Run) => (count(r) > 1 ? 1 + String(count(r)).length : 1);
  const merged: Run[] = [];
  for (const r of out) {
    const prev = merged.at(-1);
    if (prev && r.at < prev.at + chars(prev) * CHAR_PX) prev.marks.push(...r.marks);
    else merged.push({ ...r, marks: [...r.marks] });
  }
  return merged.map((r) => ({ kind: r.first.kind, t: r.first.t, marks: r.marks, count: count(r) }));
}

/** The lane's row labels: glyph, kind and the count in the window; ▲ in danger. `kinds`: the rows the product reports. */
export function frictionRowLabels(marks: Friction[], kinds: FrictionKindRow[] = FRICTION_KINDS) {
  return kinds.map(({ kind, glyph, name }) => {
    const n = marks.filter((m) => m.kind === kind).reduce((a, m) => a + Math.max(1, m.count), 0);
    if (kind !== "destructive") return `${glyph} ${name} ${n}`;
    return n > 0 ? (
      <span key={kind} className="text-danger">{`${glyph} ${name} ${n}`}</span>
    ) : (
      <span key={kind}>
        <span className="text-danger">{`${glyph} ${name}`}</span> 0
      </span>
    );
  });
}

/**
 * Friction as one row per kind (handoff §11G.A1): ↯ frustrated, ✕ interrupted,
 * ▲ destructive. Marks are mono 11px centred on their instant; close ones draw as
 * glyph + count (`▲12`, weight 600) at the first.
 */
export function FrictionTrack({ marks, interact, kinds = FRICTION_KINDS }: { marks: Friction[]; interact?: FrictionInteraction; kinds?: FrictionKindRow[] }) {
  const scale = useTimeScale();
  const trackPx = useTrackWidth();
  const px = (t: number) => (scale.x(t) / 100) * trackPx;
  const hover = useMemo<TrackHover>(() => {
    const zone = rowZone(kinds.length, ROWS_TOP, ROW_HEIGHT);
    return {
      zone,
      // The hovered kind's row (§11H.2): none nearby, and how many there are in view.
      readout: (t, y) => {
        const k = kinds[zone(y).row];
        if (!k) return null;
        const n = marks.filter((m) => m.kind === k.kind && ms(m.t) >= scale.from && ms(m.t) <= scale.to).reduce((a, m) => a + Math.max(1, m.count), 0);
        return readoutLine(t, `no ${k.glyph} ${k.name} nearby · ${formatCount(n)} in view`);
      },
    };
  }, [marks, scale, kinds]);
  return (
    <Track interactive={!!interact} hover={hover}>
      {kinds.map(({ kind, glyph }, i) => (
        <div key={kind} className="tl-row" style={{ top: ROWS_TOP + i * ROW_HEIGHT }}>
          {clusters(
            marks.filter((m) => m.kind === kind),
            px,
          ).map((c) => {
            const t = ms(c.t);
            const text = c.count > 1 ? `${glyph}${c.count}` : glyph;
            const className = `tl-friction ${c.count > 1 ? "tl-friction-cluster" : ""} ${kind === "destructive" ? "text-danger" : ""}`;
            const style = { left: `${scale.x(t)}%` };
            if (!interact) {
              return (
                <span key={c.t} className={className} style={style}>
                  {text}
                </span>
              );
            }
            return (
              <Mark
                key={c.t}
                t={t}
                tip={interact.tip(c)}
                onActivate={interact.onActivate && (() => interact.onActivate?.(c))}
                label={`${text} ${kind}`}
                className={className}
                style={style}
                force={interact.force?.t === c.t ? interact.force.state : undefined}
              >
                {text}
              </Mark>
            );
          })}
        </div>
      ))}
    </Track>
  );
}
