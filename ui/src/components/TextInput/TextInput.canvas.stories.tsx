// Parity specimens: the canvas's own content for each TextInput data-spec
// (CompControls.dc.html), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import type { ReactNode } from "react";
import { TextInput } from "./TextInput";

const P = ({ spec, children }: { spec: string; children: ReactNode }) => (
  <div data-parity={spec} className="inline-grid items-start p-4">
    {children}
  </div>
);
const base = { label: "Filter", placeholder: "path, status, session…", value: "", onChange: () => {} };

const meta = { title: "Canvas/Controls/TextInput", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-wrap items-start gap-3">
      <P spec="TextInput/empty"><TextInput id="i1" {...base} /></P>
      <P spec="TextInput/focus"><TextInput id="i2" {...base} data-force="focus" /></P>
      <P spec="TextInput/filled"><TextInput id="i3" {...base} value="status=ERR" /></P>
      <P spec="TextInput/disabled"><TextInput id="i4" {...base} disabled /></P>
      <P spec="TextInput/invalid"><TextInput id="i5" {...base} value="colour=blue" invalid message="unknown field “colour”" /></P>
    </div>
  ),
};
