import type { Meta, StoryObj } from "@storybook/react";
import { DataTable } from "../DataTable/DataTable";
import { WORK_COLUMNS, WORK_MIN_WIDTH } from "./RepoRow";
import { WorkSessionRow } from "./WorkSessionRow";

const session = { sessionId: "4c1a8e2077e0", start: "2026-11-02T10:04:00", end: null, live: true, label: "session 4c1a…77e0", turns: 34, activeMs: 22_320_000, friction: { interrupted: 2, failed: 5 } };
const meta = {
  title: "Components/Turns/WorkSessionRow",
  component: WorkSessionRow,
  parameters: { layout: "padded" },
  args: { session, work: ["Bash ×488 · Read ×402 · Edit ×171 · 6 subagents"], href: "#", flat: true, now: "2026-11-04T12:00:00" },
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

/** Content-free (§11S): the flat list, `session <id>`, tool counts, `✕N !N`. */
export const WorkDefault: Story = {};
/** Ended, no friction. */
export const WorkPlain: Story = {
  args: { session: { ...session, sessionId: "91be55c20a4d", live: false, end: "2026-10-31T17:08:00", start: "2026-10-31T16:20:00", label: "session 91be…0a4d", turns: 9, activeMs: 2_880_000, friction: { interrupted: 0, failed: 0 } }, work: ["Read ×61 · Edit ×12 · mcp:linear ×3"] },
};
/** With content: under its repo, 38px in, the branch and the plain-words line. */
export const WorkWithContent: Story = { args: { flat: false, session: { ...session, label: "partner-readiness" }, work: ["edited 23 files · 2 commits · npm test ×41 (9 failed)"] } };
export const Hover: Story = { args: { "data-force": "hover" } };
export const Linked: Story = { args: { "data-force": "linked" } };
