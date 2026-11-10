import type { Meta, StoryObj } from "@storybook/react";
import sessionDemo from "../../api/samples/session_timeline/demo";
import { friction, window } from "./demoData";
import { CODEX_FRICTION_KINDS, FrictionTrack, frictionRowLabels, type Friction, type FrictionKindRow } from "./FrictionTrack";
import { Lane, LANE_HEIGHT } from "./Lane";
import { Timeline } from "./Timeline";

const meta = { title: "Components/Timeline/FrictionTrack", component: FrictionTrack, parameters: { layout: "padded" } } satisfies Meta<typeof FrictionTrack>;
export default meta;
type Story = StoryObj<typeof meta>;

const frame = (marks: Friction[], w: { from: string; to: string }, kinds?: FrictionKindRow[]) => (Story: () => JSX.Element) => (
  <Timeline {...w} label="Friction">
    <Lane label="Friction" height={LANE_HEIGHT.rows(3)} summary="friction" rows={frictionRowLabels(marks, kinds)}>
      {Story()}
    </Lane>
  </Timeline>
);

const demo = sessionDemo.data;

/** One row per kind (§11G.A1), from the person demo sample. */
export const Default: Story = { args: { marks: friction }, decorators: [frame(friction, window)] };
/** A dense session: marks under 14px apart draw as glyph + count. */
export const Clustered: Story = { args: { marks: demo.friction }, decorators: [frame(demo.friction, demo)] };
/** No friction: the rows stay, counting 0. */
export const Empty: Story = { args: { marks: [] }, decorators: [frame([], window)] };
const codexMarks: Friction[] = [
  ...friction.filter((m) => m.kind !== "frustrated"),
  ...friction.filter((m) => m.kind === "frustrated").map((m) => ({ ...m, kind: "steered" as const, detail: "steered mid-turn" })),
];
/** Codex's rows: interrupted, destructive and its own steered; no frustration (§11J). */
export const Codex: Story = { args: { marks: codexMarks, kinds: CODEX_FRICTION_KINDS }, decorators: [frame(codexMarks, window, CODEX_FRICTION_KINDS)] };
