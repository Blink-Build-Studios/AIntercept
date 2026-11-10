// Parity specimens: the canvas's own content for each HeaderStat data-spec
// (CompSummary.dc.html), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import type { ReactNode } from "react";
import { HeaderStatGroup } from "../HeaderStatGroup/HeaderStatGroup";
import { modelCallsTip } from "../../lib/tips";
import { ParityTags } from "../../test/ParityTag";
import { HeaderStat } from "./HeaderStat";

const P = ({ spec, children }: { spec: string; children: ReactNode }) => (
  <div data-parity={spec} className="inline-grid items-start p-4">
    {children}
  </div>
);

export default { title: "Canvas/Summary/HeaderStat", parameters: { layout: "padded" } } satisfies Meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-wrap items-start gap-3">
      {/* "default" and "live" are the first and last stats of the canvas group, measured in place. */}
      <HeaderStatGroup data-parity="HeaderStat/default">
        <HeaderStat label="Role" value="admin" />
        <HeaderStat label="Plan" value="max · 20x" />
      </HeaderStatGroup>
      <HeaderStatGroup data-parity="HeaderStat/live">
        <HeaderStat label="Last seen" value="now" live />
      </HeaderStatGroup>
      <P spec="HeaderStat/unknown"><HeaderStat label="Customer since" value={null} unknownReason="not extracted" /></P>
      {/* CompTableHover: a stat that explains itself, hovered, the last of HeaderStatGroup/withHover. */}
      <ParityTags targets={[{ spec: "HeaderStat/hover", selector: ".header-stat-tip" }]}>
        <div className="grid items-start" style={{ gridTemplateColumns: "656px" }}>
          <HeaderStatGroup>
            <HeaderStat label="Role" value="admin" />
            <HeaderStat label="Plan" value="max · 20x" />
            <HeaderStat label="Model calls" value="640" qualifier="+94 not captured" qualifierTone="gap" tip={modelCallsTip(640, 94)} data-force="hover" />
          </HeaderStatGroup>
        </div>
      </ParityTags>
      <P spec="HeaderStat/withQualifier"><HeaderStat label="Model calls" value="640" qualifier="+94 not captured" qualifierTone="gap" /></P>
    </div>
  ),
};
