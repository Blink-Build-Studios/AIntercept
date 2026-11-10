import type { Meta, StoryObj } from "@storybook/react";
import { PartialMark } from "./PartialMark";

const meta = {
  title: "Components/Rows/PartialMark",
  component: PartialMark,
  decorators: [(Story) => <span className="type-data">34{Story()}</span>],
} satisfies Meta<typeof PartialMark>;
export default meta;
type Story = StoryObj<typeof meta>;

/** A session first seen at prompt #7 (§11G.D1): `34†`. */
export const Default: Story = { args: { from: 7 } };
/** Its tooltip. */
export const Open: Story = { args: { from: 7, open: true } };
