// Parity specimens: PartialMark and its tooltip (CompDensity.dc.html), measured by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import { ParityTags } from "../../test/ParityTag";
import { PartialMark } from "./PartialMark";

const meta = { title: "Canvas/Rows/PartialMark", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <ParityTags targets={[{ spec: "Tooltip/partialCapture", selector: "[role=tooltip]" }]}>
      <div className="flex flex-col items-start gap-4 p-4 type-data" style={{ paddingTop: 128 }}>
        {/* Staged as in the Prompts cell: right-aligned. */}
        <span style={{ textAlign: "right" }}>
          34
          <span data-parity="PartialMark" className="contents">
            <PartialMark from={7} open />
          </span>
        </span>
      </div>
    </ParityTags>
  ),
};
