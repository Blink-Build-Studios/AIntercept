import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import { Lane } from "./Lane";
import { Timeline } from "./Timeline";
import { TurnTrack, type TurnBlock } from "./TurnTrack";

const FROM = Date.parse("2026-11-03T10:00:00Z");
const at = (min: number) => new Date(FROM + min * 60_000).toISOString();
const blocks: TurnBlock[] = [
  { id: "t13", index: 13, from: at(2), to: at(6) },
  { id: "t14", index: 14, from: at(9), to: at(18) },
  { id: "t15", index: 15, from: at(36), to: at(36.4) },
  { id: "t16", index: 16, from: at(41), to: at(50) },
  { id: "t17", index: 17, from: at(100), to: null },
];
const meta = {
  title: "Components/Timeline/TurnTrack",
  component: TurnTrack,
  parameters: { layout: "padded" },
  args: { blocks, now: at(110) },
  decorators: [
    (S) => (
      <Timeline from={at(0)} to={at(120)} label="Turns">
        <Lane label="Turns" height={26} summary="5 turns">
          <S />
        </Lane>
      </Timeline>
    ),
  ],
} satisfies Meta<typeof TurnTrack>;
export default meta;
type Story = StoryObj<typeof meta>;

/** One block per turn, prompt to last step; the running one reaches now. */
export const Blocks: Story = {};
/** Interactive: each block explains itself and opens its turn. */
export const Interactive: Story = { args: { interact: { tip: (b) => ({ title: `Turn #${b.index}` }), onActivate: fn() } } };
