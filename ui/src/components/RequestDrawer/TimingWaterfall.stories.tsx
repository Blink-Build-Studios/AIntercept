import type { Meta, StoryObj } from "@storybook/react";
import details from "../../api/samples/request_details/normal";
import { detailRow } from "./drawerData";
import { TimingWaterfall } from "./TimingWaterfall";

const meta = {
  title: "Components/Drawer/TimingWaterfall",
  component: TimingWaterfall,
  parameters: { layout: "padded" },
  decorators: [(Story) => <div style={{ width: 620 }}>{Story()}</div>],
} satisfies Meta<typeof TimingWaterfall>;
export default meta;

export const Default: StoryObj<typeof meta> = {
  args: { phases: details.data.timing.proxy, client: details.data.timing.client.map(detailRow).map((r) => ({ k: r.k, v: r.v })), className: "border border-line bg-surface" },
};
