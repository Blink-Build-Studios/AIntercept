// Parity specimens: the canvas's own content for each StatusCode data-spec
// (CompChips.dc.html), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import type { ReactNode } from "react";
import { StatusCode } from "./StatusCode";

const P = ({ spec, children }: { spec: string; children: ReactNode }) => (
  <div data-parity={spec} className="inline-grid items-start p-4">
    {children}
  </div>
);

const meta = { title: "Canvas/Chips/StatusCode", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-wrap items-start gap-3">
      <P spec="StatusCode/2xx"><StatusCode code={200} /></P>
      <P spec="StatusCode/error"><StatusCode code="ERR" /></P>
      <P spec="StatusCode/errorStatus529"><StatusCode code={529} /></P>
      <P spec="StatusCode/noneNotCaptured"><StatusCode code={null} /></P>
    </div>
  ),
};
