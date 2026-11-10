import type { Meta, StoryObj } from "@storybook/react";
import person from "../../api/samples/person/demo";
import { Chip } from "../Chip/Chip";
import { HeaderStat } from "../HeaderStat/HeaderStat";
import { HeaderStatGroup } from "../HeaderStatGroup/HeaderStatGroup";
import { PageHeader } from "./PageHeader";

const p = person.data;
const meta = { title: "Components/Chrome/PageHeader", component: PageHeader, parameters: { layout: "fullscreen" } } satisfies Meta<typeof PageHeader>;
export default meta;
type Story = StoryObj<typeof meta>;

/** The person header from the demo sample: name and email need M7 extraction, so the title falls back. */
export const Default: Story = {
  args: {
    title: p.name ?? "name unknown",
    subtitle: `acct ${p.accountIdShort} · org ${p.orgIdShort ?? "—"} · ${p.orgKind}`,
    stats: (
      <HeaderStatGroup>
        <HeaderStat label="Plan" value={p.plan} />
        <HeaderStat label="Devices" value={p.devices.map((d) => [d.platform, d.terminal].filter(Boolean).join(" · ")).join(", ") || null} unknownReason="no analytics yet" />
        <HeaderStat label="7-day limit" value={p.limits.utilization7d === null ? null : `${Math.round(p.limits.utilization7d * 100)}% · ${p.limits.status}`} />
        <HeaderStat label="Last seen" value="now" live={p.live} />
      </HeaderStatGroup>
    ),
    posture: (
      <>
        {p.posture.bypassPct !== null && <Chip>bypassed approvals {p.posture.bypassPct}% of model calls</Chip>}
        {p.posture.sandbox && <Chip tone="muted">sandbox {p.posture.sandbox}</Chip>}
        {p.posture.proxyDetected && <Chip tone="muted">proxy detected · {p.posture.proxyDetected}</Chip>}
      </>
    ),
  },
};
