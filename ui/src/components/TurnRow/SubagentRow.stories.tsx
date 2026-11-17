import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import { DataTable } from "../DataTable/DataTable";
import { TURN_COLUMNS, TURN_MIN_WIDTH } from "./TurnRow";
import { SubagentRow } from "./SubagentRow";

const meta = {
  title: "Components/Turns/SubagentRow",
  component: SubagentRow,
  parameters: { layout: "padded" },
  args: { name: null, n: 1, work: ["9 steps · Write ×1 · Bash ×2"], tokens: "180k → 2.2k", onOpen: fn() },
  decorators: [
    (S) => (
      <DataTable expandable columns={TURN_COLUMNS} minWidth={TURN_MIN_WIDTH} label="Turns">
        <S />
      </DataTable>
    ),
  ],
} satisfies Meta<typeof SubagentRow>;
export default meta;
type Story = StoryObj<typeof meta>;

/** Content-free: `subagent N`, in the order spawned, with its tool counts. */
export const Default: Story = {};
/** With content: the name from the spawn call's input, and the work in plain words. */
export const WithContent: Story = { args: { name: "test-writer", work: ["9 steps · wrote 1 file (.test.ts) +66 · ran npm test ×2: passed"] } };
/** Codex: the thread is its own session page. */
export const CodexThread: Story = { args: { name: null, n: 2, work: ["12 steps · exec_command ×30"], tokens: "410k → 3.0k", onOpen: undefined, href: "#" } };
