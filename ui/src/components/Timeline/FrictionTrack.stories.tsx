import type { Meta, StoryObj } from "@storybook/react";
import sessionDemo from "../../api/samples/session_timeline/demo";
import { friction, window } from "./demoData";
import { FrictionTrack, frictionRowLabels, type Friction } from "./FrictionTrack";
import { Lane, LANE_HEIGHT } from "./Lane";
import { Timeline } from "./Timeline";

const meta = { title: "Components/Timeline/FrictionTrack", component: FrictionTrack, parameters: { layout: "padded" } } satisfies Meta<typeof FrictionTrack>;
export default meta;
type Story = StoryObj<typeof meta>;

const frame = (marks: Friction[], w: { from: string; to: string }) => (Story: () => JSX.Element) => (
  <Timeline {...w} label="Friction">
    <Lane label="Friction" height={LANE_HEIGHT.friction} summary="friction" rows={frictionRowLabels(marks)} subRows>
      {Story()}
    </Lane>
  </Timeline>
);

// The samples' marks as §11S's rows: their non-interrupt marks stand in for failed calls.
const twoRows = (marks: readonly Friction[]): Friction[] => marks.map((m) => (m.kind === "interrupted" ? m : { ...m, kind: "failed" as const, detail: "API error" }));
const demo = sessionDemo.data;
const marks = twoRows(friction);

/** Two rows (§11S): ✕ interrupted, ! failed calls; from the person demo sample. */
export const Default: Story = { args: { marks }, decorators: [frame(marks, window)] };
/** A dense session: marks under 14px apart draw as glyph + count. */
export const Clustered: Story = { args: { marks: twoRows(demo.friction) }, decorators: [frame(twoRows(demo.friction), demo)] };
/** No friction: the rows stay, counting 0. */
export const Empty: Story = { args: { marks: [] }, decorators: [frame([], window)] };
/** Codex: the same two rows. */
export const Codex: Story = { args: { marks: marks.slice(0, 4) }, decorators: [frame(marks.slice(0, 4), window)] };
