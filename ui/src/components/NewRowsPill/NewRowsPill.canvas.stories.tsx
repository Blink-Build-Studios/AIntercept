// Parity specimen: CompInteraction.dc.html "NewRowsPill/visible", measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import { NewRowsPill } from "./NewRowsPill";

const meta = { title: "Canvas/Interaction/NewRowsPill", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex items-center gap-3">
      <div data-parity="NewRowsPill/visible" className="flex items-center">
        <NewRowsPill count={12} direction="down" onClick={fn()} />
      </div>
    </div>
  ),
};
