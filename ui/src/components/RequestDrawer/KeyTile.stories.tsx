import type { Meta, StoryObj } from "@storybook/react";
import { KeyTile } from "./KeyTile";

const meta = {
  title: "Components/Drawer/KeyTile",
  component: KeyTile,
  parameters: { layout: "padded" },
  decorators: [(Story) => <div className="flex border border-line bg-surface" style={{ width: 128 }}>{Story()}</div>],
} satisfies Meta<typeof KeyTile>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Value: Story = { args: { label: "TTFT", value: "1.9 s", sub: "by Codex" } };
/** No usage on the call: `—` with a dotted underline, `not captured`, the API's reason in the tooltip. */
export const Unknown: Story = { args: { label: "Tokens", value: null, sub: "not captured", reason: "stream cut before response.completed" } };
