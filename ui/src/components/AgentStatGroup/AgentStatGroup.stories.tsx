import type { Meta, StoryObj } from "@storybook/react";
import { CODEX_LIMITS, codexLimitsMore, codexLimitsTip } from "../AgentSectionHead/agentSpecimens";
import { AgentStatGroup } from "./AgentStatGroup";

const meta = { title: "Components/Agents/AgentStatGroup", component: AgentStatGroup, parameters: { layout: "padded" } } satisfies Meta<typeof AgentStatGroup>;
export default meta;
type Story = StoryObj<typeof meta>;

/** Codex's usage windows, resets and credits; the windows explain themselves. */
export const CodexLimits: Story = { args: { agent: "codex", topic: "limits", value: CODEX_LIMITS, tip: codexLimitsTip, more: codexLimitsMore } };
/** Claude Code's equivalent, from its rate-limit headers. */
export const ClaudeCodeLimits: Story = { args: { agent: "claude-code", topic: "limits", value: "7d 83% · overage off" } };
/** A window at 100%: ▲ in danger, the account is blocked. */
export const AtLimit: Story = { args: { agent: "codex", topic: "limits", value: "5h 100% · week 91%", atLimit: true, tip: codexLimitsTip, more: codexLimitsMore } };
