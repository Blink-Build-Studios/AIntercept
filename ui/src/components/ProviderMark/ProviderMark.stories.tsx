import type { Meta, StoryObj } from "@storybook/react";
import { ProviderMark } from "./ProviderMark";

const meta = { title: "Components/Providers/ProviderMark", component: ProviderMark, parameters: { layout: "padded" } } satisfies Meta<typeof ProviderMark>;
export default meta;
type Story = StoryObj<typeof meta>;

export const ClaudeCodePerson: Story = { args: { agents: ["claude-code"], scope: "person" } };
export const CodexPerson: Story = { args: { agents: ["codex"], scope: "person" } };
/** A linked person: both agents, and how they were linked. */
export const LinkedPerson: Story = { args: { agents: ["claude-code", "codex"], scope: "person", linkedBy: "email" } };
/** A session: its agent and client version. */
export const CodexSession: Story = { args: { agents: ["codex"], scope: "session", client: "tui 0.167" } };
