import type { Meta, StoryObj } from "@storybook/react";
import { codexFacts, forkOrigin } from "../AgentSectionHead/agentSpecimens";
import { SessionFacts } from "./SessionFacts";

const meta = { title: "Components/Agents/SessionFacts", component: SessionFacts, parameters: { layout: "padded" } } satisfies Meta<typeof SessionFacts>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = { args: { facts: codexFacts } };
/** A forked thread: its origin last. */
export const WithOrigin: Story = { args: { facts: codexFacts, origin: forkOrigin } };
/** Codex without recorded bodies (§11M Part 4): cwd, approval, sandbox, origin; shell and tz absent. */
export const CodexNoBodies: Story = {
  args: {
    facts: [
      { k: "cwd", v: "/work/pomodoro" },
      { k: "approval", v: "on-request" },
      { k: "sandbox", v: "workspace-write" },
    ],
    origin: { kind: "resume", threadShort: "3b9a…e21", init: "cold_resume", href: "#" },
  },
};
