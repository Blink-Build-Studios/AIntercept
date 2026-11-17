import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import { Lane, LANE_HEIGHT } from "./Lane";
import { Timeline } from "./Timeline";
import { WorkTrack, type WorkBin } from "./WorkTrack";

const FROM = Date.parse("2026-11-03T10:00:00Z");
const at = (min: number) => new Date(FROM + min * 60_000).toISOString();
const bins: WorkBin[] = [0, 15, 30, 60, 75, 90, 150, 165, 180, 195].map((m, i) => ({ t: at(m), other: [6, 12, 4, 9, 14, 3, 8, 11, 5, 7][i] ?? 0, edits: [1, 4, 0, 6, 2, 0, 3, 5, 1, 0][i] ?? 0 }));
const meta = {
  title: "Components/Timeline/WorkTrack",
  component: WorkTrack,
  parameters: { layout: "padded" },
  args: { bins, binSeconds: 900 },
  decorators: [
    (S) => (
      <Timeline from={at(0)} to={at(240)} label="Work">
        <Lane label="Work" sublabel="per 15m" height={LANE_HEIGHT.workSession} summary="work per 15 minutes">
          <S />
        </Lane>
      </Timeline>
    ),
  ],
} satisfies Meta<typeof WorkTrack>;
export default meta;
type Story = StoryObj<typeof meta>;

/** Tool calls at the bottom, edit tools stacked on top (§11S: no not-captured row). */
export const Stacked: Story = {};
/** Interactive: each bin explains itself and selects its range. */
export const Interactive: Story = { args: { interact: { tip: (s) => ({ title: s.from }), label: (s) => s.from, onSelect: fn() } } };
