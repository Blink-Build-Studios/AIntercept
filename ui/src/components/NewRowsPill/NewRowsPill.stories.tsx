import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import { NewRowsPill } from "./NewRowsPill";

const meta = {
  title: "Components/Interaction/NewRowsPill",
  component: NewRowsPill,
  parameters: { layout: "padded" },
  args: { count: 12, direction: "down", onClick: fn() },
} satisfies Meta<typeof NewRowsPill>;
export default meta;
type Story = StoryObj<typeof meta>;

/** No rows arrived past the edge: mounted but invisible (its live region stays in place). */
export const Hidden: Story = { args: { count: 0 } };
/** Live rows arrived below while the viewer is scrolled up. */
export const Visible: Story = {};
/** Rows arrived above (newest-first tables). */
export const VisibleUp: Story = { args: { count: 3, direction: "up" } };
