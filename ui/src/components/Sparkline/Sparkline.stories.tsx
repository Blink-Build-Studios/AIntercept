import type { Meta, StoryObj } from "@storybook/react";
import demo from "../../api/samples/people/demo";
import { Sparkline } from "./Sparkline";

const meta = {
  title: "Components/Summary/Sparkline",
  component: Sparkline,
  parameters: { layout: "padded" },
  decorators: [(Story) => <div style={{ width: 180 }}>{Story()}</div>],
} satisfies Meta<typeof Sparkline>;
export default meta;
type Story = StoryObj<typeof meta>;

const days = (demo.data[0]?.activity ?? []).map((d) => d.count);
/** The demo person's last 14 days. */
export const Active: Story = { args: { values: days, color: "claude-code", "aria-label": "Activity, 14 days" } };
/** A few active days on the table's shared scale. */
export const Sparse: Story = { args: { values: days.map((v, i) => (i % 5 === 0 ? v : 0)), max: Math.max(...days), color: "claude-code", "aria-label": "Activity, 14 days" } };
export const NoData: Story = { args: { values: days.map(() => 0), color: "claude-code", "aria-label": "No activity in 14 days" } };
