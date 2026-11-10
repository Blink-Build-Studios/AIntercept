// Parity specimens: CoverageCell (CompDensity.dc.html), measured by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import type { ReactNode } from "react";
import { CoverageCell } from "./CoverageCell";

const P = ({ spec, children }: { spec: string; children: ReactNode }) => (
  <div role="table" aria-label="Missed">
    <div data-parity={spec} role="row" className="inline-grid">
      {children}
    </div>
  </div>
);

const meta = { title: "Canvas/Rows/CoverageCell", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-col items-start gap-4 p-4 type-data">
      <P spec="CoverageCell/gap">
        <CoverageCell coverage={{ reported: 3730, captured: 3634, notCaptured: 96, windows: [] }} />
      </P>
      <P spec="CoverageCell/complete">
        <CoverageCell coverage={{ reported: 50, captured: 50, notCaptured: 0, windows: [] }} />
      </P>
      <P spec="CoverageCell/notApplicable">
        <CoverageCell coverage={null} />
      </P>
    </div>
  ),
};
