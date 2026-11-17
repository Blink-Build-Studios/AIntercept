import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import { DataTable } from "../DataTable/DataTable";
import { TURN_COLUMNS, TURN_MIN_WIDTH } from "./TurnRow";
import { TurnStep } from "./TurnStep";

const meta = {
  title: "Components/Turns/TurnStep",
  component: TurnStep,
  parameters: { layout: "padded" },
  args: { onOpen: fn(), label: "step 22", detail: "sonnet · 96k → 610 · 4.1 s · tool_use · Bash" },
  decorators: [
    (S) => (
      <DataTable expandable columns={TURN_COLUMNS} minWidth={TURN_MIN_WIDTH} label="Turns">
        <S />
      </DataTable>
    ),
  ],
} satisfies Meta<typeof TurnStep>;
export default meta;
type Story = StoryObj<typeof meta>;

/** Content-free: model · tokens · duration · stop reason · the tool's name. */
export const Default: Story = {};
/** With content: the command and its outcome. */
export const WithContent: Story = { args: { detail: "sonnet · 96k → 610 · 4.1 s · Bash npm test · exit 1" } };
export const Hover: Story = { args: { "data-force": "hover" } };
/** Its call open in the drawer. */
export const Selected: Story = { args: { selected: true } };
