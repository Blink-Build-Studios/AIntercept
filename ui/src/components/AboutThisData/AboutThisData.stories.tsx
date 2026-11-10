import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import { AboutThisData, type AboutProductData } from "./AboutThisData";

const products: AboutProductData[] = [
  { agent: "claude-code", tokenAccuracy: "exact", sessions: 18 },
  { agent: "codex", tokenAccuracy: "exact", sessions: 7 },
  { agent: "chatgpt", tokenAccuracy: "missing", sessions: 6 },
  { agent: "claude-ai", tokenAccuracy: "estimated", sessions: 0 },
];

const meta = {
  title: "Chrome/AboutThisData",
  component: AboutThisData,
  args: { contentRetained: false, products, onClose: fn() },
  parameters: { layout: "fullscreen" },
} satisfies Meta<typeof AboutThisData>;
export default meta;
type Story = StoryObj<typeof meta>;

/** Metadata only (the customer default): Products, a ChannelTable per product, What is kept says METADATA ONLY. */
export const Default: Story = {};
/** Full interception: bodies are recorded, so nothing is "not kept". */
export const ContentRetained: Story = { args: { contentRetained: true } };
/** Before the overview loads: no Products section; Claude Code's channels. */
export const ProductsLoading: Story = { args: { products: null } };
