// Parity specimens: the canvas's own content for each SegmentedControl data-spec
// (CompControls.dc.html), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import type { ReactNode } from "react";
import { SegmentedControl } from "./SegmentedControl";

const P = ({ spec, children }: { spec: string; children: ReactNode }) => (
  <div data-parity={spec} className="inline-grid items-start p-4">
    {children}
  </div>
);
const options = ["24h", "7d", "14d", "All"].map((w) => ({ value: w, label: w }));
const base = { options, value: "14d", onChange: () => {}, "aria-label": "Time window" };

const meta = { title: "Canvas/Controls/SegmentedControl", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-wrap items-start gap-3">
      <P spec="SegmentedControl/default14dSelected"><SegmentedControl {...base} /></P>
      <P spec="SegmentedControl/window"><SegmentedControl {...base} options={[...options]} /></P>
      <P spec="SegmentedControl/hoverOn7d"><SegmentedControl {...base} force={{ value: "7d", state: "hover" }} /></P>
      <P spec="SegmentedControl/focusOn24h"><SegmentedControl {...base} force={{ value: "24h", state: "focus" }} /></P>
      {/* The canvas shows no option selected while loading. */}
      <P spec="SegmentedControl/disabledWhileLoading"><SegmentedControl {...base} value="" disabled disabledReason="Loading…" /></P>
    </div>
  ),
};
