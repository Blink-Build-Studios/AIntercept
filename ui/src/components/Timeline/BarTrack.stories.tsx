import type { Meta, StoryObj } from "@storybook/react";
import sessionDemo from "../../api/samples/session_timeline/demo";
import { BarTrack } from "./BarTrack";
import { callBins, d, window } from "./demoData";
import { Lane } from "./Lane";
import { compactionLabels, contextBins } from "./sessionLanes";
import { Timeline } from "./Timeline";

const s = sessionDemo.data;

const meta = {
  title: "Components/Timeline/BarTrack",
  component: BarTrack,
  parameters: { layout: "padded" },
  decorators: [
    (Story, ctx) =>
      ctx.parameters.context ? (
        <Timeline from={s.from} to={s.to} label="Context">
          <Lane label="Context" sublabel="tokens · main thread" height={56} summary="context tokens">
            {Story()}
          </Lane>
        </Timeline>
      ) : (
        <Timeline {...window} label="Bars">
          <Lane label="Model calls" sublabel="per hour" height={60} summary="model calls per hour">
            {Story()}
          </Lane>
        </Timeline>
      ),
  ],
  args: { bins: callBins, binSeconds: d.binSeconds, color: "model" },
} satisfies Meta<typeof BarTrack>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = {};
export const Auxiliary: Story = { args: { bins: callBins.map((b, i) => ({ ...b, auxiliary: i % 4 === 0 })) } };
/** The session page's Context lane (session demo sample): context per main-thread call as a level, compactions labelled. */
export const Context: Story = {
  args: { bins: contextBins(s.context, s.binSeconds, s.to), binSeconds: s.binSeconds, color: "count", level: true, labels: compactionLabels(s.compactions) },
  parameters: { context: true },
};
