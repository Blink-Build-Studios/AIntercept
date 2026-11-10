import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import { AgentScope } from "./AgentScope";

const meta = {
  title: "Agents/AgentScope",
  component: AgentScope,
  args: { agents: ["claude-code", "codex"], value: "all", onChange: fn() },
} satisfies Meta<typeof AgentScope>;
export default meta;
type Story = StoryObj<typeof meta>;

/** A linked person: All agents selected (the default). */
export const AllAgents: Story = {};
/** Scoped to Codex: its sessions and requests only. */
export const Codex: Story = { args: { value: "codex" } };
/** A one-agent person: no control. */
export const OneAgent: Story = { args: { agents: ["codex"] } };
