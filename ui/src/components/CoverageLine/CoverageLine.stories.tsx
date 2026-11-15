import type { Meta, StoryObj } from "@storybook/react";
import { expect, fn, userEvent, within } from "@storybook/test";
import complete from "../../api/samples/session/ended";
import demo from "../../api/samples/person_summary/demo";
import { CoverageLine } from "./CoverageLine";

const meta = { title: "Components/Summary/CoverageLine", component: CoverageLine, parameters: { layout: "padded" } } satisfies Meta<
  typeof CoverageLine
>;
export default meta;
type Story = StoryObj<typeof meta>;

/** A session whose every reported call was captured. */
const completeCoverage = complete.data.coverage;
if (!completeCoverage) throw new Error("session/ended has no coverage");

export const Complete: Story = { args: { coverage: completeCoverage } };
/** The real IPv6 bypass, from the demo sample. */
export const Gap: Story = { args: { coverage: demo.data.coverage, onView: fn() } };
/** The gap explains itself on hover (§6B.7): windows, the longest, and why. */
export const Hover: Story = {
  args: { coverage: demo.data.coverage, onView: fn() },
  play: async ({ canvasElement }) => {
    await userEvent.hover(within(canvasElement).getByText("▲ coverage gap"));
    await expect(await within(document.body).findByRole("tooltip", {}, { timeout: 1000 })).toHaveTextContent("calls not captured");
  },
};
