import { useMemo, type CSSProperties } from "react";
import { formatDayTime } from "../../lib/time";
import { Mark } from "./TimelineGuide";
import { Track } from "./Lane";
import { ms, useTimeScale } from "./scale";
import type { Segment, StateInteraction } from "./StateTrack";
import { readoutLine, rowZone, type TrackHover } from "./trackHover";

/** One state's row: its segments as 10px bars in a 16px row (§11G.A2). */
export type StateRow = { name: string; tone: "ink" | "muted" | "faint"; segments: Segment[] };

/** Where a multi-row lane's first row starts: under the 6px padding and the 14px lane label. */
export const ROWS_TOP = 20;
export const ROW_HEIGHT = 16;

/** Row lanes between bars (§11G.A2, §11H.2): the states at that instant across the rows, and the last one before it. */
export function statesReadout(rows: StateRow[], t: number) {
  const now = rows.filter((r) => r.segments.some((s) => ms(s.from) <= t && t <= ms(s.to))).map((r) => r.name);
  let last: { name: string; to: number } | null = null;
  for (const r of rows) for (const s of r.segments) if (ms(s.to) < t && (!last || ms(s.to) > last.to)) last = { name: r.name, to: ms(s.to) };
  const hint = last ? `last: ${last.name} until ${formatDayTime(new Date(last.to).toISOString()).slice(-5)}` : undefined;
  return readoutLine(t, now.length ? now.join(" · ") : "no session open", hint);
}

/**
 * States as rows, one per state (handoff §11G.A2–A3: presence; focus and sleep on
 * the session page). No text inside segments: the lane's row labels name the
 * states, and each bar's tooltip gives its span and source. Bars are at least 2px wide.
 */
export function RowTrack({ rows, interact }: { rows: StateRow[]; interact?: StateInteraction }) {
  const scale = useTimeScale();
  const forced = (at: string) => (interact?.force?.at === at ? interact.force.state : undefined);
  const hover = useMemo<TrackHover>(() => ({ zone: rowZone(rows.length, ROWS_TOP, ROW_HEIGHT), readout: (t) => statesReadout(rows, t) }), [rows]);
  return (
    <Track interactive={!!interact} hover={hover}>
      {rows.map((row, i) => (
        <div key={row.name} className="tl-row" style={{ top: ROWS_TOP + i * ROW_HEIGHT }}>
          {row.segments.map((s) => {
            const a = ms(s.from);
            const style: CSSProperties = { left: `${scale.x(a)}%`, width: `max(2px, ${scale.width(a, ms(s.to))}%)` };
            const className = `tl-rowbar tl-rowbar-${row.tone}`;
            if (!interact) return <div key={s.from} className={className} style={style} />;
            return (
              <Mark
                key={s.from}
                t={a}
                tip={interact.tip(s)}
                onActivate={interact.onSelect && (() => interact.onSelect?.(s))}
                link={{ source: "timeline", span: [s.from, s.to] }}
                label={`${row.name}: ${s.from} to ${s.to}`}
                className={className}
                style={style}
                force={forced(s.from)}
              />
            );
          })}
        </div>
      ))}
    </Track>
  );
}
