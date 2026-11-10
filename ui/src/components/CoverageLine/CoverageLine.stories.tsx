import type { Meta, StoryObj } from "@storybook/react";
import { expect, fn, userEvent, within } from "@storybook/test";
import complete from "../../api/samples/coverage/complete";
import demo from "../../api/samples/coverage/demo";
import { CoverageLine } from "./CoverageLine";

const meta = { title: "Components/Summary/CoverageLine", component: CoverageLine, parameters: { layout: "padded" } } satisfies Meta<
  typeof CoverageLine
>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Complete: Story = { args: { coverage: complete.data } };
/** The real IPv6 bypass, from the demo sample. */
export const Gap: Story = { args: { coverage: demo.data, onView: fn() } };
/** The gap explains itself on hover (§6B.7): windows, the longest, and why. */
export const Hover: Story = {
  args: { coverage: demo.data, onView: fn() },
  play: async ({ canvasElement }) => {
    await userEvent.hover(within(canvasElement).getByText("▲ coverage gap"));
    await expect(await within(document.body).findByRole("tooltip", {}, { timeout: 1000 })).toHaveTextContent("calls not captured");
  },
};
