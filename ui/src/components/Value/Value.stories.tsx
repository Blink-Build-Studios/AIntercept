import type { Meta, StoryObj } from "@storybook/react";
import { Value } from "./Value";

const meta = { title: "Components/Agents/Value", component: Value, parameters: { layout: "padded" } } satisfies Meta<typeof Value>;
export default meta;
type Story = StoryObj<typeof meta>;

/** The agent reports it, we didn't get it: "—" with the reason. */
export const Unknown: Story = { args: { unknown: "not extracted yet", label: "Cost" } };
