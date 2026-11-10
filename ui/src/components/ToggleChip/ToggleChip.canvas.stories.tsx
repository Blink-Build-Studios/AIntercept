// Parity specimens: the canvas's own content for each ToggleChip data-spec
// (CompControls.dc.html), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import type { ReactNode } from "react";
import { ToggleChip } from "./ToggleChip";

const P = ({ spec, children }: { spec: string; children: ReactNode }) => (
  <div data-parity={spec} className="inline-grid items-start p-4">
    {children}
  </div>
);
const base = { label: "hide client internals", onChange: () => {} };

const meta = { title: "Canvas/Controls/ToggleChip", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-wrap items-start gap-3">
      <P spec="ToggleChip/off"><ToggleChip {...base} pressed={false} /></P>
      <P spec="ToggleChip/on"><ToggleChip {...base} pressed /></P>
      <P spec="ToggleChip/hover"><ToggleChip {...base} pressed={false} data-force="hover" /></P>
      <P spec="ToggleChip/focus"><ToggleChip {...base} pressed={false} data-force="focus" /></P>
      <P spec="ToggleChip/disabled"><ToggleChip {...base} pressed={false} disabled disabledReason="Loading events…" /></P>
      <P spec="ToggleChip/withSwatchOn"><ToggleChip label="model" count={96} swatch="model" pressed onChange={() => {}} /></P>
      <P spec="ToggleChip/withSwatchOff"><ToggleChip label="analytics" count={71} swatch="analytics" pressed={false} onChange={() => {}} /></P>
    </div>
  ),
};
