// Parity specimens: the canvas's own content for each Tabs data-spec
// (CompControls / CompInteraction .dc.html), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import type { ReactNode } from "react";
import { Tabs } from "./Tabs";

// `track` reproduces the canvas staging: a 520px cell, or the table panel.
const P = ({ spec, track, children }: { spec: string; track: number; children: ReactNode }) => (
  <div data-parity={spec} className="inline-grid items-start p-4" style={{ gridTemplateColumns: `${track}px` }}>
    {children}
  </div>
);
const tabs = [
  { id: "prompts", label: "Prompts", count: 4 },
  { id: "requests", label: "Requests", count: 170 },
  { id: "events", label: "Events", count: 2412 },
];

const meta = { title: "Canvas/Controls/Tabs", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-col items-start gap-3">
      <P spec="Tabs/setFirstSelected" track={520}><Tabs tabs={tabs} value="prompts" onChange={() => {}} label="Views" /></P>
      <P spec="Tabs/selectedLoading" track={658}><Tabs tabs={tabs} value="requests" onChange={() => {}} label="Views" inset /></P>
    </div>
  ),
};
