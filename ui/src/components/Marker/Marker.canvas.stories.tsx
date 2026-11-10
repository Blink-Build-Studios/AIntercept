// Parity specimens: the canvas's own content for each Marker data-spec
// (CompChips.dc.html), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import type { ReactNode } from "react";
import { Marker } from "./Marker";

const P = ({ spec, children }: { spec: string; children: ReactNode }) => (
  <div data-parity={spec} className="inline-grid items-start p-4">
    {children}
  </div>
);

const meta = { title: "Canvas/Chips/Marker", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-wrap items-start gap-3">
      <P spec="Marker/frustrated"><Marker kind="frustrated" /></P>
      <P spec="Marker/frustratedCompact"><Marker kind="frustrated" count={1} /></P>
      <P spec="Marker/interrupted"><Marker kind="interrupted" /></P>
      <P spec="Marker/interruptedCompact"><Marker kind="interrupted" count={1} /></P>
      <P spec="Marker/destructive"><Marker kind="destructive" label="rm -rf (outside cwd)" /></P>
      <P spec="Marker/destructiveCompact"><Marker kind="destructive" count={10} /></P>
      <P spec="Marker/coverageGap"><Marker kind="coverageGap" label="640/734" /></P>
      <P spec="Marker/coverageGapLabel"><Marker kind="coverageGap" /></P>
      <P spec="Marker/match"><Marker kind="match" label="matches Claude Code’s report" /></P>
      <P spec="Marker/live"><Marker kind="live" label="now" /></P>
      <P spec="Marker/liveStreaming"><Marker kind="live" label="streaming · 214 tokens" /></P>
    </div>
  ),
};
