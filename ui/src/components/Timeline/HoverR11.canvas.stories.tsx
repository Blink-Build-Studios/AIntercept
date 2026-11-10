// Parity specimens from CompR11.dc.html (handoff §11H.2, hover over empty space): the
// sessions lane with a band snapped and between bands, presence between bars, and the
// model calls lane under an empty bin's hover. Drawn on a 1210px track; the marks are
// the sheet's, fed back as times.
import type { Meta, StoryObj } from "@storybook/react";
import type { ReactNode } from "react";
import { ParityTags } from "../../test/ParityTag";
import { BarTrack, type Bin } from "./BarTrack";
import { BandTrack, type Band } from "./BandTrack";
import { DENSITY } from "./densityData";
import { Lane, LANE_HEIGHT } from "./Lane";
import { RowTrack, type StateRow } from "./RowTrack";
import { Timeline } from "./Timeline";

const TRACK = 1210;
const HOUR = 3_600_000;
/** The sheet's bins are 3.61px an hour; its windows are whatever spans the track at that. */
const W = { from: new Date(2026, 9, 26).getTime(), span: 336 * HOUR };
const at = (px: number) => W.from + (px / TRACK) * W.span;
const iso = (t: number) => new Date(t).toISOString();
const range = { from: iso(W.from), to: iso(W.from + W.span) };

const band = (id: string, label: string, left: number, width: number, row: number): Band => ({ id, label, from: iso(at(left)), to: iso(at(left + width)), row, live: false });
const snapBands = [
  band("ab055b2e", "ab055b2e · partner-readiness", 80, 300, 0),
  band("69ec40e4", "69ec40e4", 420, 160, 0),
  band("c1f2", "c1f2… · docs", 500, 260, 1),
  band("7a1e", "7a1e…", 900, 200, 2),
];
const readoutBands = [snapBands[0]!, snapBands[3]!];
const tip = () => ({ title: "" });

const seg = (l: number, w: number, state: string) => ({ from: iso(at(l)), to: iso(at(l + w)), state });
const presence: StateRow[] = [
  { name: "focused", tone: "ink", segments: [seg(40, 180, "focused"), seg(300, 220, "focused"), seg(700, 160, "focused")] },
  { name: "away", tone: "muted", segments: [seg(220, 80, "away"), seg(520, 80, "away")] },
  { name: "device asleep", tone: "faint", segments: [seg(600, 100, "asleep"), seg(860, 140, "asleep")] },
];

const bins: Bin[] = DENSITY.emptyBin.bars.map(([l, , h]) => ({ t: iso(at(l!)), v: h! }));

/** A specimen inside a timeline: no box of its own. */
const In = ({ spec, children }: { spec: string; children: ReactNode }) => (
  <div data-parity={spec} className="contents">
    {children}
    <span hidden />
  </div>
);
const Frame = ({ children }: { children: ReactNode }) => (
  <div style={{ width: 132 + TRACK + 2 }}>
    <Timeline {...range} label="Specimen" gridlines={false}>
      {children}
    </Timeline>
  </div>
);

export function HoverR11Sheet() {
  return (
    <div className="flex flex-col items-start gap-8 p-4">
      <ParityTags targets={[{ spec: "BandTrack/snapped", selector: ".tl-band[data-force='hover']" }]}>
        <Frame>
          <In spec="Lane/sessionsSnap">
            <Lane label="Sessions" height={108} summary="sessions" sunken>
              <BandTrack bands={snapBands} now={range.to} interact={{ tip, force: { id: "69ec40e4", state: "hover" } }} />
            </Lane>
          </In>
        </Frame>
      </ParityTags>
      <Frame>
        <In spec="Lane/sessionsReadout">
          <Lane label="Sessions" height={108} summary="sessions" sunken>
            <BandTrack bands={readoutBands} now={range.to} interact={{ tip }} />
          </Lane>
        </In>
      </Frame>
      <Frame>
        <In spec="Lane/presenceReadout">
          <Lane label="Presence" height={LANE_HEIGHT.rows(3)} summary="presence" rows={presence.map((r) => r.name)}>
            <RowTrack rows={presence} interact={{ tip: () => null }} />
          </Lane>
        </In>
      </Frame>
      <Frame>
        <In spec="Lane/modelCallsEmptyBin">
          <Lane label="Model calls" sublabel="per 1h" height={LANE_HEIGHT.bars} summary="model calls">
            <BarTrack
              bins={bins}
              binSeconds={3600}
              color="model"
              maxV={LANE_HEIGHT.bars}
              interact={{ tip: () => null, label: () => "bin", force: { t: iso(at((211.5 * TRACK) / 336)), state: "hover" } }}
            />
          </Lane>
        </In>
      </Frame>
    </div>
  );
}

const meta = { title: "Canvas/Timeline/HoverR11", parameters: { layout: "padded" }, excludeStories: ["HoverR11Sheet"] } satisfies Meta;
export default meta;

export const Canvas: StoryObj = { render: () => <HoverR11Sheet /> };
