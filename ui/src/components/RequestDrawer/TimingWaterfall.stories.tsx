import type { Meta, StoryObj } from "@storybook/react";
import details from "../../api/samples/request_details/normal";
import { TimingWaterfall } from "./TimingWaterfall";

const meta = {
  title: "Components/Drawer/TimingWaterfall",
  component: TimingWaterfall,
  parameters: { layout: "padded" },
  decorators: [(Story) => <div style={{ width: 620 }}>{Story()}</div>],
} satisfies Meta<typeof TimingWaterfall>;
export default meta;

export const Default: StoryObj<typeof meta> = {
  args: { phases: details.data.timing.proxy, className: "border border-line bg-surface" },
};
