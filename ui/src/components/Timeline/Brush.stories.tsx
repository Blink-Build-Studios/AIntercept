import type { Meta, StoryObj } from "@storybook/react";
import { expect, fn, userEvent, within } from "@storybook/test";
import { Brush } from "./Brush";
import { window } from "./demoData";
import { Lane } from "./Lane";
import { Timeline } from "./Timeline";

const meta = {
  title: "Components/Timeline/Brush",
  component: Brush,
  parameters: { layout: "padded" },
  decorators: [
    (Story) => (
      <Timeline {...window} label="Brush">
        <Lane label="Range" height={60} summary="brush">
          {Story()}
        </Lane>
      </Timeline>
    ),
  ],
  args: { value: null, onChange: fn(), mode: "idle" },
} satisfies Meta<typeof Brush>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Idle: Story = {};
export const Range: Story = {
  args: { value: { from: "2026-11-03T00:00:00Z", to: "2026-11-03T18:00:00Z" }, mode: "range" },
  play: async ({ canvasElement, args }) => {
    const slider = within(canvasElement).getByRole("slider");
    slider.focus();
    await userEvent.keyboard("{Escape}");
    await expect(args.onChange).toHaveBeenCalledWith(null);
  },
};
/** Clearing (§11G.A8): the × in the selection's corner clears the range. */
export const ClearButton: Story = {
  args: { value: { from: "2026-11-03T00:00:00Z", to: "2026-11-03T18:00:00Z" }, mode: "range" },
  play: async ({ canvasElement, args }) => {
    await userEvent.click(within(canvasElement).getByRole("button", { name: "Clear range" }));
    await expect(args.onChange).toHaveBeenCalledWith(null);
  },
};
export const Live: Story = { args: { value: { from: "2026-11-04T12:00:00Z", to: window.to }, mode: "live" } };
