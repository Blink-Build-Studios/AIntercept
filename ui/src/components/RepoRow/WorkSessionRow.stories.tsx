import type { Meta, StoryObj } from "@storybook/react";
import { DataTable } from "../DataTable/DataTable";
import { WORK_COLUMNS, WORK_MIN_WIDTH } from "./RepoRow";
import { WorkSessionRow } from "./WorkSessionRow";

const session = { sessionId: "4c1a", start: "2026-11-02T10:04:00", end: null, live: true, label: "partner-readiness", turns: 34, capturedFromPrompt: null, activeMs: 22_320_000, friction: { frustrated: 1, interrupted: 1, destructive: 10 } };
const meta = {
  title: "Components/Turns/WorkSessionRow",
  component: WorkSessionRow,
  parameters: { layout: "padded" },
  args: { session, work: ["edited 23 files · ran npm test ×41: ", { failed: "9 failed" }, " · 2 commits"], href: "#", now: "2026-11-04T12:00:00" },
  decorators: [
    (S) => (
      <DataTable columns={WORK_COLUMNS} minWidth={WORK_MIN_WIDTH} label="Work">
        <S />
      </DataTable>
    ),
  ],
} satisfies Meta<typeof WorkSessionRow>;
export default meta;
type Story = StoryObj<typeof meta>;

export const WorkDefault: Story = {};
export const Ended: Story = { args: { session: { ...session, live: false, end: "2026-10-27T10:20:00", start: "2026-10-27T10:02:00", label: "identity and repo not parsed (pre-parser)", turns: 1, activeMs: 1_080_000, friction: { frustrated: 0, interrupted: 0, destructive: 0 } }, work: ["read 6 files"] } };
/** Capture began mid-session: `34†`. */
export const PartialCapture: Story = { args: { session: { ...session, capturedFromPrompt: 7 } } };
export const Hover: Story = { args: { "data-force": "hover" } };
export const Linked: Story = { args: { "data-force": "linked" } };
