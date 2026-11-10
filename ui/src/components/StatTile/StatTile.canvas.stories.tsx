// Parity specimens: the canvas's own content for each StatTile data-spec
// (CompSummary.dc.html), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import type { ReactNode } from "react";
import { costTip } from "../../lib/tips";
import { StatTile } from "./StatTile";

// The canvas draws each tile 200px wide.
const P = ({ spec, children }: { spec: string; children: ReactNode }) => (
  <div data-parity={spec} className="inline-grid items-start p-4" style={{ gridTemplateColumns: "200px" }}>
    {children}
  </div>
);

const meta = { title: "Canvas/Summary/StatTile", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-wrap items-start gap-3">
      <P spec="StatTile/default"><StatTile label="Analytics" swatch="analytics" value={21305} sub="969 batches · 128 types" /></P>
      <P spec="StatTile/hoverLinksToRows">
        <StatTile label="Analytics" swatch="analytics" value={21305} sub="969 batches · 128 types" href="#requests" data-force="hover" />
      </P>
      <P spec="StatTile/hover"><StatTile label="Cost" value="$41.20" sub="api-equivalent · 640 calls" tip={costTip(3)} data-force="hover" /></P>
      <P spec="StatTile/noSwatch"><StatTile label="People" value={5} sub="2 active now" /></P>
      <P spec="StatTile/unknown"><StatTile label="Tool calls" value={null} unknownReason="not extracted yet" /></P>
      <P spec="StatTile/coverageGap"><StatTile label="Coverage" value={94} tone="gap" sub="reported, not captured" /></P>
      <P spec="StatTile/loading"><StatTile label="Model calls" value={null} loading /></P>
    </div>
  ),
};
