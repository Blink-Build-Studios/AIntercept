import type { Meta, StoryObj } from "@storybook/react";
import { prompts, window } from "./demoData";
import { Lane } from "./Lane";
import { MarkerTrack } from "./MarkerTrack";
import { Timeline } from "./Timeline";

const meta = {
  title: "Components/Timeline/MarkerTrack",
  component: MarkerTrack,
  parameters: { layout: "padded" },
  decorators: [
    (Story) => (
      <Timeline {...window} label="Marks">
        <Lane label="Marks" height={26} summary="marks">
          {Story()}
        </Lane>
      </Timeline>
    ),
  ],
} satisfies Meta<typeof MarkerTrack>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Prompts: Story = { args: { markers: prompts } };
