import type { Meta, StoryObj } from "@storybook/react";
import { BarTrack } from "./BarTrack";
import { callBins, d, window } from "./demoData";
import { Lane } from "./Lane";
import { Timeline } from "./Timeline";
import { TimelineGuide } from "./TimelineGuide";
import { modelBinTip } from "./timelineTips";

const meta = {
  title: "Components/Timeline/TimelineGuide",
  component: TimelineGuide,
  parameters: { layout: "padded" },
  args: { t: null },
} satisfies Meta<typeof TimelineGuide>;
export default meta;
type Story = StoryObj<typeof meta>;

const binTip = (from: string, to: string) => modelBinTip(from, to, d.modelCalls.find((b) => b.t === from), d.binSeconds);

/** The guide and its readout follow the pointer from bin to bin; the bin's tooltip opens after 120 ms. */
function Lanes({ guide }: { guide?: number }) {
  return (
    <Timeline {...window} label="Activity" guide={guide === undefined ? undefined : { t: guide, readout: "Nov 3 10:15" }}>
      <Lane label="Model calls" sublabel="per hour" height={60} summary="model calls">
        <BarTrack bins={callBins} binSeconds={d.binSeconds} color="model" interact={{ tip: (s) => binTip(s.from, s.to), label: (s) => s.from }} />
      </Lane>
    </Timeline>
  );
}

export const Hidden: Story = { render: () => <Lanes /> };
export const Visible: Story = { render: () => <Lanes guide={(Date.parse(window.from) + Date.parse(window.to)) / 2} /> };
