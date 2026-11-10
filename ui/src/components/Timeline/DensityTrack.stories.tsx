import type { Meta, StoryObj } from "@storybook/react";
import { analyticsBins, d, window } from "./demoData";
import { DensityTrack } from "./DensityTrack";
import { Lane } from "./Lane";
import { Timeline } from "./Timeline";

const meta = {
  title: "Components/Timeline/DensityTrack",
  component: DensityTrack,
  parameters: { layout: "padded" },
  decorators: [
    (Story) => (
      <Timeline {...window} label="Density">
        <Lane label="Analytics" sublabel="events" height={44} summary="analytics events per hour">
          {Story()}
        </Lane>
      </Timeline>
    ),
  ],
} satisfies Meta<typeof DensityTrack>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = { args: { bins: analyticsBins, binSeconds: d.binSeconds, color: "analytics" } };
