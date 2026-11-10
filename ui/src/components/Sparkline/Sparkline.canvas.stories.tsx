// Parity specimens: the canvas's own content for each Sparkline data-spec
// (CompSummary.dc.html), measured against the canvas by `make parity`.
// Sparkline/inRow is the last row's on CompRows (noTokenCounts), measured inside that PersonRow.
import type { Meta, StoryObj } from "@storybook/react";
import type { ReactNode } from "react";
import { ParityTags } from "../../test/ParityTag";
import { PersonRow } from "../PersonRow/PersonRow";
import { NOW, PersonParity, rows } from "../PersonRow/PersonRow.canvas.stories";
import { Sparkline } from "./Sparkline";

// The canvas draws each line 180px wide, on a shared scale whose busiest day is 10.
const P = ({ spec, children }: { spec: string; children: ReactNode }) => (
  <div data-parity={spec} className="grid items-start" style={{ gridTemplateColumns: "180px" }}>
    {children}
  </div>
);

const meta = { title: "Canvas/Summary/Sparkline", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-col items-start gap-3">
      <P spec="Sparkline/active">
        <Sparkline values={[0, 10, 3, 2, 1, 1, 0, 1, 1, 0, 0, 10, 10, 4]} max={10} color="claude-code" aria-label="Activity, 14 days" />
      </P>
      <P spec="Sparkline/sparse">
        <Sparkline values={[0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 1, 1, 0]} max={10} color="chatgpt" aria-label="Activity, 14 days" />
      </P>
      <ParityTags targets={[{ spec: "Sparkline/inRow", selector: "[data-parity=row-noTokenCounts] .sparkline" }]}>
        <PersonParity spec="row-noTokenCounts">
          {rows.noTokenCounts && <PersonRow person={rows.noTokenCounts} href="#" now={NOW} activityMax={10} view="claude-code" />}
        </PersonParity>
      </ParityTags>
      <P spec="Sparkline/noData">
        <Sparkline values={Array.from({ length: 14 }, () => 0)} max={10} color="claude-code" aria-label="No activity in 14 days" />
      </P>
    </div>
  ),
};
