// Parity specimens from CompDensity.dc.html (handoff round 10, "Timelines at density"):
// the sheet's own marks (densityData.ts, generated from its markup) fed back through our
// components as times, so their layout rules (row bars, friction clusters, the gap row,
// bin widths, label fits, ticks) must reproduce the sheet. The sheet is drawn for a
// 1212px track, so these timelines are 1212px wide in the track (docs/ui/deviations.json).
import type { Meta, StoryObj } from "@storybook/react";
import type { CSSProperties, ReactNode } from "react";
import { ParityTags } from "../../test/ParityTag";
import { BarTrack, gapRowLabel, type Bin, type Gap } from "./BarTrack";
import { BrushSelection } from "./Brush";
import { DENSITY } from "./densityData";
import { FrictionTrack, frictionRowLabels, type Friction } from "./FrictionTrack";
import { Lane, LANE_HEIGHT } from "./Lane";
import { RowTrack, type StateRow } from "./RowTrack";
import { ScaleContext, TrackWidthContext, makeScale } from "./scale";
import { StateTrack, type Segment } from "./StateTrack";
import { TimeAxis } from "./TimeAxis";
import { Timeline } from "./Timeline";

const TRACK = 1212;
const HOUR = 3_600_000;
const DAY = 24 * HOUR;
/** The windows the sheet's labels name: 14 days from Oct 26, 7 days, and Nov 8's 24 hours. */
const W14 = { from: new Date(2026, 9, 26).getTime(), span: 14 * DAY };
const W7 = { from: new Date(2026, 10, 2).getTime(), span: 7 * DAY };
const W24 = { from: new Date(2026, 10, 8).getTime(), span: DAY };
type Win = typeof W14;
const iso = (t: number) => new Date(t).toISOString();
const at = (w: Win, px: number) => w.from + (px / TRACK) * w.span;
const range = (w: Win) => ({ from: iso(w.from), to: iso(w.from + w.span) });

const segs = (w: Win, bars: readonly (readonly number[])[], state: string): Segment[] =>
  bars.map(([l, wd]) => ({ from: iso(at(w, l!)), to: iso(at(w, l! + wd!)), state }));
const rows = (w: Win, spec: Record<string, readonly (readonly number[])[]>, names: [string, string, StateRow["tone"]][]): StateRow[] =>
  names.map(([key, name, tone]) => ({ name, tone, segments: segs(w, spec[key]!, key) }));

/** Friction marks as incidents: a drawn `▲6` is six at its instant (the glyph's centre). */
const friction: Friction[] = (["frustrated", "interrupted", "destructive"] as const).flatMap((kind) =>
  DENSITY.friction[kind].flatMap(([left, text]) => {
    const n = Number(String(text).slice(1)) || 1;
    const t = iso(at(W14, (left as number) + 3.5));
    return Array.from({ length: n }, () => ({ t, kind, count: 1, detail: "", promptIndex: null, tool: null }));
  }),
);

/** Bars from the sheet: bins on the window's grid; heights in px of the bars' area. */
const bins = (w: Win, bars: readonly (readonly number[])[]): Bin[] => bars.map(([l, , h]) => ({ t: iso(at(w, l!)), v: h! }));
/** Hatches back to gaps (a gap's hatch covers its last bin), with counts summing to the label's 107. */
const COUNTS = [60, 30, 2, 15];
const gaps: Gap[] = DENSITY.gapLane.gaps.map(([l, wd], i) => ({ from: iso(at(W14, l!)), to: iso(at(W14, l! + wd!) - HOUR), count: COUNTS[i] ?? 1 }));

const network: Segment[] = DENSITY.network.map(([l, wd, text, danger]) => ({
  from: iso(at(W14, l as number)),
  to: iso(at(W14, (l as number) + (wd as number))),
  state: danger ? "bypassed" : "ok",
  text: (text as string | null) ?? (danger ? "net B · has IPv6 → bypassed proxy" : "net A · home · IPv4"),
}));

/** A specimen inside a timeline: no box of its own. */
const In = ({ spec, rule = false, children }: { spec: string; rule?: boolean; children: ReactNode }) => (
  <div data-parity={spec} className="contents">
    {children}
    {/* A following sibling keeps the lane's bottom rule, as the sheet draws it. */}
    {rule && <span hidden />}
  </div>
);
/** A timeline whose track is 1212px (132px labels, 1px borders). */
const Frame = ({ w, grid = false, children }: { w: Win; grid?: boolean; children: ReactNode }) => (
  <div style={{ width: 132 + TRACK + 2 }}>
    <Timeline {...range(w)} label="Specimen" gridlines={grid}>
      {children}
    </Timeline>
  </div>
);
const rowTag = (spec: string, n: number) => ({ spec, selector: `.tl-track > .tl-row:nth-of-type(${n})` });

const callLane = (spec: string, w: Win, data: { bars: readonly (readonly number[])[]; sub: string | null }, binSeconds: number, grid = false) => (
  <Frame w={w} grid={grid}>
    <In spec={spec} rule>
      <Lane label="Model calls" sublabel={data.sub ?? undefined} height={LANE_HEIGHT.bars} summary="model calls">
        <BarTrack bins={bins(w, data.bars)} binSeconds={binSeconds} color="model" maxV={LANE_HEIGHT.bars} />
      </Lane>
    </In>
  </Frame>
);

