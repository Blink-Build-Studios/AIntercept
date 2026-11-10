import type { Meta, StoryObj } from "@storybook/react";
import { Notice } from "./Notice";

const meta = { title: "Components/Summary/Notice", component: Notice, parameters: { layout: "padded" } } satisfies Meta<typeof Notice>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Danger: Story = {
  args: {
    tone: "danger",
    title: "Reported by Claude Code, not captured",
    children: "Claude Code reported this call in its analytics, but no matching request passed through the proxy. Everything below comes from that report.",
  },
};
export const Neutral: Story = {
  args: { tone: "neutral", title: "Retried", children: "The first attempt failed after 0.6 s; Claude Code retried once and the retry succeeded." },
};
