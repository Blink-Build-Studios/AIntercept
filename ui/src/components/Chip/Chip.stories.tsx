import type { Meta, StoryObj } from "@storybook/react";
import { Chip } from "./Chip";

const meta = { title: "Components/Chips/Chip", component: Chip, parameters: { layout: "padded" } } satisfies Meta<typeof Chip>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Neutral: Story = { args: { tone: "neutral", children: "bypassPermissions" } };
export const Muted: Story = { args: { tone: "muted", children: "default" } };
export const Range: Story = { args: { tone: "range", children: "range Nov 3 00:00 – 06:00 ×" } };
export const NotRetained: Story = { args: { tone: "notRetained", children: "not retained" } };
export const Selected: Story = { args: { tone: "selected", children: "all agents · 1 person" } };
export const WithSwatch: Story = { args: { tone: "muted", swatch: "model", children: "model 640" } };
