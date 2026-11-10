import type { Meta, StoryObj } from "@storybook/react";
import { ChannelSwatch, type Channel } from "./ChannelSwatch";

const meta = {
  title: "Components/Chips/ChannelSwatch",
  component: ChannelSwatch,
  parameters: { layout: "padded" },
  render: (args) => <ChannelSwatch {...args} label={LABEL[args.channel]} />,
} satisfies Meta<typeof ChannelSwatch>;
export default meta;
type Story = StoryObj<typeof meta>;

const LABEL: Record<Channel, string> = {
  model: "model",
  analytics: "analytics",
  flags: "flags",
  count: "count · tokens",
  account: "account · no session",
  skills: "skills · reach",
  metrics: "metrics · OTel",
  usage: "usage & account",
  catalog: "catalog",
  other: "other",
};

export const Model: Story = { args: { channel: "model" } };
export const Analytics: Story = { args: { channel: "analytics" } };
export const Flags: Story = { args: { channel: "flags" } };
export const Count: Story = { args: { channel: "count" } };
export const Account: Story = { args: { channel: "account" } };
export const Skills: Story = { args: { channel: "skills" } };
/** The bare square, for callers that place it beside their own label. */
export const Bare: Story = { args: { channel: "model" }, render: (args) => <ChannelSwatch {...args} /> };

/** Codex's channels (§11L.6). */
export const Metrics: Story = { args: { channel: "metrics" } };
export const Usage: Story = { args: { channel: "usage" } };
export const Catalog: Story = { args: { channel: "catalog" } };
