import type { Meta, StoryObj } from "@storybook/react";
import normal from "../../api/samples/request/normal";
import retained from "../../api/samples/request/retained";
import { contentRow } from "./drawerData";
import { ContentRow, type ContentRowData } from "./ContentRow";

const meta = {
  title: "Components/Drawer/ContentRow",
  component: ContentRow,
  parameters: { layout: "padded" },
  decorators: [(Story) => <div className="border border-line bg-surface" style={{ width: 822 }}>{Story()}</div>],
} satisfies Meta<typeof ContentRow>;
export default meta;
type Story = StoryObj<typeof meta>;

const pick = (rows: typeof normal.data.received, tool: boolean): ContentRowData => {
  const r = rows.find((x) => (x.tone === "tool") === tool) ?? rows[0];
  if (!r) throw new Error("sample has no content rows");
  return contentRow(r);
};
export const ShapeOnly: Story = { args: { row: pick(normal.data.received, false) } };
/** A tool call the response asked for: the skills color. */
export const ToolCall: Story = { args: { row: pick(normal.data.received, true) } };
/** Experimentation mode: the body follows the row. */
export const Retained: Story = { args: { row: contentRow(retained.data.received.find((r) => r.body) ?? retained.data.received[0]!) } };
