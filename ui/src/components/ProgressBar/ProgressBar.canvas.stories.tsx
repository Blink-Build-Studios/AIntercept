// Parity specimens: the canvas's own staging for each ProgressBar data-spec
// (CompInteraction.dc.html), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import { ProgressBar } from "./ProgressBar";

// The bar spans 30% of its region; `region` is the canvas panel's padding-box width.
const P = ({ spec, region }: { spec: string; region: number }) => (
  <div data-parity={spec} className="relative" style={{ width: region, height: 16 }}>
    <ProgressBar active scope="region" />
  </div>
);

const meta = { title: "Canvas/Interaction/ProgressBar", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-col gap-3">
      <P spec="ProgressBar/refetching" region={438} />
      <P spec="ProgressBar/slow" region={438} />
      <P spec="ProgressBar/pagination" region={658} />
    </div>
  ),
};
