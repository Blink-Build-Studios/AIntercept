import type { Meta, StoryObj } from "@storybook/react";
import { AgentSectionHead } from "./AgentSectionHead";

const meta = { title: "Components/Agents/AgentSectionHead", component: AgentSectionHead, parameters: { layout: "padded" } } satisfies Meta<typeof AgentSectionHead>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Codex: Story = { args: { agent: "codex", topic: "limits" } };
export const ClaudeCode: Story = { args: { agent: "claude-code", topic: "this session" } };
