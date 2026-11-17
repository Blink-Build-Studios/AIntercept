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

/** CompTurns (§11S): the session header's stats, content-free and with content, each measured in place. */
export function SessionHeaderSpecimens() {
  const at = (group: string, n: number) => `[data-parity='${group}'] > [role=group] > :nth-child(${n})`;
  const free = "HeaderStatGroup/contentFree";
  const withContent = "HeaderStatGroup/withContent";
  return (
    <ParityTags
      targets={[
        ...["span", "turns", "steps", "tokens", "tools"].map((s, i) => ({ spec: `HeaderStat/${s}`, selector: at(free, i + 1) })),
        ...["turnsWithContent", "toolsWithContent", "withContent", "commits"].map((s, i) => ({ spec: `HeaderStat/${s}`, selector: at(withContent, i + 1) })),
      ]}
    >
      <div className="flex flex-col" style={{ width: 1344, gap: 32 }}>
        <div data-parity={free}>
          <HeaderStatGroup wrapGap>
            <HeaderStat label="Span" value="Nov 2 10:04 → now · 2d 0h" sub="6h 12m active" />
            <HeaderStat label="Turns" value="34" sub="2 interrupted" />
            <HeaderStat label="Steps" value="640" sub="model calls · 5 failed" />
            <HeaderStat label="Tokens" value="8.2M → 1.1M" sub="92% cache read" />
            <HeaderStat label="Tool calls" value="1,212" sub="14 tools used of 404 offered" />
          </HeaderStatGroup>
        </div>
        <div data-parity={withContent}>
          <HeaderStatGroup wrapGap>
            <HeaderStat label="Turns" value="34" sub="2 interrupted" />
            <HeaderStat label="Tool calls" value="1,212" sub="14 used of 404 offered" />
            <HeaderStat label="Edited" value="23 files" sub="+1,204 −388" />
            <HeaderStat label="Commits" value="2" sub="a41f → 9c02" />
          </HeaderStatGroup>
        </div>
      </div>
    </ParityTags>
  );
}

export default { title: "Canvas/Summary/HeaderStat", parameters: { layout: "padded" }, excludeStories: ["SessionHeaderSpecimens"] } satisfies Meta;

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
      <SessionHeaderSpecimens />
      <P spec="HeaderStat/withQualifier"><HeaderStat label="Model calls" value="640" qualifier="+94 not captured" qualifierTone="gap" /></P>
    </div>
  ),
};
