import type { Meta, StoryObj } from "@storybook/react";
import { Breadcrumb } from "./Breadcrumb";

const meta = {
  title: "Components/Chrome/Breadcrumb",
  component: Breadcrumb,
  parameters: { layout: "padded" },
  args: { crumbs: [{ label: "people", href: "#" }, { label: "name unknown", href: "#" }, { label: "partner-readiness" }] },
} satisfies Meta<typeof Breadcrumb>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = {};
export const Hover: Story = { args: { forceHover: 0 } };
/** A linked person's page: the person crumb carries both agent dots (§11L.2). */
export const WithAgents: Story = { args: { crumbs: [{ label: "people", href: "#" }, { label: "Chris Ortiz", agents: ["claude-code", "codex"] }] } };
export const Loading: Story = { args: { crumbs: [{ label: "people", href: "#" }, { label: null }] } };
