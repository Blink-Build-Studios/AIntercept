// Parity specimens: the canvas's own content for each Chip data-spec (CompChips.dc.html),
// measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import type { ReactNode } from "react";
import { Chip } from "./Chip";

const P = ({ spec, children }: { spec: string; children: ReactNode }) => (
  <div data-parity={spec} className="inline-grid items-start p-4">
    {children}
  </div>
);

const meta = { title: "Canvas/Chips/Chip", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-wrap items-start gap-3">
      <P spec="Chip/neutralPosture"><Chip>bypassPermissions</Chip></P>
      <P spec="Chip/muted"><Chip tone="muted">default</Chip></P>
      <P spec="Chip/notRetained"><Chip tone="notRetained">not retained</Chip></P>
      <P spec="Chip/rangeSelection"><Chip tone="range">range Oct 30 02:00 – 08:00 ×</Chip></P>
      <P spec="Chip/selectedFilter"><Chip tone="selected">all agents · 5 people</Chip></P>
      <P spec="Chip/withSwatch"><Chip swatch="model">model 96</Chip></P>
      <P spec="Chip/countNew"><Chip>3 new ↑</Chip></P>
    </div>
  ),
};
