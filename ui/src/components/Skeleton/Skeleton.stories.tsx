import type { Meta, StoryObj } from "@storybook/react";
import { Skeleton } from "./Skeleton";

const meta = {
  title: "Components/Chrome/Skeleton",
  component: Skeleton,
  parameters: { layout: "padded" },
  decorators: [(Story) => <div style={{ width: 448 }}>{Story()}</div>],
} satisfies Meta<typeof Skeleton>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Line: Story = { args: { shape: "line", lines: 2 } };
export const Block: Story = { args: { shape: "block" } };
export const Row: Story = { args: { shape: "row" } };
export const Lane: Story = { args: { shape: "lane" } };
