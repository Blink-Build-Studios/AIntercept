import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import { SegmentedControl } from "./SegmentedControl";

const meta = {
  title: "Components/Controls/SegmentedControl",
  component: SegmentedControl,
  parameters: { layout: "padded" },
  args: {
    options: ["1h", "24h", "7d", "14d", "All"].map((w) => ({ value: w, label: w })),
    value: "14d",
    onChange: fn(),
    "aria-label": "Time window",
  },
} satisfies Meta<typeof SegmentedControl>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = {};
export const Hover: Story = { args: { force: { value: "7d", state: "hover" } } };
export const Focus: Story = { args: { force: { value: "24h", state: "focus" } } };
export const Disabled: Story = { args: { disabled: true, disabledReason: "Connecting…" } };
