import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import { AppBar } from "./AppBar";

const meta = {
  title: "Components/Chrome/AppBar",
  component: AppBar,
  parameters: { layout: "fullscreen" },
  args: {
    breadcrumb: [{ label: "people", href: "#" }, { label: "name unknown", agents: ["claude-code"] }],
    window: { options: ["1h", "24h", "7d", "14d", "All"].map((w) => ({ value: w, label: w })), value: "14d", onChange: fn(), "aria-label": "Time window" },
    live: { status: "live", intervalS: 15 },
    onPause: fn(),
    onAbout: fn(),
  },
} satisfies Meta<typeof AppBar>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = {};
/** The person's name is still loading: its crumb is a skeleton word; the window waits. */
export const Loading: Story = { args: { loading: true, breadcrumb: [{ label: "people", href: "#" }, { label: null }] } };
/** A Codex session: the person crumb carries the agent dot, the session is the current crumb (§11L.2). */
export const CodexSession: Story = {
  args: { breadcrumb: [{ label: "people", href: "#" }, { label: "Sam Rivera", href: "#", agents: ["codex"] }, { label: "Fix flaky timer test" }] },
};
