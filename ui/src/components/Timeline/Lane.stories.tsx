import type { Meta, StoryObj } from "@storybook/react";
import { BarTrack } from "./BarTrack";
import { callBins, d, window } from "./demoData";
import { Lane } from "./Lane";
import { Timeline } from "./Timeline";

const meta = {
  title: "Components/Timeline/Lane",
  component: Lane,
  parameters: { layout: "padded" },
  decorators: [(Story) => <Timeline {...window} label="Lane">{Story()}</Timeline>],
} satisfies Meta<typeof Lane>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = {
  args: { label: "Model calls", sublabel: "per hour", height: 60, summary: "model calls per hour", children: <BarTrack bins={callBins} binSeconds={d.binSeconds} color="model" /> },
};
