import type { Meta, StoryObj } from "@storybook/react";
import { CoverageCell } from "./CoverageCell";

const meta = {
  title: "Components/Rows/CoverageCell",
  component: CoverageCell,
  decorators: [
    (Story) => (
      <div role="table" aria-label="Missed">
        <div role="row" className="flex justify-end" style={{ width: 84 }}>
          {Story()}
        </div>
      </div>
    ),
  ],
} satisfies Meta<typeof CoverageCell>;
export default meta;
type Story = StoryObj<typeof meta>;

/** Calls Claude Code reported that the proxy missed (§11G.C2); the fraction is in the tooltip. */
export const Gap: Story = { args: { coverage: { reported: 3730, captured: 3634, notCaptured: 96, windows: [] } } };
/** Every reported call captured. */
export const Complete: Story = { args: { coverage: { reported: 50, captured: 50, notCaptured: 0, windows: [] } } };
/** No self-report to compare. */
export const NotApplicable: Story = { args: { coverage: null } };
/** A product that reports nothing to compare against (§11L): the cell is empty, no placeholder. */
export const Absent: Story = { args: { coverage: null, absent: true } };
