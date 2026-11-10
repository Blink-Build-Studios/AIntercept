import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import { FilterChip } from "./FilterChip";

const meta = {
  title: "Chips/FilterChip",
  component: FilterChip,
  args: { label: "▲ not captured", removeLabel: "Remove filter: not captured", onRemove: fn() },
} satisfies Meta<typeof FilterChip>;
export default meta;
type Story = StoryObj<typeof meta>;

export const NotCaptured: Story = {};
export const RemoveHover: Story = { args: { "data-force": "hover" } };
export const RemoveFocus: Story = { args: { "data-force": "focus" } };
/** No remove button: an empty state's reminder of what's filtered. */
export const Static: Story = { args: { onRemove: undefined } };
