import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import { DrawerTabs } from "./DrawerTabs";

const meta = {
  title: "Components/Drawer/DrawerTabs",
  component: DrawerTabs,
  parameters: { layout: "padded" },
  decorators: [(Story) => <div style={{ width: 434 }}>{Story()}</div>],
  args: { value: "summary", onChange: fn(), onRawHover: fn() },
} satisfies Meta<typeof DrawerTabs>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = {};
/** Not captured: there is no wire data. */
export const RawNone: Story = { args: { raw: "none" } };
/** Failed: the request went out, no response came back. */
export const RawRequestOnly: Story = { args: { raw: "requestOnly" } };
