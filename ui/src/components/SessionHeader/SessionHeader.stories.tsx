import type { Meta, StoryObj } from "@storybook/react";
import { codexFacts, forkOrigin } from "../AgentSectionHead/agentSpecimens";
import { HeaderStat } from "../HeaderStat/HeaderStat";
import { HeaderStatGroup } from "../HeaderStatGroup/HeaderStatGroup";
import { SessionHeader } from "./SessionHeader";

const meta = { title: "Components/Agents/SessionHeader", component: SessionHeader, parameters: { layout: "padded" } } satisfies Meta<typeof SessionHeader>;
export default meta;
type Story = StoryObj<typeof meta>;

/** A forked TUI thread: its generated title, client and version, facts and origin. */
export const Codex: Story = {
  args: {
    title: "Add Expo mobile skeleton",
    agent: "codex",
    client: "tui 0.167",
    sub: "pomodoro · 9be04d1 · thread 7c1e…a90",
    facts: codexFacts,
    origin: forkOrigin,
    stats: (
      <HeaderStatGroup>
        <HeaderStat label="Prompts" value="9" />
        <HeaderStat label="Model calls" value="63" qualifier="+6 prewarm hidden" qualifierTone="hidden" />
        <HeaderStat label="Tokens" value="412k → 9.8k" />
        <HeaderStat label="Reasoning" value="3.1k" />
      </HeaderStatGroup>
    ),
  },
};
/** Claude Code: no client, its recap as the title, no facts line. */
export const ClaudeCode: Story = {
  args: {
    title: "Coverage check and IPv6 routing",
    agent: "claude-code",
    sub: "aintercept · partner-readiness · session 4c1a…77e0",
    stats: (
      <HeaderStatGroup>
        <HeaderStat label="Prompts" value="34" />
        <HeaderStat label="Model calls" value="640" />
      </HeaderStatGroup>
    ),
  },
};

/** The ProviderMark above the title (§11L.5): `CODEX · SESSION · TUI 0.167`. */
export const CodexMarked: Story = { args: { title: "Fix flaky timer test", agent: "codex", client: "tui 0.167", sub: "pomodoro · 3f2a1c9 · resumed from 3b9a…e21" } };
/** Claude Code with its repo as the title and the latest recap as a line under it (Session.dc.html). */
export const ClaudeCodeRecap: Story = {
  args: {
    title: "aintercept · partner-readiness",
    agent: "claude-code",
    client: "2.1.31",
    sub: "session 4c1a…77e0",
    note: "Latest recap: “Coverage check and IPv6 routing; PR #2 open, CI pending.”",
  },
};
/** An untitled Codex thread without recorded bodies (§11M 3.4, Part 4): `<workspace> · <first-turn start>`, facts and stats under it. */
export const CodexUntitled: Story = {
  args: {
    title: "pomodoro · Nov 9 09:00",
    agent: "codex",
    client: "tui 0.167",
    sub: "untitled · thread 01a1…aed1 · 3f2a1c9",
    facts: [
      { k: "cwd", v: "/work/pomodoro" },
      { k: "approval", v: "on-request" },
      { k: "sandbox", v: "workspace-write" },
    ],
    origin: { kind: "resume", threadShort: "3b9a…e21", init: "cold_resume", href: "#" },
    stats: (
      <HeaderStatGroup label="Session" wrapGap>
        <HeaderStat label="Span" value="Nov 9 09:00 → 11:04 · 2h 4m" />
        <HeaderStat label="Prompts" value="1" />
        <HeaderStat label="Model calls" value="203" qualifier="+6 prewarm hidden" qualifierTone="hidden" />
        <HeaderStat label="Reasoning" value="medium" />
      </HeaderStatGroup>
    ),
  },
};
