// Parity specimens: the canvas's own content for each Skeleton data-spec
// (CompStates.dc.html), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import type { ReactNode } from "react";
import { Skeleton } from "./Skeleton";

// `track` reproduces the canvas staging: each specimen is drawn at a fixed width.
const P = ({ spec, track, children }: { spec: string; track?: number; children: ReactNode }) => (
  <div data-parity={spec} className="inline-grid items-start p-4" style={track ? { gridTemplateColumns: `${track}px` } : undefined}>
    {children}
  </div>
);

const meta = { title: "Canvas/Chrome/Skeleton", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-wrap items-start gap-3">
      <P spec="Skeleton/line" track={220}><Skeleton shape="line" lines={2} /></P>
      <P spec="Skeleton/blockTile" track={180}><Skeleton shape="block" /></P>
      <P spec="Skeleton/row" track={462}><Skeleton shape="row" /></P>
      <P spec="Skeleton/lane" track={386}><Skeleton shape="lane" /></P>
    </div>
  ),
};
