import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import { ToggleChip } from "./ToggleChip";

const meta = {
  title: "Components/Controls/ToggleChip",
  component: ToggleChip,
  parameters: { layout: "padded" },
  args: { label: "hide client internals", pressed: true, onChange: fn() },
} satisfies Meta<typeof ToggleChip>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Off: Story = { args: { pressed: false } };
export const On: Story = {};
export const Hover: Story = { args: { pressed: false, "data-force": "hover" } };
export const Focus: Story = { args: { "data-force": "focus" } };
export const Disabled: Story = { args: { disabled: true, disabledReason: "Loading events…" } };
export const WithSwatch: Story = { args: { label: "model", swatch: "model", count: 640, pressed: false } };
