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
  args: { onToggle: fn(), expanded: false, kind: "read", title: "Read ×5", meta: "4 steps · 41 s" },
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

/** Content-free: consecutive steps by tool name (§11S). */
export const Collapsed: Story = {};
/** Open to its steps. */
export const Open: Story = {
  args: {
    kind: "run",
    title: "Bash ×4",
    meta: "4 steps · 2m 12s",
    expanded: true,
    children: (
      <>
        <TurnStep label="step 20" detail="sonnet · 94k → 540 · 3.8 s · tool_use · Bash" onOpen={fn()} />
        <TurnStep label="step 22" detail="sonnet · 96k → 610 · 4.1 s · tool_use · Bash" onOpen={fn()} />
      </>
    ),
  },
};
export const EditsGroup: Story = { args: { kind: "edit", title: "Edit ×2", meta: "3 steps" } };
/** An MCP server's calls, its tool names in the meta. */
export const McpGroup: Story = { args: { kind: "mcp", title: "mcp:linear ×3", meta: "list_issues ×2 · get_issue · 3 steps" } };
/** Steps that called no tool. */
export const ThinkingGroup: Story = { args: { kind: "think", title: "No tool", meta: "22 steps · 1m 40s · 210k → 3.2k" } };
export const ReplyGroup: Story = { args: { kind: "reply", title: "Reply", meta: "1.2k tokens" } };
/** Content recorded: what the steps did, paths in the meta. */
export const WithPaths: Story = { args: { kind: "edit", title: "Edited 2 files", meta: "+31 −9 · src/auth/cache.ts, src/auth/refresh.ts" } };
/** Content recorded: a command group, its exit codes (from the tool results) on the right. */
export const WithCommands: Story = { args: { kind: "run", title: "Ran npm test ×4", meta: "4 steps · 2m 12s", exits: [1, 1, 1, 0], expanded: true } };
export const Hover: Story = { args: { "data-force": "hover" } };
