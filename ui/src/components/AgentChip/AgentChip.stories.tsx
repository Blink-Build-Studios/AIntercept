import type { Meta, StoryObj } from "@storybook/react";
import { AgentChip } from "./AgentChip";

const meta = { title: "Components/Chips/AgentChip", component: AgentChip, parameters: { layout: "padded" } } satisfies Meta<
  typeof AgentChip
>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Row: Story = { args: { agent: "claude-code" } };
export const AllAgents: Story = {
  args: { agent: "claude-code" },
  render: () => (
    <div className="flex gap-2">
      <AgentChip agent="claude-code" />
      <AgentChip agent="codex" />
      <AgentChip agent="chatgpt" />
      <AgentChip agent="claude-ai" />
    </div>
  ),
};
/** The client after the agent's name (§11J gap 7): the sessions table and the session header. */
export const WithClient: Story = {
  args: { agent: "codex" },
  render: () => (
    <div className="flex gap-2">
      <AgentChip agent="codex" client="exec" />
      <AgentChip agent="codex" client="tui 0.167" />
    </div>
  ),
};
