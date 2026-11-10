import type { Meta, StoryObj } from "@storybook/react";
import { StatusCode } from "./StatusCode";

const meta = { title: "Components/Chips/StatusCode", component: StatusCode, parameters: { layout: "padded" } } satisfies Meta<
  typeof StatusCode
>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Ok: Story = { args: { code: 200 } };
export const Error: Story = { args: { code: 529 } };
export const Err: Story = { args: { code: "ERR" } };
export const Unknown: Story = { args: { code: null } };