export function DensityCanvasSheet() {
  const presence = rows(W14, DENSITY.presence, [
    ["focused", "focused", "ink"],
    ["away", "away", "muted"],
    ["asleep", "device asleep", "faint"],
  ]);
  const focus = rows(W14, DENSITY.focus, [
    ["focused", "focused", "ink"],
    ["blurred", "blurred", "muted"],
  ]);
  const device = rows(W14, DENSITY.device, [["asleep", "asleep", "faint"]]);
  const axisBox: CSSProperties = { width: 132 + TRACK };
  return (
    <div className="flex flex-col items-start gap-8 p-4">
      <ParityTags targets={[rowTag("PresenceRow/focused", 1), rowTag("PresenceRow/away", 2), rowTag("PresenceRow/asleep", 3)]}>
        <Frame w={W14}>
          <In spec="Lane/presenceRows" rule>
            <Lane label="Presence" height={LANE_HEIGHT.rows(3)} summary="presence" rows={presence.map((r) => r.name)}>
              <RowTrack rows={presence} />
            </Lane>
          </In>
        </Frame>
      </ParityTags>
      <ParityTags targets={[rowTag("FocusRow/focused", 1), rowTag("FocusRow/blurred", 2)]}>
        <Frame w={W14}>
          <In spec="Lane/focusRows" rule>
            <Lane label="Focus" height={LANE_HEIGHT.rows(2)} summary="focus" rows={focus.map((r) => r.name)}>
              <RowTrack rows={focus} />
            </Lane>
          </In>
        </Frame>
      </ParityTags>
      <ParityTags targets={[rowTag("DeviceRow/asleep", 1)]}>
        <Frame w={W14}>
          <In spec="Lane/deviceSleepRow" rule>
            <Lane label="Device" height={LANE_HEIGHT.rows(1)} summary="device" rows={device.map((r) => r.name)}>
              <RowTrack rows={device} />
            </Lane>
          </In>
        </Frame>
      </ParityTags>
      <Frame w={W14}>
        <In spec="Lane/networkRow" rule>
          <Lane label="Network" height={LANE_HEIGHT.single} summary="network">
            <StateTrack segments={network} palette="network" nowrap />
          </Lane>
        </In>
      </Frame>
      <ParityTags targets={[rowTag("FrictionRow/frustrated", 1), rowTag("FrictionRow/interrupted", 2), rowTag("FrictionRow/destructive", 3)]}>
        <Frame w={W14}>
          <In spec="Lane/frictionSplit" rule>
            <Lane label="Friction" height={LANE_HEIGHT.rows(3)} summary="friction" rows={frictionRowLabels(friction)}>
              <FrictionTrack marks={friction} />
            </Lane>
          </In>
        </Frame>
      </ParityTags>
      <ParityTags targets={[{ spec: "GapRow/merged", selector: ".tl-gaprow" }]}>
        <Frame w={W14}>
          <In spec="Lane/modelCallsWithGapRow" rule>
            <Lane label="Model calls" sublabel={DENSITY.gapLane.sub ?? undefined} height={LANE_HEIGHT.barsWithGapRow} summary="model calls" footer={gapRowLabel(gaps)}>
              <BarTrack bins={bins(W14, DENSITY.gapLane.bars)} binSeconds={3600} color="model" maxV={58} gaps={gaps} gapRow />
            </Lane>
          </In>
        </Frame>
      </ParityTags>
      {callLane("Lane/modelCalls24h", W24, DENSITY.modelCalls.modelCalls24h, 300)}
      {callLane("Lane/modelCalls7d", W7, DENSITY.modelCalls.modelCalls7d, 3600)}
      {callLane("Lane/modelCalls14d", W14, DENSITY.modelCalls.modelCalls14d, 3600)}
      {callLane("Lane/modelCallsGrid14d", W14, DENSITY.modelCalls.modelCallsGrid14d, 3600, true)}
      {callLane("Lane/modelCallsGrid24h", W24, DENSITY.modelCalls.modelCallsGrid24h, 300, true)}
      {(
        [
          ["TimeAxis/ticks14d", W14],
          ["TimeAxis/ticks24h", W24],
        ] as const
      ).map(([spec, w]) => (
        <div key={spec} data-parity={spec} className="inline-grid items-start" style={axisBox}>
          <ScaleContext.Provider value={makeScale(w.from, w.from + w.span)}>
            <TrackWidthContext.Provider value={TRACK}>
              <TimeAxis />
            </TrackWidthContext.Provider>
          </ScaleContext.Provider>
        </div>
      ))}
      <ScaleContext.Provider value={makeScale(W14.from, W14.from + W14.span)}>
        <TrackWidthContext.Provider value={TRACK}>
          <ParityTags
            targets={[
              { spec: "Brush/clearButton", selector: "[data-clear='rest'] .tl-sel-clear" },
              { spec: "Brush/clearButtonHover", selector: "[data-clear='hover'] .tl-sel-clear" },
              { spec: "Brush/clearButtonFocus", selector: "[data-clear='focus'] .tl-sel-clear" },
            ]}
          >
            {(["rest", "hover", "focus"] as const).map((state) => (
              <div key={state} data-clear={state} style={{ position: "relative", width: TRACK, height: 60 }}>
                <BrushSelection from={at(W14, 602)} to={at(W14, 782)} clearForce={state === "rest" ? undefined : state} onClear={() => {}} />
              </div>
            ))}
          </ParityTags>
        </TrackWidthContext.Provider>
      </ScaleContext.Provider>
    </div>
  );
}

const meta = { title: "Canvas/Timeline/Density", parameters: { layout: "padded" }, excludeStories: ["DensityCanvasSheet"] } satisfies Meta;
export default meta;

export const Canvas: StoryObj = { render: () => <DensityCanvasSheet /> };
