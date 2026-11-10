import type { Meta, StoryObj } from "@storybook/react";
import { codexPanelRows } from "../AgentSectionHead/agentSpecimens";
import { AgentPanel } from "./AgentPanel";

const meta = {
  title: "Components/Agents/AgentPanel",
  component: AgentPanel,
  parameters: { layout: "padded" },
  decorators: [(Story) => <div style={{ width: 400 }}>{Story()}</div>],
} satisfies Meta<typeof AgentPanel>;
export default meta;
type Story = StoryObj<typeof meta>;

export const CodexSession: Story = { args: { agent: "codex", topic: "this thread", rows: codexPanelRows } };
export const ClaudeCodeSession: Story = {
  args: {
    agent: "claude-code",
    topic: "this session",
    rows: [
      { k: "Recaps", v: "3" },
      { k: "Experiments", v: "12 gates" },
      { k: "Hooks", v: "PreToolUse ×4" },
      { k: "Skills", v: "2 used" },
    ],
  },
};
