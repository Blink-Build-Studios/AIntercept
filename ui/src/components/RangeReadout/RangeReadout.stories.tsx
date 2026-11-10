import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import { RangeReadout } from "./RangeReadout";

const meta = {
  title: "Components/Timeline/RangeReadout",
  component: RangeReadout,
  parameters: { layout: "padded" },
  args: { onPrev: fn(), onNext: fn(), onClear: fn(), range: { from: "2026-11-03T15:00:00Z", to: "2026-11-03T18:00:00Z" } },
} satisfies Meta<typeof RangeReadout>;
export default meta;
type Story = StoryObj<typeof meta>;

/** Illustrative counts (the session /state endpoint arrives in M3). */
export const Range: Story = { args: { mode: "range", counts: { prompts: 6, modelCalls: 118, requests: 402, events: 3_912, compactions: 1 } } };
export const Live: Story = { args: { mode: "live", range: { from: "2026-11-04T17:50:00Z", to: "2026-11-04T18:05:00Z" }, counts: { prompts: 1, modelCalls: 9, requests: 31, events: 287, compactions: 0 } } };
export const Empty: Story = { args: { mode: "empty", nearest: { earlier: "2026-11-03T14:41:00Z", later: "2026-11-03T19:12:00Z" } } };
