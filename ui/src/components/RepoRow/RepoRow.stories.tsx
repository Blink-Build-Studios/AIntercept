import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import { AgentChip } from "../AgentChip/AgentChip";
import { DataTable } from "../DataTable/DataTable";
import { RepoRow, WORK_COLUMNS, WORK_MIN_WIDTH } from "./RepoRow";
import { WorkSessionRow } from "./WorkSessionRow";

const meta = {
  title: "Components/Turns/RepoRow",
  component: RepoRow,
  parameters: { layout: "padded" },
  args: { repo: "aintercept", sub: "partner-readiness", sessions: 1, turns: 34, activeMs: 22_320_000, work: ["edited 23 files +1,204 −388 · ran npm test ×41: ", { failed: "9 failed" }, " · 2 commits · PR #2"], expanded: false, onToggle: fn() },
  decorators: [
    (S) => (
      <DataTable expandable columns={WORK_COLUMNS} minWidth={WORK_MIN_WIDTH} label="Work">
        <S />
      </DataTable>
    ),
  ],
} satisfies Meta<typeof RepoRow>;
export default meta;
type Story = StoryObj<typeof meta>;

/** Open: raised, its sessions below. */
export const Open: Story = {
  args: {
    expanded: true,
    children: (
      <WorkSessionRow
        session={{ sessionId: "4c1a", start: "2026-11-02T10:04:00", end: null, live: true, label: "partner-readiness", turns: 34, capturedFromPrompt: 7, activeMs: 22_320_000, friction: { frustrated: 1, interrupted: 1, destructive: 10 } }}
        work={["edited 23 files · ran npm test ×41: ", { failed: "9 failed" }, " · 2 commits"]}
        href="#"
        now="2026-11-04T12:00:00"
      />
    ),
  },
};
/** Sessions from before the repo was parsed. */
export const Collapsed: Story = { args: { repo: null, sub: "before Nov 2", sessions: 10, turns: 32, activeMs: 12_480_000, work: ["read 210 files · edited 18 files · used playwright ×40"] } };
/** A mixed person: the agent chip after the repo name. */
export const WithAgent: Story = { args: { agents: <AgentChip agent="codex" /> } };
