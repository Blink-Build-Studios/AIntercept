import type { Meta, StoryObj } from "@storybook/react";
import { summaryLabels } from "../ProviderMark/providerSpecimens";
import { SummaryLabel } from "./SummaryLabel";

const meta = {
  title: "Components/Drawer/SummaryLabel",
  component: SummaryLabel,
  parameters: { layout: "padded" },
  decorators: [(Story) => <div className="border border-line bg-surface" style={{ width: 520 }}>{Story()}</div>],
} satisfies Meta<typeof SummaryLabel>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Fits: Story = { args: summaryLabels.fits };
/** The name truncates; `×2` stays at its visible end. */
export const Truncated: Story = { args: summaryLabels.truncated };
/** A long MCP tool name: the full label is the tooltip, on hover and focus. */
export const Long: Story = { args: summaryLabels.long };
