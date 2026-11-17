import type { Meta, StoryObj } from "@storybook/react";
import person from "../../api/samples/person/demo";
import { formatDuration } from "../../lib/format";
import { HeaderStat } from "../HeaderStat/HeaderStat";
import { HeaderStatGroup } from "./HeaderStatGroup";
import { AgentStatGroup } from "../AgentStatGroup/AgentStatGroup";
import { CODEX_LIMITS, codexLimitsMore, codexLimitsTip } from "../AgentSectionHead/agentSpecimens";

const p = person.data;
const meta = { title: "Components/Summary/HeaderStatGroup", component: HeaderStatGroup, parameters: { layout: "padded" } } satisfies Meta<
  typeof HeaderStatGroup
>;
export default meta;
type Story = StoryObj<typeof meta>;

/** The person header's stats, from the demo sample. */
export const Default: Story = {
  args: {
    children: (
      <>
        <HeaderStat label="Role" value={p.role} unknownReason="not extracted yet" />
        <HeaderStat label="Plan" value={p.plan} />
        <HeaderStat label="Claude Code user" value={p.firstUseAt} unknownReason="not extracted yet" />
        <HeaderStat
          label="7-day limit"
          value={p.limits.utilization7d === null ? null : `${Math.round(p.limits.utilization7d * 100)}% · ${p.limits.status}`}
          unknownReason="no response yet"
        />
        <HeaderStat label="Last seen" value={p.live ? "now" : formatDuration(60_000)} live={p.live} />
      </>
    ),
  },
};
/** A person header with Codex's limits after the common stats (§11J screens 2). */
export const WithAgentExtras: Story = {
  args: {
    children: (
      <>
        <HeaderStat label="Plan" value="ChatGPT Plus" />
        <HeaderStat label="Last seen" value="now" live />
        <AgentStatGroup agent="codex" topic="limits" value={CODEX_LIMITS} tip={codexLimitsTip} more={codexLimitsMore} />
      </>
    ),
  },
};
/** The session header without recorded content (§11S): Span · Turns · Steps · Tokens · Tool calls. */
export const ContentFree: Story = {
  args: {
    wrapGap: true,
    children: (
      <>
        <HeaderStat label="Span" value="Nov 2 10:04 → now · 2d 0h" sub="6h 12m active" />
        <HeaderStat label="Turns" value="34" sub="2 interrupted" />
        <HeaderStat label="Steps" value="640" sub="model calls · 5 failed" />
        <HeaderStat label="Tokens" value="8.2M → 1.1M" sub="92% cache read" />
        <HeaderStat label="Tool calls" value="1,212" sub="14 tools used of 404 offered" />
      </>
    ),
  },
};
/** With content recorded: Edited and Commits join. */
export const WithContent: Story = {
  args: {
    wrapGap: true,
    children: (
      <>
        <HeaderStat label="Turns" value="34" sub="2 interrupted" />
        <HeaderStat label="Tool calls" value="1,212" sub="14 used of 404 offered" />
        <HeaderStat label="Edited" value="23 files" sub="+1,204 −388" />
        <HeaderStat label="Commits" value="2" sub="a41f → 9c02" />
      </>
    ),
  },
};
