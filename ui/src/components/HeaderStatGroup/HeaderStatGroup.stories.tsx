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
          label="Devices"
          value={p.devices.length ? p.devices.map((d) => [d.platform, d.terminal, d.shell].filter(Boolean).join(" · ") || d.deviceId.slice(0, 8)).join(", ") : null}
          unknownReason="no analytics yet"
        />
        <HeaderStat
          label="7-day limit"
          value={p.limits.utilization7d === null ? null : `${Math.round(p.limits.utilization7d * 100)}% · ${p.limits.status}`}
          unknownReason="no response yet"
        />
        <HeaderStat label="Flags" value={p.flags.total} unknownReason="not extracted yet" />
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
        <HeaderStat label="Devices" value="2" />
        <HeaderStat label="Last seen" value="now" live />
        <AgentStatGroup agent="codex" topic="limits" value={CODEX_LIMITS} tip={codexLimitsTip} more={codexLimitsMore} />
      </>
    ),
  },
};
