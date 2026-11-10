// Parity specimens: the canvas's own content for every Timeline data-spec on
// CompTimeline.dc.html and CompTimelineHover.dc.html (RangeReadout has its own), measured by `make parity`;
// the other Canvas/Timeline/* stories render this same sheet. The
// bars and density bins are the sheet's own seeded generator, so every bar's
// position and height matches; positions are given in % of the window.
import type { Meta, StoryObj } from "@storybook/react";
import type { CSSProperties, ReactNode } from "react";
import { BandFade, BandTrack, SessionBand, type Band } from "./BandTrack";
import { BarTrack, type Bin } from "./BarTrack";
import { BrushSelection } from "./Brush";
import { CodexTimelineSheet } from "./CodexTimeline.canvas.stories";
import { DensityTrack } from "./DensityTrack";
import { DensityCanvasSheet } from "./Density.canvas.stories";
import { HoverR11Sheet } from "./HoverR11.canvas.stories";
import { Lane } from "./Lane";
import { MarkerTrack, type TimelineMark } from "./MarkerTrack";
import { ScaleContext, makeScale } from "./scale";
import { StateTrack } from "./StateTrack";
import { ParityTags } from "../../test/ParityTag";
import { NowLine, Timeline } from "./Timeline";
import { TimelineHoverSheet } from "./TimelineHover.canvas.stories";

// The canvas axis reads Nov 2 08:00 → Nov 4 08:00 in the viewer's zone: 48h, 100 bins.
const FROM = new Date(2026, 10, 2, 8).getTime();
const SPAN = 48 * 3_600_000;
const iso = (t: number) => new Date(t).toISOString();
/** The instant at `p`% of the window. */
const at = (p: number) => iso(FROM + Math.round((p / 100) * SPAN));
const W = { from: iso(FROM), to: iso(FROM + SPAN) };
const BIN_SECONDS = SPAN / 100 / 1000;
const scale = makeScale(FROM, FROM + SPAN);

// The sheet's renderVals(): one seeded generator shared by bars and density, in this order.
const bars: Bin[] = [];
const dens: Bin[] = [];
{
  let seed = 5;
  const rnd = () => {
    seed = (seed * 9301 + 49297) % 233280;
    return seed / 233280;
  };
  for (let x = 0; x < 100; x++) {
    const on = (x > 4 && x < 30) || (x > 36 && x < 52) || (x > 60 && x < 96);
    if (on) {
      const h = Math.round(4 + rnd() * 52);
      bars.push({ t: at(x), v: h, auxiliary: rnd() < 0.06 });
    }
    dens.push({ t: at(x), v: Math.round(on ? 10 + rnd() * 28 : 1 + rnd() * 5) });
  }
}
const gaps = [{ from: at(52), to: at(59), count: 57 }];
const overlapping: Band = { id: "c", from: at(34), to: at(44), label: "overlapping (row 2)", row: 1, live: false };
const live: Band = { id: "d", from: at(65), to: null, label: "aintercept · partner-readiness · live", row: 0, live: true, preCapture: true };
const bands: Band[] = [
  { id: "a", from: at(6), to: at(9), label: "short", row: 0, live: false },
  { id: "b", from: at(24), to: at(54), label: "ended · aintercept · main", row: 0, live: false },
  overlapping,
  live,
];
const ticks: TimelineMark[] = [10.3, 13, 17, 20, 24, 27, 38, 41, 45, 49, 60.3, 63, 66, 70, 74, 79, 84, 88, 91, 94].map((p) => ({ t: at(p) }));
/** A specimen wrapper: its first child is measured. `style` reproduces the canvas staging (panel width, a positioned box). */
const P = ({ spec, style, children }: { spec: string; style?: CSSProperties; children: ReactNode }) => (
  <div data-parity={spec} className="inline-grid items-start" style={style}>
    {children}
  </div>
);
/** A specimen inside a lane or timeline: no box of its own, so the layout is the component's. */
const In = ({ spec, rule = false, children }: { spec: string; rule?: boolean; children: ReactNode }) => (
  <div data-parity={spec} className="contents">
    {children}
    {/* A following sibling keeps the lane's bottom rule, as the sheet draws it. */}
    {rule && <span hidden />}
  </div>
);
/** The canvas `.frame`: 1100px inside a 1px border. */
const Frame = ({ children }: { children: ReactNode }) => (
  <div style={{ width: 1102 }}>
    <Timeline {...W} label="Specimen" gridlines={false}>
      {children}
    </Timeline>
  </div>
);
const col = (w: number) => ({ gridTemplateColumns: `${w}px` });

