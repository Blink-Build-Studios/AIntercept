import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import { DataTable } from "../DataTable/DataTable";
import { TURN_COLUMNS, TURN_MIN_WIDTH } from "./TurnRow";
import { ReplyPreview } from "./ReplyPreview";

const meta = {
  title: "Components/Turns/ReplyPreview",
  component: ReplyPreview,
  parameters: { layout: "padded" },
  args: { text: "Fixed: two refreshes could race and both write the cache. Now the second waits on the first’s promise…", size: "1.2k tokens", onExpand: fn() },
  decorators: [
    (S) => (
      <DataTable expandable columns={TURN_COLUMNS} minWidth={TURN_MIN_WIDTH} label="Turns">
        <S />
      </DataTable>
    ),
  ],
} satisfies Meta<typeof ReplyPreview>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = {};
