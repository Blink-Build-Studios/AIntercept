// Parity specimens: the canvas's own content for each Tab data-spec (one tab of
// Tabs; CompControls.dc.html), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import type { ReactNode } from "react";
import { Tab } from "./Tabs";

const P = ({ spec, children }: { spec: string; children: ReactNode }) => (
  <div data-parity={spec} className="inline-grid items-start p-4">
    {children}
  </div>
);
const requests = { id: "requests", label: "Requests", count: 170 };

const meta = { title: "Canvas/Controls/Tab", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-wrap items-start gap-3">
      <P spec="Tab/default"><Tab tab={requests} selected={false} /></P>
      <P spec="Tab/hover"><Tab tab={requests} selected={false} data-force="hover" /></P>
      <P spec="Tab/selected"><Tab tab={requests} selected /></P>
      <P spec="Tab/focus"><Tab tab={requests} selected={false} data-force="focus" /></P>
      <P spec="Tab/disabled"><Tab tab={{ id: "raw", label: "Raw", count: "none", disabled: true, disabledReason: "No raw capture" }} selected={false} /></P>
    </div>
  ),
};
