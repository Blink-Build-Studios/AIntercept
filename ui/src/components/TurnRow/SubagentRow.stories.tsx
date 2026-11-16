import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import { DataTable } from "../DataTable/DataTable";
import { TURN_COLUMNS, TURN_MIN_WIDTH } from "./TurnRow";
import { SubagentRow } from "./SubagentRow";

const meta = {
  title: "Components/Turns/SubagentRow",
  component: SubagentRow,
  parameters: { layout: "padded" },
  args: { name: "test-writer", work: ["9 steps · edited 1 file +66 · ran npm test ×2: passed"], tokens: "180k → 2.2k", onOpen: fn() },
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

export const Default: Story = {};
/** Codex: the thread is its own session page. */
export const CodexThread: Story = { args: { name: "reviewer", work: ["12 steps · ran 30 commands"], tokens: "410k → 3.0k", onOpen: undefined, href: "#" } };