export function TimelineCanvasSheet() {
  return (
    <div className="flex flex-col items-start gap-8 p-4">
      <Frame>
        <In spec="Lane/modelCalls">
          <Lane label="Model calls" sublabel="per hour" height={60} summary="model calls per hour">
            <In spec="BarTrack/withGaps">
              <BarTrack bins={bars} binSeconds={BIN_SECONDS} color="model" gaps={gaps} maxV={60} />
            </In>
          </Lane>
        </In>
      </Frame>
      <Frame>
        <In spec="Lane/analytics">
          <Lane label="Analytics" sublabel="events/hour" height={44} summary="analytics events per hour">
            <In spec="DensityTrack/default">
              <DensityTrack bins={dens} binSeconds={BIN_SECONDS} color="analytics" maxV={44} />
            </In>
          </Lane>
        </In>
      </Frame>
      <Frame>
        <In spec="Lane/sessions">
          <Lane label="Sessions" height={76} summary="sessions" sunken>
            <In spec="BandTrack/default">
              <BandTrack bands={bands} now={W.to} />
            </In>
          </Lane>
        </In>
      </Frame>
      <Frame>
        <In spec="Lane/permission" rule>
          <Lane label="Permission" height={30} summary="permission mode">
            <In spec="StateTrack/permission">
              <StateTrack palette="permission" segments={[{ from: at(4), to: at(96), state: "bypassPermissions" }]} changes={[{ t: at(58), label: "→ default ×2 in 20 s" }]} />
            </In>
          </Lane>
        </In>
      </Frame>
      <Frame>
        <In spec="Lane/prompts" rule>
          <Lane label="Prompts" height={26} summary="prompts">
            <In spec="MarkerTrack/ticks">
              <MarkerTrack markers={ticks} labels={[{ t: at(10.3), text: "#1" }, { t: at(60.3), text: "#13" }]} />
            </In>
          </Lane>
        </In>
      </Frame>
      <ParityTags targets={[{ spec: "Brush/clearButtonOnTimeline", selector: ".tl-sel-clear" }]}>
      <P spec="Timeline/withBrush" style={col(1102)}>
        <Timeline {...W} now={at(92)} selection={{ from: at(30), to: at(45) }} onSelect={() => {}} brushMode="range" label="Specimen" gridlines={false}>
          <In spec="Lane/modelCallsUnderBrush">
            <Lane label="Model calls" height={60} summary="model calls">
              <In spec="BarTrack/default">
                <BarTrack bins={bars} binSeconds={BIN_SECONDS} color="model" maxV={60} />
              </In>
            </Lane>
          </In>
        </Timeline>
      </P>
      </ParityTags>
      <ScaleContext.Provider value={scale}>
        <P spec="BandTrack/overlappingBand" style={{ position: "relative", width: 968, height: 76 }}>
          <SessionBand band={overlapping} now={W.to} />
        </P>
        <P spec="BandTrack/preCaptureFade" style={{ position: "relative", width: 968, height: 76 }}>
          <BandFade band={live} />
        </P>
        <P spec="BandTrack/liveBand" style={{ position: "relative", width: 968, height: 76 }}>
          <SessionBand band={live} now={W.to} />
        </P>
        <P spec="Brush/selected" style={{ position: "relative", width: 968, height: 60 }}>
          <BrushSelection from={Date.parse(at(30))} to={Date.parse(at(45))} />
        </P>
        <P spec="NowLine/default" style={{ position: "relative", width: 1100, height: 60 }}>
          <NowLine t={at(92)} />
        </P>
      </ScaleContext.Provider>
      <P spec="Timeline/loading" style={col(1102)}>
        <Timeline {...W} loading label="Specimen" gridlines={false}>
          <Lane label="Prompts" height={26} summary="prompts" />
          <Lane label="Model calls" height={60} summary="model calls" />
          <Lane label="Analytics" height={44} summary="analytics" />
          <In spec="Lane/loading">
            <Lane label="Presence" height={30} summary="presence" />
          </In>
        </Timeline>
      </P>
      <TimelineHoverSheet />
      <DensityCanvasSheet />
      <HoverR11Sheet />
      <CodexTimelineSheet />
    </div>
  );
}

const meta = { title: "Canvas/Timeline/Timeline", parameters: { layout: "padded" }, excludeStories: ["TimelineCanvasSheet"] } satisfies Meta;
export default meta;

export const Canvas: StoryObj = { render: () => <TimelineCanvasSheet /> };
