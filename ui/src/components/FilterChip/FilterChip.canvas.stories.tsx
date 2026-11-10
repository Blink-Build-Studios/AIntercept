// Parity specimens: the canvas's own content for each FilterChip data-spec (CompChips.dc.html),
// measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import type { ReactNode } from "react";
import { FilterChip } from "./FilterChip";

const P = ({ spec, children }: { spec: string; children: ReactNode }) => (
  <div data-parity={spec} className="inline-grid items-start p-4">
    {children}
  </div>
);

const meta = { title: "Canvas/Chips/FilterChip", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

const remove = () => undefined;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-wrap items-start gap-3">
      <P spec="FilterChip/notCaptured"><FilterChip label="▲ not captured" onRemove={remove} /></P>
      <P spec="FilterChip/removeHover"><FilterChip label="▲ not captured" onRemove={remove} data-force="hover" /></P>
      <P spec="FilterChip/removeFocus"><FilterChip label="▲ not captured" onRemove={remove} data-force="focus" /></P>
    </div>
  ),
};
