// Parity specimens: the canvas's own content for each TableHeader data-spec
// (CompTableHover.dc.html, CompDensity.dc.html), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import type { CSSProperties } from "react";
import { TableHeader } from "./TableHeader";

const meta = { title: "Canvas/Rows/TableHeader", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

// The sheet's column: Missed, with its definition (CompTableHover; the column itself is gone, §11S).
const MISSED = { header: "Missed", definition: "Model calls Claude Code reported that the proxy didn’t capture, in this window." };
// Staging: a header row; the specimen is its Missed cell (84px, right-aligned), hovered.
const row = (spec: string) => (
  <div data-parity={spec} className="dt-row type-key text-ink-faint" style={{ "--c1": "0 0 84px" } as CSSProperties}>
    <TableHeader header={MISSED.header} align="right" definition={MISSED.definition} data-force="hover" />
  </div>
);
export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-col items-start gap-4">
      {row("TableHeader/hoverDefinition")}
    </div>
  ),
};
