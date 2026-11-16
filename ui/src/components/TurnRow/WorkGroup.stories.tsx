import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import { DataTable } from "../DataTable/DataTable";
import { TURN_COLUMNS, TURN_MIN_WIDTH } from "./TurnRow";
import { TurnStep } from "./TurnStep";
import { WorkGroup } from "./WorkGroup";

const meta = {
  title: "Components/Turns/WorkGroup",
  component: WorkGroup,
  parameters: { layout: "padded" },
  args: { onToggle: fn(), expanded: false, kind: "read", title: "Read 5 files", meta: ".ts ×5 · 4 steps · 41 s" },
  decorators: [
    (S) => (
      <DataTable expandable columns={TURN_COLUMNS} minWidth={TURN_MIN_WIDTH} label="Turns">
        <S />
      </DataTable>
    ),
  ],
} satisfies Meta<typeof WorkGroup>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Collapsed: Story = {};
/** A command group, open to its steps; its exit codes on the right. */
export const Open: Story = {
  args: {
    kind: "run",
    title: "Ran npm test ×4",
    meta: "4 steps · 2m 12s",
    exits: [1, 1, 1, 0],
    expanded: true,
    children: (
      <>
        <TurnStep label="step 20" detail="sonnet · 94k → 540 · 3.8 s · Bash npm · exit 1 · 17 s" onOpen={fn()} />
        <TurnStep label="step 22" detail="sonnet · 96k → 610 · 4.1 s · Bash npm · exit 1 · 18 s" onOpen={fn()} />
      </>
    ),
  },
};
export const EditsGroup: Story = { args: { kind: "edit", title: "Edited 2 files", meta: "+31 −9 · .ts ×2 · 3 steps" } };
export const ThinkingGroup: Story = { args: { kind: "think", title: "Thinking", meta: "22 steps with no tool · 1m 40s · 210k → 3.2k" } };
export const ReplyGroup: Story = { args: { kind: "reply", title: "Reply", meta: "1.2k tokens" } };
/** Content recorded: paths replace types. */
export const WithPaths: Story = { args: { kind: "edit", title: "Edited 2 files", meta: "+31 −9 · src/auth/cache.ts, src/auth/refresh.ts" } };
export const Hover: Story = { args: { "data-force": "hover" } };
