// Parity specimens from CompAgents.dc.html (handoff §11J, round 13): a Codex thread's
// timeline, its lanes (Prompts · Model calls · Approval · Network · Friction with `↻ steered`).
// Drawn on a 1210px track over 170 bins of 15 s; the marks are the sheet's, fed back as times.
import type { Meta, StoryObj } from "@storybook/react";
import type { ReactNode } from "react";
import { ParityTags } from "../../test/ParityTag";
import { BarTrack, type Bin } from "./BarTrack";
import { DENSITY } from "./densityData";
import { CODEX_FRICTION_KINDS, FrictionTrack, frictionRowLabels, type Friction } from "./FrictionTrack";
import { Lane, LANE_HEIGHT } from "./Lane";
import { MarkerTrack, type TimelineMark } from "./MarkerTrack";
import { StateTrack } from "./StateTrack";
import { Timeline } from "./Timeline";

const TRACK = 1210;
const BIN_S = 15;
const W = { from: new Date(2026, 10, 8, 14, 20).getTime(), span: 170 * BIN_S * 1000 };
const at = (px: number) => W.from + (px / TRACK) * W.span;
const iso = (t: number) => new Date(t).toISOString();
const range = { from: iso(W.from), to: iso(W.from + W.span) };

const bins: Bin[] = DENSITY.codexModelCalls.bars.map(([l, , h]) => ({ t: iso(at(l!)), v: h! }));
const ticks: TimelineMark[] = [40, 180, 300, 420, 610, 700, 880, 990, 1100].map((px) => ({ t: iso(at(px)) }));
/** Friction as incidents at the glyph's centre (the sheet places each glyph's left edge). */
const mark = (kind: Friction["kind"], left: number, n = 1): Friction[] =>
  Array.from({ length: n }, () => ({ t: iso(at(left + 3.5)), kind, count: 1, detail: "", promptIndex: null, tool: null }));
const friction: Friction[] = [...mark("interrupted", 610), ...mark("destructive", 880, 2), ...mark("steered", 300), ...mark("steered", 700), ...mark("steered", 990)];

/** A specimen inside a timeline: no box of its own. */
const In = ({ spec, children }: { spec: string; children: ReactNode }) => (
  <div data-parity={spec} className="contents">
    {children}
    <span hidden />
  </div>
);
const rowTag = (spec: string, n: number) => ({ spec, selector: `[data-parity="Lane/codexFriction"] .tl-track > .tl-row:nth-of-type(${n})` });

export function CodexTimelineSheet() {
  return (
    <div className="flex flex-col items-start gap-8 p-4">
      <ParityTags targets={[rowTag("FrictionRow/steered", 3), { spec: "TimeAxis/codex", selector: '[data-parity="Timeline/codex"] .tl-axis' }]}>
        <div data-parity="Timeline/codex" className="inline-grid items-start" style={{ gridTemplateColumns: `${132 + TRACK + 2}px` }}>
          <Timeline {...range} label="Specimen" gridlines={false}>
            <In spec="Lane/codexPrompts">
              <Lane label="Prompts" height={26} summary="prompts">
                <MarkerTrack markers={ticks} />
              </Lane>
            </In>
            <In spec="Lane/codexModelCalls">
              <Lane label="Model calls" sublabel={DENSITY.codexModelCalls.sub ?? undefined} height={LANE_HEIGHT.bars} summary="model calls">
                <BarTrack bins={bins} binSeconds={BIN_S} color="model" maxV={LANE_HEIGHT.bars} />
              </Lane>
            </In>
            <In spec="Lane/approvalSandbox">
              <Lane label="Approval" height={30} summary="approval policy and sandbox mode">
                <StateTrack palette="approval" segments={[{ from: range.from, to: iso(at(TRACK * 0.995)), state: "on-request", text: "on-request · workspace-write" }]} />
              </Lane>
            </In>
            <In spec="Lane/codexNetwork">
              <Lane label="Network" height={LANE_HEIGHT.single} summary="network">
                <StateTrack
                  palette="network"
                  segments={[
                    { from: range.from, to: iso(at(750)), state: "ok", text: "net A · home" },
                    { from: iso(at(750)), to: range.to, state: "ok", text: "net C · office" },
                  ]}
                  nowrap
                />
              </Lane>
            </In>
            <In spec="Lane/codexFriction">
              <Lane label="Friction" height={LANE_HEIGHT.rows(3)} summary="friction" rows={frictionRowLabels(friction, CODEX_FRICTION_KINDS)}>
                <FrictionTrack marks={friction} kinds={CODEX_FRICTION_KINDS} />
              </Lane>
            </In>
          </Timeline>
        </div>
      </ParityTags>
    </div>
  );
}

const meta = { title: "Canvas/Timeline/CodexTimeline", parameters: { layout: "padded" }, excludeStories: ["CodexTimelineSheet"] } satisfies Meta;
export default meta;

export const Canvas: StoryObj = { render: () => <CodexTimelineSheet /> };
