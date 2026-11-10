import type { Meta, StoryObj } from "@storybook/react";
import summary from "../../api/samples/person_summary/demo";
import { StatTile } from "./StatTile";

const rb = summary.data.ribbon;
const meta = {
  title: "Components/Summary/StatTile",
  component: StatTile,
  parameters: { layout: "padded" },
  decorators: [(Story) => <div className="flex bg-line" style={{ width: 256 }}>{Story()}</div>],
} satisfies Meta<typeof StatTile>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = {
  args: { label: "Analytics", swatch: "analytics", value: rb.analyticsEvents, sub: `${rb.analyticsBatches} batches · ${rb.eventTypes} types` },
};
export const Hover: Story = { args: { ...Default.args!, label: "Analytics", value: rb.analyticsEvents, href: "#requests" } };
export const NoSwatch: Story = { args: { label: "Sessions", value: rb.sessions, sub: "1 live" } };
export const Unknown: Story = { args: { label: "Tool calls", value: null, unknownReason: "not extracted yet" } };
export const Gap: Story = { args: { label: "Coverage", value: summary.data.coverage.notCaptured, tone: "gap", sub: "reported, not captured" } };
export const Loading: Story = { args: { label: "Model calls", value: null, loading: true } };
