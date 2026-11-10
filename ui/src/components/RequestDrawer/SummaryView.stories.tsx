import type { Meta, StoryObj } from "@storybook/react";
import failed from "../../api/samples/request/failed";
import normal from "../../api/samples/request/normal";
import notCaptured from "../../api/samples/request/not_captured";
import retained from "../../api/samples/request/retained";
import { SummaryView } from "./SummaryView";

const meta = {
  title: "Components/Drawer/SummaryView",
  component: SummaryView,
  parameters: { layout: "padded" },
  decorators: [(Story) => <div className="bg-surface" style={{ width: 820 }}>{Story()}</div>],
} satisfies Meta<typeof SummaryView>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Normal: Story = { args: { detail: normal.data } };
export const NotCaptured: Story = { args: { detail: notCaptured.data } };
export const Failed: Story = { args: { detail: failed.data } };
export const Retained: Story = { args: { detail: retained.data } };
