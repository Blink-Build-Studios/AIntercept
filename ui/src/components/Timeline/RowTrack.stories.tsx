import type { Meta, StoryObj } from "@storybook/react";
import sessionDemo from "../../api/samples/session_timeline/demo";
import { presenceRows, window } from "./demoData";
import { Lane, LANE_HEIGHT } from "./Lane";
import { RowTrack, type StateRow } from "./RowTrack";
import { sessionLanes } from "./sessionLanes";
import { Timeline } from "./Timeline";

const meta = { title: "Components/Timeline/RowTrack", component: RowTrack, parameters: { layout: "padded" } } satisfies Meta<typeof RowTrack>;
export default meta;
type Story = StoryObj<typeof meta>;

const frame = (label: string, w: { from: string; to: string }, rows: StateRow[]) => (Story: () => JSX.Element) => (
  <Timeline {...w} label={label}>
    <Lane label={label} height={LANE_HEIGHT.rows(rows.length)} summary={label} rows={rows.map((r) => r.name)}>
      {Story()}
    </Lane>
  </Timeline>
);

const demo = sessionDemo.data;
const lanes = sessionLanes(demo);

/** The person page's Presence (§11G.A2): focused, away, device asleep, from the demo sample. */
export const Presence: Story = { args: { rows: presenceRows }, decorators: [frame("Presence", window, presenceRows)] };
/** The session page's Focus (§11G.A3): focused and blurred. */
export const Focus: Story = { args: { rows: lanes.focusRows }, decorators: [frame("Focus", demo, lanes.focusRows)] };
/** The session page's Device: asleep. */
export const Sleep: Story = { args: { rows: lanes.sleepRows }, decorators: [frame("Device", demo, lanes.sleepRows)] };
