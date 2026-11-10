import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import { TextInput } from "./TextInput";

const meta = {
  title: "Components/Controls/TextInput",
  component: TextInput,
  parameters: { layout: "padded" },
  args: { id: "filter", label: "Filter", placeholder: "text or field=value", value: "", onChange: fn() },
} satisfies Meta<typeof TextInput>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Empty: Story = {};
export const Focus: Story = { args: { "data-force": "focus" } };
export const Filled: Story = { args: { value: "kind=account", pending: true } };
export const Disabled: Story = { args: { disabled: true } };
export const Invalid: Story = { args: { value: "kind=", invalid: true, message: "kind needs a value: model, analytics, flags, count or account" } };
