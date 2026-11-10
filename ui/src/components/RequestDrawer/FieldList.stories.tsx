import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import details from "../../api/samples/request_details/normal";
import { FieldList } from "./FieldList";

const meta = {
  title: "Components/Drawer/FieldList",
  component: FieldList,
  parameters: { layout: "padded" },
  decorators: [(Story) => <div style={{ width: 562 }}>{Story()}</div>],
} satisfies Meta<typeof FieldList>;
export default meta;
type Story = StoryObj<typeof meta>;

const fields = details.data.fields;
export const Default: Story = { args: { fields: fields.filter((f) => f.value !== null), filter: "", onFilter: fn(), className: "border border-line bg-surface" } };
/** Fields that exist but aren't on the rows yet (observer extraction, M7). */
export const NotExtracted: Story = { args: { fields, className: "border border-line bg-surface" } };
