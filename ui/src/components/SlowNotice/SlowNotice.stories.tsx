import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import { SlowNotice } from "./SlowNotice";

const meta = { title: "Components/Interaction/SlowNotice", component: SlowNotice, parameters: { layout: "padded" },
  decorators: [(Story) => <div className="relative border border-line" style={{ width: 448, height: 168 }}>{Story()}</div>],
} satisfies Meta<
  typeof SlowNotice
>;
export default meta;
type Story = StoryObj<typeof meta>;

/** Over its region (§6A.1): what's loading, then the size when known (§11H.5). */
export const Default: Story = { args: { reason: "the timeline", size: "2 days · 21,305 events", onCancel: fn() } };
export const NameOnly: Story = { args: { reason: "the timeline", onCancel: fn() } };
export const NameAndSize: Story = { args: { reason: "the timeline", size: "2 days · 21,305 events", onCancel: fn() } };
