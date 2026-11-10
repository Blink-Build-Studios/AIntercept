// Parity specimens: the canvas's own content for each TableHeader data-spec
// (CompTableHover.dc.html, CompDensity.dc.html), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import type { CSSProperties } from "react";
import { MISSED_COLUMN } from "../CoverageCell/CoverageCell";
import { TableHeader } from "./TableHeader";

const meta = { title: "Canvas/Rows/TableHeader", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

// Staging: a header row; the specimen is its Missed cell (84px, right-aligned), hovered.
const row = (spec: string) => (
  <div data-parity={spec} className="dt-row type-key text-ink-faint" style={{ "--c1": "0 0 84px" } as CSSProperties}>
    <TableHeader header={MISSED_COLUMN.header} align="right" definition={MISSED_COLUMN.definition} data-force="hover" />
  </div>
);
export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-col items-start gap-4">
      {row("TableHeader/hoverDefinition")}
      {/* The header's term alone (CompDensity). */}
      <div data-parity="TableHeader/missed" className="inline-grid type-key text-ink-faint">
        <TableHeader header={MISSED_COLUMN.header} definition={MISSED_COLUMN.definition} data-force="hover" />
      </div>
    </div>
  ),
};
