import type { Meta, StoryObj } from "@storybook/react";
import sessionGap from "../../api/samples/session_timeline/coverage_gap";
import sessionDemo from "../../api/samples/session_timeline/demo";
import sessionMany from "../../api/samples/session_timeline/many";
import { Lane } from "./Lane";
import { networkSegments, permissionLane } from "./sessionLanes";
import { StateTrack } from "./StateTrack";
import { Timeline } from "./Timeline";

const meta = { title: "Components/Timeline/StateTrack", component: StateTrack, parameters: { layout: "padded" } } satisfies Meta<typeof StateTrack>;
export default meta;
type Story = StoryObj<typeof meta>;

const frame = (label: string, w: { from: string; to: string }) => (Story: () => JSX.Element) => (
  <Timeline {...w} label={label}>
    <Lane label={label} height={28} summary={label}>
      {Story()}
    </Lane>
  </Timeline>
);

const demo = sessionDemo.data;
const many = sessionMany.data;
const gap = sessionGap.data;
const permission = permissionLane(demo.permission, demo.from, demo.to);
const win = (x: { from: string; to: string }) => ({ from: x.from, to: x.to });

/** Session demo sample: bypassPermissions, with brief flips to default folded into a focus tick. */
export const Permission: Story = { args: { segments: permission.segments, changes: permission.changes, palette: "permission", nowrap: true }, decorators: [frame("Permission", win(demo))] };
/** Session "many" scenario: two networks, the second IPv6. */
export const Network: Story = { args: { segments: networkSegments(many.network), palette: "network", nowrap: true }, decorators: [frame("Network", win(many))] };
/** Session "coverage_gap" scenario: a not-captured window overlaps the network, so it is bypassed (danger). */
export const NetworkBypassed: Story = { args: { segments: networkSegments(gap.network), palette: "network", nowrap: true }, decorators: [frame("Network", win(gap))] };
/** A network too narrow for its labels (§11G.A3): bare segments, the tooltip carries the label. */
export const NetworkNarrow: Story = {
  args: { segments: networkSegments(many.network).map((x) => ({ ...x, text: `${x.text} · ${x.text} · ${x.text}` })), palette: "network", nowrap: true },
  decorators: [frame("Network", win(many))],
};
/** Codex's approval policy · sandbox mode, in place of permission (§11J). */
export const Approval: Story = {
  args: { segments: [{ from: win(demo).from, to: win(demo).to, state: "on-request", text: "on-request · workspace-write" }], palette: "approval" },
  decorators: [frame("Approval", win(demo))],
};
